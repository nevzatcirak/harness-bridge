package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/lifecycle"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-022 slice S4: the create_session tool (sections 3.3, 4.1, 4.2, 5.1), against a fake
// native in this slice.
//
// It is a mutation of the native server, not of an execution: it takes no session guard (a new
// session has none) and no manager reservation, runs only in a writer-role process
// (BRIDGE_READ_ONLY returns FORBIDDEN), keeps a memory-only registry for idempotency and the
// hourly counter, and sends exactly one POST per accepted request, never retried. It is NOT
// classified by mutating(): that classification writes the AUDIT-01 prompt/interrupt lines. Its
// own audit line (event=session_create, ADR-022 4.3) is slice S5 and MUST land before any
// production wiring; there is none here. The first prompt (4.1 step 6) is not accepted in this
// slice: it needs the O2 flag and slice S9 (D16 Q7).

const (
	// createCountWindow is the largest native list a cap count may rest on: a read that
	// returns more entries than this was truncated and its count is only a lower bound.
	createCountWindow = 1000
	// createRegistryMax bounds the memory-only idempotency registry. When it is full the
	// oldest finished record is forgotten: a retry of THAT key becomes a new request (the
	// documented duplicate risk of a memory-only registry; the hourly cap bounds it).
	createRegistryMax = 10000
	maxCreateTitle    = 200
	maxCreateSubdir   = 1024
)

// createBound is the bound of each native step (ADR-022 4.1) in nanoseconds. Atomic and
// settable only so a test can shorten it.
var createBound atomic.Int64

func init() { createBound.Store(int64(5 * time.Second)) }

func createNativeBound() time.Duration { return time.Duration(createBound.Load()) }

// CreateConfig enables create_session. A Server whose Create is nil does not register the tool
// (ADR-022 3.1: no lifecycle configuration, no lifecycle tools), so a deployed Bridge keeps its
// tool set; nothing wires it from BRIDGE_LIFECYCLE_CONFIG yet.
type CreateConfig struct {
	cfg  *lifecycle.Config
	auth *lifecycle.Authorizer
	reg  *createRegistry
	sem  chan struct{} // one create at a time: the caps hold under concurrency
	now  func() time.Time
}

// NewCreateConfig builds the create tool's state from a validated lifecycle configuration.
func NewCreateConfig(cfg *lifecycle.Config) (*CreateConfig, error) {
	a, err := lifecycle.NewAuthorizer(cfg)
	if err != nil {
		return nil, err
	}
	return &CreateConfig{cfg: cfg, auth: a, reg: newCreateRegistry(), sem: make(chan struct{}, 1), now: time.Now}, nil
}

// CreateInput is the create_session request. There is no field for a prompt, permission, agent,
// model, parentID or workspaceID: the generated input schema rejects any other property.
type CreateInput struct {
	Root           string `json:"root"`
	Subdirectory   string `json:"subdirectory,omitempty"`
	Title          string `json:"title,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

// CreateOutput is the create_session result.
type CreateOutput struct {
	SessionID string `json:"session_id"`
	Root      string `json:"root"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
	Created   bool   `json:"created"`
}

// createError is an *execution.Error that also carries the evidence the audit line of slice S5
// will need.
type createError struct {
	E *execution.Error
	// BootstrapMayBeOutsideRoot is set when the native answer put the session somewhere other
	// than the requested path: the POST may already have bootstrapped an instance (and loaded
	// plugins) at a path the Bridge did not check (ADR-022 4.1 step 5).
	BootstrapMayBeOutsideRoot bool
}

func (e *createError) Error() string { return e.E.Error() }
func (e *createError) Unwrap() error { return e.E }

func unknownOutcome(msg string, outside bool) error {
	return &createError{E: &execution.Error{Code: "UNKNOWN_OUTCOME", Message: msg}, BootstrapMayBeOutsideRoot: outside}
}

func sessionLimit(msg string) error {
	return &execution.Error{Code: "SESSION_LIMIT", Message: msg, Retryable: true}
}

// ---- registry -------------------------------------------------------------------------

type createRecord struct {
	fp   [32]byte
	done chan struct{}
	out  *CreateOutput
	err  error
}

type createRegistry struct {
	mu    sync.Mutex
	recs  map[string]*createRecord
	order []string    // finished records, oldest first
	posts []time.Time // POSTs sent in the last hour
}

func newCreateRegistry() *createRegistry { return &createRegistry{recs: map[string]*createRecord{}} }

// reserve returns the record of key and whether the caller owns it (and must finish or abandon
// it). A record of the same key and a different input is IDEMPOTENCY_CONFLICT.
func (r *createRegistry) reserve(key string, fp [32]byte) (*createRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.recs[key]; ok {
		if rec.fp != fp {
			return nil, false, execution.Fail("IDEMPOTENCY_CONFLICT", "the idempotency key was used with a different input")
		}
		return rec, false, nil
	}
	rec := &createRecord{fp: fp, done: make(chan struct{})}
	r.recs[key] = rec
	return rec, true, nil
}

// finish records the outcome of a POST that was sent (a created session, or an unknown
// outcome): the same key and input return it again and never POST again.
func (r *createRegistry) finish(key string, rec *createRecord, out *CreateOutput, err error) {
	r.mu.Lock()
	rec.out, rec.err = out, err
	close(rec.done)
	r.order = append(r.order, key)
	for len(r.recs) > createRegistryMax && len(r.order) > 0 {
		delete(r.recs, r.order[0])
		r.order = r.order[1:]
	}
	r.mu.Unlock()
}

// abandon drops a record whose request was refused before or without a certain write (a
// definite refusal): concurrent waiters get the same error, and a retry is a fresh request.
func (r *createRegistry) abandon(key string, rec *createRecord, err error) {
	r.mu.Lock()
	rec.err = err
	close(rec.done)
	if r.recs[key] == rec {
		delete(r.recs, key)
	}
	r.mu.Unlock()
}

// postsInWindow prunes and counts the POSTs of the last hour.
func (r *createRegistry) postsInWindow(now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	cut := now.Add(-time.Hour)
	i := 0
	for i < len(r.posts) && !r.posts[i].After(cut) {
		i++
	}
	r.posts = r.posts[i:]
	return len(r.posts)
}

func (r *createRegistry) notePost(now time.Time) {
	r.mu.Lock()
	r.posts = append(r.posts, now)
	r.mu.Unlock()
}

// ---- the tool -------------------------------------------------------------------------

func validCreateTitle(t string) bool {
	if t == "" {
		return true
	}
	if !utf8.ValidString(t) || utf8.RuneCountInString(t) > maxCreateTitle {
		return false
	}
	for _, r := range t {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// subdirDepth validates a relative subdirectory and returns its depth: clean, no "..", no empty
// or control-character component, not absolute.
func subdirDepth(sub string) (int, bool) {
	if sub == "" {
		return 0, true
	}
	if len(sub) > maxCreateSubdir || !utf8.ValidString(sub) || strings.HasPrefix(sub, "/") || strings.ContainsAny(sub, "\\\x00") {
		return 0, false
	}
	for _, r := range sub {
		if unicode.IsControl(r) {
			return 0, false
		}
	}
	if filepath.Clean(sub) != sub {
		return 0, false
	}
	parts := strings.Split(sub, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return 0, false
		}
	}
	return len(parts), true
}

func authRefusal(err error) error {
	var ae *lifecycle.AuthError
	if errors.As(err, &ae) {
		msg := "the directory is not authorized for create_session"
		if ae.Reason != "" {
			msg += " (" + ae.Reason + ")"
		}
		switch ae.Code {
		case lifecycle.CodeNotFound:
			return execution.Fail("NOT_FOUND", "the directory does not exist (create_session never creates directories)")
		case lifecycle.CodeInvalidInput:
			return execution.Fail("INVALID_INPUT", msg)
		}
		return execution.Fail("FORBIDDEN", msg)
	}
	if errors.Is(err, security.ErrCheckUnavailable) {
		return &execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "the directory check did not complete; storage unavailable", Retryable: true}
	}
	return execution.Fail("FORBIDDEN", "the directory is not authorized for create_session")
}

// createCall is create_session. Order, with the reason for each step:
//  1. input validation and BRIDGE_READ_ONLY (no state touched);
//  2. the idempotency lookup: a recorded outcome is returned before any cap, so a replay is
//     never refused by the cap its own creation just reached;
//  3. the path (config + live directory, no native call): root, subdirectory, depth, Git top
//     level: every path refusal is local, so "no native call" holds for them;
//  4. the hourly counter, then the native catalog count (the only native READ);
//  5. the identity re-check, then the one POST, then the verification of its answer.
func (s *Server) createCall(ctx context.Context, a args) (any, error) {
	c := s.Create
	if c == nil {
		return nil, execution.Fail("INVALID_INPUT", "unknown tool")
	}
	if s.ReadOnly {
		return nil, execution.Fail("FORBIDDEN", "mutations disabled on this bridge")
	}
	if !validCreateTitle(a.Title) {
		return nil, execution.Fail("INVALID_INPUT", "title must be 1 to 200 characters without control characters")
	}
	if n := len(a.IdempotencyKey); n < 8 || n > 128 {
		return nil, execution.Fail("INVALID_INPUT", "idempotency_key must be 8 to 128 bytes")
	}
	depth, ok := subdirDepth(a.Subdirectory)
	if !ok {
		return nil, execution.Fail("INVALID_INPUT", "subdirectory must be a relative, clean path without .. or control characters")
	}
	var root *lifecycle.Root
	for i := range c.cfg.Roots {
		if c.cfg.Roots[i].Path == a.Root {
			root = &c.cfg.Roots[i]
		}
	}
	if root == nil {
		return nil, execution.Fail("FORBIDDEN", "root is not a configured lifecycle root")
	}
	if depth > root.MaxDepth {
		return nil, execution.Fail("FORBIDDEN", fmt.Sprintf("subdirectory is deeper than max_depth %d of the root", root.MaxDepth))
	}

	sum := sha256.Sum256([]byte(a.IdempotencyKey))
	keyHash := hex.EncodeToString(sum[:])
	fp := sha256.Sum256([]byte(strings.Join([]string{a.Root, a.Subdirectory, a.Title}, "\x00")))
	rec, owner, err := c.reg.reserve(keyHash, fp)
	if err != nil {
		return nil, err
	}
	if !owner {
		select {
		case <-rec.done:
		case <-ctx.Done():
			return nil, &execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "an identical request is in flight; wait and retry", Retryable: true}
		}
		if rec.err != nil {
			return nil, rec.err
		}
		out := *rec.out
		return out, nil
	}
	out, err := c.run(ctx, s, root, a, keyHash, rec, fp)
	return out, err
}

func (c *CreateConfig) run(ctx context.Context, s *Server, root *lifecycle.Root, a args, keyHash string, rec *createRecord, fp [32]byte) (any, error) {
	refuse := func(err error) (any, error) {
		c.reg.abandon(keyHash, rec, err)
		return nil, err
	}
	// 3. the path.
	dir := root.Path
	if a.Subdirectory != "" {
		dir = root.Path + "/" + a.Subdirectory
	}
	grant, err := c.auth.Authorize(ctx, dir, lifecycle.ModeCreate)
	if err != nil {
		return refuse(authRefusal(err))
	}
	// 4. serialize creates so the caps hold under concurrency, then count.
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return refuse(&execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "another create_session is in progress; retry", Retryable: true})
	}
	if c.reg.postsInWindow(c.now()) >= c.cfg.CreatePerHour {
		return refuse(sessionLimit("create_per_hour reached; retry after the window"))
	}
	if err := c.checkCaps(ctx, s.Client, root); err != nil {
		return refuse(err)
	}
	// 5. identity re-check immediately before the native write.
	if err := grant.Recheck(ctx); err != nil {
		return refuse(authRefusal(err))
	}
	meta := map[string]string{"hb_origin": "bridge", "hb_key": keyHash[:16]}
	pctx, cancel := context.WithTimeout(ctx, createNativeBound())
	defer cancel()
	c.reg.notePost(c.now())
	sess, perr := s.Client.CreateSession(pctx, grant.Directory, a.Title, meta)
	if perr != nil {
		if opencode.IsDefiniteRefusal(perr) {
			return refuse(execution.Fail("UPSTREAM_FAILURE", "the native server refused the create"))
		}
		// A timeout, a lost response or an unreadable answer: the session may exist. Never
		// retried and never replayed: the recorded outcome is returned for the same key.
		e := unknownOutcome("the native create did not complete or its answer was lost; the session may exist: find it through the catalog (hb_key "+meta["hb_key"]+"); it is not retried", false)
		c.reg.finish(keyHash, rec, nil, e)
		return nil, e
	}
	// Verify the answer (ADR-022 4.1 step 5).
	if !opencode.ValidID(sess.ID, "ses") {
		e := unknownOutcome("the native answer carried no valid session ID; the session may exist: find it through the catalog (hb_key "+meta["hb_key"]+")", false)
		c.reg.finish(keyHash, rec, nil, e)
		return nil, e
	}
	if sess.Directory != grant.Directory || grant.Recheck(ctx) != nil {
		e := unknownOutcome("the created session's directory differs from the requested path or its identity changed; bootstrap_may_be_outside_root=true; the session is not used: find it through the catalog (hb_key "+meta["hb_key"]+")", true)
		c.reg.finish(keyHash, rec, nil, e)
		return nil, e
	}
	out := &CreateOutput{SessionID: "opencode:" + sess.ID, Root: root.Path, Directory: grant.Directory, Title: sess.Title, Created: true}
	c.reg.finish(keyHash, rec, out, nil)
	return *out, nil
}

// checkCaps reads the native catalog under every configured root (one bounded read each, in
// parallel up to four) and refuses when this root or the total is at its cap. A count that
// cannot be established is not under the cap: it is SESSION_LIMIT (cap state unknown) or
// UPSTREAM_TIMEOUT, and nothing is written (ADR-022 3.3).
func (c *CreateConfig) checkCaps(ctx context.Context, client *opencode.Client, target *lifecycle.Root) error {
	type result struct {
		ids map[string]bool
		err error
	}
	roots := c.cfg.Roots
	results := make([]result, len(roots))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	rctx, cancel := context.WithTimeout(ctx, createNativeBound())
	defer cancel()
	for i := range roots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			list, err := client.ListDirectory(rctx, roots[i].Path, createCountWindow+1)
			if err != nil {
				results[i].err = err
				return
			}
			if len(list) > createCountWindow {
				results[i].err = errTruncated
				return
			}
			ids := map[string]bool{}
			for _, sess := range list {
				// A session whose directory is outside the root is not this root's; one with no
				// directory cannot be placed and counts (an unknown is never under the cap).
				if sess.Directory != "" && !within(roots[i].Path, sess.Directory) {
					continue
				}
				id := sess.ID
				if id == "" {
					id = fmt.Sprintf("anon-%d-%d", i, len(ids))
				}
				ids[id] = true
			}
			results[i].ids = ids
		}(i)
	}
	wg.Wait()
	total := map[string]bool{}
	perRoot := 0
	for i, r := range results {
		if r.err != nil {
			// A count that is incomplete for any reason is never under the cap: a timeout is
			// UPSTREAM_TIMEOUT, everything else (truncated, a failing page, an unreadable
			// answer) is SESSION_LIMIT with the cap state unknown. Nothing was written.
			if errors.Is(r.err, context.DeadlineExceeded) || errors.Is(r.err, context.Canceled) || errors.Is(rctx.Err(), context.DeadlineExceeded) {
				return &execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "the native session count did not complete in time; no session was created", Retryable: true}
			}
			return sessionLimit("the number of existing sessions could not be established (cap state unknown); no session was created")
		}
		for id := range r.ids {
			total[id] = true
		}
		if roots[i].Path == target.Path {
			perRoot = len(r.ids)
		}
	}
	if perRoot >= target.MaxSessions {
		return sessionLimit(fmt.Sprintf("max_sessions %d of the root reached", target.MaxSessions))
	}
	if len(total) >= c.cfg.MaxTotalSessions {
		return sessionLimit(fmt.Sprintf("max_total_sessions %d reached", c.cfg.MaxTotalSessions))
	}
	return nil
}

var errTruncated = errors.New("native list truncated")

// within reports whether dir is root or lies under it, lexically.
func within(root, dir string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	rel, err := filepath.Rel(root, filepath.Clean(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
