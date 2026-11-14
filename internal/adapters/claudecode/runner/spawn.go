package runner

import (
	"io"
	"sync"
)

// Spec is one child to start. Argv is everything after Path; the prompt is never
// in it. Env is the complete scrubbed environment; Dir is the session root.
type Spec struct {
	Path string
	Argv []string
	Env  []string
	Dir  string
}

// Exit is what wait reported.
type Exit struct {
	Code    int
	Signal  string // non-empty when the child died of a signal
	Unknown bool   // the status could not be obtained
}

// Signal is a signal the runner sends to the child's process group: SigInt (the §3
// interrupt) and SigTerm (the second step of the §4 escalation). There is deliberately no
// SIGKILL: ADR-021 §4 forbids killing the wrapper (a kill of the `sudo` parent orphans the
// command), and removing a stuck process is an operator action. No other value exists and
// a Child refuses any other value.
type Signal int

const (
	SigInt Signal = iota + 1
	SigTerm
)

// Child is a started process.
type Child interface {
	Stdout() io.Reader
	// WriteStdin writes p once. n is the number of bytes the pipe accepted.
	WriteStdin(p []byte) (n int, err error)
	CloseStdin() error
	Signal(Signal) error
	// Wait reaps the child. Call it after stdout was read to the end.
	Wait() Exit
	// GroupEmpty reports whether the child's process group has no member left. It is
	// the one extra fact the ADR-020 release extension needs (ADR-021 §3, last bullet)
	// and sends no signal. Only a definite "no such group" is true: every other answer
	// (members left, a permission error, any failure) is false with a non-nil error or
	// false, so release fails closed.
	GroupEmpty() (bool, error)
}

// Spawner starts children. ExecSpawner is the production one (spawn_linux.go);
// tests inject fakes.
type Spawner interface {
	Start(Spec) (Child, error)
}

// ringBuffer keeps the last cap bytes written to it: the child's stderr, bounded
// (64 KiB) and never forwarded to a client.
type ringBuffer struct {
	mu  sync.Mutex
	cap int
	buf []byte
}

func newRing(capacity int) *ringBuffer { return &ringBuffer{cap: capacity} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.cap:]...)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
