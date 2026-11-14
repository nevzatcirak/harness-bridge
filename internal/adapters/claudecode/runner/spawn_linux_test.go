package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// The fake CLI is this test binary re-executed through a wrapper script. The
// wrapper bakes in the scenario (the child's environment is scrubbed, so nothing
// can be passed through it). The helper records what it saw into the session root
// (its working directory) and emits recorded NDJSON.
const spikeSession, spikeRoot = "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "/opt/developer/hb-work/a/p6-spike/proj-x"

// TestMain turns this test binary into the fake CLI when the wrapper script
// re-executes it with --fakecli; otherwise it runs the tests. No test is skipped.
func TestMain(m *testing.M) {
	for n, a := range os.Args {
		if a == "--fakecli" {
			fakeCLI(n)
		}
	}
	os.Exit(m.Run())
}

func fakeCLI(i int) {
	scenario, fixture, uuid, root := os.Args[i+1], os.Args[i+2], os.Args[i+3], os.Args[i+4]
	prompt, _ := os.ReadFile("/dev/stdin")
	cwd, _ := os.Getwd()
	pgid, _ := syscall.Getpgid(0)
	rec := fmt.Sprintf("args=%s\ncwd=%s\npid=%d\npgid=%d\nppid_pgid=%d\nprompt=%s\nenv=%s\n", strings.Join(os.Args[1:i], " "), cwd, os.Getpid(), pgid, 0, prompt, strings.Join(os.Environ(), "|"))
	_ = os.WriteFile(filepath.Join(root, "fakecli.rec"), []byte(rec), 0o600)
	emit := func() {
		if fixture == "-" {
			return
		}
		b, err := os.ReadFile(fixture)
		if err != nil {
			os.Exit(97)
		}
		s := strings.ReplaceAll(strings.ReplaceAll(string(b), spikeRoot, root), spikeSession, uuid)
		fmt.Print(s)
	}
	initLine := func() string {
		return fmt.Sprintf(`{"type":"system","subtype":"init","cwd":%q,"session_id":%q,"model":"m","permissionMode":"default","apiKeySource":"none","claude_code_version":"2.1.322","mcp_servers":[],"plugins":[],"tools":["Read","Glob","Grep"]}`+"\n", root, uuid)
	}
	switch scenario {
	case "init-abort": // a CLI that acknowledges SIGINT with an aborted_tools result and exits 0 (OBSERVED, spike step 6)
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT)
		fmt.Print(initLine())
		_ = os.WriteFile(filepath.Join(root, "ready"), nil, 0o600)
		<-ch
		fmt.Printf(`{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":1,"session_id":%q,"terminal_reason":"aborted_tools","permission_denials":[]}`+"\n", uuid)
		os.Exit(0)
	case "init-ignoreint": // ignores SIGINT; SIGTERM ends it (exit 143 by signal, no result)
		signal.Ignore(syscall.SIGINT)
		fmt.Print(initLine())
		_ = os.WriteFile(filepath.Join(root, "ready"), nil, 0o600)
		time.Sleep(time.Hour)
	case "init-ignoreboth": // ignores SIGINT and SIGTERM: only the operator can end it
		signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
		fmt.Print(initLine())
		_ = os.WriteFile(filepath.Join(root, "ready"), nil, 0o600)
		time.Sleep(time.Hour)
	case "exit0":
		emit()
		os.Exit(0)
	case "exit1":
		emit()
		os.Exit(1)
	case "sigterm":
		emit()
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
		time.Sleep(time.Hour)
	case "hang":
		emit()
		time.Sleep(time.Hour)
	case "ignoreint":
		signal.Ignore(syscall.SIGINT)
		emit()
		time.Sleep(time.Hour)
	}
	os.Exit(96)
}

type integ struct {
	*harness
	bin string
}

func newInteg(t *testing.T, scenario, fixture string, mod func(*Config)) *integ {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wrapper string
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) {
		c.Spawner = ExecSpawner{}
		if mod != nil {
			mod(c)
		}
	})
	_ = h
	wrapper = filepath.Join(t.TempDir(), "hb-claude")
	script := fmt.Sprintf("#!/bin/sh\nexec %s -- \"$@\" --fakecli %s %s %s %s\n", bin, scenario, fixture, uuidN(1), h.root(1))
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A second executor over the same harness state, pointing at the real wrapper.
	h.ex.Close()
	h.cfg.Wrapper = wrapper
	h.cfg.FS = &rootFS{wrapper: wrapper}
	h.cfg.Spawner = ExecSpawner{}
	if mod != nil {
		mod(&h.cfg)
	}
	ex, err := New(h.cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.ex = ex
	t.Cleanup(func() { ex.Close() })
	return &integ{harness: h, bin: bin}
}

func fixturePath(name string) string {
	p, _ := filepath.Abs(filepath.Join("..", "testdata", "spike", name))
	return p
}

func (i *integ) run(key string) (exec1 string) {
	i.t.Helper()
	x := i.mustSend(1, "the real prompt over a real pipe", key)
	return x.ID
}

func (i *integ) record() map[string]string {
	i.t.Helper()
	b, err := os.ReadFile(filepath.Join(i.root(1), "fakecli.rec"))
	if err != nil {
		i.t.Fatalf("the fake CLI never ran: %v", err)
	}
	out := map[string]string{}
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}
	return out
}

// Recorded NDJSON through a real process: the OBSERVED verdicts of the spike hold.
func TestFakeCLIVerdicts(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-poison-0123456789")
	t.Setenv("BRIDGE_TOKEN", "bridge-secret-value")
	for _, tc := range []struct {
		name, scenario, fixture, state, row string
		code                                string
		released                            bool
	}{
		{"resume ok, exit 0", "exit0", "step2_resume_ok.ndjson", "completed", "3", "", true},
		{"max turns, exit 1", "exit1", "step3_max_turns.ndjson", "failed", "4", "ERROR_MAX_TURNS", true},
		{"SIGTERM, no result", "sigterm", "step6_sigterm.ndjson", "unknown", "6", "", false},
		{"denied tool is a completed run", "exit0", "step7_denial.ndjson", "completed", "3", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := newInteg(t, tc.scenario, fixturePath(tc.fixture), nil)
			id := i.run("key-fake-" + tc.row + tc.scenario)
			x := i.wait(id)
			v := i.verdict(id)
			if x.State != tc.state || v.Row != tc.row || v.Code != tc.code || v.GuardReleased != tc.released {
				t.Fatalf("state %s verdict %+v", x.State, v)
			}
			rec := i.record()
			if rec["prompt"] != "the real prompt over a real pipe" {
				t.Errorf("the child got prompt %q on stdin", rec["prompt"])
			}
			if strings.Contains(rec["args"], "real prompt") {
				t.Errorf("the prompt reached argv: %s", rec["args"])
			}
			if !strings.Contains(rec["args"], "--permission-mode default") || !strings.Contains(rec["args"], "--resume "+uuidN(1)) {
				t.Errorf("argv = %s", rec["args"])
			}
			if rec["cwd"] != i.root(1) {
				t.Errorf("cwd = %q, want the session root", rec["cwd"])
			}
			for _, kv := range strings.Split(rec["env"], "|") {
				if strings.Contains(kv, "ANTHROPIC") || strings.Contains(kv, "BRIDGE") || strings.Contains(kv, "poison") {
					t.Errorf("the Bridge environment reached the child: %q", kv)
				}
			}
			if !strings.Contains(rec["env"], "HOME=/home/claude") {
				t.Errorf("env = %s", rec["env"])
			}
		})
	}
}

// The child runs in its own process group (so signals reach the whole wrapper tree).
func TestChildHasItsOwnProcessGroup(t *testing.T) {
	i := newInteg(t, "exit0", fixturePath("step2_resume_ok.ndjson"), nil)
	i.wait(i.run("key-pgid-000001"))
	rec := i.record()
	if rec["pid"] == "" || rec["pgid"] != rec["pid"] {
		t.Fatalf("the child is not its own group leader: pid=%s pgid=%s", rec["pid"], rec["pgid"])
	}
	if mine, _ := syscall.Getpgid(0); fmt.Sprint(mine) == rec["pgid"] {
		t.Fatal("the child shares the Bridge's process group")
	}
}

// A wrapper that cannot be executed is a pre-submission failure and frees the guard.
func TestRealForkExecFailure(t *testing.T) {
	i := newInteg(t, "exit0", "-", nil)
	if err := os.Remove(i.cfg.Wrapper); err != nil {
		t.Fatal(err)
	}
	// ValidateWrapper is answered by the fake FS (the file "exists"), so exec itself fails.
	x := i.mustSend(1, "p", "key-forkfail-001")
	x = i.wait(x.ID)
	if x.State != "failed" || x.LocalFailure == nil || x.LocalFailure.Code != "PRE_SUBMISSION_UNAVAILABLE" {
		t.Fatalf("view = %+v", x)
	}
	if held, _ := i.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("guard must be released")
	}
}

// The cap interrupts a real child with SIGINT; a child that ignores it is NOT killed.
func TestRealCapAndReap(t *testing.T) {
	t.Run("SIGINT ends a hanging child", func(t *testing.T) {
		i := newInteg(t, "hang", fixturePath("step2_resume_ok.ndjson"), func(c *Config) { c.RunMax = 300 * time.Millisecond })
		id := i.run("key-realcap-0001")
		x := i.wait(id)
		if v := i.verdict(id); x.State != "unknown" || v.Row != "9" || v.ExitClass != "none" && v.ExitClass != "signal" {
			t.Fatalf("state %s verdict %+v", x.State, v)
		}
	})
	t.Run("a child that ignores SIGINT is not killed", func(t *testing.T) {
		i := newInteg(t, "ignoreint", "-", func(c *Config) { c.RunMax = 300 * time.Millisecond })
		id := i.run("key-realreap-001")
		i.waitState(id, "unknown") // the cap latched row 9 and sent SIGINT, which the child ignores
		time.Sleep(600 * time.Millisecond)
		rec := i.record()
		pid := atoi(rec["pid"])
		// The test, not the runner, removes the stuck process (an operator action, ADR-021 §4).
		t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("the runner killed (or lost) a child that ignores SIGINT: %v", err)
		}
		if held, _ := i.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
			t.Fatal("the guard stays held while the child lives")
		}
		if v := i.verdict(id); v.Row != "9" || v.GuardReleased {
			t.Fatalf("verdict %+v", v)
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := i.ex.Wait(ctx, id); err != nil {
			t.Fatalf("the run did not end after the operator removed the child: %v", err)
		}
	})
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

var _ = exec.Command
