package claudecode

import (
	"io/fs"
	"strings"
	"testing"
)

// R6 run 4 follow-up (#430, review #429 P2-1): the run-home part of the pre-flight is exported for the runs that
// hand the CLI the run home as a readable scope (items 4 and 6 of the acceptance driver). It must refuse exactly
// what Preflight refuses for the same run home, with the same wording, because both go through one helper.
// Fabricated filesystem; nothing is read.

func homeFS(mode fs.FileMode, uid uint32) *canaryFS {
	f := goodFS(CanaryFormA)
	f.info[cHome] = FileInfo{Mode: mode, UID: uid, Nlink: 2}
	return f
}

func TestPreflightRunHomeAcceptsAGoodRunHome(t *testing.T) {
	for _, m := range []fs.FileMode{fs.ModeDir | 0o700, fs.ModeDir | 0o750, fs.ModeDir | 0o755} {
		if err := PreflightRunHome(homeFS(m, cRunUID), cTree, cHome, cRunUID); err != nil {
			t.Errorf("mode %v: %v", m, err)
		}
	}
	// the acceptance driver uses the run home as the tree too
	f := goodFS(CanaryFormA)
	if err := PreflightRunHome(f, cHome, cHome, cRunUID); err != nil {
		t.Errorf("tree = run home: %v", err)
	}
}

func TestPreflightRunHomeRefusesWhatPreflightRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		fsys *canaryFS
		want string
	}{
		{"a symlink", homeFS(fs.ModeSymlink|0o777, cRunUID), "run home is not a directory"},
		{"a regular file", homeFS(0o600, cRunUID), "run home is not a directory"},
		{"owned by another user", homeFS(fs.ModeDir|0o700, cRunUID+1), "run home is not owned by the run user"},
		{"owned by root", homeFS(fs.ModeDir|0o700, 0), "run home is not owned by the run user"},
		{"group-writable", homeFS(fs.ModeDir|0o770, cRunUID), "run home is group- or world-writable"},
		{"world-writable", homeFS(fs.ModeDir|0o757, cRunUID), "run home is group- or world-writable"},
		{"world-writable and sticky", homeFS(fs.ModeDir|fs.ModeSticky|0o777, cRunUID), "run home is group- or world-writable"},
	} {
		got := PreflightRunHome(c.fsys, cTree, cHome, cRunUID)
		if got == nil || !strings.Contains(got.Error(), c.want) {
			t.Errorf("%s: %v, want a refusal naming %q", c.name, got, c.want)
		}
		// the same run home through the full Preflight: the same refusal, word for word
		full := Preflight(c.fsys, planFor(CanaryFormA))
		if got != nil && (full == nil || full.Error() != got.Error()) {
			t.Errorf("%s: PreflightRunHome says %q, Preflight says %v", c.name, got, full)
		}
	}
}

func TestPreflightRunHomeChecksTheTreeAndHome(t *testing.T) {
	// the run user must not be root
	if err := PreflightRunHome(homeFS(fs.ModeDir|0o700, 0), cTree, cHome, 0); err == nil {
		t.Error("a root run user was accepted")
	}
	// paths must be clean absolute paths, never the root
	for _, c := range []struct{ tree, home string }{
		{"", cHome}, {cTree, ""}, {"srv/tree", cHome}, {cTree, "home"}, {"/", cHome}, {cTree, "/"},
		{cTree + "/", cHome}, {cTree, cHome + "/"}, {cTree + "/../tree", cHome}, {cTree, cTree + "/home/../home"},
	} {
		if err := PreflightRunHome(homeFS(fs.ModeDir|0o700, cRunUID), c.tree, c.home, cRunUID); err == nil {
			t.Errorf("tree %q home %q was accepted", c.tree, c.home)
		}
	}
	// a tree that is missing, or not a directory
	f := goodFS(CanaryFormA)
	delete(f.info, cTree)
	if err := PreflightRunHome(f, cTree, cHome, cRunUID); err == nil {
		t.Error("a missing tree was accepted")
	}
	g := goodFS(CanaryFormA)
	g.info[cTree] = FileInfo{Mode: 0o600, UID: cRunUID, Nlink: 1}
	if err := PreflightRunHome(g, cTree, cHome, cRunUID); err == nil {
		t.Error("a tree that is a regular file was accepted")
	}
	// HOME outside the tree is refused
	if err := PreflightRunHome(homeFS(fs.ModeDir|0o700, cRunUID), cTree, "/srv/canary/elsewhere", cRunUID); err == nil {
		t.Error("a HOME outside the tree was accepted")
	}
}
