package execution

// ADR-022 section 5.1 and ADR-025 sections 4.3 and 4.5 (slice S6): the lifecycle
// reservation kind and the archive refusal of send_prompt.
//
// The reservation is stored in its own table (Manager.lifecycle), never in busy: busy
// holds execution IDs that ADR-020 release, ADR-011 promotion and reconcile compare with,
// and the reservation has no run. No path here takes a run's op token (x.op is a per-run
// channel). The reservation is never persisted.

// SetArchiveCheck installs the archive snapshot lookup consulted by SendWithRunnerOutcome
// inside the same m.mu critical section as the busy check (ADR-025 4.5). The function must
// answer from memory only: no I/O and no lock that a lifecycle writer holds while it waits
// for the manager. A nil function removes the check. It is set once at wiring time.
func (m *Manager) SetArchiveCheck(f func(session string) bool) {
	if f == nil {
		m.archiveCheck.Store(nil)
		return
	}
	m.archiveCheck.Store(&f)
}

// archivedLocked reports whether the installed snapshot says the session is archived. The
// caller holds m.mu.
func (m *Manager) archivedLocked(session string) bool {
	f := m.archiveCheck.Load()
	return f != nil && (*f)(session)
}

// ReserveLifecycle takes the lifecycle reservation of one native session for the duration of
// a lifecycle change. It refuses with SESSION_BUSY when an execution holds the guard, a
// submission is pending admission, or another lifecycle reservation is held. The returned
// release function is idempotent and must be called on every exit path (defer it: it also
// covers a panic). While held, a concurrent Send gets SESSION_BUSY, SessionGuard reports the
// session held and "submitting", and ReleaseGuard, promotion and reconcile never act on it.
func (m *Manager) ReserveLifecycle(session string) (func(), error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	if m.busy[session] != "" || m.lifecycle[session] != "" {
		m.mu.Unlock()
		return nil, Fail("SESSION_BUSY", "session has an active or uncertain execution or a lifecycle change in flight")
	}
	id := token()
	m.lifecycle[session] = id
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if m.lifecycle[session] == id {
			delete(m.lifecycle, session)
		}
		m.mu.Unlock()
	}, nil
}

// LifecycleGuard reports whether a lifecycle reservation is held for the native session. It
// performs no I/O.
func (m *Manager) LifecycleGuard(session string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lifecycle[session] != ""
}
