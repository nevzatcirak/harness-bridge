package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/execution"
	"github.com/nevzatcirak/cirak/internal/lifecycle"
	"github.com/nevzatcirak/cirak/internal/overlay"
	"github.com/nevzatcirak/cirak/internal/security"
)

// ADR-022 slice S6 with ADR-025 (accepted, D18): archive_session, unarchive_session and
// close_session over the persisted overlay.
//
// archive_session and unarchive_session make no native write: the overlay is Bridge state, never a
// native PATCH, DELETE or POST. close_session makes exactly one, the abort POST of the existing
// interrupt path, and only while a Bridge execution holds the session's guard. The preconditions are
// native READS. Every change runs under the manager's lifecycle reservation (never x.op, never an
// execution ID) and then under the overlay's own mutex. The overlay is a label store and never an
// authority: no authorization function in this package calls Archived or reads an entry (an AST test
// pins it).

// archiveBound bounds each native read and the interrupt settle of one call. A variable only so a
// test can shorten it.
var archiveBound = 5 * time.Second

// archiveHook is a test seam called after the first precondition check and before the pre-write
// re-check. Nil in production.
var archiveHook func()

// ArchiveStore is what the three tools need of the overlay: the read interface of the catalog,
// the two serialized writers, and the closed counters of the audit line.
type ArchiveStore interface {
	ArchiveOverlay
	Archive(ctx context.Context, id, principal string) (overlay.Entry, bool, error)
	Unarchive(ctx context.Context, id string) (bool, error)
	ArchivedKnown(id string) bool
	Generation() uint64
	Entries() int
	CanWrite() bool
	Err() error
}

// ArchiveConfig enables the three tools. A Server whose Archive is nil registers none of them
// (ADR-022 3.1: no lifecycle configuration, no lifecycle tools; ADR-025 Q5: CIRAK_OVERLAY_DIR
// unset means no archive tools).
type ArchiveConfig struct {
	cfg   *lifecycle.Config
	auth  *lifecycle.Authorizer
	store ArchiveStore
	now   func() time.Time

	mu     sync.Mutex
	window []time.Time // the hourly cap shared by the three tools (memory-only, like create_per_hour)
}

// NewArchiveConfig builds the tools' state from a validated lifecycle configuration and an overlay
// store that holds the write role.
func NewArchiveConfig(cfg *lifecycle.Config, store ArchiveStore) (*ArchiveConfig, error) {
	if store == nil || !store.CanWrite() {
		return nil, errors.New("the archive tools need an overlay store that holds the write role")
	}
	a, err := lifecycle.NewAuthorizer(cfg)
	if err != nil {
		return nil, err
	}
	return &ArchiveConfig{cfg: cfg, auth: a, store: store, now: time.Now}, nil
}

// Install wires the in-memory archive lookup into the manager so send_prompt refuses an archived
// session (ADR-025 4.5, Q1). The lookup answers from the published snapshot with no I/O.
func (c *ArchiveConfig) Install(m *execution.Manager) {
	m.SetArchiveCheck(func(session string) bool { return c.store.ArchivedKnown("opencode:" + session) })
}

// ArchiveOutput is the result of the three tools.
type ArchiveOutput struct {
	SessionID string `json:"session_id"`
	// Archived is the overlay state of the session after the call.
	Archived bool `json:"archived"`
	// Changed is false for an idempotent no-op.
	Changed    bool   `json:"changed"`
	ArchivedAt string `json:"archived_at,omitempty"`
	// Interrupt is close_session only: interrupted, none or failed (failed never reaches a result:
	// the call is refused and the overlay is unchanged).
	Interrupt string `json:"interrupt,omitempty"`
}

const (
	reasonOverlayUnavailable = "overlay_unavailable"
	reasonOverlayUncertain   = "overlay_uncertain"
)

// archiveEvidence is what one call learned, for its audit line (ADR-025 4.6).
type archiveEvidence struct {
	tool      string
	rootIdx   int
	sessionID string
	changed   bool
	interrupt string // close_session: "interrupted", "none", "failed"
	outcome   string
	inWindow  int
	window    bool
}

type archiveEvKey struct{}

func archiveEv(ctx context.Context) *archiveEvidence {
	if ev, ok := ctx.Value(archiveEvKey{}).(*archiveEvidence); ok {
		return ev
	}
	return &archiveEvidence{rootIdx: -1}
}

func isArchiveTool(name string) bool {
	return name == "archive_session" || name == "unarchive_session" || name == "close_session"
}

func overlayError(err error) error {
	switch overlay.KindOf(err) {
	case overlay.KindLimit:
		return &execution.Error{Code: "SESSION_LIMIT", Message: "the archive overlay is full"}
	case overlay.KindUncertain:
		return &execution.Error{Code: "UNKNOWN_OUTCOME", Message: "the overlay write may have happened; read list_catalog with include_archived and repeat the idempotent call if needed", Reason: reasonOverlayUncertain}
	case overlay.KindTimeout:
		return &execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "a previous overlay write has not returned", Retryable: true}
	case overlay.KindInvalidID:
		return execution.Fail("INVALID_INPUT", "invalid session_id")
	}
	return &execution.Error{Code: "UPSTREAM_FAILURE", Message: "the archive overlay is unavailable", Reason: reasonOverlayUnavailable}
}

func archiveAuthRefusal(tool string, err error) error {
	var ae *lifecycle.AuthError
	if errors.As(err, &ae) {
		msg := "the session is not authorized for " + tool
		if ae.Reason != "" {
			msg += " (" + ae.Reason + ")"
		}
		switch ae.Code {
		case lifecycle.CodeNotFound:
			return execution.Fail("NOT_FOUND", "the session directory does not exist")
		case lifecycle.CodeInvalidInput:
			return execution.Fail("INVALID_INPUT", msg)
		}
		return execution.Fail("FORBIDDEN", msg)
	}
	if errors.Is(err, security.ErrCheckUnavailable) {
		return &execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "the directory check did not complete; storage unavailable", Retryable: true}
	}
	return execution.Fail("FORBIDDEN", "the session is not authorized for "+tool)
}

// charge applies the hourly cap (archive_per_hour, shared by the three tools). It counts the
// calls that reach the overlay write, before the write, so a failing write still spends budget.
func (c *ArchiveConfig) charge(ev *archiveEvidence) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	kept := c.window[:0]
	for _, t := range c.window {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	c.window = kept
	ev.window, ev.inWindow = true, len(c.window)
	if len(c.window) >= c.cfg.ArchivePerHour {
		return &execution.Error{Code: "SESSION_LIMIT", Message: fmt.Sprintf("archive_per_hour %d reached", c.cfg.ArchivePerHour), Retryable: true}
	}
	c.window = append(c.window, now)
	ev.inWindow = len(c.window)
	return nil
}

func (c *ArchiveConfig) rootIndex(dir string) int {
	for i, r := range c.cfg.Roots {
		if dir == r.Path || strings.HasPrefix(dir, strings.TrimSuffix(r.Path, "/")+"/") {
			return i
		}
	}
	return -1
}

// archiveCall is archive_session, unarchive_session and close_session. Order, with the reason for each:
//  1. the tool exists, the Bridge is not read-only, the ID is an OpenCode session (no state touched);
//  2. the static ACL (s.authorized) and the lifecycle authorization with the archive mode (ADR-022 3.2);
//  3. the overlay is readable and this process holds the write role (a refusal before any reservation);
//  4. close_session only: interrupt the Bridge execution that holds the guard and wait until it settled;
//  5. the lifecycle reservation, then the preconditions of ADR-022 7.3 (native reads, fail-closed);
//  6. the cap, the pre-write re-check (reservation still held, authorization and native state read
//     again), then the one overlay write.
func (s *Server) archiveCall(ctx context.Context, name string, a args) (any, error) {
	c := s.Archive
	if c == nil || s.Runs == nil {
		return nil, execution.Fail("INVALID_INPUT", "unknown tool")
	}
	if s.ReadOnly {
		return nil, execution.Fail("FORBIDDEN", "mutations disabled on this bridge")
	}
	ev := archiveEv(ctx)
	id, e := native(a.SessionID)
	if e != nil {
		return nil, e
	}
	ev.sessionID = "opencode:" + id
	v, client, e := s.authorized(ctx, id)
	if e != nil {
		return nil, e
	}
	grant, err := c.auth.Authorize(ctx, v.Directory, lifecycle.ModeArchive)
	if err != nil {
		ev.rootIdx = c.rootIndex(v.Directory)
		return nil, archiveAuthRefusal(name, err)
	}
	ev.rootIdx = c.rootIndex(grant.Root)
	if err := c.store.Err(); err != nil {
		return nil, overlayError(err)
	}
	if !c.store.CanWrite() {
		return nil, overlayError(nil)
	}
	if name == "close_session" {
		if e := s.closeInterrupt(ctx, id, v, ev); e != nil {
			return nil, e
		}
	}
	// ADR-025 4.3 step 1: the reservation. It is released on every exit path, a panic included.
	release, err := s.Runs.ReserveLifecycle(id)
	if err != nil {
		return nil, err
	}
	defer release()

	if e := s.archivePreconditions(ctx, id, v, client); e != nil {
		return nil, e
	}
	if archiveHook != nil {
		archiveHook()
	}
	if err := c.charge(ev); err != nil {
		return nil, err
	}
	// The re-check immediately before the write (ADR-022 7.3): authorization and native state again.
	if err := grant.Recheck(ctx); err != nil {
		return nil, archiveAuthRefusal(name, err)
	}
	if e := s.archivePreconditions(ctx, id, v, client); e != nil {
		return nil, e
	}
	principal := s.auditPrincipal()
	out := ArchiveOutput{SessionID: ev.sessionID, Interrupt: ev.interrupt}
	if name == "unarchive_session" {
		removed, err := c.store.Unarchive(ctx, ev.sessionID)
		if err != nil {
			return nil, overlayError(err)
		}
		ev.changed, out.Changed = removed, removed
		return out, nil
	}
	entry, archived, err := c.store.Archive(ctx, ev.sessionID, principal)
	if err != nil {
		return nil, overlayError(err)
	}
	ev.changed, out.Changed = archived, archived
	out.Archived, out.ArchivedAt = true, entry.ArchivedAt.UTC().Format(time.RFC3339)
	return out, nil
}

// archivePreconditions is ADR-022 7.3 (2) to (4), all native READS, all fail-closed: an unreadable
// state is a refusal, never "idle". (1), the lifecycle authorization, is the caller's; the Bridge
// part of (2) is the reservation itself (a held execution guard refuses it with SESSION_BUSY).
func (s *Server) archivePreconditions(ctx context.Context, id string, v opencode.Session, client *opencode.Client) error {
	rctx, cancel := context.WithTimeout(ctx, archiveBound)
	defer cancel()
	statuses, err := client.Status(rctx)
	if err != nil || statuses == nil {
		return &execution.Error{Code: "UPSTREAM_FAILURE", Message: "the native session status could not be read; nothing changed"}
	}
	if st, listed := statuses[id]; listed && st.Type != "idle" {
		return execution.Fail("SESSION_BUSY", "the native session is not idle")
	}
	perms, err := client.Permissions(rctx)
	if err != nil {
		return &execution.Error{Code: "UPSTREAM_FAILURE", Message: "the pending permissions could not be read; nothing changed"}
	}
	for _, p := range perms {
		if p.SessionID == id {
			return execution.Fail("SESSION_BUSY", "a permission request is pending")
		}
	}
	if h := s.checkUnfinished(rctx, v, 0); !h.checked || h.unfinished {
		return execution.Fail("SESSION_BUSY", "the native history has an unfinished message or could not be read")
	}
	sessions, err := client.List(rctx)
	if err != nil || len(sessions) >= createCountWindow {
		return &execution.Error{Code: "UPSTREAM_FAILURE", Message: "the native session tree could not be read completely; nothing changed"}
	}
	children := map[string][]string{}
	for _, x := range sessions {
		if x.ParentID != "" {
			children[x.ParentID] = append(children[x.ParentID], x.ID)
		}
	}
	seen := map[string]bool{id: true}
	for queue := []string{id}; len(queue) > 0 && len(seen) <= createCountWindow; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if seen[child] {
				continue
			}
			seen[child] = true
			if st, listed := statuses[child]; listed && st.Type != "idle" {
				return execution.Fail("SESSION_BUSY", "a child session is running")
			}
			queue = append(queue, child)
		}
	}
	return nil
}

// closeInterrupt is the first half of close_session: the existing interrupt path, then wait until
// the execution settled. Anything but a settled execution leaves the overlay unchanged. The
// interrupt is the manager's own function, below the tool layer, so no second AUDIT-01 line is
// written for the same call (ADR-025 4.6).
func (s *Server) closeInterrupt(ctx context.Context, id string, v opencode.Session, ev *archiveEvidence) error {
	ev.interrupt = "none"
	execID := ""
	if g, ok := s.Runs.DashboardSnapshot().Guards["opencode:"+id]; ok {
		execID = g.ExecutionID
	}
	if execID == "" {
		return nil
	}
	ictx, cancel := context.WithTimeout(ctx, archiveBound)
	defer cancel()
	if e := s.Runs.ValidateSession(ictx, execID, v); e != nil {
		ev.interrupt = "failed"
		return e
	}
	if _, e := s.Runs.Interrupt(ictx, execID); e != nil {
		ev.interrupt = "failed"
		return e
	}
	for {
		x, e := s.Runs.Get(ictx, execID)
		if e != nil {
			ev.interrupt = "failed"
			return e
		}
		if x.State == "interrupted" || x.State == "completed" || x.State == "failed" {
			if held, _ := s.Runs.SessionGuard(id); !held {
				ev.interrupt = "interrupted"
				return nil
			}
		}
		select {
		case <-ictx.Done():
			ev.interrupt = "failed"
			return &execution.Error{Code: "UNKNOWN_OUTCOME", Message: "the interrupt did not settle within the bound; the session was not archived", ExecutionID: execID}
		case <-time.After(25 * time.Millisecond):
		}
	}
}
