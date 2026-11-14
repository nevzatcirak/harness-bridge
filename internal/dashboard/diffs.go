package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The four labels of ADR-023 section 1.2 / 5.2. "Not computed" is never shown as
// "no changes": a missing summary.diffs is its own label.
const (
	LabelChanges       = "changes"
	LabelComputedEmpty = "computed, no changes"
	LabelNotComputed   = "not computed"
	LabelUnavailable   = "unavailable"
)

// Rendering caps (ADR-023 section 3; the values are stated in the "as built" note).
const (
	maxDiffFiles     = 200
	maxPatchPerFile  = 256 << 10
	maxPatchTotal    = 1 << 20
	maxPathRunes     = 512
	binarySniffBytes = 8 << 10
	redactOverlap    = 4 << 10
	maxTurnsShown    = 100
	diffCacheTTL     = 2 * time.Second // the detail poll interval (ADR-023 section 4)
	diffCacheMax     = 64
)

// TurnState is what a user message says about its per-turn diff.
type TurnState string

const (
	TurnNotComputed   TurnState = "not_computed"
	TurnComputedEmpty TurnState = "computed_empty"
	TurnChanges       TurnState = "changes"
)

// Turn is one user message of a session with the presence state of its native
// info.summary.diffs.
type Turn struct {
	MessageID string
	Created   time.Time
	State     TurnState
}

// DiffFile is one file of a diff as the Source received it. HasPatch is false when
// the native item had no patch at all (distinct from an empty patch).
type DiffFile struct {
	Path      string
	HasPath   bool
	Status    string
	Additions float64
	Deletions float64
	Patch     string
	HasPatch  bool
}

// DiffResult is a successful native diff read.
type DiffResult struct {
	Files []DiffFile
}

// ErrUnavailable is what a Source may return for a diff it could not read. Any
// other non-NotFound error is also shown as unavailable.
var ErrUnavailable = errors.New("dashboard: diff unavailable")

// unavailableReason maps an error to a closed reason; the error text is never shown.
func unavailableReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	return "error"
}

type fileView struct {
	Path, Status         string
	Additions, Deletions string
	Patch                string
	Note                 string // why Patch is empty, or that it was cut
	ShowPatch            bool
}

type diffView struct {
	Label      string
	LabelClass string
	Scope      string // "working tree (not per turn)" or "turn <id>"
	Reason     string // for unavailable / empty answers
	Files      []fileView
	More       string // "N more files not shown"
	Redaction  string // "redaction on: N values redacted"
}

func countText(v float64) string {
	if v != v || v < 0 || v > 1e15 {
		return "?"
	}
	return fmt.Sprintf("%d", int64(v))
}

func statusText(s string) string {
	switch s {
	case "added", "deleted", "modified":
		return s
	}
	return "unknown"
}

// looksBinary applies the ADR-023 section 3 rule: a NUL byte in the first 8 KiB,
// invalid UTF-8 above a threshold, or git's own binary notice.
func looksBinary(p string) bool {
	head := p
	if len(head) > binarySniffBytes {
		head = head[:binarySniffBytes]
	}
	if strings.IndexByte(head, 0) >= 0 {
		return true
	}
	if strings.Contains(head, "\nBinary files ") || strings.HasPrefix(head, "Binary files ") || strings.Contains(head, "\nGIT binary patch") {
		return true
	}
	if len(head) > 0 {
		bad := 0
		for i := 0; i < len(head); {
			r, w := utf8.DecodeRuneInString(head[i:])
			if r == utf8.RuneError && w == 1 {
				bad++
			}
			i += w
		}
		if bad*10 > len(head) {
			return true
		}
	}
	return false
}

// cutUTF8 cuts s to at most n bytes on a rune boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// hasHunk reports whether a patch carries any hunk. A patch with no hunk but
// non-zero counts is what OpenCode substitutes when its own size cap was hit.
func hasHunk(p string) bool { return strings.Contains(p, "\n@@ ") || strings.HasPrefix(p, "@@ ") }

// buildDiffView applies, in this order, the file-count cap, the per-file and
// total byte caps, binary detection and redaction, and marks every omission. Text
// that reaches the template is plain data: html/template escapes it.
func buildDiffView(label, scope string, res DiffResult, red *Redactor) diffView {
	v := diffView{Label: label, Scope: scope}
	files := res.Files
	if len(files) > maxDiffFiles {
		v.More = fmt.Sprintf("%d more files not shown (cap %d files).", len(files)-maxDiffFiles, maxDiffFiles)
		files = files[:maxDiffFiles]
	}
	redacted, total := 0, 0
	for _, f := range files {
		fv := fileView{Status: statusText(f.Status), Additions: countText(f.Additions), Deletions: countText(f.Deletions)}
		path := "(no file name)"
		if f.HasPath {
			path = truncRunes(f.Path, maxPathRunes)
		}
		p, n := red.Redact(path)
		fv.Path, redacted = p, redacted+n
		switch {
		case !f.HasPatch:
			fv.Note = "no patch in the answer"
		case looksBinary(f.Patch):
			fv.Note = fmt.Sprintf("binary file, not displayed (%d bytes)", len(f.Patch))
		case !hasHunk(f.Patch) && (f.Additions > 0 || f.Deletions > 0):
			fv.Note = "patch not provided (OpenCode replaces a patch over its own size cap by a header)"
		case total >= maxPatchTotal:
			fv.Note = fmt.Sprintf("not shown: total cap of %d bytes reached", maxPatchTotal)
		default:
			// Redact before cutting, with an overlap, so a token that straddles the cut
			// is replaced whole instead of leaving a prefix of it.
			window := cutUTF8(strings.ToValidUTF8(f.Patch, "�"), maxPatchPerFile+redactOverlap)
			text, n := red.Redact(window)
			redacted += n
			cut := false
			if len(text) > maxPatchPerFile {
				text, cut = cutUTF8(text, maxPatchPerFile), true
			} else if len(f.Patch) > len(window) {
				cut = true
			}
			if remaining := maxPatchTotal - total; len(text) > remaining {
				text, cut = cutUTF8(text, remaining), true
			}
			total += len(text)
			fv.Patch, fv.ShowPatch = text, true
			if cut {
				fv.Note = fmt.Sprintf("truncated (per-file cap %d bytes, total cap %d bytes)", maxPatchPerFile, maxPatchTotal)
			}
		}
		v.Files = append(v.Files, fv)
	}
	v.Redaction = fmt.Sprintf("redaction on: %d values redacted", redacted)
	return v
}

func unavailableView(scope string, err error) diffView {
	return diffView{Label: LabelUnavailable, Scope: scope, Reason: unavailableReason(err)}
}

// flightCache holds a read for a short TTL and shares one in-flight read per key
// (single-flight, ADR-023 section 4 and T7). It is generic so the diff reads and the
// polling view models use the same code: memory only, a bounded number of entries,
// never written to disk. A failed read is cached for the TTL like a good one only
// when the caller returns it as a value; callers that must not cache an error
// return it and the entry is dropped after the waiters have seen it (see get).
type flightCache[V any] struct {
	mu  sync.Mutex
	now func() time.Time
	ttl time.Duration
	max int
	m   map[string]*cacheEntry[V]
}

type cacheEntry[V any] struct {
	done chan struct{}
	res  V
	err  error
	at   time.Time
}

func newFlightCache[V any](now func() time.Time, ttl time.Duration, max int) *flightCache[V] {
	if now == nil {
		now = time.Now
	}
	return &flightCache[V]{now: now, ttl: ttl, max: max, m: map[string]*cacheEntry[V]{}}
}

// diffCache is the cache of diff reads: the detail poll interval, 64 entries.
type diffCache = flightCache[DiffResult]

func newDiffCache(now func() time.Time) *diffCache {
	return newFlightCache[DiffResult](now, diffCacheTTL, diffCacheMax)
}

func (c *flightCache[V]) get(ctx context.Context, key string, load func(context.Context) (V, error)) (V, error) {
	var zero V
	c.mu.Lock()
	if e, ok := c.m[key]; ok {
		select {
		case <-e.done:
			if c.now().Sub(e.at) < c.ttl {
				c.mu.Unlock()
				return e.res, e.err
			}
			delete(c.m, key)
		default:
			c.mu.Unlock()
			select {
			case <-e.done:
				return e.res, e.err
			case <-ctx.Done():
				return zero, ctx.Err()
			}
		}
	}
	if len(c.m) >= c.max {
		c.evict()
	}
	e := &cacheEntry[V]{done: make(chan struct{})}
	c.m[key] = e
	c.mu.Unlock()
	e.res, e.err = load(ctx)
	e.at = c.now()
	close(e.done)
	return e.res, e.err
}

// evict drops finished entries, oldest first, until the cache has room.
func (c *flightCache[V]) evict() {
	for len(c.m) >= c.max {
		oldestKey, oldest := "", time.Time{}
		for k, e := range c.m {
			select {
			case <-e.done:
				if oldestKey == "" || e.at.Before(oldest) {
					oldestKey, oldest = k, e.at
				}
			default:
			}
		}
		if oldestKey == "" {
			return // only in-flight entries: leave them, the map stays bounded by the request concurrency limit
		}
		delete(c.m, oldestKey)
	}
}
