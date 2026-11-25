package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func info(main string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{GoVersion: "go1.27.2", Main: debug.Module{Path: "github.com/nevzatcirak/cirak", Version: main}, Settings: settings}
}

func TestFromBuildInfo(t *testing.T) {
	for _, c := range []struct {
		name string
		bi   *debug.BuildInfo
		ok   bool
		want string
	}{
		{"tagged module version", info("v0.1.0"), true, "v0.1.0"},
		{"pseudo-version of a vcs build", info("v0.0.0-20261121034852-e2ac772bf94c"), true, "v0.0.0-20261121034852-e2ac772bf94c"},
		{"dirty pseudo-version is kept as stamped", info("v0.0.0-20261121034852-e2ac772bf94c+dirty"), true, "v0.0.0-20261121034852-e2ac772bf94c+dirty"},
		{"devel build", info("(devel)"), true, "devel"},
		{"empty version", info(""), true, "devel"},
		{"no build info", nil, false, "unknown"},
		{"build info absent even with a value", info("v9.9.9"), false, "unknown"},
	} {
		if got := fromBuildInfo(c.bi, c.ok); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDetailNeverFabricates(t *testing.T) {
	got := Detail(info("v0.1.0",
		debug.BuildSetting{Key: "vcs.revision", Value: "e2ac772bf94c521f6cc1ec4ae152c28cd4b54782"},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"}), true)
	want := "cirak v0.1.0\nrevision e2ac772bf94c521f6cc1ec4ae152c28cd4b54782 modified=false\ngo go1.27.2\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := Detail(info("(devel)"), true); got != "cirak devel\ngo go1.27.2\n" {
		t.Fatalf("no vcs settings: %q", got)
	}
	if got := Detail(nil, false); got != "cirak unknown\n" || strings.Contains(got, "go") {
		t.Fatalf("no build info: %q", got)
	}
}

func TestVersionOfTheTestBinary(t *testing.T) {
	// A test binary is not a stamped release: the value is one of the documented fallbacks
	// or a module version, never empty and never the old hard-coded literal.
	v := Version()
	if v == "" || v == "0.2.0" || v == "(devel)" {
		t.Fatalf("Version() = %q", v)
	}
}
