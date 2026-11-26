package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/config"
)

// ADR-027 S4: `cirak config import`. Unit and environment files are temp files or fakes; nothing reads the real
// systemd, the real shell environment or /etc.

// ch is the doctor harness plus what the import writes with: a staging directory and the file-name randomness.
type ch struct {
	*dh
	stage string
}

func newCH(t *testing.T) *ch { return &ch{dh: newDH(t)} }

func (c *ch) deps() doctorDeps {
	d := c.dh.deps()
	d.stageDir = func() (string, error) {
		if c.stage == "" {
			c.stage = filepath.Join(c.f.dir, "stage")
			if err := os.Mkdir(c.stage, 0o700); err != nil {
				return "", err
			}
		}
		return c.stage, nil
	}
	d.random = bytes.NewReader(bytes.Repeat([]byte{7}, 256))
	d.now = func() time.Time { return initNow }
	return d
}

func (c *ch) run(args ...string) int { return runConfigDeps(args, c.deps()) }

// unit points the fake `systemctl show` at an inline Environment= and one EnvironmentFile= (written under dir).
func (c *ch) unit(t *testing.T, inline []string, fileLines ...string) string {
	t.Helper()
	ef := filepath.Join(c.f.dir, "unit.env")
	writeFile(t, ef, strings.Join(fileLines, "\n")+"\n", 0o600)
	c.show = fmt.Sprintf(showFmt, strings.Join(inline, " "), ef+" (ignore_errors=no)", "")
	return ef
}

func (c *ch) outPath() string { return filepath.Join(c.f.dir, "out", "config.json") }

// mkOut makes the writable directory of the default --out.
func (c *ch) mkOut(t *testing.T) string {
	t.Helper()
	if err := os.Mkdir(filepath.Join(c.f.dir, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	return c.outPath()
}

// effectiveOf resolves a configuration the way a start does and returns every schema key's effective value.
func effectiveOf(t *testing.T, in config.Input) map[string]string {
	t.Helper()
	res, err := config.Resolve(in)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := map[string]string{}
	for _, k := range config.Keys {
		m[k.Env] = res.Get(k.Env)
	}
	return m
}

func parseOut(t *testing.T, path string) *config.File {
	t.Helper()
	f, err := config.Parse([]byte(readF(t, path)))
	if err != nil {
		t.Fatalf("the imported file does not parse: %v\n%s", err, readF(t, path))
	}
	return f
}

const (
	urlCanary = "URLPASSWORD-CANARY-9d8c7b"
	pwCanary  = "PASSWORD-CANARY-7f6e5d4c"
	tokCanary = "INLINE-TOKEN-CANARY-0123456789abcdefghijklmnopqrstuvwxyz"
)

func TestImportWritesTheUnitSettingsAsAConfigFileAndShowsNoValue(t *testing.T) {
	c := newCH(t)
	out := c.mkOut(t)
	ef := c.unit(t,
		[]string{"BRIDGE_LISTEN=127.0.0.1:9191", "BRIDGE_READ_ONLY=1", "CIRAK_ALLOWED_ROOTS=" + c.f.ws, "CIRAK_ALLOWED_SESSIONS=ses_a,ses_b", "CIRAK_CLAUDE_MAX_TURNS=7"},
		"CIRAK_TOKEN_FILE="+c.f.token, "CIRAK_TOKEN_EXPIRES=2099-01-01T00:00:00Z", "CIRAK_WRITER_LOCK_DIR="+c.f.locks,
		"OPENCODE_URL=http://127.0.0.1:4096", "UNRELATED_URL=http://user:"+urlCanary+"@evil.example/?token="+urlCanary, "SOME_OTHER=1")
	code := c.run("import", "--from-unit", "cirak", "--out", out)
	all := c.all()
	if code != 1 { // the legacy BRIDGE_* names of the unit warn once each
		t.Fatalf("exit %d:\n%s", code, all)
	}
	if mode(t, out) != 0o600 {
		t.Errorf("the config mode is %o, want 0600", mode(t, out))
	}
	f := parseOut(t, out)
	for path, want := range map[string]string{"server.listen": "127.0.0.1:9191", "server.read_only": "1", "acl.sessions": "ses_a,ses_b", "limits.claude_max_turns": "7",
		"token.file": c.f.token, "token.expires": "2099-01-01T00:00:00Z", "writer.lock_dir": c.f.locks} {
		if got := f.EnvForm(path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	for _, want := range []string{"environment source: unit cirak", "  read: " + ef, "importing 9 setting(s)", "wrote " + out + " (mode 0600)", "result: imported with warnings (exit 1)",
		"cirak config check --from-unit 'cirak' --config " + shq(out), "legacy environment variable BRIDGE_LISTEN is deprecated"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	// value-free in every output: the listen address, the session IDs and the URL's credential never appear
	for _, bad := range []string{urlCanary, "9191", "ses_a", "ses_b", c.f.token, "evil.example"} {
		if strings.Contains(all, bad) {
			t.Errorf("a value reached the output: %q", bad)
		}
	}
	// a variable the daemon does not read is never copied, whatever it holds
	if strings.Contains(readF(t, out), urlCanary) || strings.Contains(readF(t, out), "evil.example") {
		t.Error("a value of an unrelated variable was written into the config")
	}
	if !strings.Contains(all, "SOME_OTHER") && strings.Contains(all, "unknown environment name SOME_OTHER") {
		t.Error("a name without either prefix was reported as unknown")
	}
}

// Test plan 7: env-only and imported config produce the same effective settings, over a table of units.
func TestImportEquivalenceWithTheUnitEnvironment(t *testing.T) {
	cases := map[string][]string{
		"minimal":      {"CIRAK_ALLOWED_ROOTS=@WS@", "CIRAK_ALLOWED_SESSIONS=ses_a"},
		"read only":    {"BRIDGE_READ_ONLY=1", "CIRAK_LISTEN=127.0.0.1:9999"},
		"read only 0":  {"CIRAK_READ_ONLY=0"},
		"legacy pair":  {"BRIDGE_LISTEN=127.0.0.1:9191", "CIRAK_LISTEN=127.0.0.1:9191"},
		"lists":        {"CIRAK_ALLOWED_ROOTS=@WS@:@LOCKS@", "CIRAK_ALLOWED_SESSIONS=ses_a,ses_b,ses_c", `CIRAK_DASHBOARD_REDACT_EXTRA=["a","b"]`},
		"ints":         {"CIRAK_CLAUDE_MAX_TURNS=3", "CIRAK_LIVENESS_NO_PROGRESS_S=90", "CIRAK_LIVENESS_TOOL_LONG_S=700"},
		"credentials":  {"CIRAK_TOKEN_CREDENTIAL=token", "CIRAK_OPENCODE_PASSWORD_CREDENTIAL=ocpw"},
		"durations":    {"CIRAK_TOKEN_MAX_LIFETIME=168h", "CIRAK_CLAUDE_RUN_MAX=30m", "CIRAK_CLAUDE_MAX_BUDGET_USD=2.5"},
		"permission":   {"CIRAK_PERMISSION_RELAY=reject_only", "CIRAK_PERMISSION_INCARNATION=off"},
		"unrelated":    {"PATH=/usr/bin", "HOME=/nonexistent", "SOME_OTHER=1"},
		"empty values": {"CIRAK_LISTEN=", "CIRAK_PRINCIPAL="},
	}
	for name, lines := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			c := newCH(t)
			out := c.mkOut(t)
			rep := strings.NewReplacer("@WS@", c.f.ws, "@LOCKS@", c.f.locks)
			for i := range lines {
				lines[i] = rep.Replace(lines[i])
			}
			ef := filepath.Join(c.f.dir, "x.env")
			writeFile(t, ef, strings.Join(lines, "\n")+"\n", 0o600)
			code := c.run("import", "--env-file", ef, "--out", out)
			if code == 2 {
				t.Fatalf("exit %d:\n%s", code, c.all())
			}
			getenv := func(k string) string {
				for _, l := range lines {
					if n, v, _ := strings.Cut(l, "="); n == k {
						return v
					}
				}
				return ""
			}
			env0, _, err := cirakEnv(getenv, legacyPhaseFor("dev"))
			if err != nil {
				t.Fatal(err)
			}
			fromEnv := effectiveOf(t, config.Input{Env: env0})
			fromFile := effectiveOf(t, config.Input{Env: func(string) string { return "" }, File: parseOut(t, out)})
			for k, v := range fromEnv {
				if fromFile[k] != v {
					t.Errorf("%s differs: env %q, imported file %q", k, v, fromFile[k])
				}
			}
		})
	}
}

func TestImportMergesTheUnitLikeSystemd(t *testing.T) {
	c := newCH(t)
	out := c.mkOut(t)
	missing := filepath.Join(c.f.dir, "optional.env")
	ef := filepath.Join(c.f.dir, "unit.env")
	writeFile(t, ef, "CIRAK_LISTEN=127.0.0.1:9191\nCIRAK_PRINCIPAL=gone\n", 0o600)
	// Environment= and EnvironmentFile= for the same key: the file wins; UnsetEnvironment= removes; a missing
	// "-" file is skipped and listed
	c.show = fmt.Sprintf(showFmt, "CIRAK_LISTEN=127.0.0.1:9090 CIRAK_ALLOWED_ROOTS="+c.f.ws+" CIRAK_ALLOWED_SESSIONS=ses_a", ef+" (ignore_errors=no) "+missing+" (ignore_errors=yes)", "CIRAK_PRINCIPAL")
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	f := parseOut(t, out)
	if f.EnvForm("server.listen") != "127.0.0.1:9191" {
		t.Errorf("the EnvironmentFile did not override Environment=: %q", f.EnvForm("server.listen"))
	}
	if f.Has("server.principal") {
		t.Error("UnsetEnvironment= did not remove the variable")
	}
	for _, want := range []string{"  read: " + ef, "  skipped (optional, missing): " + missing} {
		if !strings.Contains(c.all(), want) {
			t.Errorf("missing %q in:\n%s", want, c.all())
		}
	}
}

func TestImportErrorsStopTheImportAndWriteNothing(t *testing.T) {
	big := "CIRAK_LISTEN=127.0.0.1:9191\n# " + strings.Repeat("x", maxEnvFile) + "\n"
	cases := []struct {
		name  string
		setup func(c *ch, ef string)
		text  string
	}{
		{"unreadable", func(c *ch, ef string) { c.denied[ef] = true }, "is unreadable"},
		{"missing and not optional", func(c *ch, ef string) { _ = os.Remove(ef) }, "does not exist and is not marked optional"},
		{"unparseable line", func(c *ch, ef string) {
			writeFile(c.t, ef, "CIRAK_LISTEN=127.0.0.1:9191\nthis is not an assignment\n", 0o600)
		}, "line 2 is not a NAME=VALUE assignment"},
		{"unexpanded specifier", func(c *ch, ef string) { writeFile(c.t, ef, "CIRAK_TOKEN_FILE=%d/token\n", 0o600) }, "unexpanded systemd specifier"},
		{"over 1 MiB", func(c *ch, ef string) { c.files[ef] = big }, "larger than 1 MiB"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			c := newCH(t)
			out := c.mkOut(t)
			ef := c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191")
			tc.setup(c, ef)
			code := c.run("import", "--from-unit", "cirak", "--out", out)
			if code != 2 || !strings.Contains(c.all(), tc.text) {
				t.Fatalf("exit %d, want 2 with %q:\n%s", code, tc.text, c.all())
			}
			if exists(out) || c.stage != "" {
				t.Error("something was written or staged")
			}
			if strings.Contains(c.all(), "9191") {
				t.Error("a value reached the output")
			}
		})
	}
}

func TestImportNeverCopiesAnInlineSecret(t *testing.T) {
	c := newCH(t)
	out := c.mkOut(t)
	c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191", "CIRAK_TOKEN="+tokCanary, "OPENCODE_SERVER_PASSWORD="+pwCanary)
	code := c.run("import", "--from-unit", "cirak", "--out", out)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	body := readF(t, out)
	for _, bad := range []string{tokCanary, pwCanary} {
		if strings.Contains(body, bad) || strings.Contains(c.all(), bad) {
			t.Errorf("an inline secret was copied or printed: %q", bad)
		}
	}
	f := parseOut(t, out)
	if f.Has("token.file") || f.Has("token.credential") || f.Has("harness.opencode.password_file") {
		t.Error("a secret reference was invented")
	}
	dir := filepath.Dir(out)
	for _, want := range []string{"install -m 0600 /dev/null " + shq(filepath.Join(dir, "token")), "install -m 0600 /dev/null " + shq(filepath.Join(dir, "opencode-password")), "is NOT copied into the config"} {
		if !strings.Contains(c.all(), want) {
			t.Errorf("missing %q in:\n%s", want, c.all())
		}
	}
}

func TestImportRejectUnknown(t *testing.T) {
	setup := func(t *testing.T) (*ch, string) {
		c := newCH(t)
		out := c.mkOut(t)
		c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191", "BRIDGE_TYPO_NAME=secret-value", "CIRAK_ALSO_UNKNOWN=1")
		return c, out
	}
	c, out := setup(t)
	if code := c.run("import", "--from-unit", "cirak", "--out", out, "--reject-unknown"); code != 2 || exists(out) {
		t.Fatalf("exit %d, out exists %v:\n%s", code, exists(out), c.all())
	}
	for _, want := range []string{"unknown environment name BRIDGE_TYPO_NAME", "unknown environment name CIRAK_ALSO_UNKNOWN"} {
		if !strings.Contains(c.all(), want) {
			t.Errorf("missing %q in:\n%s", want, c.all())
		}
	}
	// without the flag the names are warnings, by name only, and the import still happens
	c, out = setup(t)
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 1 || !exists(out) || !strings.Contains(c.all(), "unknown environment name BRIDGE_TYPO_NAME") {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	if strings.Contains(c.all(), "secret-value") {
		t.Error("an unknown variable's value was printed")
	}
}

func TestImportOutRules(t *testing.T) {
	c := newCH(t)
	out := c.mkOut(t)
	c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191")
	for name, args := range map[string][]string{
		"no source":      {"import", "--out", out},
		"no out":         {"import", "--from-unit", "cirak"},
		"relative out":   {"import", "--from-unit", "cirak", "--out", "config.json"},
		"unclean out":    {"import", "--from-unit", "cirak", "--out", out + "/../out/config.json"},
		"newline in out": {"import", "--from-unit", "cirak", "--out", out + "\nx"},
		"stray argument": {"import", "--from-unit", "cirak", "--out", out, "stray"},
		"bad unit name":  {"import", "--from-unit", "bad name;rm", "--out", out},
	} {
		c2 := newCH(t)
		if code := c2.run(args...); code != 2 || exists(out) {
			t.Errorf("%s: exit %d\n%s", name, code, c2.all())
		}
	}
	// an existing --out needs --force, and the backup keeps the old bytes (the rollback of section 5 step 5)
	writeFile(t, out, `{"server":{"listen":"127.0.0.1:7000"}}`+"\n", 0o600)
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 2 || !strings.Contains(c.all(), "already exists; --force replaces it") {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	if !strings.Contains(readF(t, out), "7000") {
		t.Fatal("an existing file was replaced without --force")
	}
	c = newCH(t)
	c.mkOut(t)
	c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191")
	writeFile(t, out, `{"server":{"listen":"127.0.0.1:7000"}}`+"\n", 0o600)
	if code := c.run("import", "--from-unit", "cirak", "--out", out, "--force"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	if !strings.Contains(readF(t, out), "9191") {
		t.Error("--force did not replace the file")
	}
	backups, _ := filepath.Glob(out + ".bak-*")
	if len(backups) != 1 || !strings.Contains(readF(t, backups[0]), "7000") || mode(t, backups[0]) != 0o600 {
		t.Fatalf("backups %v", backups)
	}
	// rollback: the backup restores the old file
	if err := os.Rename(backups[0], out); err != nil || !strings.Contains(readF(t, out), "7000") {
		t.Error("the backup does not restore the old config")
	}
}

func TestImportRefusesASymlinkInTheOutPath(t *testing.T) {
	c := newCH(t)
	real := filepath.Join(c.f.dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.f.dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191")
	if code := c.run("import", "--from-unit", "cirak", "--out", filepath.Join(link, "config.json")); code != 2 || !strings.Contains(c.all(), "symbolic link") {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 || c.stage != "" {
		t.Errorf("something was written through the link: %v", entries)
	}
}

// Where the invoker cannot write --out, the config is staged and the exact root command is printed; nothing is
// applied, and nothing is run.
func TestImportStagesWhenOutIsNotWritable(t *testing.T) {
	c := newCH(t)
	c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191", "CIRAK_TOKEN="+tokCanary)
	out := filepath.Join(c.f.dir, "no-such-root", "etc", "cirak", "config.json") // neither it nor its parent exists
	code := c.run("import", "--from-unit", "cirak", "--out", out)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	if exists(out) || exists(filepath.Dir(out)) {
		t.Fatal("init wrote under a directory it cannot write")
	}
	staged := filepath.Join(c.stage, "config.json")
	if c.stage == "" || mode(t, staged) != 0o600 {
		t.Fatal("the config was not staged 0600")
	}
	for _, want := range []string{"install -d -o root -g root -m 0755 " + shq(filepath.Dir(out)), "install -o root -g root -m 0600 " + shq(staged) + " " + shq(out), "rm -r " + shq(c.stage)} {
		if !strings.Contains(c.all(), want) {
			t.Errorf("missing %q in:\n%s", want, c.all())
		}
	}
	if strings.Contains(readF(t, staged), tokCanary) || strings.Contains(c.all(), tokCanary) {
		t.Error("the inline token reached the staged file or the output")
	}
}

// The import reads the unit, not the operator's shell.
func TestImportReadsTheUnitNotTheShell(t *testing.T) {
	c := newCH(t)
	out := c.mkOut(t)
	c.env["CIRAK_LISTEN"] = "127.0.0.1:1111" // the SHELL
	c.env["CIRAK_READ_ONLY"] = "1"
	c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191")
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 0 {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	f := parseOut(t, out)
	if f.EnvForm("server.listen") != "127.0.0.1:9191" || f.Has("server.read_only") {
		t.Errorf("the shell leaked into the import: listen %q, read_only present %v", f.EnvForm("server.listen"), f.Has("server.read_only"))
	}
}

func TestImportRefusesAnInconsistentUnit(t *testing.T) {
	c := newCH(t)
	out := c.mkOut(t)
	// a token file and an inline token together: the daemon itself refuses this pair
	c.unit(t, nil, "CIRAK_TOKEN_FILE="+c.f.token, "CIRAK_TOKEN="+tokCanary)
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 2 || exists(out) || strings.Contains(c.all(), tokCanary) {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	// a legacy name that differs from its CIRAK_ twin
	c = newCH(t)
	out = c.mkOut(t)
	c.unit(t, nil, "BRIDGE_LISTEN=127.0.0.1:1", "CIRAK_LISTEN=127.0.0.1:2")
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 2 || exists(out) {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	// a value the file cannot express
	c = newCH(t)
	out = c.mkOut(t)
	c.unit(t, nil, "CIRAK_READ_ONLY=yes")
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 2 || exists(out) || !strings.Contains(c.all(), "CIRAK_READ_ONLY must be 0 or 1") {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
}

// The guard of the import: the file alone must resolve to what the source resolves to, for every key. A file that
// says something else is refused (nothing is written), so a wrong conversion can never reach the disk.
func TestSameEffectiveSettingsRefusesAFileThatSaysSomethingElse(t *testing.T) {
	env := func(k string) string {
		return map[string]string{"CIRAK_LISTEN": "127.0.0.1:9191", "CIRAK_READ_ONLY": "1"}[k]
	}
	file := func(body string) *config.File {
		f, err := config.Parse([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if err := sameEffectiveSettings(env, file(`{"server":{"listen":"127.0.0.1:9191","read_only":true}}`)); err != nil {
		t.Fatalf("an equal file was refused: %v", err)
	}
	for name, body := range map[string]string{
		"another value":   `{"server":{"listen":"127.0.0.1:9292","read_only":true}}`,
		"a missing key":   `{"server":{"listen":"127.0.0.1:9191"}}`,
		"an extra key":    `{"server":{"listen":"127.0.0.1:9191","read_only":true,"principal":"x"}}`,
		"another boolean": `{"server":{"listen":"127.0.0.1:9191","read_only":false}}`,
	} {
		err := sameEffectiveSettings(env, file(body))
		if err == nil || !strings.Contains(err.Error(), "would differ after the import") {
			t.Errorf("%s: %v", name, err)
		} else if strings.Contains(err.Error(), "9191") || strings.Contains(err.Error(), "9292") {
			t.Errorf("%s: the error echoes a value: %v", name, err)
		}
	}
}

// P3-1 of #562/#566: the exclusivity of --from-unit and --env-file is tested with a VALID unit and a VALID file, so
// that exit 2 can only come from the exclusivity check (with the check gone the import would succeed).
func TestImportRefusesBothSourcesWithAValidUnitAndAValidFile(t *testing.T) {
	c := newCH(t)
	out := c.mkOut(t)
	c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191")
	ef := filepath.Join(c.f.dir, "valid.env")
	writeFile(t, ef, "CIRAK_LISTEN=127.0.0.1:9292\n", 0o600)
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 0 {
		t.Fatalf("the fixture unit is not valid on its own: exit %d\n%s", code, c.all())
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	c.out.Reset()
	c.err.Reset()
	code := c.run("import", "--from-unit", "cirak", "--env-file", ef, "--out", out)
	if code != 2 || !strings.Contains(c.all(), "exactly one of --from-unit and --env-file is required") {
		t.Fatalf("exit %d, want 2 from the exclusivity check:\n%s", code, c.all())
	}
	if exists(out) || c.stage != "" {
		t.Error("something was written or staged")
	}
	if strings.Contains(c.all(), "environment source") {
		t.Error("a source was read before the exclusivity was checked")
	}
}

// P2-1 of #562/#566: a URL that carries a credential (userinfo, a query, a fragment) is never persisted. The import
// refuses it as the daemon does (opencode.New and the remote origin rule both refuse userinfo, a query and a
// fragment), with a value-free message that names only the key, and writes nothing: no file, no staging directory,
// no backup, and an existing --out is left exactly as it was.
func TestImportRefusesAURLThatCarriesACredential(t *testing.T) {
	bases := []struct{ env, key, clean string }{
		{"OPENCODE_URL", "harness.opencode.url", "http://127.0.0.1:4096"},
		{"CIRAK_PUBLIC_ORIGIN", "server.public_origin", "https://bridge.example"},
	}
	suffixes := map[string]func(clean string) string{
		"userinfo":            func(u string) string { return strings.Replace(u, "://", "://user:"+urlCanary+"@", 1) },
		"user only":           func(u string) string { return strings.Replace(u, "://", "://"+urlCanary+"@", 1) },
		"query":               func(u string) string { return u + "/?token=" + urlCanary },
		"query on the origin": func(u string) string { return u + "?token=" + urlCanary },
		"empty query":         func(u string) string { return u + "/?" },
		"fragment":            func(u string) string { return u + "/#" + urlCanary },
		"unparsable":          func(u string) string { return "http://127.0.0.1:" + urlCanary }, // an invalid port: the URL parser refuses it
		"userinfo and query":  func(u string) string { return strings.Replace(u, "://", "://u:"+urlCanary+"@", 1) + "/?k=" + urlCanary },
	}
	for _, b := range bases {
		for name, mk := range suffixes {
			for _, source := range []string{"env-file", "unit"} {
				t.Run(b.env+"/"+strings.ReplaceAll(name, " ", "_")+"/"+source, func(t *testing.T) {
					c := newCH(t)
					out := c.mkOut(t)
					old := `{"server":{"listen":"127.0.0.1:7000"}}` + "\n"
					writeFile(t, out, old, 0o600)
					line := b.env + "=" + mk(b.clean)
					var code int
					if source == "unit" {
						c.unit(t, []string{line, "CIRAK_LISTEN=127.0.0.1:9191"}, "CIRAK_READ_ONLY=0")
						code = c.run("import", "--from-unit", "cirak", "--out", out, "--force")
					} else {
						ef := filepath.Join(c.f.dir, "x.env")
						writeFile(t, ef, line+"\nCIRAK_LISTEN=127.0.0.1:9191\n", 0o600)
						code = c.run("import", "--env-file", ef, "--out", out, "--force")
					}
					all := c.all()
					if code != 2 {
						t.Fatalf("exit %d, want 2:\n%s", code, all)
					}
					if strings.Contains(all, urlCanary) || strings.Contains(all, "user:") || strings.Contains(all, "token=") {
						t.Errorf("the credential reached the output:\n%s", all)
					}
					if !strings.Contains(all, b.env) || !strings.Contains(all, b.key) || !strings.Contains(all, "result: refused; nothing was written (exit 2)") {
						t.Errorf("the refusal does not name the key or say nothing was written:\n%s", all)
					}
					if readF(t, out) != old {
						t.Error("an existing --out was changed")
					}
					if backups, _ := filepath.Glob(out + ".bak-*"); len(backups) != 0 {
						t.Errorf("a backup was made: %v", backups)
					}
					entries, _ := os.ReadDir(filepath.Dir(out))
					if len(entries) != 1 || c.stage != "" {
						t.Errorf("something was written or staged beside the old config: %v (stage %q)", entries, c.stage)
					}
				})
			}
		}
	}
}

// A refused URL leaves no file at all when --out is new, and the staged path is not used either.
func TestImportRefusesAURLThatCarriesACredentialBeforeStaging(t *testing.T) {
	c := newCH(t)
	ef := filepath.Join(c.f.dir, "x.env")
	writeFile(t, ef, "OPENCODE_URL=http://u:"+urlCanary+"@127.0.0.1:4096\n", 0o600)
	out := filepath.Join(c.f.dir, "no-such-root", "etc", "cirak", "config.json") // not writable: the staged path
	if code := c.run("import", "--env-file", ef, "--out", out); code != 2 || c.stage != "" || exists(out) || strings.Contains(c.all(), urlCanary) {
		t.Fatalf("exit %d, staged %q:\n%s", code, c.stage, c.all())
	}
}

// Plain URLs, with and without a trailing slash, are still imported and equal the source's effective value.
func TestImportKeepsAPlainURL(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:4096", "http://127.0.0.1:4096/"} {
		c := newCH(t)
		out := c.mkOut(t)
		ef := filepath.Join(c.f.dir, "x.env")
		writeFile(t, ef, "OPENCODE_URL="+u+"\nCIRAK_PUBLIC_ORIGIN=https://bridge.example\n", 0o600)
		if code := c.run("import", "--env-file", ef, "--out", out); code == 2 {
			t.Fatalf("%s: exit %d\n%s", u, code, c.all())
		}
		if got := parseOut(t, out).EnvForm("harness.opencode.url"); got != u {
			t.Errorf("harness.opencode.url = %q, want %q", got, u)
		}
	}
}
