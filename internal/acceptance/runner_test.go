package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ExecRunner against FAKE wrappers (shell scripts): no Claude CLI, no network, no sudo. The scripts
// stand in for hb-claude-wrapper only to exercise process handling: timings, signal sequencing,
// SIGPIPE, exit classes, the stderr flag and the hard validation of the wrapper and argv.

func fakeWrapper(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("shell wrapper tests need linux")
	}
	p := filepath.Join(t.TempDir(), "wrapper")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

var okArgs = []string{"-p", "--max-turns", "1"}

const initJSON = `{"type":"system","subtype":"init","session_id":"s","cwd":"/","permissionMode":"default","apiKeySource":"none","claude_code_version":"2.1.325","mcp_servers":[],"plugins":[],"tools":["Read"]}`
const toolJSON = `{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","id":"t","name":"Glob","input":{"pattern":"/x/*"}}]}}`
const resultJSON = `{"type":"result","subtype":"success","is_error":false,"session_id":"s","num_turns":1,"terminal_reason":"completed"}`

// Result-to-exit lag is measured from the result line to the exit.
func TestExecRunnerMeasuresResultToExitLag(t *testing.T) {
	w := fakeWrapper(t, "cat >/dev/null\necho '"+initJSON+"'\necho '"+resultJSON+"'\nsleep 0.3\nexit 0\n")
	res, err := ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, Stdin: "hi"})
	if err != nil || res.ExitCode != 0 || res.Timeout || res.InitAt < 0 || res.ResultAt < 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if lag := res.ExitAt - res.ResultAt; lag < 250*time.Millisecond || lag > 2*time.Second {
		t.Fatalf("lag = %v", lag)
	}
	info := ParseStream(res.Stdout)
	if !info.InitSeen || !info.ResultSeen || info.Version != "2.1.325" {
		t.Fatalf("%+v", info)
	}
}

// The stdin prompt reaches the wrapper; stderr is reduced to the one recorded flag.
func TestExecRunnerStdinAndStderrFlag(t *testing.T) {
	w := fakeWrapper(t, "read line\necho \"[mcp-sdk] SEP-2352: stored credential\" >&2\necho '"+initJSON+"'\nif [ \"$line\" = ping ]; then echo '"+resultJSON+"'; fi\n")
	res, _ := ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, Stdin: "ping\n"})
	if !res.StderrSEP2352 || !ParseStream(res.Stdout).ResultSeen {
		t.Fatalf("%+v", res)
	}
	if strings.Contains(string(res.Stdout), "SEP-2352") {
		t.Fatal("stderr leaked into stdout")
	}
}

// Signal sequencing: SIGINT after the first tool_use, then the escalation to SIGTERM only if the
// process is still alive. A process that handles SIGINT never sees SIGTERM sent as "sent".
func TestExecRunnerSignalSequence(t *testing.T) {
	// ignores INT, dies on TERM
	w := fakeWrapper(t, "trap '' INT\ncat >/dev/null &\necho '"+initJSON+"'\necho '"+toolJSON+"'\ni=0; while [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done\n")
	steps := []Step{{OnEvent: "tool_use", Signal: syscall.SIGINT}, {OnEvent: "tool_use", Delay: 300 * time.Millisecond, Signal: syscall.SIGTERM}}
	res, _ := ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, Stdin: "", Signals: steps, Timeout: 10 * time.Second})
	if strings.Join(res.Sent, ",") != "interrupt,terminated" || res.ExitSig != "terminated" || res.Timeout {
		t.Fatalf("%+v", res)
	}
	// dies on INT: the escalation finds nothing to signal and is not reported as sent
	w = fakeWrapper(t, "echo '"+initJSON+"'\necho '"+toolJSON+"'\ni=0; while [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done\n")
	res, _ = ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, Signals: steps, Timeout: 10 * time.Second})
	if strings.Join(res.Sent, ",") != "interrupt" || res.ExitSig != "interrupt" {
		t.Fatalf("%+v", res)
	}
	// SIGTERM only
	res, _ = ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, Signals: steps[:1:1], Timeout: 10 * time.Second})
	_ = res
	res, _ = ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, Signals: []Step{{OnEvent: "tool_use", Signal: syscall.SIGTERM}}, Timeout: 10 * time.Second})
	if strings.Join(res.Sent, ",") != "terminated" || res.ExitSig != "terminated" {
		t.Fatalf("%+v", res)
	}
}

// SIGPIPE: when the reader closes its end after init, a child that keeps writing ends by SIGPIPE.
func TestExecRunnerReaderCloseEndsAWritingChild(t *testing.T) {
	w := fakeWrapper(t, "echo '"+initJSON+"'\ni=0; while [ $i -lt 250 ]; do echo '"+toolJSON+"'; sleep 0.02; i=$((i+1)); done\n")
	res, _ := ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, CloseReaderOn: "init", Timeout: 10 * time.Second})
	if res.Timeout || res.ExitAt < 0 || (res.ExitSig != "broken pipe" && res.ExitCode == 0) {
		t.Fatalf("%+v", res)
	}
}

// A child that ignores SIGTERM at the timeout is abandoned, never killed.
func TestExecRunnerTimeoutNeverKills(t *testing.T) {
	w := fakeWrapper(t, "trap '' TERM INT\necho '"+initJSON+"'\ni=0; while [ $i -lt 160 ]; do sleep 0.05; i=$((i+1)); done\n")
	start := time.Now()
	res, _ := ExecRunner{Wrapper: w}.Run(context.Background(), Invocation{Args: okArgs, Timeout: 200 * time.Millisecond})
	if !res.Timeout || res.Err != "abandoned" || res.ExitAt != -1 {
		t.Fatalf("%+v", res)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("slow")
	}
	// the script bounds its own loop (8 s), so the abandoned shell ends by itself
}

func TestExecRunnerRefusesABadWrapperOrArgv(t *testing.T) {
	for _, w := range []string{"", "relative/wrapper", "/home/x/.local/bin/claude", "/usr/local/libexec/../bin/w"} {
		if _, err := (ExecRunner{Wrapper: w}).Run(context.Background(), Invocation{Args: okArgs}); err == nil {
			t.Errorf("wrapper %q accepted", w)
		}
	}
	w := fakeWrapper(t, "exit 0\n")
	for _, a := range [][]string{{"-p", "--bare", "--max-turns", "1"}, {"-p"}, {"-p", "--max-turns", "9"}} {
		if _, err := (ExecRunner{Wrapper: w}).Run(context.Background(), Invocation{Args: a}); err == nil {
			t.Errorf("argv %v accepted", a)
		}
	}
	if res, _ := (ExecRunner{Wrapper: "/nonexistent/wrapper"}).Run(context.Background(), Invocation{Args: okArgs}); res.Err != "start_failed" {
		t.Errorf("missing wrapper: %+v", res)
	}
}

// ProcFS reports a process by a unique token and never claims "gone" when it cannot tell.
func TestProcFSAlive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	if !(ProcFS{Root: "/nonexistent-proc"}).Alive("x") {
		t.Error("an unreadable /proc was reported as 'gone'")
	}
	if !(ProcFS{}).Alive("") {
		t.Error("an empty token was reported as 'gone'")
	}
	if (ProcFS{}).Alive("no-such-token-" + strings.Repeat("z", 24)) {
		t.Error("an unknown token was reported alive")
	}
}
