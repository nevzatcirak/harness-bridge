package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalTemp returns a temp directory whose path has no symlink component.
func canonicalTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// realValidator is the production validator for the user running the tests.
func realValidator() validator {
	return validator{uid: uint32(os.Geteuid()), owner: ownerOf}
}

// ownedBy returns a validator whose ownership probe reports owner for every
// file, so the owner refusals do not depend on the user running the tests.
func ownedBy(uid, owner uint32) validator {
	return validator{uid: uid, owner: func(fs.FileInfo) (uint32, bool) { return owner, true }}
}

func rootsJSON(t *testing.T, roots ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"roots": roots})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func root(path string, modes ...string) map[string]any {
	return map[string]any{"path": path, "modes": modes}
}

func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want refusal %q, got %v", reason, err)
	}
	if e.Reason != reason {
		t.Fatalf("want refusal %q, got %q (%v)", reason, e.Reason, err)
	}
}

func TestValidConfig(t *testing.T) {
	base := canonicalTemp(t)
	a, b := mkdir(t, base, "a"), mkdir(t, base, "b")
	cfg, err := realValidator().parse(rootsJSON(t,
		map[string]any{"path": a, "modes": []string{"create", "archive"}, "max_sessions": 7, "max_depth": 3, "existing_tools": true},
		root(b, "archive")))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled() || len(cfg.Roots) != 2 {
		t.Fatalf("roots: %+v", cfg.Roots)
	}
	ra, rb := cfg.Roots[0], cfg.Roots[1]
	if ra.Path != a || ra.MaxSessions != 7 || ra.MaxDepth != 3 || !ra.ExistingTools || !ra.Allows(ModeCreate) || !ra.Allows(ModeArchive) {
		t.Fatalf("root a: %+v", ra)
	}
	if rb.Path != b || rb.MaxSessions != DefaultMaxSessions || rb.MaxDepth != 0 || rb.ExistingTools || rb.Allows(ModeCreate) || !rb.Allows(ModeArchive) {
		t.Fatalf("root b: %+v", rb)
	}
	if cfg.CreatePerHour != 10 || cfg.ArchivePerHour != 10 || cfg.MaxTotalSessions != 50 {
		t.Fatalf("defaults: %+v", cfg)
	}
}

func TestGlobalLimits(t *testing.T) {
	cfg, err := realValidator().parse([]byte(`{"create_per_hour":3,"archive_per_hour":4,"max_total_sessions":9}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CreatePerHour != 3 || cfg.ArchivePerHour != 4 || cfg.MaxTotalSessions != 9 {
		t.Fatalf("limits: %+v", cfg)
	}
	for _, in := range []string{
		`{"create_per_hour":0}`, `{"create_per_hour":-1}`,
		`{"archive_per_hour":0}`, `{"archive_per_hour":-5}`,
		`{"max_total_sessions":0}`, `{"max_total_sessions":-1}`,
		`{"create_per_hour":1.5}`, `{"create_per_hour":"10"}`, `{"create_per_hour":null,"archive_per_hour":0}`,
	} {
		if _, err := realValidator().parse([]byte(in)); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
}

func TestEmptyConfigIsDisabled(t *testing.T) {
	for _, in := range []string{`{}`, `{"roots":[]}`} {
		cfg, err := realValidator().parse([]byte(in))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if cfg.Enabled() {
			t.Fatalf("%s enabled lifecycle", in)
		}
	}
	var none *Config
	if none.Enabled() {
		t.Fatal("nil config enabled")
	}
}

func TestRootRefusals(t *testing.T) {
	base := canonicalTemp(t)
	real1 := mkdir(t, base, "real", "sub")
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path, reason string
	}{
		{"relative", "relative/dir", ReasonRootNotAbsolute},
		{"dot relative", ".", ReasonRootNotAbsolute},
		{"trailing slash", real1 + "/", ReasonRootNotCanon},
		{"dot dot", real1 + "/../sub", ReasonRootNotCanon},
		{"double slash", base + "//real/sub", ReasonRootNotCanon},
		{"dot component", base + "/./real/sub", ReasonRootNotCanon},
		{"root slash", "/", ReasonRootIsSlash},
		{"root slashes", "//", ReasonRootNotCanon},
		{"missing", filepath.Join(base, "does-not-exist"), ReasonRootMissing},
		{"dangling symlink", dangling, ReasonRootMissing},
		{"symlink alias as the root", filepath.Join(base, "alias"), ReasonRootNotCanon},
		{"symlink alias in a parent", filepath.Join(base, "alias", "sub"), ReasonRootNotCanon},
		{"regular file", file, ReasonRootNotDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := realValidator().parse(rootsJSON(t, root(tc.path, "create")))
			wantReason(t, err, tc.reason)
		})
	}
}

func TestRootOwner(t *testing.T) {
	dir := mkdir(t, canonicalTemp(t), "r")
	in := rootsJSON(t, root(dir, "create"))
	// Service user 1000, directory owned by 2000: refused.
	_, err := ownedBy(1000, 2000).parse(in)
	wantReason(t, err, ReasonRootOwner)
	// Owned by the service user: accepted.
	if _, err := ownedBy(1000, 1000).parse(in); err != nil {
		t.Fatalf("service-user owner refused: %v", err)
	}
	// Owned by root: accepted.
	if _, err := ownedBy(1000, 0).parse(in); err != nil {
		t.Fatalf("root owner refused: %v", err)
	}
	// Ownership cannot be determined: refused (fail closed).
	unknown := validator{uid: 1000, owner: func(fs.FileInfo) (uint32, bool) { return 0, false }}
	_, err = unknown.parse(in)
	wantReason(t, err, ReasonRootOwner)
}

func TestRootOverlap(t *testing.T) {
	base := canonicalTemp(t)
	parent := mkdir(t, base, "p")
	child := mkdir(t, parent, "c")
	sib1, sib2 := mkdir(t, base, "sib"), mkdir(t, base, "sibling")
	cases := []struct {
		name  string
		roots []string
		ok    bool
	}{
		{"same root twice", []string{parent, parent}, false},
		{"child after parent", []string{parent, child}, false},
		{"parent after child", []string{child, parent}, false},
		{"unrelated", []string{mkdir(t, base, "x"), mkdir(t, base, "y")}, true},
		{"name prefix is not containment", []string{sib1, sib2}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rs []map[string]any
			for _, p := range tc.roots {
				rs = append(rs, root(p, "create"))
			}
			cfg, err := realValidator().parse(rootsJSON(t, rs...))
			if tc.ok {
				if err != nil || len(cfg.Roots) != 2 {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			wantReason(t, err, ReasonRootOverlap)
		})
	}
}

func TestOverlapWithAllowedRootsIsNotChecked(t *testing.T) {
	// ADR-022 3.1: overlap with BRIDGE_ALLOWED_ROOTS is allowed; this package does
	// not read that variable at all.
	dir := mkdir(t, canonicalTemp(t), "r")
	t.Setenv("BRIDGE_ALLOWED_ROOTS", dir)
	if _, err := realValidator().parse(rootsJSON(t, root(dir, "create"))); err != nil {
		t.Fatalf("refused a root equal to an allowed root: %v", err)
	}
}

func TestRootModesAndLimits(t *testing.T) {
	dir := mkdir(t, canonicalTemp(t), "r")
	cases := []struct {
		name   string
		root   map[string]any
		reason string
	}{
		{"no modes key", map[string]any{"path": dir}, ReasonModes},
		{"empty modes", map[string]any{"path": dir, "modes": []string{}}, ReasonModes},
		{"unknown mode", map[string]any{"path": dir, "modes": []string{"delete"}}, ReasonModes},
		{"mixed unknown", map[string]any{"path": dir, "modes": []string{"create", "Create"}}, ReasonModes},
		{"duplicate mode", map[string]any{"path": dir, "modes": []string{"create", "create"}}, ReasonModes},
		{"max_sessions zero", map[string]any{"path": dir, "modes": []string{"create"}, "max_sessions": 0}, ReasonLimit},
		{"max_sessions negative", map[string]any{"path": dir, "modes": []string{"create"}, "max_sessions": -3}, ReasonLimit},
		{"max_depth negative", map[string]any{"path": dir, "modes": []string{"create"}, "max_depth": -1}, ReasonLimit},
		{"no path", map[string]any{"modes": []string{"create"}}, ReasonNoRootPath},
		{"empty path", map[string]any{"path": "", "modes": []string{"create"}}, ReasonNoRootPath},
		{"null path", map[string]any{"path": nil, "modes": []string{"create"}}, ReasonNoRootPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := realValidator().parse(rootsJSON(t, tc.root))
			wantReason(t, err, tc.reason)
		})
	}
	// max_depth zero is allowed (only the root itself).
	cfg, err := realValidator().parse(rootsJSON(t, map[string]any{"path": dir, "modes": []string{"archive"}, "max_depth": 0}))
	if err != nil || cfg.Roots[0].MaxDepth != 0 {
		t.Fatalf("max_depth 0: %v", err)
	}
}

func TestExistingToolsFlagIsPerRoot(t *testing.T) {
	base := canonicalTemp(t)
	a, b, c := mkdir(t, base, "a"), mkdir(t, base, "b"), mkdir(t, base, "c")
	cfg, err := realValidator().parse(rootsJSON(t,
		map[string]any{"path": a, "modes": []string{"create"}, "existing_tools": true},
		map[string]any{"path": b, "modes": []string{"create"}, "existing_tools": false},
		root(c, "create")))
	if err != nil {
		t.Fatal(err)
	}
	got := []bool{cfg.Roots[0].ExistingTools, cfg.Roots[1].ExistingTools, cfg.Roots[2].ExistingTools}
	if got[0] != true || got[1] != false || got[2] != false {
		t.Fatalf("flags: %v (must be per root, default off)", got)
	}
	for _, bad := range []any{"true", 1, nil, []bool{true}} {
		in := rootsJSON(t, map[string]any{"path": a, "modes": []string{"create"}, "existing_tools": bad})
		if bad == nil {
			// null decodes to the zero value for a bool; it must not enable the flag.
			cfg, err := realValidator().parse(in)
			if err != nil || cfg.Roots[0].ExistingTools {
				t.Fatalf("null flag: %v %+v", err, cfg)
			}
			continue
		}
		_, err := realValidator().parse(in)
		wantReason(t, err, ReasonSyntax)
	}
}

func TestStrictJSON(t *testing.T) {
	dir := mkdir(t, canonicalTemp(t), "r")
	good := string(rootsJSON(t, root(dir, "create")))
	for name, in := range map[string]string{
		"empty input":        ``,
		"whitespace":         `   `,
		"not an object":      `[]`,
		"string":             `"x"`,
		"unknown top field":  `{"roots":[],"extra":1}`,
		"unknown root field": `{"roots":[{"path":"` + dir + `","modes":["create"],"id":"x"}]}`,
		"trailing object":    good + good,
		"trailing garbage":   good + ` x`,
		"truncated":          good[:len(good)-2],
		"roots wrong type":   `{"roots":{}}`,
		"path wrong type":    `{"roots":[{"path":5,"modes":["create"]}]}`,
		"modes wrong type":   `{"roots":[{"path":"` + dir + `","modes":"create"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := realValidator().parse([]byte(in))
			wantReason(t, err, ReasonSyntax)
		})
	}
	if _, err := realValidator().parse([]byte(good + "\n  \n")); err != nil {
		t.Fatalf("trailing whitespace refused: %v", err)
	}
}

func TestTooManyRoots(t *testing.T) {
	base := canonicalTemp(t)
	var rs []map[string]any
	for i := 0; i < maxRoots+1; i++ {
		rs = append(rs, root(mkdir(t, base, fmt.Sprintf("r%03d", i)), "create"))
	}
	_, err := realValidator().parse(rootsJSON(t, rs...))
	wantReason(t, err, ReasonTooManyRoots)
}

func writeConfig(t *testing.T, mode fs.FileMode, content []byte) string {
	t.Helper()
	p := filepath.Join(canonicalTemp(t), "lifecycle.json")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFile(t *testing.T) {
	dir := mkdir(t, canonicalTemp(t), "r")
	good := rootsJSON(t, root(dir, "create"))
	cfg, err := realValidator().load(writeConfig(t, 0o600, good))
	if err != nil || len(cfg.Roots) != 1 || cfg.Roots[0].Path != dir {
		t.Fatalf("valid 0600 file: %v %+v", err, cfg)
	}
	for _, mode := range []fs.FileMode{0o644, 0o640, 0o660, 0o666, 0o400, 0o700, 0o601, 0o602, 0o604, 0o000,
		fs.ModeSetuid | 0o600, fs.ModeSetgid | 0o600, fs.ModeSticky | 0o600} {
		_, err := realValidator().load(writeConfig(t, mode, good))
		wantReason(t, err, ReasonConfigMode)
	}
}

func TestLoadFileRefusals(t *testing.T) {
	dir := mkdir(t, canonicalTemp(t), "r")
	good := rootsJSON(t, root(dir, "create"))
	tmp := canonicalTemp(t)

	_, err := realValidator().load("relative.json")
	wantReason(t, err, ReasonConfigPath)
	_, err = realValidator().load(tmp + "/../lifecycle.json")
	wantReason(t, err, ReasonConfigPath)
	_, err = realValidator().load(filepath.Join(tmp, "missing.json"))
	wantReason(t, err, ReasonConfigMissing)

	// A directory is not a config file.
	_, err = realValidator().load(mkdir(t, tmp, "d"))
	wantReason(t, err, ReasonConfigType)

	// A symlink to a valid 0600 file is refused (the path is not the file).
	target := writeConfig(t, 0o600, good)
	link := filepath.Join(tmp, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err = realValidator().load(link)
	wantReason(t, err, ReasonConfigType)

	// Wrong owner: the service user is 1000 and the file is owned by 2000.
	_, err = ownedBy(1000, 2000).load(writeConfig(t, 0o600, good))
	wantReason(t, err, ReasonConfigOwner)
	// Owned by root is not enough for the config file itself.
	_, err = ownedBy(1000, 0).load(writeConfig(t, 0o600, good))
	wantReason(t, err, ReasonConfigOwner)

	// Too large.
	big := append(append([]byte(nil), good...), make([]byte, maxConfigBytes)...)
	_, err = realValidator().load(writeConfig(t, 0o600, big))
	wantReason(t, err, ReasonConfigSize)

	// Content is validated: a file that passes the file checks but holds a bad root.
	_, err = realValidator().load(writeConfig(t, 0o600, rootsJSON(t, root("/", "create"))))
	wantReason(t, err, ReasonRootIsSlash)

	// An empty file is a syntax refusal, never "lifecycle disabled".
	_, err = realValidator().load(writeConfig(t, 0o600, nil))
	wantReason(t, err, ReasonSyntax)
}

func TestFromEnv(t *testing.T) {
	uid := os.Geteuid()
	cfg, err := FromEnv(func(string) string { return "" }, uid)
	if cfg != nil || err != nil {
		t.Fatalf("unset must disable lifecycle: %v %v", cfg, err)
	}
	dir := mkdir(t, canonicalTemp(t), "r")
	path := writeConfig(t, 0o600, rootsJSON(t, root(dir, "create")))
	cfg, err = FromEnv(func(k string) string {
		if k != EnvConfig {
			t.Fatalf("read %q", k)
		}
		return path
	}, uid)
	if err != nil || !cfg.Enabled() {
		t.Fatalf("FromEnv: %v %+v", err, cfg)
	}
	// A set but invalid variable is a startup refusal, not "disabled".
	_, err = FromEnv(func(string) string { return filepath.Join(filepath.Dir(path), "missing.json") }, uid)
	wantReason(t, err, ReasonConfigMissing)
}

func TestErrorTextNamesTheReason(t *testing.T) {
	err := refuse(ReasonRootOverlap, "/a", "overlaps root /b")
	if !strings.Contains(err.Error(), ReasonRootOverlap) || !strings.Contains(err.Error(), "/a") {
		t.Fatalf("error text: %v", err)
	}
}
