//go:build !windows

// This file signals processes with syscall.Kill, which Windows does not have; the ExecRunner it tests runs
// only where the wrapper and sudo exist (review #386 P3-3: `GOOS=windows go vet` failed here).

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// R317-FIX-1 tests written against the PR head 97d92f3 API: each one is RED there.

// P1-1: -service-uid unset (0) must be refused, and so must run == service.
func TestValidateRefusesMissingServiceUIDAndEqualUIDs(t *testing.T) {
	for _, c := range []Config{
		{RunHome: tHome, Root: tRoot, RunUID: 1500},                   // service uid missing (0)
		{RunHome: tHome, Root: tRoot, RunUID: 1500, ServiceUID: 0},    // explicit 0
		{RunHome: tHome, Root: tRoot, RunUID: 1500, ServiceUID: 1500}, // run == service
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

// P2-1: without the history/file-canary/sink observations item 1 is not PASS and the evidence says
// unknown, never true.
func TestItem1WithoutObservationsIsNotPassAndPrintsUnknown(t *testing.T) {
	d, buf, _ := newTestDriver(t, &sim{})
	d.Obs = nil
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result == Pass {
		t.Fatalf("item 1 PASS with unobserved checks: %+v", v)
	}
	out := buf.String()
	for _, bad := range []string{"no_file_canary=true", "no_sink_request=true", "no_decoy_leak=true", "verdict=pass"} {
		if strings.Contains(out, bad) {
			t.Errorf("evidence claims %q for an unobserved check:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "no_file_canary=unknown") || !strings.Contains(out, "no_sink_request=unknown") {
		t.Errorf("evidence lacks unknown:\n%s", out)
	}
}

// P3-1: a pre-existing canary-control directory is never deleted.
func TestPreexistingControlDirIsNeverRemoved(t *testing.T) {
	d, _, fsys := newTestDriver(t, &sim{})
	keep := tHome + "/canary-control/operator-notes.txt"
	fsys.content[keep] = []byte("operator data")
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result == Pass {
		t.Errorf("item 1 PASS over a pre-existing control directory: %+v", v)
	}
	if string(fsys.get(keep)) != "operator data" {
		t.Fatal("a pre-existing file in canary-control was deleted")
	}
}

// P3-2: a cancelled context with a child that ignores SIGTERM returns within the bounded fallback.
func TestExecRunnerCancelIsBounded(t *testing.T) {
	pidf := filepath.Join(t.TempDir(), "pid")
	w := fakeWrapper(t, "echo $$ > "+pidf+"\ntrap '' TERM\nwhile :; do sleep 1; done\n")
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidf); err == nil {
			var pid int
			for _, c := range strings.TrimSpace(string(b)) {
				pid = pid*10 + int(c-'0')
			}
			if pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL) // test hygiene only: the driver itself never SIGKILLs
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	done := make(chan Result, 1)
	go func() {
		r, _ := ExecRunner{Wrapper: w}.Run(ctx, Invocation{Args: okArgs, Timeout: time.Minute})
		done <- r
	}()
	select {
	case r := <-done:
		if r.Err != "canceled" && r.Err != "abandoned" {
			t.Errorf("err = %q", r.Err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Run did not return after cancel: no bounded fallback")
	}
}

// P3-3: items 4 and 8 run alone and still find files to read.
type seenRunner struct {
	inner  Runner
	fsys   *fakeFS
	mu     sync.Mutex
	counts map[string]int
}

func (s *seenRunner) Run(ctx context.Context, inv Invocation) (Result, error) {
	n := 0
	s.fsys.mu.Lock()
	for p := range s.fsys.content {
		if strings.HasPrefix(p, tHome+"/canary-control/") {
			n++
		}
	}
	s.fsys.mu.Unlock()
	s.mu.Lock()
	s.counts[inv.Label] = n
	s.mu.Unlock()
	return s.inner.Run(ctx, inv)
}

func TestItems4And8CreateTheirOwnDecoys(t *testing.T) {
	for _, item := range []int{4, 8} {
		s := &sim{}
		d, _, fsys := newTestDriver(t, s)
		sr := &seenRunner{inner: s, fsys: fsys, counts: map[string]int{}}
		d.Runner = sr
		d.RunItems(context.Background(), []int{item})
		label := map[int]string{4: "signal_interrupt", 8: "sigpipe"}[item]
		if sr.counts[label] == 0 {
			t.Errorf("item %d: no files under canary-control when the run started (%v)", item, sr.counts)
		}
		if len(fsys.content) != 0 {
			t.Errorf("item %d left files behind: %v", item, fsys.content)
		}
	}
}

// P3-4: the argv allow-check refuses what grants permission.
func TestValidateArgvRefusesPermissionGrants(t *testing.T) {
	base := []string{"-p", "--max-turns", "2"}
	for _, extra := range [][]string{
		{"--permission-mode", "bypassPermissions"}, {"--permission-mode", "acceptEdits"}, {"--permission-mode", "plan"},
		{"--allowedTools", "Bash"}, {"--allowed-tools", "Bash"}, {"--permission-prompts", "allow"}, {"--permission-prompt-tool", "x"},
	} {
		if ValidateArgv(append(append([]string{}, base...), extra...)) == nil {
			t.Errorf("accepted %v", extra)
		}
	}
	for _, extra := range [][]string{{"--permission-mode", "default"}, {"--permission-prompts", "none"}, {"--safe-mode"}} {
		if err := ValidateArgv(append(append([]string{}, base...), extra...)); err != nil {
			t.Errorf("refused %v: %v", extra, err)
		}
	}
}

// P3-6: a runner-level failure (Result.Err) reaches the evidence.
type errRunner struct{}

func (errRunner) Run(context.Context, Invocation) (Result, error) {
	return Result{ExitCode: -1, InitAt: -1, ResultAt: -1, ExitAt: -1, Err: "start_failed"}, nil
}

func TestResultErrReachesTheEvidence(t *testing.T) {
	d, buf, _ := newTestDriver(t, &sim{})
	d.Runner = errRunner{}
	d.RunItems(context.Background(), []int{7})
	if !strings.Contains(buf.String(), "err=start_failed") {
		t.Fatalf("Result.Err is not in the evidence:\n%s", buf.String())
	}
}

// P1-1: the production decoy filesystem removes only directories it created.
func TestOSDecoyFSRemovesOnlyDirsItCreated(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".config"), 0o700); err != nil { // pre-existing: not ours
		t.Fatal(err)
	}
	o := &OSDecoyFS{UID: os.Getuid(), GID: os.Getgid()}
	f := filepath.Join(home, ".config", "anthropic", "configs", "decoy-profile.json")
	if err := o.Write(f, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := o.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := o.RemoveDir(filepath.Join(home, ".config")); err != nil {
		t.Fatalf("RemoveDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "anthropic")); err == nil {
		t.Error("a directory the driver created was left behind")
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); err != nil {
		t.Error("a pre-existing directory was removed")
	}
}
