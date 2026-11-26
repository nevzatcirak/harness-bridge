package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nevzatcirak/cirak/internal/config"
	"github.com/nevzatcirak/cirak/internal/version"
)

// `cirak config import` (ADR-027 section 5 step 2) and the side-by-side comparison of `doctor` (section 5 step
// 3). The import reads the environment a unit would run with (systemctl show, then its EnvironmentFile= files,
// merged with systemd's order) or one environment file, and writes the non-secret settings as a config file.
//
// It is value-free in every output: it names keys, files and variables, never a value. An inline token or
// password is NOT copied: a config file has no inline secret, so the command prints the `install -m 0600` line
// that makes the private file and stops there. The file is written only where the invoker can write it; for
// anything else the exact root command is printed and nothing is applied (the S2/S3 rule). It never calls sudo,
// systemctl, chown or chmod, and never starts anything.

const configImportUsage = `usage: cirak config import (--from-unit name | --env-file path) --out path [--reject-unknown] [--force]

Writes the non-secret settings of a systemd unit's environment (or of one environment file) as a config file,
mode 0600. --from-unit reads 'systemctl show' of the unit and its EnvironmentFile= files, merged the way systemd
merges them (EnvironmentFile= overrides Environment=, UnsetEnvironment= removes); run it as root or as a user that
can read every file. A missing "-" file is skipped; an unreadable or unparseable file, or a file over 1 MiB, is
an error that stops the import. The output names keys and files, never a value. An inline token or password is not
copied: the command prints how to move it to a private file. --reject-unknown makes an unknown CIRAK_* name or
legacy twin of it an error. --force replaces an existing --out file after a backup. Where you cannot write --out the exact
install command for root is printed and nothing is applied.
`

type importOptions struct {
	fromUnit, envFile, out string
	rejectUnknown, force   bool
}

func runConfigImport(args []string, d doctorDeps) int {
	fs := flag.NewFlagSet("cirak config import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := importOptions{}
	fs.StringVar(&o.fromUnit, "from-unit", "", "read the environment of this systemd unit")
	fs.StringVar(&o.envFile, "env-file", "", "read this environment file")
	fs.StringVar(&o.out, "out", "", "the config file to write (absolute path)")
	fs.BoolVar(&o.rejectUnknown, "reject-unknown", false, "an unknown CIRAK_* name, or a legacy twin of one, is an error")
	fs.BoolVar(&o.force, "force", false, "replace an existing --out file (a backup is made)")
	fs.Usage = func() { fmt.Fprint(d.stderr, configImportUsage) }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprint(d.stderr, configImportUsage)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(d.stderr, "config import: "+format+"\n", a...)
		return 2
	}
	switch {
	case (o.fromUnit == "") == (o.envFile == ""):
		return usage("exactly one of --from-unit and --env-file is required")
	case o.out == "":
		return usage("--out is required (the output is value-free, so the config is never printed)")
	case !filepath.IsAbs(o.out) || filepath.Clean(o.out) != o.out:
		return usage("--out must be an absolute canonical path")
	case strings.ContainsAny(o.out, "\n\r\x00"):
		return usage("--out must not contain a newline, a carriage return or a NUL")
	}
	rep := &report{out: d.stdout}
	importConfig(rep, o, d)
	switch rep.code() {
	case 0:
		fmt.Fprintln(d.stdout, "result: imported")
	case 1:
		fmt.Fprintln(d.stdout, "result: imported with warnings (exit 1)")
	default:
		fmt.Fprintln(d.stdout, "result: refused; nothing was written (exit 2)")
	}
	return rep.code()
}

// importConfig is the whole import. Everything that can refuse runs before anything is written or staged.
func importConfig(rep *report, o importOptions, d doctorDeps) {
	src, ok := d.selectSource(rep, o.fromUnit, o.envFile)
	if !ok {
		return
	}
	env0, legacy, err := cirakEnv(src.getenv, legacyPhaseFor(version.Version()))
	if err != nil {
		rep.refuse("environment: %v", err)
		return
	}
	for _, w := range legacy {
		rep.warn("%s", w)
	}
	if unknown := unknownEnvNames(src.environ); len(unknown) > 0 {
		for _, n := range unknown {
			if o.rejectUnknown {
				rep.refuse("unknown environment name %s is not read by the daemon (--reject-unknown)", n)
			} else {
				rep.warn("unknown environment name %s is not read by the daemon and is not imported", n)
			}
		}
		if o.rejectUnknown {
			return
		}
	}
	if env0(config.EnvConfig) != "" {
		rep.warn("%s is set in the source: it names the file a start reads, it is not a setting and is not imported", config.EnvConfig)
	}

	tree := map[string]any{}
	var imported []string
	for _, k := range config.Keys {
		ev := env0(k.Env)
		if ev == "" {
			continue
		}
		v, err := config.JSONValue(k, ev)
		if err != nil {
			rep.refuse("%v", err)
			return
		}
		setPath(tree, k.Path, v)
		imported = append(imported, k.Path)
	}
	data, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		rep.refuse("internal: %v", err)
		return
	}
	data = append(data, '\n')
	parsed, err := config.Parse(data)
	if err != nil {
		rep.refuse("the source cannot be written as a config file: %v", err)
		return
	}
	if err := sameEffectiveSettings(env0, parsed); err != nil {
		rep.refuse("%v", err)
		return
	}
	if len(imported) == 0 {
		rep.warn("the source sets no setting the daemon reads; the config file would be empty")
	}

	dir := filepath.Dir(o.out)
	if err := noSymlinkInExistingPrefix(o.out); err != nil {
		rep.refuse("%v; nothing was written or staged", err)
		return
	}
	_, statErr := os.Lstat(o.out)
	exists := statErr == nil
	if exists && !o.force {
		rep.refuse("%s already exists; --force replaces it (a backup is made)", o.out)
		return
	}
	// the inline secrets: refused as a copy, never silently dropped
	var tokenInline, passwordInline bool
	tokenInline = env0(config.EnvInlineToken) != ""
	passwordInline = env0(config.EnvInlinePassword) != ""

	fmt.Fprintf(rep.out, "importing %d setting(s) (keys only; no value is shown): %s\n", len(imported), strings.Join(imported, ", "))
	if canWriteInto(dir) {
		if err := writeImported(rep, d, o.out, dir, data, exists); err != nil {
			rep.refuse("%v", err)
			return
		}
	} else if err := stageImported(rep, d, o.out, dir, data, exists); err != nil {
		rep.refuse("%v", err)
		return
	}
	if tokenInline {
		inlineSecretNote(rep, "token", "the unit's inline token (CIRAK_TOKEN or its legacy twin)", filepath.Join(dir, "token"), "token.file")
	}
	if passwordInline {
		inlineSecretNote(rep, "password", "the unit's inline OPENCODE_SERVER_PASSWORD", filepath.Join(dir, "opencode-password"), "harness.opencode.password_file")
	}
	srcArg := "--from-unit " + shq(o.fromUnit)
	if o.envFile != "" {
		srcArg = "--env-file " + shq(o.envFile)
	}
	fmt.Fprintln(rep.out, "Next (printed, never run by this command):")
	fmt.Fprintf(rep.out, "  cirak config check %s --config %s\n", srcArg, shq(o.out))
	fmt.Fprintf(rep.out, "  cirak doctor --config %s %s\n", shq(o.out), srcArg)
	fmt.Fprintf(rep.out, "  then add Environment=CIRAK_CONFIG=%s to the unit and remove the legacy lines only when the comparison above shows no difference (ADR-027 section 5 steps 3 to 5)\n", shq(o.out))
}

// inlineSecretNote prints the command that makes the private file for a secret the unit holds inline. The value
// is not read, not copied and not shown; the operator puts it in the file with an editor, never on a command line.
func inlineSecretNote(rep *report, what, label, path, key string) {
	rep.warn("%s is NOT copied into the config (a config file has no inline secret). Move it to a private file or a systemd credential:", label)
	fmt.Fprintf(rep.out, "  install -m 0600 /dev/null %s   (then put the %s in it with an editor, never on a command line; set %s)\n", shq(path), what, key)
}

// sameEffectiveSettings proves the imported file says what the source says: the effective value of every schema
// key from the source's environment alone equals the effective value from the file alone. A difference means the
// conversion is wrong; nothing is written.
func sameEffectiveSettings(env func(string) string, file *config.File) error {
	fromEnv, err := config.Resolve(config.Input{Env: env})
	if err != nil {
		return fmt.Errorf("the source's own environment is inconsistent: %v", err)
	}
	fromFile, err := config.Resolve(config.Input{Env: func(string) string { return "" }, File: file})
	if err != nil {
		return fmt.Errorf("internal: the imported file does not resolve: %v", err)
	}
	for _, k := range config.Keys {
		if fromEnv.Get(k.Env) != fromFile.Get(k.Env) {
			return fmt.Errorf("internal: %s would differ after the import; nothing was written", k.Path)
		}
	}
	return nil
}

// setPath stores v at a dotted key path in a nested object.
func setPath(tree map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	m := tree
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

// canWriteInto: the directory exists and the invoker can write it (the S2 rule: a real directory, not group or
// world writable), or it is missing and may be created under such a parent.
func canWriteInto(dir string) bool {
	if checkTargetDir(dir) == nil {
		return true
	}
	if _, err := os.Lstat(dir); err == nil {
		return false
	}
	return checkTargetDir(filepath.Dir(dir)) == nil
}

func writeImported(rep *report, d doctorDeps, out, dir string, data []byte, exists bool) error {
	if _, err := os.Lstat(dir); err != nil {
		if err := os.Mkdir(dir, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", dir, unwrapErr(err))
		}
	}
	if exists {
		backup, err := backupAndReplace(out, data, 0o600, stamp(d.now()), d.random)
		if err != nil {
			return err
		}
		fmt.Fprintf(rep.out, "backup: %s\n", backup)
	} else if err := createExclusive(out, data, 0o600); err != nil {
		return err
	}
	rep.ok("wrote %s (mode 0600)", out)
	return nil
}

// stageImported is the file the invoker cannot write itself: it is staged in a private directory and the exact
// install command for root is printed. Nothing is applied.
func stageImported(rep *report, d doctorDeps, out, dir string, data []byte, exists bool) error {
	stage, err := d.stageDir()
	if err != nil {
		return fmt.Errorf("cannot make a staging directory: %w", err)
	}
	if strings.ContainsAny(stage, "\n\r\x00") {
		_ = os.Remove(stage)
		return errors.New("the staging directory path contains a newline, a carriage return or a NUL (check TMPDIR): refused before anything was staged")
	}
	staged := filepath.Join(stage, "config.json")
	if err := createExclusive(staged, data, 0o600); err != nil {
		_ = os.Remove(stage)
		return err
	}
	fmt.Fprintf(rep.out, "%s is not writable by you, so the config is staged in %s (private, mode 0700). Run as root (never applied by this command):\n", dir, shq(stage))
	if _, err := os.Lstat(dir); err != nil {
		fmt.Fprintf(rep.out, "  install -d -o root -g root -m 0755 %s\n", shq(dir))
	}
	if exists {
		fmt.Fprintf(rep.out, "  cp -p %s %s\n", shq(out), shq(out+".bak-"+stamp(d.now())))
	}
	fmt.Fprintf(rep.out, "  install -o root -g root -m 0600 %s %s\n", shq(staged), shq(out))
	fmt.Fprintf(rep.out, "Then remove the staging directory: rm -r %s\n", shq(stage))
	rep.warn("the config was staged, not installed: run the commands above as root")
	return nil
}

// prodStageDir makes the private temporary directory of a staged file.
func prodStageDir() (string, error) { return os.MkdirTemp("", "cirak-import-") }

// compareWithConfig prints, for a unit or file source, the side-by-side view of the source's environment and the
// config file a start would use (ADR-027 section 5 step 3). A setting the source sets and the file does not, or
// sets differently, blocks the removal of the legacy line: a setting only in the source is a warning (the service
// still starts, the source supplies it), a differing guarded pair is REFUSED (a start with both refuses it). Values: a secret reference is shown as set or unset, a URL redacted; nothing else
// is secret. It prints nothing when no config file is in use.
func compareWithConfig(rep *report, f serveFlags, src *envSource, d doctorDeps) {
	env0, _, err := cirakEnv(src.getenv, legacyPhaseFor(version.Version()))
	if err != nil {
		return // the configuration check that follows reports it
	}
	path, _, err := locateConfig(f.config, env0, src.home)
	if err != nil {
		return
	}
	if path == "" {
		fmt.Fprintln(rep.out, "compare: no config file in use; there is nothing to compare with the source")
		return
	}
	file, err := config.Load(path, uint32(d.uid))
	if err != nil {
		return
	}
	fmt.Fprintf(rep.out, "compare: the source's environment and the config file %s (a secret reference as set or unset, a URL redacted)\n", path)
	differ, onlySource := 0, 0
	for _, k := range config.Keys {
		ev, has := env0(k.Env), file.Has(k.Path)
		var status string
		switch {
		case ev == "" && !has:
			continue
		case ev != "" && has && config.SameValue(k, ev, file):
			status = "same"
		case ev != "" && has && k.Guarded:
			status, differ = "DIFFERENT", differ+1
		case ev != "" && has:
			status, onlySource = "DIFFERENT (display only)", onlySource+1
		case ev != "":
			status, onlySource = "ONLY IN THE SOURCE", onlySource+1
		default:
			status = "only in the file"
		}
		fmt.Fprintf(rep.out, "  %-36s source=%-30s file=%-30s %s\n", k.Path, compareValue(k, ev), compareValue(k, file.EnvForm(k.Path)), status)
	}
	if env0(config.EnvInlineToken) != "" && !file.Has("token.file") && !file.Has("token.credential") {
		fmt.Fprintf(rep.out, "  %-36s source=%-30s file=%-30s %s\n", "token", "inline", "unset", "ONLY IN THE SOURCE")
		onlySource++
	}
	switch {
	case differ > 0:
		rep.refuse("compare: %d setting(s) DIFFER between the source and the config file: a start with both refuses a differing pair; do NOT remove the legacy lines", differ)
	case onlySource > 0:
		rep.warn("compare: %d setting(s) are set only in the source (or differ in a display-only key): the config file does not carry them yet; do NOT remove the legacy lines", onlySource)
	default:
		rep.ok("compare: the config file and the source agree on every setting the source sets; the legacy lines may go after a clean check")
	}
}

// compareValue is the display form of one value of the comparison, as Describe shows it.
func compareValue(k config.Key, v string) string {
	switch {
	case v == "":
		return "unset"
	case k.Secret:
		return "set"
	case k.URL:
		return config.RedactURL(v)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fmt.Sprintf("%q", v)
		}
	}
	return v
}
