package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/config"
	"github.com/nevzatcirak/cirak/internal/dashboard"
	"github.com/nevzatcirak/cirak/internal/execution"
	"github.com/nevzatcirak/cirak/internal/security"
	"github.com/nevzatcirak/cirak/internal/version"
)

// `cirak config check` (ADR-027 section 1.5): the static validation an operator runs before restarting the
// service and in CI. It starts no listener, opens no socket, makes no network call, takes no lock and writes
// nothing. It resolves the configuration exactly as the daemon does (loadEffective), then calls the daemon's
// own validators on the result; it does not copy a rule.
//
// Output is value-free: it names keys, files and variables, never a value; a secret is reported only as set
// or unset. Exit 0 clean, 1 warnings or unverified items, 2 refused.

const configCheckUsage = `usage: cirak config check [--config path] [--listen addr] [--read-only] [--token-file path] [--print-effective]

Validates the configuration that a start would use (flag, environment, config file, default), with no listener
and no network. Exit 0 clean, 1 warnings or unverified items, 2 refused. --print-effective lists every key with
its source; a secret reference is shown only as set or unset.

`

type report struct {
	out      io.Writer
	refused  int
	warnings int
}

func (r *report) ok(format string, a ...any) {
	fmt.Fprintf(r.out, "ok       %s\n", fmt.Sprintf(format, a...))
}
func (r *report) warn(format string, a ...any) {
	r.warnings++
	fmt.Fprintf(r.out, "warning  %s\n", fmt.Sprintf(format, a...))
}
func (r *report) unverified(format string, a ...any) {
	r.warnings++
	fmt.Fprintf(r.out, "UNVERIFIED %s\n", fmt.Sprintf(format, a...))
}
func (r *report) refuse(format string, a ...any) {
	r.refused++
	fmt.Fprintf(r.out, "REFUSED  %s\n", fmt.Sprintf(format, a...))
}

func (r *report) code() int {
	switch {
	case r.refused > 0:
		return 2
	case r.warnings > 0:
		return 1
	}
	return 0
}

// runConfig is the "config" subcommand.
func runConfig(args []string, getenv func(string) string, environ []string, uid int, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprint(stderr, configCheckUsage)
		return 2
	}
	fs, flags := newFlagSet("cirak config check", io.Discard)
	printEffective := fs.Bool("print-effective", false, "list every key with its source")
	fs.Usage = func() { fmt.Fprint(stderr, configCheckUsage) }
	// The check takes the daemon's flags, so it resolves what a start with the same arguments would.
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fmt.Fprint(stderr, configCheckUsage)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rep := &report{out: stdout}
	checkConfiguration(rep, *flags, getenv, environ, uid, *printEffective)
	switch rep.code() {
	case 0:
		fmt.Fprintln(stdout, "result: clean")
	case 1:
		fmt.Fprintln(stdout, "result: warnings (exit 1)")
	default:
		fmt.Fprintln(stdout, "result: refused (exit 2)")
	}
	return rep.code()
}

func checkConfiguration(rep *report, f serveFlags, getenv func(string) string, environ []string, uid int, printEffective bool) {
	env0, legacy, err := cirakEnv(getenv, legacyPhaseFor(version.Version()))
	if err != nil {
		rep.refuse("environment: %v", err)
		return
	}
	for _, w := range legacy {
		rep.warn("%s", w)
	}
	ld, err := loadEffective(f, env0, uint32(uid), processHome)
	if err != nil {
		rep.refuse("%v", err)
		return
	}
	fmt.Fprintln(rep.out, ld.describePath())
	for _, w := range ld.warnings {
		rep.warn("%s", w)
	}
	env := ld.res.Get
	now := nowFunc()

	// The token. A source this process cannot see (a credential outside systemd, or a value in the unit's
	// EnvironmentFile=) is UNVERIFIED, never PASS.
	token, tokenKnown := "", false
	switch {
	case unresolved(ld, "token.credential"):
		rep.unverified("token: it is a systemd credential and CREDENTIALS_DIRECTORY is not set here; run the check under the unit or pass the unit's source (not in this slice)")
	case env("CIRAK_TOKEN") == "" && env("CIRAK_TOKEN_FILE") == "":
		rep.unverified("token: no token source is visible to this process (a token in the unit's EnvironmentFile= or a credential cannot be seen here)")
	default:
		t, err := readSecret(env, "CIRAK_TOKEN")
		if err != nil {
			rep.refuse("%v", err)
		} else {
			token, tokenKnown = t, true
		}
	}
	var cfg ingress
	if tokenKnown {
		cfg, err = ingressWithToken(env, token, now)
	} else {
		cfg, err = readIngressAt(env, now)
	}
	if err != nil {
		rep.refuse("ingress: %v", err)
	} else {
		rep.ok("ingress settings (listen, mode, token lifetime)")
		if tokenKnown {
			rep.ok("token source readable, private and long enough")
		}
		for _, w := range cfg.warnings {
			rep.warn("%s", w)
		}
	}

	checkOpenCode(rep, ld, env)
	roots, sessions := checkACL(rep, env, uid)
	checkWriter(rep, env, sessions, cfg.readOnly, uid)
	checkClaude(rep, env, roots, sessions)
	checkLifecycle(rep, env, uid)
	checkDashboard(rep, env, uid)
	checkPermission(rep, env)
	checkLiveness(rep, env)
	checkUnknownNames(rep, environ)

	if printEffective {
		fmt.Fprintln(rep.out, "effective settings (key, source, value; a secret reference is set or unset):")
		for _, e := range ld.res.Describe() {
			fmt.Fprintf(rep.out, "  %-40s %-12s %s\n", e.Key, e.Source, e.Value)
		}
	}
}

func unresolved(ld *loaded, key string) bool {
	for _, u := range ld.res.Unresolved {
		if u == key {
			return true
		}
	}
	return false
}

func checkOpenCode(rep *report, ld *loaded, env func(string) string) {
	password := ""
	switch {
	case unresolved(ld, "harness.opencode.password_credential"):
		rep.unverified("harness.opencode.password_credential: a systemd credential and CREDENTIALS_DIRECTORY is not set here")
	case env("OPENCODE_SERVER_PASSWORD") != "" || env("OPENCODE_SERVER_PASSWORD_FILE") != "":
		p, err := readSecret(env, "OPENCODE_SERVER_PASSWORD")
		if err != nil {
			rep.refuse("%v", err)
			return
		}
		password = p
	}
	base := env("OPENCODE_URL")
	if base == "" {
		base = "http://127.0.0.1:4096"
	}
	// The URL is not printed: it may carry userinfo.
	if _, err := opencode.New(base, env("OPENCODE_SERVER_USERNAME"), password); err != nil {
		rep.refuse("harness.opencode.url is not a valid OpenCode base URL")
		return
	}
	rep.ok("harness.opencode (url and credentials shape)")
}

// rootOwner is fileOwner; a variable so a test can present a root owned by somebody else.
var rootOwner = fileOwner

// checkACL validates the allowlist with the daemon's own policy constructor and adds the root rules of
// section 1.5: owned by root or the service user, and readable and traversable by it.
func checkACL(rep *report, env func(string) string, uid int) (roots, sessions []string) {
	roots = splitList(env("CIRAK_ALLOWED_ROOTS"), string(os.PathListSeparator))
	sessions = splitList(env("CIRAK_ALLOWED_SESSIONS"), ",")
	bad := false
	for _, id := range sessions {
		if err := config.ValidACLSession(id); err != nil {
			rep.refuse("acl.sessions: %v", err)
			bad = true
		}
	}
	policy, err := security.New(strings.Split(env("CIRAK_ALLOWED_ROOTS"), string(os.PathListSeparator)), strings.Split(env("CIRAK_ALLOWED_SESSIONS"), ","))
	if err != nil {
		rep.refuse("acl: %v", err)
		return roots, sessions
	}
	for _, r := range policy.Roots {
		info, err := os.Stat(r)
		if err != nil {
			rep.refuse("acl.roots: %s: %v", r, unwrapErr(err))
			bad = true
			continue
		}
		owner, ok := rootOwner(info)
		if !ok || (owner != 0 && owner != uint32(uid)) {
			rep.refuse("acl.roots: %s must be owned by root or the service user", r)
			bad = true
		}
		if !canReadExec(r) {
			rep.refuse("acl.roots: %s is not readable and traversable by the service user", r)
			bad = true
		}
	}
	if !bad {
		rep.ok("acl (%d root(s), %d session(s); roots owned by root or the service user and readable)", len(policy.Roots), len(policy.Sessions))
	}
	return roots, sessions
}

func checkWriter(rep *report, env func(string) string, sessions []string, readOnly bool, uid int) {
	dir := env("CIRAK_WRITER_LOCK_DIR")
	if !readOnly && len(sessions) > 0 && dir == "" {
		rep.refuse("writer.lock_dir is required for a writer (set server.read_only for a reader)")
		return
	}
	if dir != "" {
		switch exists, err := privateDir(dir, uid); {
		case err != nil:
			rep.refuse("writer.lock_dir: %v", err)
		case !exists:
			rep.warn("writer.lock_dir %s does not exist yet (a service needs StateDirectory= or an existing 0700 directory)", dir)
		default:
			rep.ok("writer.lock_dir is a private directory")
		}
	}
	if sock := env("CIRAK_ADMIN_SOCKET"); sock != "" {
		if _, err := os.Stat(filepath.Dir(sock)); err != nil {
			rep.warn("writer.admin_socket: the directory %s does not exist yet", filepath.Dir(sock))
		} else {
			rep.ok("writer.admin_socket directory exists")
		}
	}
}

func checkClaude(rep *report, env func(string) string, roots, sessions []string) {
	cfg, err := claudeConfig(env, roots, sessions)
	if err != nil {
		rep.refuse("harness.claude: %v", err)
		return
	}
	if cfg == nil {
		rep.ok("harness.claude is off")
		return
	}
	if _, err := claudecode.ParseVersions(cfg.Versions); err != nil {
		rep.refuse("harness.claude.versions: %v", err)
		return
	}
	info, err := os.Lstat(cfg.SessionsFile)
	switch {
	case err != nil:
		rep.refuse("harness.claude.sessions_file: %v", unwrapErr(err))
		return
	case !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0:
		rep.refuse("harness.claude.sessions_file must be a private regular file (mode 0600)")
		return
	}
	rep.ok("harness.claude (command, versions, sessions file)")
}

func checkLifecycle(rep *report, env func(string) string, uid int) {
	cfg, _, err := lifecycleConfig(env, uid)
	if err != nil {
		rep.refuse("lifecycle: %v", err)
		return
	}
	if cfg == nil {
		rep.ok("lifecycle is off")
		return
	}
	paths := make([]string, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		paths = append(paths, r.Path)
	}
	if _, err := security.NewLifecycleRoots(paths); err != nil {
		rep.refuse("lifecycle roots: %v", err)
		return
	}
	rep.ok("lifecycle (%d root(s))", len(cfg.Roots))
}

func checkDashboard(rep *report, env func(string) string, uid int) {
	c, err := dashboard.ConfigFromEnv(env)
	if err != nil {
		rep.refuse("dashboard: %v", err)
		return
	}
	if _, err := dashboard.RedactorFromEnv(env); err != nil {
		rep.refuse("dashboard.redact_extra: %v", err)
		return
	}
	if c.Socket != "" {
		switch exists, err := privateDir(filepath.Dir(c.Socket), uid); {
		case err != nil:
			rep.refuse("dashboard.socket directory: %v", err)
			return
		case !exists:
			rep.warn("dashboard.socket: the directory %s does not exist yet", filepath.Dir(c.Socket))
			return
		}
	}
	rep.ok("dashboard settings")
}

func checkPermission(rep *report, env func(string) string) {
	if _, err := permissionRelayMode(env); err != nil {
		rep.refuse("permission.relay: %v", err)
		return
	}
	if _, err := permissionIncarnationMode(env); err != nil {
		rep.refuse("permission.incarnation: %v", err)
		return
	}
	rep.ok("permission relay and incarnation")
}

// checkUnknownNames warns, by name only, about variables of either prefix (the CIRAK_ one and its legacy twin) that the daemon does not know.
// checkLiveness reuses the start path's validator (ADR-017 A2 S3, A2.6): the CIRAK_* names and the
// ADR-027 liveness.* keys resolve to the same value, and an out-of-range value a start would refuse is
// refused here too.
func checkLiveness(rep *report, env func(string) string) {
	if _, err := execution.ParseLivenessEnv(env); err != nil {
		rep.refuse("liveness: %v", err)
		return
	}
	rep.ok("liveness thresholds (no_progress_s, tool_long_s)")
}

func checkUnknownNames(rep *report, environ []string) {
	known := config.KnownEnvNames()
	var unknown []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		var canonical string
		switch {
		case strings.HasPrefix(name, envPrefix):
			canonical = name
		case strings.HasPrefix(name, legacyPrefix):
			canonical = envPrefix + strings.TrimPrefix(name, legacyPrefix)
		default:
			continue
		}
		if !known[canonical] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, n := range unknown {
		rep.warn("unknown environment name %s is not read by the daemon", n)
	}
}

// privateDir checks an existing directory is owned by the service user with mode 0700. A missing directory is
// not an error here (exists false): a service may create it at start.
func privateDir(path string, uid int) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, unwrapErr(err)
	}
	if !info.IsDir() {
		return true, fmt.Errorf("%s is not a directory", path)
	}
	owner, ok := fileOwner(info)
	if !ok || owner != uint32(uid) {
		return true, fmt.Errorf("%s must be owned by the service user", path)
	}
	if info.Mode().Perm() != 0o700 {
		return true, fmt.Errorf("%s must have mode 0700", path)
	}
	return true, nil
}

func unwrapErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
