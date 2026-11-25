package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/config"
	"github.com/nevzatcirak/cirak/internal/security"
)

// `cirak init` core (ADR-027 section 2, slice S2): detect OpenCode, check the workspace roots, generate a
// token, write the config and the lifecycle file, and print the connector values and the smoke steps. It
// installs no service, writes no unit or plist, runs no doctor, and has no remote or TLS path (S3, S4).
//
// init writes ONLY the explicit target paths, never follows a symlink, and never applies chown, chmod or
// setfacl: where a fix needs them it PRINTS the exact command. The token is shown once, on a terminal only.

const initUsage = `usage: cirak init [--yes] [--local] [--listen addr] [--root path]... [--session id]... [--opencode-url url]
                  [--service-user name] [--config path] [--token-file path] [--expires RFC3339]
                  [--lifecycle-root path]... [--force]
                  [--install-service [--launch-daemon] [--claude-home path]] [--no-service]

Interactive by default (a terminal is required); --yes never prompts and needs --root, --session and
--token-file. A token is generated only when --token-file names a NEW file; an existing file is referenced.
Local mode only in this release: --remote, --tls-cert and --tls-key are refused. --install-service renders a
systemd unit (linux) or a launchd plist (macOS) and places it only where you can write it, otherwise it prints
the install command; init never starts or loads a service.
`

// Defaults of section 2 item 3: 64 characters, 7 days, and the pair written explicitly.
// serviceLockDir is the persistent writer lock directory of a service user (ADR-027 section 2 item 5); a
// variable only so a test can point it at a temp directory.
var serviceLockDir = "/var/lib/cirak/writer-locks"

const (
	tokenChars      = 64
	tokenValidity   = 7 * 24 * time.Hour
	tokenMaxLife    = "168h"
	defaultListen   = "127.0.0.1:8080"
	defaultOpenCode = "http://127.0.0.1:4096"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// initOptions are the parsed arguments.
type initOptions struct {
	yes, local, remote, force bool
	installService, noService bool
	launchDaemon              bool
	claudeHome                string
	listen, opencodeURL       string
	serviceUser, configPath   string
	tokenFile, expires        string
	tlsCert, tlsKey           string
	roots, sessions           listFlag
	lifecycleRoots            listFlag
}

func parseInitFlags(args []string, stderr io.Writer) (*initOptions, error) {
	o := &initOptions{}
	fs := flag.NewFlagSet("cirak init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.yes, "yes", false, "never prompt; every non-secret answer is a flag")
	fs.BoolVar(&o.local, "local", false, "local mode (the only mode in this release)")
	fs.BoolVar(&o.remote, "remote", false, "remote mode (not in this release)")
	fs.StringVar(&o.listen, "listen", "", "listen address (loopback)")
	fs.Var(&o.roots, "root", "workspace root (repeatable)")
	fs.Var(&o.sessions, "session", "native session ID of the ACL (repeatable)")
	fs.Var(&o.lifecycleRoots, "lifecycle-root", "lifecycle root that lets Chat create sessions (repeatable; default none)")
	fs.StringVar(&o.opencodeURL, "opencode-url", "", "OpenCode base URL (loopback)")
	fs.StringVar(&o.serviceUser, "service-user", "", "the user the service runs as (empty: you, local mode)")
	fs.StringVar(&o.configPath, "config", "", "config file to write")
	fs.StringVar(&o.tokenFile, "token-file", "", "token file; generated when it does not exist")
	fs.StringVar(&o.expires, "expires", "", "rotation deadline, RFC3339, within 168h")
	fs.StringVar(&o.tlsCert, "tls-cert", "", "TLS certificate (not in this release)")
	fs.StringVar(&o.tlsKey, "tls-key", "", "TLS key (not in this release)")
	fs.BoolVar(&o.installService, "install-service", false, "write a systemd unit (linux) or a launchd plist (macOS); never starts it")
	fs.BoolVar(&o.noService, "no-service", false, "install no service (the default)")
	fs.BoolVar(&o.launchDaemon, "launch-daemon", false, "macOS: a system LaunchDaemon instead of the user LaunchAgent (needs --service-user)")
	fs.StringVar(&o.claudeHome, "claude-home", "", "render the unit for a Claude Code adapter with this HOME (no NoNewPrivileges)")
	fs.BoolVar(&o.force, "force", false, "replace an existing config (a backup is made)")
	fs.Usage = func() { fmt.Fprint(stderr, initUsage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, errors.New("unexpected argument " + fs.Arg(0))
	}
	switch {
	case o.remote, o.tlsCert != "", o.tlsKey != "":
		return nil, errors.New("remote and TLS setup are not in this release (ADR-027 S4); init sets up local mode")
	case o.installService && o.noService:
		return nil, errors.New("--install-service and --no-service are exclusive")
	case (o.launchDaemon || o.claudeHome != "") && !o.installService:
		return nil, errors.New("--launch-daemon and --claude-home need --install-service")
	case o.local && o.remote:
		return nil, errors.New("--local and --remote are exclusive")
	}
	return o, nil
}

// userInfo is the service user as the ownership rules need it.
type userInfo struct {
	Name, Group string
	UID, GID    int
	GIDs        []int // every group of the user
}

// openCodeProbe is the result of the OpenCode detection.
type openCodeProbe struct {
	Reachable    bool
	Unauthorized bool
}

// initDeps is every side effect of init, injected: tests use temp directories and fakes only.
type initDeps struct {
	getenv         func(string) string
	stdin          io.Reader
	stdout, stderr io.Writer
	isTTY          bool // standard input is a terminal
	outTTY         bool // standard output is a terminal: the token value is shown only then
	uid, gid       int
	now            func() time.Time
	random         io.Reader
	lookup         func(name string) (userInfo, error) // "" is the invoking user
	probe          func(ctx context.Context, baseURL, username, password string) openCodeProbe
	readPassword   func(r *bufio.Reader) (string, error)   // reads one line through the wizard's own reader
	run            func(name string, args ...string) error // only the read-only sudo -u <user> test -r/-x probe
	stageDir       func() (string, error)
	home           func() (string, error)
	goos           string                               // runtime.GOOS: which service kind is possible
	executable     func() (string, error)               // the path of this binary, for a launchd plist
	makeTemp       func(pattern string) (string, error) // a private temporary directory (the generated-config check)
}

// runInit is the "init" subcommand.
func runInit(args []string, d initDeps) int {
	o, err := parseInitFlags(args, d.stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(d.stderr, "init:", err)
		return 2
	}
	w := &wizard{d: d, o: o, in: bufio.NewReader(d.stdin)}
	if err := w.run(); err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(d.stderr, "init:", err)
			return 2
		}
		fmt.Fprintln(d.stderr, "init:", err)
		return 1
	}
	return 0
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

type wizard struct {
	d  initDeps
	o  *initOptions
	in *bufio.Reader

	svc       userInfo
	sameUser  bool // the invoking uid is the service uid: init writes the final files itself
	operator  bool // no --service-user: local mode, the files are the operator's
	listen    string
	ocURL     string
	cfgPath   string
	cfgDir    string
	tokenPath string
	pwPath    string
	lcPath    string
	lockDir   string

	token       string // the newly generated token (never persisted anywhere but its file)
	newToken    bool
	expires     time.Time
	ocUser      string
	ocPwFile    string // the existing password file the config will reference
	typedPw     string // a password typed after a 401: goes to pwPath only
	tokenShown  bool
	stagedToken string // set when the new token was only staged, not yet at its final path
	svcKind     string // "", systemd, systemd-dropin, launchd-agent, launchd-daemon
	svcFiles    []svcFile
	svcStage    string // a staging directory made only for the service files
	logDir      string // the LaunchAgent log directory init creates
}

func (w *wizard) say(format string, a ...any) { fmt.Fprintf(w.d.stdout, format+"\n", a...) }

// ask prompts for one line; an empty answer takes def.
func (w *wizard) ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(w.d.stdout, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(w.d.stdout, "%s: ", label)
	}
	line, err := w.in.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("input ended before the answer to: " + label)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

func (w *wizard) askList(label string) ([]string, error) {
	var out []string
	for {
		v, err := w.ask(label+" (empty to finish)", "")
		if err != nil {
			return nil, err
		}
		if v == "" {
			return out, nil
		}
		out = append(out, v)
	}
}

func (w *wizard) askYes(label string) (bool, error) {
	v, err := w.ask(label+" [y/N]", "")
	return strings.EqualFold(v, "y") || strings.EqualFold(v, "yes"), err
}

func (w *wizard) run() error {
	o := w.o
	if !o.yes && !w.d.isTTY {
		return usagef("interactive init needs a terminal; use --yes with --root, --session and --token-file")
	}
	if o.yes {
		var missing []string
		if len(o.roots) == 0 {
			missing = append(missing, "--root")
		}
		if len(o.sessions) == 0 {
			missing = append(missing, "--session")
		}
		if o.tokenFile == "" {
			missing = append(missing, "--token-file")
		}
		if len(missing) > 0 {
			return usagef("--yes never prompts: %s required", strings.Join(missing, ", "))
		}
	}
	if err := w.resolveUser(); err != nil {
		return err
	}
	if err := w.resolvePaths(); err != nil {
		return err
	}
	if !o.yes {
		if err := w.interview(); err != nil {
			return err
		}
	}
	if err := w.rejectUnsafePaths(); err != nil {
		return err
	}
	if err := w.planService(); err != nil {
		return err
	}
	w.listen, w.ocURL = o.listen, o.opencodeURL
	if w.listen == "" {
		w.listen = defaultListen
	}
	if w.ocURL == "" {
		w.ocURL = defaultOpenCode
	}
	if err := security.ValidateListen(w.listen, false); err != nil {
		return fmt.Errorf("--listen: %w (init sets up local mode)", err)
	}
	if err := validateOpenCodeURL(w.ocURL); err != nil {
		return err
	}
	if err := w.parseExpiry(); err != nil {
		return err
	}
	w.detectOpenCode()
	if err := w.checkRoots(); err != nil {
		return err
	}
	if err := w.prepareToken(); err != nil {
		return err
	}
	cfg, lc, err := w.render()
	if err != nil {
		return err
	}
	if !w.sameUser {
		return w.stage(cfg, lc)
	}
	if err := w.write(cfg, lc); err != nil {
		return err
	}
	if err := w.placeServiceFiles(""); err != nil {
		return err
	}
	return w.finish()
}

// rejectUnsafePaths refuses, before anything is probed, written or printed, a path that holds a newline, a
// carriage return or a NUL: init prints these paths inside commands for root, and such a character cannot be
// shown on one line safely even when quoted.
func (w *wizard) rejectUnsafePaths() error {
	all := append(append([]string(nil), w.o.roots...), w.o.lifecycleRoots...)
	all = append(all, w.cfgPath, w.tokenPath, w.lcPath, w.pwPath, w.lockDir)
	for _, p := range all {
		if strings.ContainsAny(p, "\n\r\x00") {
			return usagef("a path contains a newline, a carriage return or a NUL: refused before anything was written")
		}
	}
	return nil
}

// resolveUser finds the service user. With no --service-user the service is the invoking user (local mode).
func (w *wizard) resolveUser() error {
	u, err := w.d.lookup(w.o.serviceUser)
	if err != nil {
		if w.o.serviceUser != "" {
			return fmt.Errorf("--service-user %s: %w", w.o.serviceUser, err)
		}
		return fmt.Errorf("cannot look up the invoking user: %w", err)
	}
	w.svc, w.operator = u, w.o.serviceUser == ""
	w.sameUser = u.UID == w.d.uid
	return nil
}

func (w *wizard) resolvePaths() error {
	home, herr := w.d.home()
	cfgDir := ""
	if w.o.configPath != "" {
		cfgDir = filepath.Dir(w.o.configPath)
		w.cfgPath = w.o.configPath
	} else if !w.operator {
		cfgDir, w.cfgPath = "/etc/cirak", "/etc/cirak/config.json"
	} else {
		base := w.d.getenv("XDG_CONFIG_HOME")
		if base == "" {
			if herr != nil || home == "" {
				return usagef("cannot find a home directory: pass --config")
			}
			base = filepath.Join(home, ".config")
		}
		cfgDir = filepath.Join(base, "cirak")
		w.cfgPath = filepath.Join(cfgDir, "config.json")
	}
	w.cfgDir = cfgDir
	w.tokenPath = w.o.tokenFile
	if w.tokenPath == "" && w.o.yes == false {
		w.tokenPath = filepath.Join(cfgDir, "token")
	}
	w.pwPath = filepath.Join(cfgDir, "opencode-password")
	w.lcPath = filepath.Join(cfgDir, "lifecycle.json")
	if w.operator {
		state := w.d.getenv("XDG_STATE_HOME")
		if state == "" {
			if herr != nil || home == "" {
				return usagef("cannot find a home directory for the writer lock: pass --config under a directory you own")
			}
			state = filepath.Join(home, ".local", "state")
		}
		w.lockDir = filepath.Join(state, "cirak", "writer-locks")
	} else {
		w.lockDir = serviceLockDir
	}
	for name, p := range map[string]string{"--config": w.cfgPath, "--token-file": w.tokenPath, "the writer lock directory": w.lockDir} {
		if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p) {
			return usagef("%s must be an absolute canonical path", name)
		}
	}
	return nil
}

// interview asks for what the flags did not give (a terminal only).
func (w *wizard) interview() error {
	o := w.o
	var err error
	if o.listen == "" {
		if o.listen, err = w.ask("Listen address (loopback)", defaultListen); err != nil {
			return err
		}
	}
	if o.opencodeURL == "" {
		if o.opencodeURL, err = w.ask("OpenCode URL", defaultOpenCode); err != nil {
			return err
		}
	}
	if len(o.roots) == 0 {
		if o.roots, err = w.askList("Workspace root"); err != nil {
			return err
		}
	}
	if len(o.sessions) == 0 {
		if o.sessions, err = w.askList("Native session ID the connector may use (ses_...)"); err != nil {
			return err
		}
	}
	if len(o.roots) == 0 || len(o.sessions) == 0 {
		return usagef("at least one workspace root and one session ID are required")
	}
	if len(o.lifecycleRoots) == 0 {
		if o.lifecycleRoots, err = w.askList("Lifecycle root where Chat may create sessions (default none)"); err != nil {
			return err
		}
	}
	return nil
}

func (w *wizard) parseExpiry() error {
	now := w.d.now().UTC().Truncate(time.Second)
	if w.o.expires == "" {
		w.expires = now.Add(tokenValidity)
		return nil
	}
	t, err := time.Parse(time.RFC3339, w.o.expires)
	if err != nil {
		return usagef("--expires must be RFC3339")
	}
	if !t.After(now) || t.After(now.Add(tokenValidity)) {
		return usagef("--expires must be in the future and within %s (limits.token_max_lifetime)", tokenMaxLife)
	}
	w.expires = t.UTC()
	return nil
}

// validateOpenCodeURL applies the daemon's own OpenCode base-URL rule (loopback only, no userinfo, path, query
// or fragment) before anything is probed, printed or written. The URL may carry a credential, so a diagnostic
// shows it only in the redacted form of `config check --print-effective`.
func validateOpenCodeURL(raw string) error {
	if _, err := opencode.New(raw, "", ""); err != nil {
		return usagef("--opencode-url (%s) is not a valid OpenCode base URL: %v (loopback http or https only; no credentials, path, query or fragment)", config.RedactURL(raw), err)
	}
	return nil
}

// detectOpenCode probes the configured URL (step 1). Nothing is installed or changed; an unreachable server is
// reported, not fatal: the Bridge may start before it.
func (w *wizard) detectOpenCode() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	w.ocUser = w.d.getenv("OPENCODE_SERVER_USERNAME")
	pw := ""
	if f := w.d.getenv("OPENCODE_SERVER_PASSWORD_FILE"); f != "" {
		if v, err := readSecret(func(k string) string {
			if k == "OPENCODE_SERVER_PASSWORD_FILE" {
				return f
			}
			return ""
		}, "OPENCODE_SERVER_PASSWORD"); err == nil {
			pw, w.ocPwFile = v, f
		}
	}
	res := w.d.probe(ctx, w.ocURL, w.ocUser, pw)
	switch {
	case res.Reachable:
		w.say("OpenCode: reachable at %s (version: unknown)", config.RedactURL(w.ocURL))
	case res.Unauthorized:
		w.say("OpenCode: reachable at %s and it asks for credentials", config.RedactURL(w.ocURL))
		if w.o.yes || !w.d.isTTY {
			w.say("  Run interactively, or set OPENCODE_SERVER_USERNAME and OPENCODE_SERVER_PASSWORD_FILE (a 0600 file) and run again.")
			return
		}
		w.askCredentials(ctx)
	default:
		w.say("OpenCode: not reachable at %s. Check it with: systemctl --user status opencode", config.RedactURL(w.ocURL))
	}
}

// askCredentials asks for the username and the password (echo off) and retries once. A password that works is
// kept in memory only to be written to the 0600 password file; it is never printed, logged or put in the config.
func (w *wizard) askCredentials(ctx context.Context) {
	u, err := w.ask("OpenCode username", "opencode")
	if err != nil {
		return
	}
	fmt.Fprint(w.d.stdout, "OpenCode password (hidden): ")
	pw, err := w.d.readPassword(w.in)
	fmt.Fprintln(w.d.stdout)
	if err != nil || pw == "" {
		w.say("OpenCode: no password entered; the config will not reference one")
		return
	}
	res := w.d.probe(ctx, w.ocURL, u, pw)
	if !res.Reachable {
		w.say("OpenCode: the credentials were not accepted; the config will not reference them")
		return
	}
	w.ocUser, w.typedPw = u, pw
	w.say("OpenCode: reachable with the credentials (version: unknown)")
}

// checkRoots applies section 2 item 2 to every workspace and lifecycle root. A failure prints the exact fix and
// stops before anything is written; init never applies the fix.
func (w *wizard) checkRoots() error {
	var bad []string
	for _, r := range append(append([]string(nil), w.o.roots...), w.o.lifecycleRoots...) {
		problems := w.rootProblems(r)
		if len(problems) == 0 {
			w.say("root %s: ok (owned by root or %s, readable and traversable by %s)", r, w.svc.Name, w.svc.Name)
			continue
		}
		bad = append(bad, r)
		for _, p := range problems {
			w.say("root %s: %s", r, p)
		}
		w.say("  fix (init never applies it): chown %s %s && chmod 0710 %s && setfacl -m %s %s", shq("root:"+w.svc.Group), shq(r), shq(r), shq("u:"+w.svc.Name+":rwx"), shq(r))
	}
	if len(bad) > 0 {
		return fmt.Errorf("%d root(s) cannot be used as they are; nothing was written", len(bad))
	}
	return nil
}

func (w *wizard) rootProblems(root string) []string {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return []string{"must be an absolute canonical path other than /"}
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return []string{"does not exist"}
	}
	if resolved != root {
		return []string{"is not canonical (a symlink alias is in the path); use " + resolved}
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return []string{"is not a directory"}
	}
	var out []string
	if owner, ok := rootOwner(info); !ok || (owner != 0 && int(owner) != w.svc.UID) {
		out = append(out, "must be owned by root or "+w.svc.Name)
	}
	if !w.serviceCanReadExec(root, info) {
		out = append(out, w.svc.Name+" needs r-x on it (checked by mode bits; an ACL is not evaluated here)")
	}
	if g, err := os.Lstat(filepath.Join(root, ".git")); err == nil {
		if !w.serviceCanRead(filepath.Join(root, ".git"), g) {
			out = append(out, w.svc.Name+" needs read on its .git")
		}
	}
	return out
}

// serviceCanReadExec: the invoking user's access(2) when the service user is the invoking user; otherwise the
// mode bits for the service user and its groups, then the read-only sudo probe when the operator agrees.
func (w *wizard) serviceCanReadExec(path string, info os.FileInfo) bool {
	if w.sameUser {
		return canReadExec(path)
	}
	if w.svc.UID == 0 || modeAllows(info, w.svc, 0o5) {
		return true
	}
	return w.sudoProbe(path, "-r", "-x")
}

func (w *wizard) serviceCanRead(path string, info os.FileInfo) bool {
	if w.svc.UID == 0 || modeAllows(info, w.svc, 0o4) {
		return true
	}
	if w.sameUser {
		f, err := os.Open(path)
		if err == nil {
			f.Close()
		}
		return err == nil
	}
	return w.sudoProbe(path, "-r")
}

// modeAllows evaluates the owner, group and other bits (want is 4 read, 1 execute, 5 both).
func modeAllows(info os.FileInfo, u userInfo, want os.FileMode) bool {
	owner, ok := fileOwner(info)
	if !ok {
		return false
	}
	perm := info.Mode().Perm()
	if int(owner) == u.UID {
		return perm>>6&want == want
	}
	if g, ok := fileGroup(info); ok {
		for _, id := range append([]int{u.GID}, u.GIDs...) {
			if int(g) == id {
				return perm>>3&want == want
			}
		}
	}
	return perm&want == want
}

// sudoProbe is the optional read-only probe of D30 Q8: `sudo -u <user> test -r|-x <path>`. It is shown to the
// operator, asked first, and only ever test; with --yes it is not run.
func (w *wizard) sudoProbe(path string, tests ...string) bool {
	if w.o.yes || !w.d.isTTY {
		return false
	}
	var cmds []string
	for _, t := range tests {
		cmds = append(cmds, fmt.Sprintf("sudo -u %s test %s %s", shq(w.svc.Name), t, shq(path)))
	}
	w.say("  The mode bits do not grant it. A read-only probe can check an ACL:\n    %s", strings.Join(cmds, "\n    "))
	if ok, err := w.askYes("  Run the probe?"); err != nil || !ok {
		return false
	}
	for _, t := range tests {
		if err := w.d.run("sudo", "-u", w.svc.Name, "test", t, path); err != nil {
			return false
		}
	}
	return true
}

// prepareToken generates a token for a NEW token file, or validates the existing one (section 2 item 3).
func (w *wizard) prepareToken() error {
	if w.tokenPath == "" {
		return usagef("--token-file is required")
	}
	if err := noSymlinkInExistingPrefix(w.tokenPath); err != nil {
		return fmt.Errorf("--token-file: %w", err)
	}
	_, err := os.Lstat(w.tokenPath)
	switch {
	case err == nil:
		env := func(k string) string {
			if k == "CIRAK_TOKEN_FILE" {
				return w.tokenPath
			}
			return ""
		}
		tok, err := readSecret(env, "CIRAK_TOKEN")
		if err != nil {
			return fmt.Errorf("the existing --token-file is not usable: %w", err)
		}
		if min := security.TokenMinLength(7 * 24 * time.Hour); len(tok) < min {
			return fmt.Errorf("the existing --token-file holds a token shorter than %d characters (limits.token_max_lifetime is %s)", min, tokenMaxLife)
		}
		w.say("token: using the existing file %s (not regenerated, not shown)", w.tokenPath)
		return nil
	case errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("--token-file: %w", unwrapErr(err))
	}
	raw := make([]byte, tokenChars*3/4)
	if _, err := io.ReadFull(w.d.random, raw); err != nil {
		return errors.New("no randomness to generate a token")
	}
	w.token, w.newToken = base64.RawURLEncoding.EncodeToString(raw), true
	if len(w.token) != tokenChars {
		return errors.New("internal: token length")
	}
	return nil
}

// render builds the config and the lifecycle file, both checked with the S1 strict parser before anything is
// written. The config holds file references only, never a secret.
func (w *wizard) render() (cfg, lc []byte, err error) {
	opencodeCfg := map[string]any{"url": w.ocURL}
	if w.ocUser != "" && (w.typedPw != "" || w.ocPwFile != "") {
		opencodeCfg["username"] = w.ocUser
	}
	switch {
	case w.typedPw != "":
		opencodeCfg["password_file"] = w.pwPath
	case w.ocPwFile != "":
		opencodeCfg["password_file"] = w.ocPwFile
	}
	tokenCfg := map[string]any{"file": w.tokenPath, "expires": w.expires.Format(time.RFC3339)}
	switch w.tokenSourceKind() {
	case "credential": // the full unit delivers the file as the systemd credential "token"
		tokenCfg = map[string]any{"credential": credentialName, "expires": w.expires.Format(time.RFC3339)}
	case "packaged": // the packaged unit sets CIRAK_TOKEN_FILE itself: the config names neither
		tokenCfg = map[string]any{"expires": w.expires.Format(time.RFC3339)}
	}
	m := map[string]any{
		"server":  map[string]any{"listen": w.listen},
		"token":   tokenCfg,
		"harness": map[string]any{"opencode": opencodeCfg},
		"acl":     map[string]any{"roots": []string(w.o.roots), "sessions": []string(w.o.sessions)},
		"writer":  map[string]any{"lock_dir": w.lockDir},
		"limits":  map[string]any{"token_max_lifetime": tokenMaxLife},
	}
	if len(w.o.lifecycleRoots) > 0 {
		m["lifecycle"] = map[string]any{"config_file": w.lcPath}
		var roots []map[string]any
		for _, r := range w.o.lifecycleRoots {
			roots = append(roots, map[string]any{"path": r, "modes": []string{"create"}})
		}
		if lc, err = json.MarshalIndent(map[string]any{"roots": roots}, "", "  "); err != nil {
			return nil, nil, err
		}
		lc = append(lc, '\n')
	}
	if cfg, err = json.MarshalIndent(m, "", "  "); err != nil {
		return nil, nil, err
	}
	cfg = append(cfg, '\n')
	if _, err := config.Parse(cfg); err != nil {
		return nil, nil, fmt.Errorf("internal: the generated config does not pass the strict parser: %w", err)
	}
	return cfg, lc, nil
}

// write creates the files in an order that leaves the old config intact on a failure: password, token,
// lifecycle, then the config. Files created by this run are removed if a later step fails.
func (w *wizard) write(cfg, lc []byte) error {
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	if exists(w.cfgPath) && !w.o.force {
		return fmt.Errorf("%s already exists; --force replaces it (a backup is made)", w.cfgPath)
	}
	if lc != nil && exists(w.lcPath) && !w.o.force {
		return fmt.Errorf("%s already exists; --force replaces it (a backup is made)", w.lcPath)
	}
	if !w.o.yes && w.o.force && (exists(w.cfgPath) || (lc != nil && exists(w.lcPath))) {
		ok, err := w.askYes("An existing config will be replaced after a backup. Continue?")
		if err != nil || !ok {
			return errors.New("not replaced; nothing was changed")
		}
	}
	if err := w.prepareDirs(); err != nil {
		return err
	}
	var created []string
	type replacedFile struct{ path, backup string }
	var replaced []replacedFile
	undo := func() {
		for i := len(created) - 1; i >= 0; i-- {
			_ = os.Remove(created[i])
		}
		// a file this run replaced goes back to its old bytes (the backup stays)
		for i := len(replaced) - 1; i >= 0; i-- {
			if old, err := readNoFollow(replaced[i].backup); err == nil {
				_ = atomicReplace(replaced[i].path, old, 0o600, w.d.random)
			}
		}
	}
	put := func(path string, data []byte) error {
		if err := createExclusive(path, data, 0o600); err != nil {
			undo()
			return err
		}
		created = append(created, path)
		return nil
	}
	if w.typedPw != "" {
		if err := put(w.pwPath, []byte(w.typedPw+"\n")); err != nil {
			return err
		}
	}
	if w.newToken {
		if err := put(w.tokenPath, []byte(w.token+"\n")); err != nil {
			return err
		}
	}
	replace := func(path string, data []byte) error {
		if !exists(path) {
			return put(path, data)
		}
		backup, err := backupAndReplace(path, data, 0o600, stamp(w.d.now()), w.d.random)
		if err != nil {
			undo()
			return err
		}
		replaced = append(replaced, replacedFile{path, backup})
		w.say("backup: %s", backup)
		return nil
	}
	if lc != nil {
		if err := replace(w.lcPath, lc); err != nil {
			return err
		}
	}
	if err := replace(w.cfgPath, cfg); err != nil {
		return err
	}
	w.say("wrote %s (mode 0600)", w.cfgPath)
	if lc != nil {
		w.say("wrote %s (mode 0600)", w.lcPath)
	}
	if w.newToken {
		w.say("wrote %s (mode 0600)", w.tokenPath)
	}
	if w.typedPw != "" {
		w.say("wrote %s (mode 0600)", w.pwPath)
	}
	return nil
}

// prepareDirs: the operator's own directories are created 0700; a service user's system directories must
// already exist and be writable, otherwise init prints the install -d command and stops before writing.
func (w *wizard) prepareDirs() error {
	for _, dir := range uniq(filepath.Dir(w.cfgPath), filepath.Dir(w.tokenPath)) {
		if err := checkTargetDir(dir); err == nil {
			continue
		} else if !w.operator {
			w.say("directory %s: %v", dir, err)
			w.say("  create it first (init never applies this): install -d -o %s -g %s -m 0700 %s", shq(w.svc.Name), shq(w.svc.Group), shq(dir))
			return errors.New("a target directory is not ready; nothing was written")
		}
		if err := mkdirPrivate(dir); err != nil {
			return err
		}
	}
	if w.operator {
		if err := mkdirPrivate(w.lockDir); err != nil {
			return err
		}
	} else {
		// A service user's state directory is not created by init either. Written without it, the config could
		// not check clean (the check warns that the lock directory does not exist), so stop before writing.
		switch exists, err := privateDir(w.lockDir, w.svc.UID); {
		case err != nil:
			w.say("directory %s: %v", w.lockDir, err)
			return errors.New("the writer lock directory is not ready; nothing was written")
		case !exists:
			w.say("directory %s does not exist yet", w.lockDir)
			w.say("  create it first (init never applies this): install -d -o %s -g %s -m 0700 %s", shq(w.svc.Name), shq(w.svc.Group), shq(w.lockDir))
			return errors.New("the writer lock directory is not ready; nothing was written")
		}
	}
	return nil
}

// mkdirPrivate creates the missing directories of dir with mode 0700. The part of the path that already exists
// must be real directories (no symlink); only the missing tail is created.
func mkdirPrivate(dir string) error {
	var missing []string
	existing := dir
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		missing = append(missing, existing)
		parent := filepath.Dir(existing)
		if parent == existing {
			return fmt.Errorf("%s cannot be created", dir)
		}
		existing = parent
	}
	if err := config.NoSymlinkComponent(existing); err != nil {
		return fmt.Errorf("%s has a symbolic link in its path", existing)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("cannot create %s: %w", missing[i], unwrapErr(err))
		}
	}
	return checkTargetDir(dir)
}

// stage handles an invoking uid that is not the service uid: the files are written to a private staging
// directory the invoking user owns, and the exact install commands are printed. init never applies chown.
func (w *wizard) stage(cfg, lc []byte) error {
	// The printed commands run as root: refuse a symlink anywhere in the existing part of every target path
	// BEFORE anything is staged or printed (a missing tail is fine, install -d creates it).
	targets := []string{w.cfgPath}
	if lc != nil {
		targets = append(targets, w.lcPath)
	}
	if w.newToken {
		targets = append(targets, w.tokenPath)
	}
	if w.typedPw != "" {
		targets = append(targets, w.pwPath)
	}
	targets = append(targets, w.lockDir) // printed as "install -d" for root as well
	for _, t := range targets {
		if err := noSymlinkInExistingPrefix(t); err != nil {
			return fmt.Errorf("%w; nothing was staged and no command was printed", err)
		}
	}
	dir, err := w.d.stageDir()
	if err != nil {
		return fmt.Errorf("cannot make a staging directory: %w", err)
	}
	if strings.ContainsAny(dir, "\n\r\x00") {
		// $TMPDIR with a control character: the directory is ours and empty, remove it and refuse
		_ = os.Remove(dir)
		return usagef("the staging directory path contains a newline, a carriage return or a NUL (check TMPDIR): refused before anything was staged")
	}
	type item struct {
		name, dst string
		data      []byte
	}
	items := []item{{"config.json", w.cfgPath, cfg}}
	if w.newToken {
		w.stagedToken = filepath.Join(dir, "token")
	}
	if lc != nil {
		items = append(items, item{"lifecycle.json", w.lcPath, lc})
	}
	if w.newToken {
		items = append(items, item{"token", w.tokenPath, []byte(w.token + "\n")})
	}
	if w.typedPw != "" {
		items = append(items, item{"opencode-password", w.pwPath, []byte(w.typedPw + "\n")})
	}
	for _, it := range items {
		if err := createExclusive(filepath.Join(dir, it.name), it.data, 0o600); err != nil {
			return err
		}
	}
	w.say("The invoking user (uid %d) is not the service user %s (uid %d), so init writes nothing the service would refuse.", w.d.uid, w.svc.Name, w.svc.UID)
	w.say("The files are staged in %s (private, mode 0700). Run as root (init never applies these):", shq(dir))
	for _, d := range uniq(filepath.Dir(w.cfgPath), filepath.Dir(w.tokenPath)) {
		w.say("  install -d -o %s -g %s -m 0700 %s", shq(w.svc.Name), shq(w.svc.Group), shq(d))
	}
	w.say("  install -d -o %s -g %s -m 0700 %s", shq(w.svc.Name), shq(w.svc.Group), shq(w.lockDir))
	for _, it := range items {
		w.say("  install -o %s -g %s -m 0600 %s %s", shq(w.svc.Name), shq(w.svc.Group), shq(filepath.Join(dir, it.name)), shq(it.dst))
	}
	if err := w.placeServiceFiles(dir); err != nil {
		return err
	}
	w.say("Then remove the staging directory (it holds the token): rm -r %s", shq(dir))
	w.connector()
	w.say("Then: cirak config check --config %s", shq(w.cfgPath))
	return nil
}

// finish verifies the written config with the S1 check and prints the connector values and smoke steps.
func (w *wizard) finish() error {
	rep := &report{out: w.d.stdout}
	fmt.Fprintln(w.d.stdout, "config check of what was written:")
	env, cleanup, err := w.serviceCheckEnv()
	if err != nil {
		return err
	}
	defer cleanup()
	generatedConfigCheck(rep, serveFlags{config: w.cfgPath}, env, nil, w.d.uid, false)
	switch rep.code() {
	case 0:
		w.say("result: clean")
	case 1:
		w.say("result: warnings (exit 1)")
	default:
		w.say("result: refused (exit 2)")
	}
	w.connector()
	w.say("Next steps:")
	w.say("  1. cirak config check --config %s", shq(w.cfgPath))
	w.say("  2. start it: cirak --config %s", shq(w.cfgPath))
	w.say("  3. smoke: cirak doctor --config %s --smoke (initialize + tools/list against the running service)", shq(w.cfgPath))
	if rep.code() != 0 {
		return errors.New("the generated config does not check clean (see above); fix the cause and run init again with --force")
	}
	return nil
}

// generatedConfigCheck is the S1 check init runs on what it wrote; a variable only so a test can present a
// warning (ADR-027 section 2: init --local passes config check clean, so any nonzero result is a failure).
var generatedConfigCheck = checkConfiguration

// connector prints the Claude connector values (section 2 item 6). The token is shown at most once in the
// whole run, only on a terminal, and only when this run generated it; --yes prints the file path.
func (w *wizard) connector() {
	w.say("Connector values (Claude Chat, custom connector):")
	w.say("  URL:    http://%s/mcp", w.listen)
	w.say("  Header: Authorization: Bearer <token>")
	switch {
	case w.newToken && w.d.isTTY && w.d.outTTY && !w.o.yes && !w.tokenShown:
		w.tokenShown = true
		w.say("  Token (shown once, it is in %s and nowhere else; run `set +o history` before you paste it):", w.tokenPath)
		w.say("%s", w.token)
	case w.newToken && !w.o.yes && w.d.isTTY && !w.d.outTTY:
		// stdin is a terminal but stdout is redirected (init | tee log): the value would land in a file
		if w.stagedToken != "" {
			w.say("  Token: staged at %s, and put at %s by the install command above (not printed: standard output is not a terminal)", w.stagedToken, w.tokenPath)
		} else {
			w.say("  Token: read it from %s (not printed: standard output is not a terminal)", w.tokenPath)
		}
	case w.stagedToken != "":
		w.say("  Token: staged at %s, and put at %s by the install command above (it is not printed)", w.stagedToken, w.tokenPath)
	default:
		w.say("  Token: read it from %s (it is not printed)", w.tokenPath)
	}
}

func uniq(in ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ---- production wiring ----

func productionInit(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runInit(args, productionDeps(getenv, stdin, stdout, stderr))
}

// productionDeps wires the real process: crypto/rand for the token, the real OpenCode probe, the terminal.
func productionDeps(getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) initDeps {
	return initDeps{
		getenv: getenv, stdin: stdin, stdout: stdout, stderr: stderr,
		isTTY: stdinIsTerminal(), outTTY: stdoutIsTerminal(), uid: os.Geteuid(), gid: os.Getegid(), now: time.Now, random: rand.Reader,
		lookup: lookupUser, probe: probeOpenCode, readPassword: readPasswordNoEcho,
		run:      prodRun,
		stageDir: func() (string, error) { return os.MkdirTemp("", "cirak-init-") },
		home:     os.UserHomeDir,
		goos:     runtime.GOOS, executable: os.Executable, makeTemp: func(p string) (string, error) { return os.MkdirTemp("", p) },
	}
}

// prodRun runs the one command init may run: the read-only `sudo -u <user> test -r|-x <path>` probe.
func prodRun(name string, args ...string) error {
	if !sudoProbeAllowed(name, args) {
		return errors.New("init runs only the read-only sudo probe")
	}
	return exec.Command(name, args...).Run()
}

// sudoProbeAllowed is the one command shape init may run: sudo -u <user> test -r|-x <path>.
func sudoProbeAllowed(name string, args []string) bool {
	return name == "sudo" && len(args) == 5 && args[0] == "-u" && args[2] == "test" && (args[3] == "-r" || args[3] == "-x") && args[1] != "" && filepath.IsAbs(args[4])
}

// lookupUser resolves a user by name, or the invoking user for "".
func lookupUser(name string) (userInfo, error) {
	var u *user.User
	var err error
	if name == "" {
		u, err = user.LookupId(strconv.Itoa(os.Geteuid()))
	} else {
		u, err = user.Lookup(name)
	}
	if err != nil {
		return userInfo{}, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	info := userInfo{Name: u.Username, UID: uid, GID: gid, Group: u.Gid}
	if g, err := user.LookupGroupId(u.Gid); err == nil {
		info.Group = g.Name
	}
	if ids, err := u.GroupIds(); err == nil {
		for _, s := range ids {
			if n, err := strconv.Atoi(s); err == nil {
				info.GIDs = append(info.GIDs, n)
			}
		}
	}
	return info, nil
}

// probeOpenCode is the bounded read the Bridge uses (GET /session/status).
func probeOpenCode(ctx context.Context, baseURL, username, password string) openCodeProbe {
	c, err := opencode.New(baseURL, username, password)
	if err != nil {
		return openCodeProbe{}
	}
	_, err = c.Status(ctx)
	var he *opencode.HTTPError
	switch {
	case err == nil:
		return openCodeProbe{Reachable: true}
	case errors.As(err, &he) && he.Status == 401:
		return openCodeProbe{Unauthorized: true}
	}
	return openCodeProbe{}
}
