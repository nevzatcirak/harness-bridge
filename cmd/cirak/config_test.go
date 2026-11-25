package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func home(dir string) func() (string, error) { return func() (string, error) { return dir, nil } }

func privDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestServeFlags(t *testing.T) {
	var out bytes.Buffer
	f, err := parseServeFlags(nil, &out)
	if err != nil || f != (serveFlags{}) {
		t.Fatalf("%+v %v", f, err)
	}
	// "serve" is accepted and optional: a unit may spell it
	f, err = parseServeFlags([]string{"serve", "--config", "/c", "--listen", "127.0.0.1:1", "--read-only", "--token-file", "/t"}, &out)
	if err != nil || f != (serveFlags{config: "/c", listen: "127.0.0.1:1", readOnly: true, tokenFile: "/t"}) {
		t.Fatalf("%+v %v", f, err)
	}
	m := f.asMap()
	if m["server.listen"] != "127.0.0.1:1" || m["server.read_only"] != "1" || m["token.file"] != "/t" || len(m) != 3 {
		t.Fatalf("%v", m)
	}
	if len((serveFlags{}).asMap()) != 0 {
		t.Fatal("an unset flag is not a flag value")
	}
	for _, bad := range [][]string{{"bogus"}, {"--nope"}, {"serve", "extra"}, {"--token", "secret-value"}} {
		if _, err := parseServeFlags(bad, &out); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if _, err := parseServeFlags([]string{"-h"}, &out); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
	// there is no flag that carries a secret: the usage names a file, never a value
	if !strings.Contains(out.String(), "never the token itself") || strings.Contains(out.String(), "-token ") {
		t.Errorf("usage: %q", out.String())
	}
	for _, want := range []string{"flag, then environment", "no file", "current directory"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--help lacks the precedence note %q", want)
		}
	}
}

func TestLocateConfig(t *testing.T) {
	d := privDir(t)
	sys, xdg := filepath.Join(d, "etc", "config.json"), filepath.Join(d, "xdg", "cirak", "config.json")
	explicit := filepath.Join(d, "explicit.json")
	writeFile(t, explicit, "{}", 0o600)
	old := systemConfigPath
	defer func() { systemConfigPath = old }()
	systemConfigPath = sys
	envOf := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	// nothing: environment only
	if p, _, err := locateConfig("", envOf(map[string]string{"XDG_CONFIG_HOME": filepath.Join(d, "xdg")}), home(d)); p != "" || err != nil {
		t.Fatalf("%q %v", p, err)
	}
	// the per-user file
	writeFile(t, xdg, "{}", 0o600)
	if p, ex, err := locateConfig("", envOf(map[string]string{"XDG_CONFIG_HOME": filepath.Join(d, "xdg")}), home(d)); p != xdg || ex || err != nil {
		t.Fatalf("%q %v %v", p, ex, err)
	}
	// the home fallback when XDG_CONFIG_HOME is unset
	writeFile(t, filepath.Join(d, ".config", "cirak", "config.json"), "{}", 0o600)
	if p, _, err := locateConfig("", envOf(nil), home(d)); p != filepath.Join(d, ".config", "cirak", "config.json") || err != nil {
		t.Fatalf("%q %v", p, err)
	}
	// the system file beats the per-user one
	writeFile(t, sys, "{}", 0o600)
	if p, _, err := locateConfig("", envOf(map[string]string{"XDG_CONFIG_HOME": filepath.Join(d, "xdg")}), home(d)); p != sys || err != nil {
		t.Fatalf("%q %v", p, err)
	}
	// CIRAK_CONFIG beats discovery, --config beats CIRAK_CONFIG
	if p, ex, err := locateConfig("", envOf(map[string]string{"CIRAK_CONFIG": explicit}), home(d)); p != explicit || !ex || err != nil {
		t.Fatalf("%q %v %v", p, ex, err)
	}
	other := filepath.Join(d, "other.json")
	writeFile(t, other, "{}", 0o600)
	if p, _, err := locateConfig(other, envOf(map[string]string{"CIRAK_CONFIG": explicit}), home(d)); p != other || err != nil {
		t.Fatalf("%q %v", p, err)
	}
	// an asked-for file must exist; it must be absolute
	if _, _, err := locateConfig(filepath.Join(d, "missing.json"), envOf(nil), home(d)); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("--config missing: %v", err)
	}
	if _, _, err := locateConfig("", envOf(map[string]string{"CIRAK_CONFIG": filepath.Join(d, "missing.json")}), home(d)); err == nil {
		t.Fatal("CIRAK_CONFIG missing was ignored")
	}
	if _, _, err := locateConfig("config.json", envOf(nil), home(d)); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("a relative --config: %v", err)
	}
}

// No implicit file in the current directory, whatever it is called.
func TestNoImplicitFileInTheCurrentDirectory(t *testing.T) {
	d := privDir(t)
	for _, name := range []string{"config.json", "cirak.json", ".cirak.json", "cirak/config.json", filepath.Join(".config", "cirak", "config.json")} {
		writeFile(t, filepath.Join(d, name), `{"server":{"listen":"127.0.0.1:1"}}`, 0o600)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(d); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	oldSys := systemConfigPath
	systemConfigPath = filepath.Join(privDir(t), "none.json")
	defer func() { systemConfigPath = oldSys }()
	// HOME is somewhere else, XDG_CONFIG_HOME is unset
	if p, _, err := locateConfig("", func(string) string { return "" }, home(privDir(t))); p != "" || err != nil {
		t.Fatalf("a file in the working directory was discovered: %q %v", p, err)
	}
}

func TestDiscoverySkipsOnlyAMissingFile(t *testing.T) {
	d := privDir(t)
	old := systemConfigPath
	defer func() { systemConfigPath = old }()
	systemConfigPath = filepath.Join(d, "etc", "config.json")
	// a dangling symlink and an unreadable parent are errors, not "no file"
	if err := os.MkdirAll(filepath.Dir(systemConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(d, "gone"), systemConfigPath); err != nil {
		t.Fatal(err)
	}
	_, err := loadEffective(serveFlags{}, func(string) string { return "" }, uint32(os.Geteuid()), home(d))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a symlinked system config must be refused, not skipped: %v", err)
	}
}

func envFn(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// No config file: the effective environment IS the environment (the live H5 drop-in keeps working).
func TestNoFileIsTheEnvironmentOnly(t *testing.T) {
	old := systemConfigPath
	defer func() { systemConfigPath = old }()
	systemConfigPath = filepath.Join(privDir(t), "none.json")
	h5 := map[string]string{
		"BRIDGE_TOKEN_FILE": "/etc/harness-bridge/token", "BRIDGE_TOKEN": "", "BRIDGE_ALLOWED_ROOTS": "/opt/w", "BRIDGE_ALLOWED_SESSIONS": "ses_h5",
		"BRIDGE_READ_ONLY": "1", "OPENCODE_URL": "http://127.0.0.1:4096", "OPENCODE_SERVER_PASSWORD_FILE": "/etc/pw",
	}
	e, _, err := cirakEnv(envFn(h5), phaseAccept)
	if err != nil {
		t.Fatal(err)
	}
	ld, err := loadEffective(serveFlags{}, e, uint32(os.Geteuid()), home(privDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	if ld.path != "" || len(ld.warnings) != 0 || !strings.Contains(ld.describePath(), "no config file") {
		t.Fatalf("%+v", ld)
	}
	// every name the daemon reads has the same effective value as the environment alone
	for _, k := range []string{"CIRAK_TOKEN_FILE", "CIRAK_TOKEN", "CIRAK_ALLOWED_ROOTS", "CIRAK_ALLOWED_SESSIONS", "CIRAK_READ_ONLY", "OPENCODE_URL", "OPENCODE_SERVER_PASSWORD_FILE", "CIRAK_LISTEN", "CREDENTIALS_DIRECTORY"} {
		if ld.res.Get(k) != e(k) {
			t.Errorf("%s differs from the environment", k)
		}
	}
	if len(ld.res.Unresolved) != 0 {
		t.Fatal("unresolved credentials")
	}
}

func TestFileLayerFeedsTheRuntimeValidators(t *testing.T) {
	d := privDir(t)
	cfg := filepath.Join(d, "config.json")
	writeFile(t, cfg, `{"server":{"listen":"127.0.0.1:9191","read_only":true},"token":{"expires":"2099-01-01T00:00:00Z"},"acl":{"sessions":["ses_a"],"roots":["/r"]}}`, 0o600)
	ld, err := loadEffective(serveFlags{config: cfg}, envFn(nil), uint32(os.Geteuid()), home(d))
	if err != nil {
		t.Fatal(err)
	}
	in, err := readIngressAt(ld.res.Get, t0)
	if err != nil || in.addr != "127.0.0.1:9191" || !in.readOnly || in.remote != nil {
		t.Fatalf("%+v %v", in, err)
	}
	if ld.path != cfg || !strings.Contains(ld.describePath(), cfg) {
		t.Fatalf("the path in use is not logged: %q", ld.describePath())
	}
	// a flag beats the file without a conflict
	ld, err = loadEffective(serveFlags{config: cfg, listen: "127.0.0.1:1"}, envFn(nil), uint32(os.Geteuid()), home(d))
	if err != nil || ld.res.Get("CIRAK_LISTEN") != "127.0.0.1:1" {
		t.Fatalf("%v", err)
	}
	// a differing env value for a file key is a refusal before anything starts
	if _, err := loadEffective(serveFlags{config: cfg}, envFn(map[string]string{"CIRAK_READ_ONLY": "0"}), uint32(os.Geteuid()), home(d)); err == nil || !strings.Contains(err.Error(), "server.read_only") || !strings.Contains(err.Error(), "CIRAK_READ_ONLY") {
		t.Fatalf("a stale CIRAK_READ_ONLY=0 over a hardened file: %v", err)
	}
	// the legacy name of the same variable is the same variable (cirakEnv resolved it)
	legacy, _, err := cirakEnv(envFn(map[string]string{"BRIDGE_READ_ONLY": "0"}), phaseAccept)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadEffective(serveFlags{config: cfg}, legacy, uint32(os.Geteuid()), home(d)); err == nil {
		t.Fatal("a legacy BRIDGE_READ_ONLY=0 silently overrode a hardened file")
	}
}

func TestPerUserFileOfAServiceRunWarns(t *testing.T) {
	d := privDir(t)
	old := systemConfigPath
	defer func() { systemConfigPath = old }()
	systemConfigPath = filepath.Join(d, "none.json")
	xdg := filepath.Join(d, "x", "cirak", "config.json")
	writeFile(t, xdg, "{}", 0o600)
	svc := map[string]string{"XDG_CONFIG_HOME": filepath.Join(d, "x"), "INVOCATION_ID": "abc"}
	ld, err := loadEffective(serveFlags{}, envFn(svc), uint32(os.Geteuid()), home(d))
	if err != nil || len(ld.warnings) != 1 || !strings.Contains(ld.warnings[0], xdg) || !strings.Contains(ld.warnings[0], systemConfigPath) {
		t.Fatalf("%+v %v", ld, err)
	}
	ld, err = loadEffective(serveFlags{}, envFn(map[string]string{"XDG_CONFIG_HOME": filepath.Join(d, "x")}), uint32(os.Geteuid()), home(d))
	if err != nil || len(ld.warnings) != 0 {
		t.Fatalf("an interactive run warned: %+v %v", ld, err)
	}
	// the system file in a service run is the expected place
	writeFile(t, systemConfigPath, "{}", 0o600)
	ld, err = loadEffective(serveFlags{}, envFn(svc), uint32(os.Geteuid()), home(d))
	if err != nil || len(ld.warnings) != 0 || ld.path != systemConfigPath {
		t.Fatalf("%+v %v", ld, err)
	}
}
