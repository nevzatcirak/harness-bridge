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

// Signal is the only signal the runner sends, to the child's process group: SigInt (the
// §3 interrupt). There is deliberately no SIGKILL: ADR-021 §4 forbids killing the wrapper
// (a kill of the `sudo` parent orphans the command), and removing a stuck process is an
// operator action. The SIGINT to SIGTERM escalation is slice 3.
type Signal int

const (
	SigInt Signal = iota + 1
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
