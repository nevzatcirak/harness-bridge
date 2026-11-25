package acceptance

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
)

// Step is one signal of a signal plan: after an event of the given kind (a type such as "init",
// "assistant", "result", or "tool_use" for an assistant event that carries one) is read from the
// child's stdout, wait Delay and send Signal to the wrapper process. A step whose target already
// exited sends nothing (and is not reported as sent).
type Step struct {
	OnEvent string
	Delay   time.Duration
	Signal  syscall.Signal
}

// Invocation is one CLI run through the wrapper.
type Invocation struct {
	Label   string
	Args    []string
	Stdin   string
	Dir     string
	Env     []string
	Timeout time.Duration
	Signals []Step
	// CloseReaderOn closes the parent's read end of the child's stdout right after an event of
	// this kind (the SIGPIPE test: the reader dies, the child must end). Empty: never.
	CloseReaderOn string
	// ProbeRunHome is set only on a run that is given the run home as a readable scope: an item 1 probe or
	// control run (R6 run 1 fix, #416) and the runs of items 4 and 6 (R6 run 4, #426). It is the run home the
	// trailing `--add-dir <run home>` of its argv must name. ExecRunner then validates the argv with
	// ValidateProbeArgv; every other run (ProbeRunHome empty) goes through the generic ValidateArgv, which
	// refuses --add-dir, so the extra readable scope exists only where the driver asks for it.
	ProbeRunHome string
}

// Result is the recorded run. Stdout stays in memory for the verdict logic and is never written out.
type Result struct {
	Stdout   []byte
	ExitCode int    // -1 when the process was killed by a signal or never exited
	ExitSig  string // the terminating signal's name, if any
	Timeout  bool
	// Offsets from the start of the run; -1 when the event did not happen.
	InitAt, ResultAt, ExitAt time.Duration
	Sent                     []string // names of the signals actually sent, in order
	StderrSEP2352            bool     // the one stderr line the maintainer asked to be recorded
	Err                      string   // a start or wait error class, never output
}

// Runner starts a run. The production Runner is ExecRunner; tests use fakes.
type Runner interface {
	Run(ctx context.Context, inv Invocation) (Result, error)
}

// ExecRunner runs the wrapper as a child process with a minimal environment.
type ExecRunner struct{ Wrapper string }

const maxStdout = 8 << 20

func eventKind(e claudecode.Event) []string {
	if e.Undecodable {
		return nil
	}
	kinds := []string{e.Type}
	for _, b := range e.Blocks {
		if b.Type == "tool_use" {
			kinds = append(kinds, "tool_use")
			break
		}
	}
	return kinds
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, inv Invocation) (Result, error) {
	res := Result{ExitCode: -1, InitAt: -1, ResultAt: -1, ExitAt: -1}
	if err := ValidateWrapper(r.Wrapper); err != nil {
		return res, err
	}
	if inv.ProbeRunHome != "" {
		if err := ValidateProbeArgv(inv.Args, inv.ProbeRunHome); err != nil {
			return res, err
		}
	} else if err := ValidateArgv(inv.Args); err != nil {
		return res, err
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		return res, err
	}
	cmd := exec.Command(r.Wrapper, inv.Args...)
	cmd.Dir = inv.Dir
	cmd.Env = append([]string{"PATH=/usr/sbin:/usr/bin:/bin"}, inv.Env...)
	cmd.Stdin = strings.NewReader(inv.Stdin)
	cmd.Stdout = wr
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, max: 64 << 10}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		rd.Close()
		wr.Close()
		res.Err = "start_failed"
		return res, nil
	}
	wr.Close() // the child holds the only write end
	var mu sync.Mutex
	var sent []string
	var timers []*time.Timer
	addSent := func(n string) { mu.Lock(); sent = append(sent, n); mu.Unlock() }
	fired := make([]bool, len(inv.Signals))
	var out bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		br := bufio.NewReaderSize(rd, 64<<10)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				if out.Len()+len(line) <= maxStdout {
					out.Write(line)
				}
				e := claudecode.DecodeLine(bytes.TrimSpace(line))
				kinds := eventKind(e)
				now := time.Since(start)
				mu.Lock()
				for _, k := range kinds {
					if k == "system" && e.Subtype == "init" && res.InitAt < 0 {
						res.InitAt = now
					}
					if k == "result" && res.ResultAt < 0 {
						res.ResultAt = now
					}
				}
				mu.Unlock()
				for i, st := range inv.Signals {
					if fired[i] {
						continue
					}
					for _, k := range kinds {
						if k == st.OnEvent || (st.OnEvent == "init" && k == "system" && e.Subtype == "init") {
							fired[i] = true
							st := st
							tm := time.AfterFunc(st.Delay, func() {
								if cmd.Process != nil && cmd.Process.Signal(st.Signal) == nil {
									addSent(st.Signal.String())
								}
							})
							mu.Lock()
							timers = append(timers, tm)
							mu.Unlock()
							break
						}
					}
				}
				for _, k := range kinds {
					if inv.CloseReaderOn != "" && (k == inv.CloseReaderOn || (inv.CloseReaderOn == "init" && k == "system" && e.Subtype == "init")) {
						rd.Close()
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	timeout := inv.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var werr error
	waited := true // false when Wait has not returned: cmd.ProcessState is then not ours to read
	select {
	case werr = <-waitErr:
	case <-timer.C:
		res.Timeout = true
		_ = cmd.Process.Signal(syscall.SIGTERM) // never SIGKILL: a run that ignores it is abandoned
		select {
		case werr = <-waitErr:
		case <-time.After(5 * time.Second):
			res.Err, waited = "abandoned", false
		}
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select { // the same bounded fallback as the timeout path: never wait forever, never SIGKILL
		case werr = <-waitErr:
			res.Err = "canceled"
		case <-time.After(5 * time.Second):
			res.Err, waited = "abandoned", false
		}
	}
	if res.Err != "abandoned" {
		res.ExitAt = time.Since(start)
	}
	// Let the reader drain what the child wrote before it exited; close the read end only if it
	// does not finish (a grandchild still holding the pipe, or an abandoned child).
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
	}
	rd.Close()
	<-readDone
	mu.Lock()
	for _, tm := range timers {
		tm.Stop() // a step that has not fired by now never will
	}
	res.Stdout = append([]byte(nil), out.Bytes()...)
	res.Sent = append([]string(nil), sent...)
	mu.Unlock()
	if waited && cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.ExitSig = ws.Signal().String()
		}
	}
	var ee *exec.ExitError
	if werr != nil && !errors.As(werr, &ee) && res.Err == "" {
		res.Err = "wait_failed"
	}
	res.StderrSEP2352 = bytes.Contains(stderr.Bytes(), []byte("SEP-2352"))
	return res, nil
}

type limitedWriter struct {
	w   *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.w.Len() < l.max {
		room := l.max - l.w.Len()
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}

var _ io.Writer = (*limitedWriter)(nil)
