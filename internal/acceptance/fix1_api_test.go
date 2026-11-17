package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// obsFn is an Observer whose answers are computed when item 1 asks, so a test can return the
// decoy content (known only to the filesystem fake) as the "history".
type obsFn struct {
	hist           func() []byte
	histOK, fileOK bool
	sinkOK         bool
	files, sink    int
}

func (o obsFn) History(context.Context) ([]byte, bool) {
	if o.hist == nil {
		return nil, o.histOK
	}
	return o.hist(), o.histOK
}
func (o obsFn) FileCanaries(context.Context) (int, bool) { return o.files, o.fileOK }
func (o obsFn) SinkRequests(context.Context) (int, bool) { return o.sink, o.sinkOK }

// P2-1: each observed check can FAIL item 1; a partly observed set is INCOMPLETE, never PASS, and
// names what is missing.
func TestItem1ObservationsDecideTheVerdict(t *testing.T) {
	cases := []struct {
		name    string
		obs     func(f *fakeFS) Observer
		want    string
		whyHas  string
		lineHas string
	}{
		{"all observed and clean", func(*fakeFS) Observer { return allObserved() }, Pass, "all_probes_denied", "no_sink_request=true"},
		{"history shows the decoy", func(f *fakeFS) Observer {
			return obsFn{histOK: true, fileOK: true, sinkOK: true, hist: func() []byte { return f.get(tHome + "/.claude/.credentials.decoy.json") }}
		}, Fail, "canary_failed", "no_decoy_leak=false"},
		{"a file canary exists", func(*fakeFS) Observer { return obsFn{histOK: true, fileOK: true, sinkOK: true, files: 1} }, Fail, "canary_failed", "no_file_canary=false"},
		{"the sink got a request", func(*fakeFS) Observer { return obsFn{histOK: true, fileOK: true, sinkOK: true, sink: 2} }, Fail, "canary_failed", "no_sink_request=false"},
		{"only the sink observed", func(*fakeFS) Observer { return obsFn{sinkOK: true} }, Incomplete, "unobserved_history_file_canary", "no_sink_request=true"},
		{"history not observed", func(*fakeFS) Observer { return obsFn{fileOK: true, sinkOK: true} }, Incomplete, "unobserved_history", "no_decoy_leak=unknown"},
		{"file scan not observed", func(*fakeFS) Observer { return obsFn{histOK: true, sinkOK: true} }, Incomplete, "unobserved_file_canary", "no_file_canary=unknown"},
	}
	for _, c := range cases {
		d, buf, fsys := newTestDriver(t, &sim{})
		d.Obs = c.obs(fsys)
		v := verdict(d.RunItems(context.Background(), []int{1}), 1)
		if v.Result != c.want || !strings.Contains(v.Why, c.whyHas) {
			t.Errorf("%s: %+v", c.name, v)
		}
		if !strings.Contains(buf.String(), c.lineHas) {
			t.Errorf("%s: evidence lacks %q:\n%s", c.name, c.lineHas, buf.String())
		}
		if c.want != Pass && strings.Contains(buf.String(), "verdict=pass") {
			t.Errorf("%s: evidence says verdict=pass", c.name)
		}
	}
}

// The unobserved evidence never contains the marker, and the precondition line is printed.
func TestPreconditionAndNoMarkerInEvidence(t *testing.T) {
	d, buf, _ := newTestDriver(t, &sim{})
	d.RunItems(context.Background(), []int{1})
	if !strings.Contains(buf.String(), "child_home=operator_precondition_not_verified") {
		t.Errorf("no precondition line:\n%s", buf.String())
	}
}

// P1-1: a driver cannot be built with a missing service UID, whatever the other fields say.
func TestNewRefusesMissingServiceUID(t *testing.T) {
	if _, err := New(Config{RunHome: tHome, Root: tRoot, RunUID: 1500}, &sim{}, newFakeFS(), newFakeFS(), noProcs{}, NewEvidence(&strings.Builder{})); err == nil {
		t.Fatal("New accepted ServiceUID 0")
	}
}

// P3-1: a failed Mkdir of the control directory runs nothing and removes nothing.
func TestControlDirCollisionRunsNothing(t *testing.T) {
	d, _, fsys := newTestDriver(t, &sim{})
	fsys.dirs = map[string]bool{tHome + "/canary-control": true} // exists (not ours)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Fail || v.Why != "control_dir_exists_or_not_creatable" || d.Runs() != 0 {
		t.Fatalf("%+v runs %d", v, d.Runs())
	}
	if !fsys.dirs[tHome+"/canary-control"] {
		t.Fatal("the pre-existing directory was removed")
	}
}

// Items 4 and 8 also refuse a pre-existing control directory.
func TestItems4And8RefuseAPreexistingDir(t *testing.T) {
	for _, item := range []int{4, 8} {
		d, _, fsys := newTestDriver(t, &sim{})
		fsys.dirs = map[string]bool{tHome + "/canary-control": true}
		v := verdict(d.RunItems(context.Background(), []int{item}), item)
		if v.Result != Fail || !strings.Contains(v.Why, "control_dir_exists") {
			t.Errorf("item %d: %+v", item, v)
		}
		if !fsys.dirs[tHome+"/canary-control"] {
			t.Errorf("item %d removed a pre-existing directory", item)
		}
	}
}

// P1-1: the production FS refuses to adopt an existing directory and leaves a non-empty one.
func TestOSDecoyFSMkdirAndNonEmptyDir(t *testing.T) {
	root := t.TempDir()
	o := &OSDecoyFS{UID: os.Getuid(), GID: os.Getgid()}
	existing := filepath.Join(root, "canary-control")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := o.Mkdir(existing); err == nil {
		t.Fatal("Mkdir adopted an existing directory")
	}
	if err := o.RemoveDir(existing); err != nil {
		t.Fatalf("RemoveDir of an unrecorded directory: %v", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Fatal("an unrecorded directory was removed")
	}
	mine := filepath.Join(root, "mine")
	if err := o.Mkdir(mine); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "operator.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := o.RemoveDir(mine); err == nil {
		t.Error("a non-empty directory was reported removed")
	}
	if _, err := os.Stat(filepath.Join(mine, "operator.txt")); err != nil {
		t.Fatal("a file the driver did not write was deleted (no RemoveAll)")
	}
}

// Every directory the driver creates (control directory, decoy parents) is removed again.
func TestCleanupRemovesTheDirectoriesItCreated(t *testing.T) {
	for _, item := range []int{1, 4, 6, 8} {
		d, _, fsys := newTestDriver(t, &sim{})
		d.RunItems(context.Background(), []int{item})
		if len(fsys.dirs) != 0 || len(fsys.content) != 0 {
			t.Errorf("item %d left dirs %v files %v", item, fsys.dirs, fsys.content)
		}
	}
}
