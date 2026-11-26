package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/config"
	"github.com/nevzatcirak/cirak/internal/mcpclient"
)

// `cirak doctor` (ADR-027 section 3): read-only diagnostics, safe to paste. It runs everything `config check`
// runs on the same effective configuration, then adds the live and the host checks: the OpenCode reads, the
// sockets, the lifecycle and overlay directories, the ACL and the Claude files, the environment, and with --smoke
// one bounded MCP handshake against an already-running service. It never starts a listener, never writes, never
// restarts anything, and never includes a token, a password or a free-text provider error.

const doctorUsage = `usage: cirak doctor [--config path] [--listen addr] [--read-only] [--token-file path]
                    [--from-unit name | --env-file path] [--smoke]

Read-only diagnostics of the configuration a start would use, with no listener and no write. Exit 0 all checks
pass, 1 warnings or unverified items, 2 a check failed. --from-unit reads the environment of a systemd unit
(systemctl show, then its EnvironmentFile= files; run as root or as a user that can read them) instead of your
shell; --env-file reads one environment file the same way. --smoke makes one bounded initialize + tools/list call
against the ALREADY RUNNING local service: no prompt, no mutation.
`

// doctorDeps is every side effect of the doctor, injected: tests use temp files, loopback servers and fakes.
type doctorDeps struct {
	getenv         func(string) string
	environ        []string
	uid            int
	now            func() time.Time
	stdout, stderr io.Writer
	showUnit       func(ctx context.Context, name string) (string, error)
	readFile       fileReader
	openCode       func(ctx context.Context, baseURL, username, password string) openCodeLive
	dial           func(network, addr string, timeout time.Duration) error
	smoke          func(ctx context.Context, endpoint, token string) ([]string, error)
	stageDir       func() (string, error) // a private directory for a file `config import` may not write itself
	random         io.Reader              // the names of temporary files and nothing else
}

// openCodeLive is the result of the live OpenCode reads: Status is the HTTP status of the first failing read
// (0 for a transport failure), OK is true when both reads answered.
type openCodeLive struct {
	OK     bool
	Status int
}

type doctorOptions struct {
	serve    serveFlags
	fromUnit string
	envFile  string
	smoke    bool
}

var unitNamePattern = regexp.MustCompile(`^[A-Za-z0-9:_.@-]{1,200}$`)

func runDoctor(args []string, d doctorDeps) int {
	fs, sf := newFlagSet("cirak doctor", io.Discard)
	opts := doctorOptions{}
	fs.StringVar(&opts.fromUnit, "from-unit", "", "read the environment of this systemd unit instead of the shell")
	fs.StringVar(&opts.envFile, "env-file", "", "read this environment file instead of the shell")
	fs.BoolVar(&opts.smoke, "smoke", false, "one initialize + tools/list call against the running local service")
	fs.Usage = func() { fmt.Fprint(d.stderr, doctorUsage) }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprint(d.stderr, doctorUsage)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if opts.fromUnit != "" && opts.envFile != "" {
		fmt.Fprintln(d.stderr, "doctor: --from-unit and --env-file are exclusive")
		return 2
	}
	opts.serve = *sf
	rep := &report{out: d.stdout}
	doctor(rep, opts, d)
	switch rep.code() {
	case 0:
		fmt.Fprintln(d.stdout, "result: all checks pass")
	case 1:
		fmt.Fprintln(d.stdout, "result: warnings (exit 1)")
	default:
		fmt.Fprintln(d.stdout, "result: a check failed (exit 2)")
	}
	return rep.code()
}

func doctor(rep *report, o doctorOptions, d doctorDeps) {
	src, ok := d.selectSource(rep, o.fromUnit, o.envFile)
	if !ok {
		return
	}
	getenv, environ := src.getenv, src.environ
	if src.fromSrc {
		compareWithConfig(rep, o.serve, src, d)
	}
	env, ld := checkConfigurationHome(rep, o.serve, getenv, environ, d.uid, true, src.home, src.fromSrc)
	if ld == nil {
		return
	}
	now := d.now()
	checkTokenDeadline(rep, env, now)
	checkTLS(rep, env, now, d)
	cfg, _ := readIngressAt(env, now)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	checkOpenCodeLive(rep, ld, env, d, ctx)
	checkSockets(rep, env, cfg, d)
	checkLifecycleHost(rep, env, d.uid)
	checkACLHost(rep, env)
	checkEnvironmentNames(rep, environ, o.fromUnit != "" || o.envFile != "")
	if o.smoke {
		checkSmoke(rep, ld, env, cfg, d, ctx)
	}
}

func unitSourceLines(rep *report, what string, u *unitEnvironment) {
	fmt.Fprintf(rep.out, "environment source: %s (names and paths only; no value is shown)\n", what)
	for _, p := range u.FilesRead {
		fmt.Fprintf(rep.out, "  read: %s\n", p)
	}
	for _, p := range u.Skipped {
		fmt.Fprintf(rep.out, "  skipped (optional, missing): %s\n", p)
	}
	rep.ok("environment source parsed")
}

func (d doctorDeps) unitSource(name string) (*unitEnvironment, error) {
	if !unitNamePattern.MatchString(name) {
		return nil, errors.New("not a valid unit name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	text, err := d.showUnit(ctx, name)
	if err != nil {
		return nil, errors.New("systemctl show failed (is the unit name right, and is systemd available?)")
	}
	assign, files, unset, err := parseSystemctlShow(text)
	if err != nil {
		return nil, err
	}
	return loadUnitEnvironment(assign, files, unset, d.readFile)
}

// checkTokenDeadline reports the recorded rotation deadline of a local token (no runtime expiry, ADR-019).
func checkTokenDeadline(rep *report, env func(string) string, now time.Time) {
	v := env("CIRAK_TOKEN_EXPIRES")
	if v == "" {
		rep.ok("token deadline: none recorded")
		return
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return // the ingress check above already refused it
	}
	left := t.Sub(now)
	switch {
	case left <= 0:
		rep.warn("token deadline passed on %s: rotate the token (local mode enforces no expiry, ADR-019)", t.UTC().Format("2006-01-02"))
	case left < 48*time.Hour:
		rep.warn("token deadline in %d hour(s), on %s", int(left.Hours()), t.UTC().Format("2006-01-02"))
	default:
		rep.ok("token deadline in %d day(s), on %s", int(left.Hours()/24), t.UTC().Format("2006-01-02"))
	}
}

// checkTLS reports a TLS pair: the certificate's expiry and the key's presence and mode. Neither content is
// printed, and the key is never read.
func checkTLS(rep *report, env func(string) string, now time.Time, d doctorDeps) {
	cert, key := env("CIRAK_TLS_CERT"), env("CIRAK_TLS_KEY")
	if cert == "" && key == "" {
		return
	}
	if cert == "" || key == "" {
		rep.refuse("tls: a certificate and a key are both required")
		return
	}
	data, err := d.readFile(cert)
	if err != nil {
		rep.refuse("tls.cert_file is unreadable")
		return
	}
	notAfter, err := parseCertificateNotAfter(data)
	if err != nil {
		rep.refuse("tls.cert_file %v", err)
		return
	}
	switch left := notAfter.Sub(now); {
	case left <= 0:
		rep.refuse("tls certificate expired on %s", notAfter.UTC().Format("2006-01-02"))
	case left < 14*24*time.Hour:
		rep.warn("tls certificate expires in %d day(s), on %s", int(left.Hours()/24), notAfter.UTC().Format("2006-01-02"))
	default:
		rep.ok("tls certificate valid until %s", notAfter.UTC().Format("2006-01-02"))
	}
	if info, err := os.Lstat(key); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		rep.refuse("tls.key_file must be a private regular file (mode 0600)")
	} else {
		rep.ok("tls key present and private")
	}
}

// checkOpenCodeLive performs the reads the Bridge makes (GET /session/status and GET /session): reachability, and
// a 401 or 403 is a credential problem. The version is "unknown" (no supported endpoint, D30 Q7).
func checkOpenCodeLive(rep *report, ld *loaded, env func(string) string, d doctorDeps, ctx context.Context) {
	password := ""
	switch {
	case unresolved(ld, "harness.opencode.password_credential"):
		rep.unverified("opencode: the password is a systemd credential that is not visible here, so the live reads cannot be tried with it")
		return
	case env("OPENCODE_SERVER_PASSWORD") != "" || env("OPENCODE_SERVER_PASSWORD_FILE") != "":
		p, err := readSecret(env, "OPENCODE_SERVER_PASSWORD")
		if err != nil {
			rep.refuse("opencode credentials: %v", err)
			return
		}
		password = p
	}
	base := env("OPENCODE_URL")
	if base == "" {
		base = defaultOpenCode
	}
	if _, err := opencode.New(base, env("OPENCODE_SERVER_USERNAME"), password); err != nil {
		return // already refused by the static check, which does not print the URL
	}
	res := d.openCode(ctx, base, env("OPENCODE_SERVER_USERNAME"), password)
	switch {
	case res.OK:
		rep.ok("opencode reachable at %s: status and session reads answered (version: unknown)", config.RedactURL(base))
	case res.Status == 401 || res.Status == 403:
		rep.refuse("opencode answered HTTP %d: a credential problem (check the username and the password file)", res.Status)
	case res.Status != 0:
		rep.warn("opencode answered HTTP %d to a read", res.Status)
	default:
		rep.warn("opencode is not reachable at %s (is it running? systemctl --user status opencode)", config.RedactURL(base))
	}
}

// checkSockets: the admin and dashboard socket directories and sockets, and the listener.
func checkSockets(rep *report, env func(string) string, cfg ingress, d doctorDeps) {
	for _, s := range []struct{ name, path string }{{"writer.admin_socket", env("CIRAK_ADMIN_SOCKET")}, {"dashboard.socket", env("CIRAK_DASHBOARD_SOCKET")}} {
		if s.path == "" {
			continue
		}
		dir := filepath.Dir(s.path)
		switch exists, err := privateDir(dir, d.uid); {
		case err != nil:
			rep.refuse("%s: %v", s.name, err)
			continue
		case !exists:
			rep.warn("%s: the directory %s does not exist yet (a service creates it, for example with RuntimeDirectory=)", s.name, dir)
			continue
		}
		info, err := os.Lstat(s.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			rep.ok("%s: directory private (drwx------), no socket yet (the service is not running)", s.name)
		case err != nil:
			rep.refuse("%s: %v", s.name, unwrapErr(err))
		case info.Mode()&os.ModeSocket == 0:
			rep.refuse("%s: %s exists and is not a socket", s.name, s.path)
		case info.Mode().Perm() != 0o600:
			rep.refuse("%s: the socket must be srw------- (mode 0600), not %o", s.name, info.Mode().Perm())
		default:
			if err := d.dial("unix", s.path, 2*time.Second); err != nil {
				rep.warn("%s: stale socket (nothing answers): remove %s only after the service is stopped", s.name, s.path)
			} else {
				rep.ok("%s: socket srw------- and answering", s.name)
			}
		}
	}
	if cfg.addr != "" {
		if cfg.remote == nil {
			rep.ok("listener %s is loopback (local mode)", cfg.addr)
		} else if cfg.cert != "" && cfg.key != "" {
			rep.ok("remote mode: TLS pair, public origin and principal are consistent")
		}
		if err := d.dial("tcp", cfg.addr, 2*time.Second); err != nil {
			rep.warn("nothing is listening on %s yet (the service is not running)", cfg.addr)
		} else {
			rep.ok("something is listening on %s", cfg.addr)
		}
	}
}

// checkLifecycleHost: the host rules of each lifecycle root and the overlay directory (F-H5-1..3).
func checkLifecycleHost(rep *report, env func(string) string, uid int) {
	cfg, dir, err := lifecycleConfig(env, uid)
	if err != nil || cfg == nil {
		return // refused or off: already reported by the static check
	}
	for _, r := range cfg.Roots {
		info, err := os.Lstat(r.Path)
		if err != nil {
			rep.refuse("lifecycle root %s: %v", r.Path, unwrapErr(err))
			continue
		}
		owner, ok := fileOwner(info)
		switch {
		case !ok || (owner != 0 && int(owner) != uid):
			rep.refuse("lifecycle root %s must be owned by root or the service user", r.Path)
		case !canReadExec(r.Path):
			rep.refuse("lifecycle root %s is not readable and traversable by the service user", r.Path)
		default:
			rep.ok("lifecycle root %s: owner and r-x as the service user", r.Path)
		}
		if g, err := os.Lstat(filepath.Join(r.Path, ".git")); err == nil {
			if f, err := os.Open(filepath.Join(r.Path, ".git")); err != nil {
				rep.refuse("lifecycle root %s: its .git is not readable by the service user", r.Path)
			} else {
				f.Close()
				_ = g
			}
		}
	}
	if dir != "" {
		switch exists, err := privateDir(dir, uid); {
		case err != nil:
			rep.refuse("overlay.dir: %v", err)
		case !exists:
			rep.warn("overlay.dir %s does not exist yet (StateDirectory= creates it for a service)", dir)
		case !canWriteDir(dir):
			rep.refuse("overlay.dir %s is not writable by the service user", dir)
		default:
			rep.ok("overlay.dir is private and writable")
		}
	}
}

// checkACLHost: the shape and the overlap of the allowlist, and the Claude files (stat only: the wrapper is never read).
func checkACLHost(rep *report, env func(string) string) {
	roots := splitList(env("CIRAK_ALLOWED_ROOTS"), string(os.PathListSeparator))
	sessions := splitList(env("CIRAK_ALLOWED_SESSIONS"), ",")
	seen := map[string]bool{}
	dup := 0
	for _, s := range sessions {
		if seen[s] {
			dup++
		}
		seen[s] = true
	}
	if dup > 0 {
		rep.warn("acl.sessions lists %d session ID(s) more than once", dup)
	}
	sorted := append([]string(nil), roots...)
	sort.Strings(sorted)
	overlaps := 0
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] == sorted[i] || strings.HasPrefix(sorted[j], strings.TrimSuffix(sorted[i], "/")+"/") {
				overlaps++
			}
		}
	}
	if overlaps > 0 {
		rep.warn("acl.roots has %d root(s) nested inside or equal to another (each session still needs its own entry)", overlaps)
	}
	if cmd := env("CIRAK_CLAUDE_COMMAND"); cmd != "" {
		info, err := os.Stat(cmd)
		switch {
		case err != nil:
			rep.refuse("harness.claude.command: %v", unwrapErr(err))
		case !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0:
			rep.refuse("harness.claude.command must be a regular executable file")
		case info.Mode().Perm()&0o022 != 0:
			rep.refuse("harness.claude.command must not be group or world writable")
		default:
			rep.ok("harness.claude.command: a regular executable file, not writable by others (its content is not read)")
		}
	}
}

// checkEnvironmentNames lists the names in effect, never a value.
func checkEnvironmentNames(rep *report, environ []string, fromSource bool) {
	var cirak, legacy, upstream []string
	for _, kv := range environ {
		name, v, _ := strings.Cut(kv, "=")
		if v == "" {
			continue
		}
		switch {
		case strings.HasPrefix(name, envPrefix):
			cirak = append(cirak, name)
		case strings.HasPrefix(name, legacyPrefix):
			legacy = append(legacy, name)
		case strings.HasPrefix(name, "OPENCODE_"):
			upstream = append(upstream, name)
		}
	}
	sort.Strings(cirak)
	sort.Strings(legacy)
	sort.Strings(upstream)
	src := "in this process"
	if fromSource {
		src = "in the source"
	}
	rep.ok("environment %s: %d CIRAK_ name(s) %v, %d legacy name(s) %v, OPENCODE_ names unchanged (not legacy) %v", src, len(cirak), cirak, len(legacy), legacy, upstream)
}

// doctorBaseTools are the 11 tools every daemon registers; the others of the 17 of the contract golden depend on the
// configuration.
var doctorBaseTools = []string{"find_sessions", "get_execution_result", "get_execution_status", "get_harness_capabilities", "get_repository", "get_session",
	"interrupt_execution", "list_repositories", "list_sessions", "read_session_history", "send_prompt"}
var doctorArchiveTools = []string{"archive_session", "close_session", "unarchive_session"}

// expectedTools is the surface the configuration asks for: the 11 base tools, reply_permission unless the relay
// is off, list_catalog with a lifecycle config, create_session when it is not read-only; the archive trio is
// allowed (not required) with an overlay and no read-only, because a second writer without the lock lacks them.
func expectedTools(env func(string) string, uid int, readOnly bool) (required, allowed map[string]bool) {
	required, allowed = map[string]bool{}, map[string]bool{}
	for _, t := range doctorBaseTools {
		required[t], allowed[t] = true, true
	}
	if mode, err := permissionRelayMode(env); err == nil && mode != "off" {
		required["reply_permission"], allowed["reply_permission"] = true, true
	}
	if cfg, overlayDir, err := lifecycleConfig(env, uid); err == nil && cfg != nil {
		required["list_catalog"], allowed["list_catalog"] = true, true
		if !readOnly {
			required["create_session"], allowed["create_session"] = true, true
			if overlayDir != "" {
				for _, t := range doctorArchiveTools {
					allowed[t] = true
				}
			}
		}
	}
	return required, allowed
}

// checkSmoke is the bounded handshake (D30 Q9): initialize and tools/list against the already-running local
// service. It sends no prompt and mutates nothing. The token is read through readSecret and never printed.
func checkSmoke(rep *report, ld *loaded, env func(string) string, cfg ingress, d doctorDeps, ctx context.Context) {
	if cfg.remote != nil {
		rep.unverified("smoke: only the local endpoint is exercised; this is remote mode")
		return
	}
	if unresolved(ld, "token.credential") {
		rep.unverified("smoke: the token is a systemd credential that is not visible here")
		return
	}
	if env("CIRAK_TOKEN") == "" && env("CIRAK_TOKEN_FILE") == "" {
		rep.unverified("smoke: no token source is visible to this process")
		return
	}
	token, err := readSecret(env, "CIRAK_TOKEN")
	if err != nil {
		rep.refuse("smoke: %v", err)
		return
	}
	endpoint := "http://" + cfg.addr + "/mcp"
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	names, err := d.smoke(cctx, endpoint, token)
	if err != nil {
		rep.warn("smoke: no answer from %s (is the service running? start it, then run this again)", endpoint)
		return
	}
	required, allowed := expectedTools(env, d.uid, cfg.readOnly)
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	var missing, extra []string
	for n := range required {
		if !got[n] {
			missing = append(missing, n)
		}
	}
	for n := range got {
		if !allowed[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	switch {
	case len(missing) > 0:
		rep.refuse("smoke: tools/list lacks %v", missing)
	case len(extra) > 0:
		rep.refuse("smoke: tools/list shows a wider surface than configured: %v", extra)
	default:
		rep.ok("smoke: initialize and tools/list answered, %d tools (%d required by the configuration), no prompt sent", len(names), len(required))
	}
}

// ---- production wiring ----

func productionDoctor(args []string, getenv func(string) string, environ []string, stdout, stderr io.Writer) int {
	return runDoctor(args, productionDoctorDeps(getenv, environ, stdout, stderr))
}

func productionDoctorDeps(getenv func(string) string, environ []string, stdout, stderr io.Writer) doctorDeps {
	return doctorDeps{
		getenv: getenv, environ: environ, uid: os.Geteuid(), now: time.Now, stdout: stdout, stderr: stderr,
		showUnit: prodShowUnit, readFile: osReadEnvFile, openCode: liveOpenCode, dial: dialOnce, smoke: smokeCall,
		stageDir: prodStageDir, random: rand.Reader,
	}
}

// prodShowUnit is the one command the doctor runs: the read-only `systemctl show` of a unit's environment.
func prodShowUnit(ctx context.Context, name string) (string, error) {
	if !unitNamePattern.MatchString(name) {
		return "", errors.New("invalid unit name")
	}
	out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", "Environment", "-p", "EnvironmentFiles", "-p", "UnsetEnvironment", "--", name).Output()
	return string(out), err
}

func dialOnce(network, addr string, timeout time.Duration) error {
	c, err := net.DialTimeout(network, addr, timeout)
	if err != nil {
		return err
	}
	return c.Close()
}

// liveOpenCode makes the two reads of the Bridge's own client: GET /session/status and GET /session.
func liveOpenCode(ctx context.Context, baseURL, username, password string) openCodeLive {
	c, err := opencode.New(baseURL, username, password)
	if err != nil {
		return openCodeLive{}
	}
	read := func(err error) (openCodeLive, bool) {
		var he *opencode.HTTPError
		switch {
		case err == nil:
			return openCodeLive{}, false
		case errors.As(err, &he):
			return openCodeLive{Status: he.Status}, true
		}
		return openCodeLive{}, true
	}
	if _, err := c.Status(ctx); err != nil {
		r, _ := read(err)
		return r
	}
	if _, err := c.List(ctx); err != nil {
		r, _ := read(err)
		return r
	}
	return openCodeLive{OK: true}
}

// smokeCall is initialize + tools/list through the SDK client. It sends nothing else.
func smokeCall(ctx context.Context, endpoint, token string) ([]string, error) {
	sess, err := mcpclient.Connect(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	res, err := sess.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, t := range res.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}
