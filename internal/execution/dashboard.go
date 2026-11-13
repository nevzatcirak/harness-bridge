package execution

import "sort"

// GuardView is the busy guard of one session as seen by DashboardSnapshot.
type GuardView struct {
	ExecutionID string // the execution that holds the guard
	State       string // its state; "submitting" for a reservation not yet published
}

// DashboardView is the read-only enumeration for the ADR-023 dashboard: value
// copies only, nothing that aliases run state.
type DashboardView struct {
	// Executions are copies of every execution this Manager holds, oldest first
	// (by Started, then ID).
	Executions []Execution
	// Guards maps a harness-qualified session ID ("opencode:ses_...", the same
	// form as Execution.SessionID) to the guard its holder keeps.
	Guards map[string]GuardView
}

// DashboardSnapshot enumerates the executions and guards for the dashboard
// (ADR-023 section 1.1, T10). It is read-only and bounded by memory only:
//
//   - it copies the run pointers and the guard table under m.mu, releases m.mu,
//     and only then locks each run's mu ALONE for the copy, so it holds one
//     mutex at a time and cannot invert the nested run-then-manager order that
//     finalizeUnsubmitted and commitRelease use;
//   - it never takes a run's op, so it cannot stall or be stalled by a native
//     read, and it makes no native call;
//   - every returned value is a copy (snapshot deep-copies the pointer fields).
//
// A guard whose holder is observed already terminal reports state "unknown"
// (held), like SessionGuard: the guard moved between the two reads and the
// conservative answer is never "free".
func (m *Manager) DashboardSnapshot() DashboardView {
	m.mu.Lock()
	runs := make([]*run, 0, len(m.runs))
	for _, x := range m.runs {
		runs = append(runs, x)
	}
	busy := make(map[string]string, len(m.busy))
	for session, id := range m.busy {
		busy[session] = id
	}
	m.mu.Unlock()

	view := DashboardView{Executions: make([]Execution, 0, len(runs)), Guards: make(map[string]GuardView, len(busy))}
	state := make(map[string]string, len(runs))
	for _, x := range runs {
		x.mu.Lock()
		e := snapshot(x.view)
		x.mu.Unlock()
		view.Executions = append(view.Executions, e)
		state[e.ID] = e.State
	}
	sort.Slice(view.Executions, func(i, j int) bool {
		a, b := view.Executions[i], view.Executions[j]
		if !a.Started.Equal(b.Started) {
			return a.Started.Before(b.Started)
		}
		return a.ID < b.ID
	})
	for session, id := range busy {
		g := GuardView{ExecutionID: id, State: "submitting"}
		if s, published := state[id]; published {
			g.State = s
			if terminal(s) {
				g.State = "unknown"
			}
		}
		view.Guards["opencode:"+session] = g
	}
	return view
}
