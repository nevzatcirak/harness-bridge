package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

type directoryScoped interface{ Directory() string }
type reconciliation struct {
	done chan struct{}
	err  error
}

func take(ctx context.Context, gate chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func give(gate chan struct{}) { <-gate }
func copyTime(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func snapshot(v Execution) Execution {
	v.Ended = copyTime(v.Ended)
	v.MonitorEndedAt = copyTime(v.MonitorEndedAt)
	v.ReconciledAt = copyTime(v.ReconciledAt)
	if v.Unresolved != nil {
		u := *v.Unresolved
		u.LastCheckedAt = copyTime(u.LastCheckedAt)
		v.Unresolved = &u
	}
	return v
}

// Caller holds run.mu. preserve keeps the cause that stopped background tracking.
func unresolved(x *run, reason, message string, now time.Time, preserve bool) {
	old := x.view.Unresolved
	u := &Unresolved{Reason: reason, Since: now}
	if old != nil {
		if preserve || old.Reason == reason {
			u.Reason = old.Reason
			u.Since = old.Since
		}
		u.LastCheckedAt = copyTime(old.LastCheckedAt)
	}
	x.view.Unresolved = u
	x.view.State = "unknown"
	x.view.Error = "UNKNOWN_OUTCOME: " + message
	x.view.Updated = now
}

func validateSession(x *run, meta opencode.Session) error {
	if meta.ID != x.native || meta.Directory != x.directory {
		return Fail("FORBIDDEN", "execution session or workspace changed")
	}
	canonical, err := filepath.EvalSymlinks(meta.Directory)
	if err != nil || canonical != x.canonicalDirectory || canonical != meta.Directory {
		return Fail("FORBIDDEN", "execution workspace identity changed")
	}
	info, err := os.Stat(canonical)
	if err != nil || x.workspace == nil || !os.SameFile(x.workspace, info) {
		return Fail("FORBIDDEN", "execution workspace replaced")
	}
	return nil
}

// ValidateSession binds the already authorized MCP metadata to the acceptance
// snapshot, including terminal cached results. No native I/O occurs here.
func (m *Manager) ValidateSession(id string, meta opencode.Session) error {
	x, err := m.find(id)
	if err != nil {
		return err
	}
	return validateSession(x, meta)
}

func (m *Manager) beginRead() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.wg.Add(1)
	return true
}
func (m *Manager) readContext(ctx context.Context) (context.Context, context.CancelFunc) {
	bounded, cancel := context.WithTimeout(ctx, m.reconcileTimeout)
	stop := context.AfterFunc(m.ctx, cancel)
	return bounded, func() { stop(); cancel() }
}

// One caller does one bounded probe. Followers join it, or read the cached
// result during the interval cooldown. No reconciliation worker survives a call.
func (m *Manager) refresh(ctx context.Context, x *run) error {
	// Cached terminal results also belong to the original workspace identity.
	if err := validateSession(x, opencode.Session{ID: x.native, Directory: x.directory}); err != nil {
		return err
	}
	if !m.beginRead() {
		return nil
	}
	defer m.wg.Done()
	bounded, cancel := m.readContext(ctx)
	defer cancel()
	x.mu.Lock()
	if terminal(x.view.State) || !x.monitorDone {
		x.mu.Unlock()
		return nil
	}
	if f := x.flight; f != nil {
		x.mu.Unlock()
		select {
		case <-f.done:
			return f.err
		case <-bounded.Done():
			return bounded.Err()
		}
	}
	if time.Now().Before(x.nextProbe) {
		err := x.probeError
		x.mu.Unlock()
		return err
	}
	flight := &reconciliation{done: make(chan struct{})}
	x.flight = flight
	x.mu.Unlock()
	err := take(bounded, m.reconcileSlots)
	if err == nil {
		defer give(m.reconcileSlots)
		err = take(bounded, x.op)
		if err == nil {
			err = m.probe(bounded, x, true)
			give(x.op)
		}
	}
	x.mu.Lock()
	x.nextProbe = time.Now().Add(m.interval)
	x.probeError = err
	flight.err = err
	x.flight = nil
	close(flight.done)
	x.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// probe runs only while run.op is held. Native reads are never submission
// retries. Terminal transitions and busy release commit once, under run.mu.
func (m *Manager) probe(ctx context.Context, x *run, reconciling bool) error {
	x.mu.Lock()
	already := terminal(x.view.State)
	x.mu.Unlock()
	if already {
		return nil
	}
	failed := func(reason, message string, err error) error {
		x.mu.Lock()
		defer x.mu.Unlock()
		x.observationValid = false
		if !terminal(x.view.State) {
			now := time.Now().UTC()
			unresolved(x, reason, message, now, x.monitorDone)
			if reconciling {
				x.view.Unresolved.LastCheckedAt = &now
			}
		}
		return err
	}
	meta, err := x.runner.Get(ctx, x.native)
	if err != nil {
		return failed("OBSERVATION_UNAVAILABLE", "native metadata unavailable", nil)
	}
	if err = validateSession(x, meta); err != nil {
		x.mu.Lock()
		x.messages = nil
		x.mu.Unlock()
		return failed("IDENTITY_MISMATCH", "original session/workspace no longer matches", err)
	}
	statuses, err := x.runner.Status(ctx)
	if err != nil {
		return failed("OBSERVATION_UNAVAILABLE", "native status unavailable", nil)
	}
	msgs, err := x.runner.History(ctx, x.native)
	if err != nil {
		return failed("OBSERVATION_UNAVAILABLE", "native history unavailable", nil)
	}
	permissions, err := x.runner.Permissions(ctx)
	if err != nil {
		return failed("OBSERVATION_UNAVAILABLE", "native permissions unavailable", nil)
	}
	// Native reads are not atomic. Detect observed replacement during those reads
	// before exposing content, committing terminal proof or permitting interruption.
	if err = validateSession(x, meta); err != nil {
		x.mu.Lock()
		x.messages = nil
		x.mu.Unlock()
		return failed("IDENTITY_MISMATCH", "workspace changed during observation", err)
	}
	if ctx.Err() != nil || m.ctx.Err() != nil {
		return failed("OBSERVATION_UNAVAILABLE", "native observation canceled or expired", nil)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if terminal(x.view.State) {
		return nil
	}
	now := time.Now().UTC()
	x.observationValid = true
	x.messages = nil
	userFound, unfinished := false, false
	var proof *opencode.Message
	for _, msg := range msgs {
		if msg.Info.SessionID != x.native {
			continue
		}
		if msg.Info.ID == x.view.MessageID && msg.Info.Role == "user" {
			userFound = true
			x.messages = append(x.messages, msg)
		}
		if msg.Info.ParentID != x.view.MessageID || msg.Info.Role != "assistant" {
			continue
		}
		if msg.Info.Path.Cwd != "" {
			cwd, err := filepath.EvalSymlinks(msg.Info.Path.Cwd)
			if err != nil || cwd != x.canonicalDirectory {
				x.messages = nil
				x.observationValid = false
				unresolved(x, "IDENTITY_MISMATCH", "correlated assistant workspace differs", now, x.monitorDone)
				if reconciling {
					x.view.Unresolved.LastCheckedAt = &now
				}
				return Fail("FORBIDDEN", "correlated assistant workspace differs")
			}
		}
		x.messages = append(x.messages, msg)
		if msg.Info.Time.Completed <= 0 {
			unfinished = true
			continue
		}
		if terminalState(msg) != "" && (proof == nil || msg.Info.Time.Completed >= proof.Info.Time.Completed) {
			v := msg
			proof = &v
		}
	}
	// Pending/unknown permission state prevents a terminal promotion and admission.
	for _, p := range permissions {
		if p.SessionID == x.native {
			if x.monitorDone {
				unresolved(x, "MONITOR_EXPIRED", "native permission pending after tracking stopped", now, true)
				x.view.Unresolved.LastCheckedAt = &now
			} else {
				x.view.Error = ""
				x.view.Unresolved = nil
			}
			x.view.State = "waiting_for_permission"
			x.view.Updated = now
			return nil
		}
	}
	status, exists := statuses[x.native]
	if userFound && !unfinished && proof != nil && (!exists || status.Type == "idle") {
		x.view.State = terminalState(*proof)
		x.view.Updated = now
		ended := time.UnixMilli(proof.Info.Time.Completed).UTC()
		x.view.Ended = &ended
		x.view.Unresolved = nil
		x.view.Error = ""
		if x.view.State == "failed" {
			x.view.Error = "UPSTREAM_FAILURE: correlated assistant error (inspect result)"
		}
		if reconciling {
			x.view.ReconciledAt = &now
		}
		m.mu.Lock()
		if m.busy[x.native] == x.view.ID {
			delete(m.busy, x.native)
		}
		m.mu.Unlock()
		return nil
	}
	if x.monitorDone {
		unresolved(x, "MONITOR_EXPIRED", "native terminal evidence remains inconclusive", now, true)
		x.view.Unresolved.LastCheckedAt = &now
	} else if !exists || (status.Type != "idle" && status.Type != "busy" && status.Type != "retry") {
		unresolved(x, "OBSERVATION_UNAVAILABLE", "native status absent or unsupported without terminal proof", now, false)
	} else {
		x.view.State = "running"
		x.view.Error = ""
		x.view.Unresolved = nil
		x.view.Updated = now
	}
	return nil
}

func terminalState(msg opencode.Message) string {
	if msg.Info.Time.Completed <= 0 {
		return ""
	}
	if len(msg.Info.Error) > 0 && string(msg.Info.Error) != "null" {
		var native struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(msg.Info.Error, &native) != nil || native.Name == "" {
			return ""
		}
		if native.Name == "MessageAbortedError" {
			return "interrupted"
		}
		return "failed"
	}
	if msg.Info.Finish == "stop" || msg.Info.Finish == "end_turn" {
		return "completed"
	}
	return ""
}

func copyMessages(messages []opencode.Message) []opencode.Message {
	out := append([]opencode.Message(nil), messages...)
	for i := range out {
		out[i].Info.Error = append(json.RawMessage(nil), out[i].Info.Error...)
		out[i].Parts = append([]json.RawMessage(nil), out[i].Parts...)
		for j := range out[i].Parts {
			out[i].Parts[j] = append(json.RawMessage(nil), out[i].Parts[j]...)
		}
	}
	return out
}
