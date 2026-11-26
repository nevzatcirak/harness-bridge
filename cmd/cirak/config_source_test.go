package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-027 S4: `config check --from-unit/--env-file` (the gap #547 recorded), the unit's own HOME for config
// discovery (#548 P3-4), the credential limitation line (#548 P3-5), the 1 MiB rule of an environment file
// (#548 P3-6), and the side-by-side comparison of `doctor` (ADR-027 section 5 step 3).

func TestConfigCheckFromUnitReadsTheUnitNotTheShell(t *testing.T) {
	c := newCH(t)
	c.f.write(t, cleanConfig)
	ef := c.unit(t, []string{"CIRAK_CONFIG=" + c.f.cfg}, "CIRAK_LISTEN=127.0.0.1:9191", "CIRAK_READ_ONLY=0")
	c.env["CIRAK_LISTEN"] = "127.0.0.1:1111" // the SHELL disagrees; the unit is the source, so there is no refusal
	c.env["CIRAK_READ_ONLY"] = "1"
	code := c.run("check", "--from-unit", "cirak", "--print-effective")
	out := c.all()
	if code == 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"environment source: unit cirak", "  read: " + ef} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !regexpMatch(out, `server\.listen\s+environment\s+127\.0\.0\.1:9191`) || strings.Contains(out, "127.0.0.1:1111") {
		t.Errorf("the check read the shell, not the unit:\n%s", out)
	}
	// --env-file is the same source
	c2 := newCH(t)
	c2.f.write(t, cleanConfig)
	ef2 := filepath.Join(c2.f.dir, "x.env")
	writeFile(t, ef2, "CIRAK_CONFIG="+c2.f.cfg+"\nCIRAK_LISTEN=127.0.0.1:9191\n", 0o600)
	if code := c2.run("check", "--env-file", ef2, "--print-effective"); code == 2 || !regexpMatch(c2.all(), `server\.listen\s+environment\s+127\.0\.0\.1:9191`) {
		t.Fatalf("exit %d:\n%s", code, c2.all())
	}
}

func TestConfigCheckSourceErrorsAreErrorsNotGuesses(t *testing.T) {
	cases := map[string]func(c *ch, ef string){
		"unreadable":     func(c *ch, ef string) { c.denied[ef] = true },
		"mandatory gone": func(c *ch, ef string) { _ = os.Remove(ef) },
		"bad line":       func(c *ch, ef string) { writeFile(c.t, ef, "garbage line\n", 0o600) },
		"over 1 MiB":     func(c *ch, ef string) { c.files[ef] = "A=1\n# " + strings.Repeat("x", maxEnvFile) + "\n" },
	}
	for name, mutate := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			c := newCH(t)
			c.f.write(t, cleanConfig)
			ef := c.unit(t, []string{"CIRAK_CONFIG=" + c.f.cfg}, "CIRAK_LISTEN=127.0.0.1:9191")
			mutate(c, ef)
			if code := c.run("check", "--from-unit", "cirak"); code != 2 || !strings.Contains(c.all(), "REFUSED") || strings.Contains(c.all(), "result: clean") {
				t.Fatalf("exit %d:\n%s", code, c.all())
			}
		})
	}
	c := newCH(t)
	if code := c.run("check", "--from-unit", "u", "--env-file", "/x"); code != 2 {
		t.Errorf("--from-unit with --env-file: exit %d", code)
	}
}

// #548 P3-4: a unit or file source never falls back to the invoking user's HOME for the per-user config.
func TestSourceModeNeverUsesTheInvokingUsersHome(t *testing.T) {
	decoyHome := filepath.Join(privDir(t), "decoy")
	decoyCfg := filepath.Join(decoyHome, ".config", "cirak", "config.json")
	if err := os.MkdirAll(filepath.Dir(decoyCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", decoyHome)
	for _, args := range [][]string{{"check", "--from-unit", "cirak"}, {"doctor-unit"}} {
		c := newCH(t)
		c.f.write(t, cleanConfig)
		writeFile(t, decoyCfg, readF(t, c.f.cfg), 0o600)
		c.unit(t, nil, "CIRAK_LISTEN=127.0.0.1:9191") // no CIRAK_CONFIG, no HOME
		var code int
		if args[0] == "doctor-unit" {
			code = c.dh.run("--from-unit", "cirak")
		} else {
			code = c.run(args...)
		}
		out := c.all()
		if strings.Contains(out, decoyCfg) || strings.Contains(out, "config: using") {
			t.Fatalf("%v: the invoking user's config was used (exit %d):\n%s", args, code, out)
		}
		if !strings.Contains(out, "config: no config file (environment only)") {
			t.Errorf("%v: the absence of a config file is not stated:\n%s", args, out)
		}
	}
	// the unit's own HOME is the one used
	c := newCH(t)
	c.f.write(t, cleanConfig)
	unitHome := filepath.Join(c.f.dir, "unit-home")
	unitCfg := filepath.Join(unitHome, ".config", "cirak", "config.json")
	if err := os.MkdirAll(filepath.Dir(unitCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, unitCfg, readF(t, c.f.cfg), 0o600)
	c.unit(t, []string{"HOME=" + unitHome}, "CIRAK_LISTEN=127.0.0.1:9191")
	if code := c.run("check", "--from-unit", "cirak"); code == 2 || !strings.Contains(c.all(), "config: using "+unitCfg) || strings.Contains(c.all(), decoyCfg) {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	// a relative HOME is not trusted
	c = newCH(t)
	c.f.write(t, cleanConfig)
	c.unit(t, []string{"HOME=relative/home"}, "CIRAK_LISTEN=127.0.0.1:9191")
	if code := c.run("check", "--from-unit", "cirak"); strings.Contains(c.all(), "config: using") {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
	// the process source (no flag) keeps today's behaviour: the invoking user's home
	c = newCH(t)
	c.f.write(t, cleanConfig)
	writeFile(t, decoyCfg, readF(t, c.f.cfg), 0o600)
	if code := c.run("check"); !strings.Contains(c.all(), "config: using "+decoyCfg) {
		t.Fatalf("exit %d:\n%s", code, c.all())
	}
}

// #548 P3-5: a token delivered by LoadCredential cannot be resolved from `systemctl show`; the command says so,
// as UNVERIFIED, and never reads the credential.
func TestFromUnitSaysACredentialTokenIsUnverifiedAndNeverReadsIt(t *testing.T) {
	c := newCH(t)
	c.f.write(t, `{"token":{"credential":"token"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	c.unit(t, []string{"CIRAK_CONFIG=" + c.f.cfg}, "CIRAK_LISTEN=127.0.0.1:9191")
	d := c.deps()
	var reads []string
	orig := d.readFile
	d.readFile = func(p string) ([]byte, error) { reads = append(reads, p); return orig(p) }
	code := runConfigDeps([]string{"check", "--from-unit", "cirak"}, d)
	out := c.all()
	if code != 1 || !strings.Contains(out, "UNVERIFIED token: the unit delivers it as a systemd credential; `systemctl show` does not expose $CREDENTIALS_DIRECTORY") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, p := range reads {
		if strings.Contains(p, "credentials") {
			t.Errorf("a credential path was read: %s", p)
		}
	}
	if strings.Contains(out, "result: clean") || strings.Contains(out, "token source readable") {
		t.Errorf("an unresolved credential must not pass:\n%s", out)
	}
	// without a source the line points at --from-unit and says it still cannot resolve a credential
	c2 := newCH(t)
	c2.f.write(t, `{"token":{"credential":"token"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	c2.run("check", "--config", c2.f.cfg)
	if !strings.Contains(c2.all(), "use --from-unit (which still cannot resolve a credential)") {
		t.Errorf("the shell-mode line is not clear:\n%s", c2.all())
	}
}

// #548 P3-6: an environment file over 1 MiB is an error, never a silently truncated source.
func TestEnvironmentFileOverOneMiBIsAnError(t *testing.T) {
	dir := privDir(t)
	over, exact := filepath.Join(dir, "over.env"), filepath.Join(dir, "exact.env")
	pad := func(n int) string {
		head := "CIRAK_LISTEN=127.0.0.1:9191\n#"
		return head + strings.Repeat("x", n-len(head)-1) + "\n"
	}
	writeFile(t, over, pad(maxEnvFile+1), 0o600)
	writeFile(t, exact, pad(maxEnvFile), 0o600)
	if _, err := osReadEnvFile(over); err == nil || err.Error() != "the file is larger than 1 MiB" {
		t.Fatalf("a 1 MiB + 1 file: %v", err)
	}
	data, err := osReadEnvFile(exact)
	if err != nil || len(data) != maxEnvFile {
		t.Fatalf("a file of exactly 1 MiB: %d bytes, %v", len(data), err)
	}
	// the cut would have hidden this setting: it is at the end of the file
	late := filepath.Join(dir, "late.env")
	writeFile(t, late, "#"+strings.Repeat("x", maxEnvFile)+"\nCIRAK_PRINCIPAL=late-setting\n", 0o600)
	if _, err := envFileSource(late, osReadEnvFile); err == nil || !strings.Contains(err.Error(), "larger than 1 MiB") {
		t.Fatalf("a setting after the 1 MiB mark was silently dropped: %v", err)
	}
	// a reader that does not enforce the cap itself is still refused
	if _, err := envFileSource(over, func(string) ([]byte, error) { return make([]byte, maxEnvFile+5), nil }); err == nil || !strings.Contains(err.Error(), "larger than 1 MiB") {
		t.Fatalf("a long fake read: %v", err)
	}
}

// ---- the side-by-side comparison of `doctor` ----

// envFor writes an environment file next to the fixture config and returns its path.
func (c *ch) envFor(t *testing.T, lines ...string) string {
	t.Helper()
	ef := filepath.Join(c.f.dir, "side.env")
	writeFile(t, ef, strings.Join(lines, "\n")+"\n", 0o600)
	return ef
}

func (c *ch) matchingLines() []string {
	return []string{"CIRAK_TOKEN_FILE=" + c.f.token, "CIRAK_TOKEN_EXPIRES=2099-01-01T00:00:00Z", "CIRAK_ALLOWED_ROOTS=" + c.f.ws, "CIRAK_ALLOWED_SESSIONS=ses_abc", "CIRAK_WRITER_LOCK_DIR=" + c.f.locks}
}

func TestDoctorCompareAgreesWhenTheFileSaysWhatTheUnitSays(t *testing.T) {
	c := newCH(t)
	c.f.write(t, cleanConfig)
	ef := c.envFor(t, c.matchingLines()...)
	code := c.dh.run("--config", c.f.cfg, "--env-file", ef)
	out := c.all()
	if !strings.Contains(out, "compare: the config file and the source agree on every setting the source sets") || strings.Contains(out, "do NOT remove") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "DIFFERENT") || strings.Contains(out, "ONLY IN THE SOURCE") {
		t.Errorf("a difference was reported for an equal pair:\n%s", out)
	}
	for _, canary := range []string{c.f.token, c.f.ws} {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "token.file") && strings.Contains(line, canary) {
				t.Errorf("a secret reference path was printed in the comparison: %q", line)
			}
		}
	}
}

func TestDoctorCompareBlocksTheLegacyRemovalOnADifference(t *testing.T) {
	c := newCH(t)
	c.f.write(t, cleanConfig)
	lines := c.matchingLines()
	lines[3] = "CIRAK_ALLOWED_SESSIONS=ses_other" // the file says ses_abc
	ef := c.envFor(t, lines...)
	code := c.dh.run("--config", c.f.cfg, "--env-file", ef)
	out := c.all()
	if code != 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"acl.sessions", "DIFFERENT", "REFUSED  compare: 1 setting(s) DIFFER", "do NOT remove the legacy lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "agree on every setting") {
		t.Errorf("a difference was reported as agreement:\n%s", out)
	}
}

func TestDoctorCompareReportsASettingOnlyInTheSourceAsAWarning(t *testing.T) {
	c := newCH(t)
	c.f.write(t, cleanConfig)
	ef := c.envFor(t, append(c.matchingLines(), "CIRAK_LISTEN=127.0.0.1:9191")...)
	code := c.dh.run("--config", c.f.cfg, "--env-file", ef)
	out := c.all()
	if code != 1 || !regexpMatch(out, `server\.listen\s+source=127\.0\.0\.1:9191\s+file=unset\s+ONLY IN THE SOURCE`) || !strings.Contains(out, "warning  compare: 1 setting(s) are set only in the source") || !strings.Contains(out, "do NOT remove the legacy lines") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// a setting only in the file is informational: the file carries more than the unit does
	c = newCH(t)
	c.f.write(t, cleanConfig)
	ef = c.envFor(t, "CIRAK_ALLOWED_SESSIONS=ses_abc")
	c.dh.run("--config", c.f.cfg, "--env-file", ef)
	if !regexpMatch(c.all(), `acl\.roots\s+source=unset\s+file=\S+\s+only in the file`) {
		t.Errorf("a file-only setting is not listed:\n%s", c.all())
	}
}

func TestDoctorCompareShowsSecretsAsSetAndRedactsURLs(t *testing.T) {
	c := newCH(t)
	c.f.write(t, `{"token":{"file":"@TOKEN@","expires":"2099-01-01T00:00:00Z"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"},"harness":{"opencode":{"url":"http://user:`+urlCanary+`@127.0.0.1:4096"}}}`)
	lines := append(c.matchingLines(), "OPENCODE_URL=http://user:"+urlCanary+"@127.0.0.1:4096")
	ef := c.envFor(t, lines...)
	c.dh.run("--config", c.f.cfg, "--env-file", ef)
	out := c.all()
	if strings.Contains(out, urlCanary) {
		t.Errorf("a URL credential reached the output:\n%s", out)
	}
	if !regexpMatch(out, `token\.file\s+source=set\s+file=set\s+same`) || !regexpMatch(out, `harness\.opencode\.url\s+source=http://\*\*\*@127\.0\.0\.1:4096\s+file=http://\*\*\*@127\.0\.0\.1:4096\s+same`) {
		t.Errorf("the rows are not in the secret-safe form:\n%s", out)
	}
}

func TestDoctorCompareCountsAnInlineTokenTheFileDoesNotCarry(t *testing.T) {
	c := newCH(t)
	c.f.write(t, `{"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	ef := c.envFor(t, "CIRAK_TOKEN="+tokCanary, "CIRAK_ALLOWED_SESSIONS=ses_abc")
	code := c.dh.run("--config", c.f.cfg, "--env-file", ef)
	out := c.all()
	if code == 0 || !regexpMatch(out, `token\s+source=inline\s+file=unset\s+ONLY IN THE SOURCE`) || strings.Contains(out, tokCanary) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestDoctorCompareIsAbsentWithoutASourceOrAConfigFile(t *testing.T) {
	c := newCH(t)
	c.f.write(t, cleanConfig)
	c.dh.run("--config", c.f.cfg)
	if strings.Contains(c.all(), "compare:") {
		t.Errorf("the shell source printed a comparison:\n%s", c.all())
	}
	c = newCH(t)
	ef := c.envFor(t, "CIRAK_LISTEN=127.0.0.1:9191")
	c.dh.run("--env-file", ef)
	if !strings.Contains(c.all(), "compare: no config file in use; there is nothing to compare with the source") {
		t.Errorf("the absence of a config file is not stated:\n%s", c.all())
	}
}

func TestSideBySideUsesTheImportedConfig(t *testing.T) {
	// the whole migration of section 5, in the fakes: import, then compare, then check
	c := newCH(t)
	out := c.mkOut(t)
	c.unit(t, nil, c.matchingLines()...)
	if code := c.run("import", "--from-unit", "cirak", "--out", out); code != 0 {
		t.Fatalf("import: exit %d:\n%s", code, c.all())
	}
	c.out.Reset()
	c.err.Reset()
	code := c.dh.run("--config", out, "--from-unit", "cirak")
	if !strings.Contains(c.all(), "agree on every setting") || strings.Contains(c.all(), "REFUSED") {
		t.Fatalf("doctor after import: exit %d:\n%s", code, c.all())
	}
	// a drift after the import blocks the removal of the legacy lines
	writeFile(t, c.f.dir+"/unit.env", strings.Join(append(c.matchingLines()[:3], "CIRAK_ALLOWED_SESSIONS=ses_changed", "CIRAK_WRITER_LOCK_DIR="+c.f.locks), "\n")+"\n", 0o600)
	c.out.Reset()
	c.err.Reset()
	if code := c.dh.run("--config", out, "--from-unit", "cirak"); code != 2 || !strings.Contains(c.all(), "do NOT remove the legacy lines") {
		t.Fatalf("a drifted unit: exit %d:\n%s", code, c.all())
	}
	_ = fmt.Sprint
}
