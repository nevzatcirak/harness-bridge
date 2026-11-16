package acceptance

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
)

// Evidence is the only writer of the driver's output. It accepts verdict lines made of a closed
// character set and refuses anything token-shaped, any marker and any registered secret, so a bug
// elsewhere cannot put file content, a credential or transcript text into the evidence.
type Evidence struct {
	mu        sync.Mutex
	w         io.Writer
	forbidden []string
	rejected  int
}

var (
	lineChars  = regexp.MustCompile(`^[A-Za-z0-9 _.:=/,"@+()\[\]-]{1,400}$`)
	longToken  = regexp.MustCompile(`[A-Za-z0-9_+/-]{40,}`)
	keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	valPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/@+,()-]{0,96}$`)
)

// ErrEvidence is returned for a line that was refused.
var ErrEvidence = errors.New("acceptance: evidence line refused")

// NewEvidence writes to w. Anything in forbid (markers, secrets) is refused wherever it appears.
func NewEvidence(w io.Writer, forbid ...string) *Evidence {
	e := &Evidence{w: w}
	e.Forbid(forbid...)
	return e
}

// Forbid registers more strings that must never appear.
func (e *Evidence) Forbid(s ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range s {
		if f != "" {
			e.forbidden = append(e.forbidden, f)
		}
	}
}

// Rejected is the number of refused lines.
func (e *Evidence) Rejected() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rejected
}

func (e *Evidence) refuse(why string) error {
	e.rejected++
	fmt.Fprintf(e.w, "evidence_refused reason=%s\n", why)
	return ErrEvidence
}

// Raw writes one preformatted verdict line (for example CanaryReport.Lines()).
func (e *Evidence) Raw(line string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !lineChars.MatchString(line) {
		return e.refuse("charset")
	}
	if longToken.MatchString(line) || strings.Contains(line, "sk-") || strings.Contains(line, "hbcanary-") {
		return e.refuse("token_shaped")
	}
	for _, f := range e.forbidden {
		if strings.Contains(line, f) {
			return e.refuse("forbidden_value")
		}
	}
	_, err := fmt.Fprintln(e.w, line)
	return err
}

// KV writes `kind key=value ...` from pairs; keys and values must match their closed patterns.
func (e *Evidence) KV(kind string, pairs ...string) error {
	if len(pairs)%2 != 0 || !keyPattern.MatchString(kind) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.refuse("shape")
	}
	var b strings.Builder
	b.WriteString(kind)
	for i := 0; i < len(pairs); i += 2 {
		if !keyPattern.MatchString(pairs[i]) || !valPattern.MatchString(pairs[i+1]) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.refuse("pair")
		}
		b.WriteString(" " + pairs[i] + "=" + pairs[i+1])
	}
	return e.Raw(b.String())
}
