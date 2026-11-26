package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The service step of `cirak init --install-service` (ADR-027 section 2 item 5, slice S3). It renders a systemd
// unit (a full unit, or a drop-in over the packaged one) or a launchd plist, and places the file by the S2
// pattern: when the invoker can write the target directory the file is written there (exclusively, never over an
// existing file without --force); otherwise it is staged and the exact quoted install command is printed.
// init NEVER runs systemctl or launchctl and never starts anything: the reload and start commands are printed.

type svcFile struct {
	stageName string // the name inside a staging directory
	dst       string
	data      string
	mkdirDst  bool   // the destination directory may be created (a drop-in directory, the user's LaunchAgents)
	owner     string // owner and group of the installed file, for the printed install command
	group     string
}

// planService decides the service kind and renders the file, before anything is probed, written or printed.
func (w *wizard) planService() error {
	o := w.o
	if !o.installService {
		return nil
	}
	if !unitSafe.MatchString(w.cfgPath) || !unitSafe.MatchString(w.tokenPath) {
		return usagef("--config and --token-file must use only letters, digits and . _ / + @ : , = - to be written into a service file")
	}
	if o.claudeHome != "" {
		if err := validateClaudeHome(o.claudeHome); err != nil {
			return usagef("%v", err)
		}
	}
	switch w.d.goos {
	case "linux":
		return w.planSystemd()
	case "darwin":
		return w.planLaunchd()
	}
	return usagef("--install-service is supported on linux (systemd) and darwin (launchd), not %s", w.d.goos)
}

// systemClaudeHomes are the top-level directories that are never a Claude Code adapter HOME: ReadWritePaths= on one
// of them would make part of the system writable again.
var systemClaudeHomes = map[string]bool{
	"/etc": true, "/usr": true, "/var": true, "/bin": true, "/sbin": true, "/lib": true, "/lib32": true, "/lib64": true,
	"/libx32": true, "/boot": true, "/proc": true, "/sys": true, "/dev": true, "/run": true, "/root": true,
}

// validateClaudeHome checks --claude-home before anything is written, staged or printed. The value becomes a
// ReadWritePaths= entry of a sandboxed unit with ProtectHome=off, so a careless value (for example "/") would switch
// the sandbox off, and systemd gives a leading "-", "+", "!" or "@" in that field a meaning of its own. It must be
// an absolute, canonical path that is not "/" and not a top-level system directory.
func validateClaudeHome(p string) error {
	switch p[0] {
	case '-', '+', '!', '@', ':':
		return fmt.Errorf("--claude-home must be an absolute path: %q starts with %q, which systemd reads as a prefix of its own", p, p[:1])
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("--claude-home must be an absolute path (got %q)", p)
	}
	if strings.Contains(p, "..") && filepath.Clean(p) != p {
		return fmt.Errorf("--claude-home must not contain .. components (got %q)", p)
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("--claude-home must be a clean path (no trailing slash, //, or . component): use %q", filepath.Clean(p))
	}
	if p == "/" || systemClaudeHomes[p] {
		return fmt.Errorf("--claude-home %s is a system directory: ReadWritePaths= on it would make the system writable; name the adapter's own home", p)
	}
	return nil
}

func (w *wizard) planSystemd() error {
	o := w.o
	if o.launchDaemon {
		return usagef("--launch-daemon is a macOS option")
	}
	if o.serviceUser == "" {
		return usagef("--install-service needs --service-user (the dedicated user of the service, for example cirak)")
	}
	uo := unitOptions{User: w.svc.Name, Group: w.svc.Group, ConfigPath: w.cfgPath, TokenPath: w.tokenPath, ClaudeAdapter: o.claudeHome != "", ClaudeHome: o.claudeHome}
	w.say("Service: a systemd system unit. Writing it needs root: init writes it only where it can, otherwise it prints the install command (it never runs systemctl).")
	if _, err := os.Lstat(packagedUnitPath); err == nil {
		data, err := renderDropIn(uo)
		if err != nil {
			return usagef("%v", err)
		}
		w.svcKind = "systemd-dropin"
		w.svcFiles = []svcFile{{stageName: dropInName, dst: filepath.Join(systemUnitDir, unitName+".d", dropInName), data: data, mkdirDst: true, owner: "root", group: "root"}}
		w.say("  the packaged unit %s exists: init writes the drop-in only, never a second %s that would shadow it.", packagedUnitPath, unitName)
		if _, err := os.Lstat(filepath.Join(systemUnitDir, unitName)); err == nil {
			w.say("  warning: %s exists and already shadows the packaged unit; init does not touch it, and this drop-in applies to it.", filepath.Join(systemUnitDir, unitName))
		}
	} else {
		data, err := renderFullUnit(uo)
		if err != nil {
			return usagef("%v", err)
		}
		w.svcKind = "systemd"
		w.svcFiles = []svcFile{{stageName: unitName, dst: filepath.Join(systemUnitDir, unitName), data: data, owner: "root", group: "root"}}
		w.say("  no packaged unit: init writes the full unit. It runs %s: install the binary there, or edit ExecStart.", defaultUnitBinary)
	}
	if o.claudeHome != "" {
		w.say("  Claude adapter variant: no NoNewPrivileges=yes (the child runs under sudo), ProtectHome=off and ReadWritePaths=%s.", o.claudeHome)
	}
	return w.refuseExistingServiceFiles()
}

func (w *wizard) planLaunchd() error {
	o := w.o
	exe, err := w.d.executable()
	if err != nil || !filepath.IsAbs(exe) {
		return usagef("cannot find the path of this binary for the property list")
	}
	var f svcFile
	var po plistOptions
	home, herr := w.d.home()
	switch {
	case o.launchDaemon:
		if o.serviceUser == "" {
			return usagef("--launch-daemon needs --service-user (the user the daemon runs as)")
		}
		po = plistOptions{Label: daemonLabel, Binary: exe, ConfigPath: w.cfgPath, LogDir: "/var/log/cirak", UserName: w.svc.Name}
		f = svcFile{stageName: daemonLabel + ".plist", dst: filepath.Join(launchDaemonDir, daemonLabel+".plist"), owner: "root", group: "wheel"}
		w.svcKind = "launchd-daemon"
		w.say("Service: a launchd LaunchDaemon (system). Writing it needs root: init writes it only where it can, otherwise it prints the install command.")
	default:
		if o.serviceUser != "" {
			return usagef("a LaunchAgent runs as you: leave --service-user empty, or use --launch-daemon")
		}
		if herr != nil || home == "" {
			return usagef("cannot find a home directory for the LaunchAgent")
		}
		po = plistOptions{Label: agentLabel, Binary: exe, ConfigPath: w.cfgPath, LogDir: filepath.Join(home, "Library", "Logs", "cirak")}
		f = svcFile{stageName: agentLabel + ".plist", dst: filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), mkdirDst: true}
		w.svcKind = "launchd-agent"
		w.logDir = po.LogDir
		w.say("Service: a launchd LaunchAgent for you. It needs no root.")
	}
	data, err := renderPlist(po)
	if err != nil {
		return usagef("%v", err)
	}
	f.data = data
	w.svcFiles = []svcFile{f}
	return w.refuseExistingServiceFiles()
}

// refuseExistingServiceFiles: a service file that exists is replaced only with --force, in every flow, so a
// printed install command can never overwrite a root-owned file the operator did not mean to replace.
func (w *wizard) refuseExistingServiceFiles() error {
	// the printed install commands run as root: a symlink anywhere in the existing part of a target path is refused
	// here, in the pre-flight, before any config or token command is printed and before anything is staged
	for _, f := range w.svcFiles {
		if err := noSymlinkInExistingPrefix(f.dst); err != nil {
			return fmt.Errorf("%w; no service file was written or staged", err)
		}
	}
	for _, f := range w.svcFiles {
		if _, err := os.Lstat(f.dst); err == nil && !w.o.force {
			return usagef("%s already exists; --force replaces it (a backup is made)", f.dst)
		}
	}
	return nil
}

// tokenSource is what the config says about the token for the chosen service kind.
func (w *wizard) tokenSourceKind() string {
	switch w.svcKind {
	case "systemd":
		return "credential"
	case "systemd-dropin":
		return "packaged"
	}
	return "file"
}

// placeServiceFiles writes or stages the service files. With stageDir "" it is the same-user flow: a file whose
// directory the invoker can write is written there; anything else is staged in a directory of its own. With a
// stageDir (the staged config flow) every service file is staged beside the config files.
func (w *wizard) placeServiceFiles(stageDir string) error {
	w.stagedFlow = stageDir != ""
	// the symlink refusal for every target ran in the pre-flight (refuseExistingServiceFiles), before anything was printed
	var printed []string
	if w.svcKind == "launchd-agent" && w.logDir != "" {
		if err := mkdirPrivate(w.logDir); err != nil {
			return err
		}
	}
	for _, f := range w.svcFiles {
		dir := filepath.Dir(f.dst)
		if stageDir == "" && w.canWriteServiceDir(dir, f.mkdirDst) {
			if err := w.writeServiceFile(f, dir); err != nil {
				return err
			}
			w.say("wrote %s (mode 0644)", f.dst)
			continue
		}
		if stageDir == "" {
			d, err := w.d.stageDir()
			if err != nil {
				return fmt.Errorf("cannot make a staging directory: %w", err)
			}
			if strings.ContainsAny(d, "\n\r\x00") {
				_ = os.Remove(d)
				return usagef("the staging directory path contains a newline, a carriage return or a NUL (check TMPDIR): refused before anything was staged")
			}
			stageDir = d
			w.svcStage = d
		}
		if err := createExclusive(filepath.Join(stageDir, f.stageName), []byte(f.data), 0o644); err != nil {
			return err
		}
		w.say("The service file is staged in %s. Run as root (init never applies this):", shq(stageDir))
		if _, err := os.Lstat(dir); err != nil {
			w.say("  install -d -o %s -g %s -m 0755 %s", shq(f.owner), shq(f.group), shq(dir))
		}
		w.say("  install -o %s -g %s -m 0644 %s %s", shq(f.owner), shq(f.group), shq(filepath.Join(stageDir, f.stageName)), shq(f.dst))
		printed = append(printed, f.dst)
	}
	if w.svcStage != "" {
		w.say("Then remove the staging directory: rm -r %s", shq(w.svcStage))
	}
	w.serviceHints()
	return nil
}

// canWriteServiceDir: the directory exists and the invoker can write it (by the S2 rule: a real directory, not
// group or world writable), or it is missing and may be created under such a parent.
func (w *wizard) canWriteServiceDir(dir string, mkdirOK bool) bool {
	if checkTargetDir(dir) == nil {
		return true
	}
	if _, err := os.Lstat(dir); err == nil || !mkdirOK {
		return false
	}
	return checkTargetDir(filepath.Dir(dir)) == nil
}

func (w *wizard) writeServiceFile(f svcFile, dir string) error {
	if _, err := os.Lstat(dir); err != nil {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", dir, unwrapErr(err))
		}
	}
	if _, err := os.Lstat(f.dst); err == nil {
		backup, err := backupAndReplace(f.dst, []byte(f.data), 0o644, stamp(w.d.now()), w.d.random)
		if err != nil {
			return err
		}
		w.say("backup: %s", backup)
		return nil
	}
	return createExclusive(f.dst, []byte(f.data), 0o644)
}

// serviceHints prints the commands the operator runs next. init never runs them.
func (w *wizard) serviceHints() {
	switch w.svcKind {
	case "systemd", "systemd-dropin":
		verb := "placed"
		if w.svcStage != "" || w.stagedFlow {
			verb = "staged (not yet installed)"
		}
		w.say("Service file %s: %s. Next (printed, never run by init):", verb, shq(w.svcFiles[0].dst))
		w.say("  systemctl daemon-reload")
		w.say("  systemctl restart cirak   (after the binary is at %s)", defaultUnitBinary)
	case "launchd-agent":
		w.say("Next (printed, never run by init): launchctl bootstrap gui/%d %s", w.d.uid, shq(w.svcFiles[0].dst))
	case "launchd-daemon":
		w.say("Next (printed, never run by init): launchctl bootstrap system %s", shq(w.svcFiles[0].dst))
	}
}

// serviceCheckEnv gives the generated-config check a view of the token as the service will see it: a private
// credentials directory holding a copy of the token (the full unit's credential), or CIRAK_TOKEN_FILE pointing at
// it (the packaged unit). Without it the check outside systemd could only say UNVERIFIED. The copy is removed.
func (w *wizard) serviceCheckEnv() (func(string) string, func(), error) {
	none := func(string) string { return "" }
	if w.svcKind != "systemd" && w.svcKind != "systemd-dropin" {
		return none, func() {}, nil
	}
	tok := w.token
	if !w.newToken {
		v, err := readSecret(func(k string) string {
			if k == "CIRAK_TOKEN_FILE" {
				return w.tokenPath
			}
			return ""
		}, "CIRAK_TOKEN")
		if err != nil {
			return nil, nil, errors.New("the existing token file cannot be read for the check")
		}
		tok = v
	}
	base, err := w.d.makeTemp("cirak-init-check-")
	if err != nil {
		return nil, nil, fmt.Errorf("cannot make a temporary directory for the check: %w", err)
	}
	cleanup := func() { _ = os.Remove(filepath.Join(base, "token")); _ = os.Remove(base) }
	if err := createExclusive(filepath.Join(base, "token"), []byte(tok+"\n"), 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	if w.svcKind == "systemd" {
		return func(k string) string {
			if k == "CREDENTIALS_DIRECTORY" {
				return base
			}
			return ""
		}, cleanup, nil
	}
	return func(k string) string {
		if k == "CIRAK_TOKEN_FILE" {
			return filepath.Join(base, "token")
		}
		return ""
	}, cleanup, nil
}
