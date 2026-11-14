package runner

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ExecSpawner starts the wrapper with exec.Command (no shell, no PATH lookup:
// the path is absolute and validated), in its own process group, with the
// scrubbed environment and the session root as the working directory.
//
// This is the only file in the module's Claude Code adapter that imports os/exec
// (TestExecIsConfinedToTheSpawnFile).
type ExecSpawner struct {
	// WaitDelay bounds how long Wait lingers for the child's stderr after it exited (a
	// descendant that keeps the pipe open would otherwise make Wait unbounded). Its
	// expiry is an unknown wait, never a confirmed reap. Zero means 5 s.
	WaitDelay time.Duration

	stderr io.Writer // tests only: replaces the stderr ring
}

type execChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr *ringBuffer

	mu   sync.Mutex
	done bool
}

// Start implements Spawner.
func (s2 ExecSpawner) Start(s Spec) (Child, error) {
	if s.Path == "" || s.Path[0] != '/' {
		return nil, errors.New("runner: the wrapper path must be absolute")
	}
	cmd := exec.Command(s.Path, s.Argv...)
	cmd.Dir = s.Dir
	cmd.Env = append([]string{}, s.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	ring := newRing(64 << 10)
	var sink io.Writer = ring
	if s2.stderr != nil {
		sink = s2.stderr
	}
	cmd.Stderr = sink
	cmd.WaitDelay = s2.WaitDelay
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = 5 * time.Second
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execChild{cmd: cmd, stdin: stdin, stdout: stdout, stderr: ring}, nil
}

func (c *execChild) Stdout() io.Reader { return c.stdout }

func (c *execChild) WriteStdin(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *execChild) CloseStdin() error { return c.stdin.Close() }

// Signal sends sig (SigInt or SigTerm, nothing else) to the child's process group. After a
// confirmed reap it refuses (the group id could have been reused).
func (c *execChild) Signal(sig Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done || c.cmd.Process == nil {
		return errors.New("runner: the child was already reaped")
	}
	switch sig {
	case SigInt:
		return syscall.Kill(-c.cmd.Process.Pid, syscall.SIGINT)
	case SigTerm:
		return syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
	}
	return errors.New("runner: only SIGINT and SIGTERM are ever sent (ADR-021 §4 forbids killing the wrapper)")
}

// GroupEmpty reports whether the child's process group is empty: kill(-pgid, 0) sends no
// signal and answers ESRCH only when no process of the group exists. EPERM means members
// exist that this user cannot signal (for example behind sudo): not empty. A recycled group
// id can only make the answer "not empty", never "empty".
func (c *execChild) GroupEmpty() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd.Process == nil {
		return false, errors.New("runner: no process")
	}
	err := syscall.Kill(-c.cmd.Process.Pid, 0)
	switch err {
	case nil:
		return false, nil
	case syscall.ESRCH:
		return true, nil
	}
	return false, nil
}

// markReaped records a confirmed reap: only then is a signal to the group refused. An
// unknown Wait result leaves the child signalable, so a later escalation step can still
// reach it (the runner never sends anything stronger than SIGTERM).
func (c *execChild) markReaped() {
	c.mu.Lock()
	c.done = true
	c.mu.Unlock()
}

func (c *execChild) Wait() Exit {
	err := c.cmd.Wait()
	if err == nil {
		c.markReaped()
		return Exit{Code: 0}
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The child exited but a descendant still holds a pipe: nothing is confirmed
		// about the group, so the guard of the run stays held.
		return Exit{Unknown: true}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if x, ok := statusOf(ee.Sys()); ok {
			c.markReaped()
			return x
		}
	}
	// Another error (for example a stderr copy error): cmd.Wait has still reaped the
	// process when ProcessState is set, which is a known status.
	if ps := c.cmd.ProcessState; ps != nil {
		if x, ok := statusOf(ps.Sys()); ok {
			c.markReaped()
			return x
		}
	}
	return Exit{Unknown: true}
}

func statusOf(sys any) (Exit, bool) {
	ws, ok := sys.(syscall.WaitStatus)
	switch {
	case !ok:
		return Exit{}, false
	case ws.Signaled():
		return Exit{Code: 128 + int(ws.Signal()), Signal: ws.Signal().String()}, true
	case ws.Exited():
		return Exit{Code: ws.ExitStatus()}, true
	}
	return Exit{}, false
}
