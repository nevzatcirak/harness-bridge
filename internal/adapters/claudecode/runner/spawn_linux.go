package runner

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
)

// ExecSpawner starts the wrapper with exec.Command (no shell, no PATH lookup:
// the path is absolute and validated), in its own process group, with the
// scrubbed environment and the session root as the working directory.
//
// This is the only file in the module's Claude Code adapter that imports os/exec
// (TestExecIsConfinedToTheSpawnFile).
type ExecSpawner struct{}

type execChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr *ringBuffer

	mu   sync.Mutex
	done bool
}

// Start implements Spawner.
func (ExecSpawner) Start(s Spec) (Child, error) {
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
	cmd.Stderr = ring
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execChild{cmd: cmd, stdin: stdin, stdout: stdout, stderr: ring}, nil
}

func (c *execChild) Stdout() io.Reader { return c.stdout }

func (c *execChild) WriteStdin(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *execChild) CloseStdin() error { return c.stdin.Close() }

// Signal sends sig to the child's process group. After Wait returned it does
// nothing (the group id could have been reused).
func (c *execChild) Signal(sig Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done || c.cmd.Process == nil {
		return errors.New("runner: the child was already reaped")
	}
	s := syscall.SIGINT
	if sig == SigKill {
		s = syscall.SIGKILL
	}
	return syscall.Kill(-c.cmd.Process.Pid, s)
}

// markReaped records a confirmed reap: only then is a signal to the group refused. An
// unknown Wait result leaves the child signalable, so the runner's SIGKILL can still
// reach it.
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
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				c.markReaped()
				return Exit{Code: 128 + int(ws.Signal()), Signal: ws.Signal().String()}
			}
			if ws.Exited() {
				c.markReaped()
				return Exit{Code: ws.ExitStatus()}
			}
		}
	}
	return Exit{Unknown: true}
}
