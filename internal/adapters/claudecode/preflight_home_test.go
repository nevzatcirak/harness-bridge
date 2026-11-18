package claudecode

import (
	"io/fs"
	"strings"
	"testing"
)

// R6 run 1 fix, round 2 (#416, #412 P3-2): the pre-flight lstat-checks the run home itself: a directory (not a
// symlink), owned by the run user, not group- or world-writable. Fabricated filesystem; nothing is read.
func TestPreflightChecksTheRunHome(t *testing.T) {
	good := func(mode fs.FileMode, uid uint32) *canaryFS {
		f := goodFS(CanaryFormA)
		f.info[cHome] = FileInfo{Mode: mode, UID: uid, Nlink: 2}
		return f
	}
	for _, ok := range []struct {
		name string
		mode fs.FileMode
	}{{"0700", fs.ModeDir | 0o700}, {"0750", fs.ModeDir | 0o750}, {"0755", fs.ModeDir | 0o755}} {
		if err := Preflight(good(ok.mode, cRunUID), planFor(CanaryFormA)); err != nil {
			t.Errorf("a %s run home owned by the run user was refused: %v", ok.name, err)
		}
	}
	for _, c := range []struct {
		name string
		fsys *canaryFS
		want string
	}{
		{"a symlink", good(fs.ModeSymlink|0o777, cRunUID), "run home is not a directory"},
		{"a regular file", good(0o600, cRunUID), "run home is not a directory"},
		{"owned by another user", good(fs.ModeDir|0o700, cRunUID+1), "run home is not owned by the run user"},
		{"owned by root", good(fs.ModeDir|0o700, 0), "run home is not owned by the run user"},
		{"group-writable", good(fs.ModeDir|0o770, cRunUID), "run home is group- or world-writable"},
		{"world-writable", good(fs.ModeDir|0o757, cRunUID), "run home is group- or world-writable"},
		{"world-writable and sticky", good(fs.ModeDir|fs.ModeSticky|0o777, cRunUID), "run home is group- or world-writable"},
	} {
		err := Preflight(c.fsys, planFor(CanaryFormA))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a refusal naming %q", c.name, err, c.want)
		}
	}
}
