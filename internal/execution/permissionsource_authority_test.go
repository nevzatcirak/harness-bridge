package execution

import (
	"context"
	"sort"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-024 S6 review fix (#309 P2-1, #310 P2-1): every input filter of the trusted authority has a
// negative test. The fixtures put runs straight into the manager's tables, so each case can hold
// a terminal run, another session's run, a prompt that was never sent, another directory and a
// non-canonical directory side by side. Each filter, removed alone, turns one of these RED.

type authRun struct {
	id, session, directory, canonical string
	state                             string
	attempted                         bool
	guard                             bool // holds the session guard (m.busy)
}

func authorityFixture(t *testing.T, runs ...authRun) runAuthority {
	t.Helper()
	m := setup(t, &fake{status: "idle"})
	cli, err := opencode.New("http://127.0.0.1:1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewOpenCodePermissionSource(cli, m, PermissionSourceConfig{Incarnation: epochFake{epoch: "epoch-1"}})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	for _, r := range runs {
		x := &run{native: r.session, directory: r.directory, canonicalDirectory: r.canonical, submissionAttempted: r.attempted,
			view: Execution{ID: r.id, SessionID: "opencode:" + r.session, MessageID: "msg_" + r.id, State: r.state}}
		m.runs[r.id] = x
		if r.guard {
			m.busy[r.session] = r.id
		}
	}
	m.mu.Unlock()
	return runAuthority{src}
}

func executionIDs(known []opencode.KnownPermissionExecution) []string {
	var ids []string
	for _, k := range known {
		ids = append(ids, k.ExecutionID)
	}
	sort.Strings(ids)
	return ids
}

// The scope comes only from the run that HOLDS the session guard. A terminal run (which no longer
// holds it) grants none, and with a live run next to older terminal ones the live run's directory wins.
func TestAuthorityScopeOnlyFromTheGuardHolder(t *testing.T) {
	a := authorityFixture(t,
		authRun{id: "x_term", session: "ses_a", directory: "/w/a", canonical: "/w/a", state: "completed", attempted: true},
	)
	if sc, err := a.PermissionScope(context.Background(), "ses_a"); err == nil {
		t.Fatalf("a terminal run granted a scope: %+v", sc)
	}
	a = authorityFixture(t,
		authRun{id: "x_old", session: "ses_a", directory: "/w/old", canonical: "/w/old", state: "completed", attempted: true},
		authRun{id: "x_live", session: "ses_a", directory: "/w/live", canonical: "/w/live", state: "waiting_for_permission", attempted: true, guard: true},
	)
	sc, err := a.PermissionScope(context.Background(), "ses_a")
	if err != nil || sc.Directory != "/w/live" || sc.WorkspaceRef != "workspace-"+directoryRef("/w/live") {
		t.Fatalf("scope = %+v err = %v", sc, err)
	}
}

// A guard entry whose run belongs to another native session is no scope for the asked session.
func TestAuthorityScopeRefusesARunOfAnotherSession(t *testing.T) {
	a := authorityFixture(t,
		authRun{id: "x_b", session: "ses_b", directory: "/w/b", canonical: "/w/b", state: "running", attempted: true},
	)
	a.s.m.mu.Lock()
	a.s.m.busy["ses_a"] = "x_b" // inconsistent guard table: the guard of ses_a names a run of ses_b
	a.s.m.mu.Unlock()
	if sc, err := a.PermissionScope(context.Background(), "ses_a"); err == nil {
		t.Fatalf("a run of another session granted a scope: %+v", sc)
	}
}

// The run's directory must be present and equal to its canonical directory (no alias, no empty).
func TestAuthorityScopeRequiresACanonicalDirectory(t *testing.T) {
	for name, r := range map[string]authRun{
		"alias":     {id: "x1", session: "ses_a", directory: "/w/link", canonical: "/w/real", state: "running", attempted: true, guard: true},
		"empty":     {id: "x2", session: "ses_a", directory: "", canonical: "", state: "running", attempted: true, guard: true},
		"unlocated": {id: "x3", session: "ses_a", directory: "", canonical: "/w/real", state: "running", attempted: true, guard: true},
	} {
		a := authorityFixture(t, r)
		if sc, err := a.PermissionScope(context.Background(), "ses_a"); err == nil {
			t.Errorf("%s: scope granted: %+v", name, sc)
		}
	}
}

// Only this session's runs, sent to the native, in the scope's own directory, are executions.
// Terminal runs that were sent still count (a request may belong to an earlier execution).
func TestAuthorityExecutionsAreFilteredBySessionSubmissionAndDirectory(t *testing.T) {
	a := authorityFixture(t,
		authRun{id: "x_live", session: "ses_a", directory: "/w/a", canonical: "/w/a", state: "waiting_for_permission", attempted: true, guard: true},
		authRun{id: "x_done", session: "ses_a", directory: "/w/a", canonical: "/w/a", state: "completed", attempted: true},
		authRun{id: "x_unsent", session: "ses_a", directory: "/w/a", canonical: "/w/a", state: "failed", attempted: false},
		authRun{id: "x_otherdir", session: "ses_a", directory: "/w/other", canonical: "/w/other", state: "completed", attempted: true},
		authRun{id: "x_othersession", session: "ses_b", directory: "/w/a", canonical: "/w/a", state: "running", attempted: true, guard: true},
	)
	scope, err := a.PermissionScope(context.Background(), "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	known, err := a.PermissionExecutions(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	got := executionIDs(known)
	if len(got) != 2 || got[0] != "x_done" || got[1] != "x_live" {
		t.Fatalf("executions = %v, want [x_done x_live]", got)
	}
	for _, k := range known {
		if k.Scope != scope || !k.SubmissionAttempted || k.UserMessageID != "msg_"+k.ExecutionID {
			t.Fatalf("known = %+v", k)
		}
	}
}

// The other session gets its own, separate answer from the same manager.
func TestAuthorityExecutionsOfTheOtherSessionStaySeparate(t *testing.T) {
	a := authorityFixture(t,
		authRun{id: "x_a", session: "ses_a", directory: "/w/a", canonical: "/w/a", state: "running", attempted: true, guard: true},
		authRun{id: "x_b", session: "ses_b", directory: "/w/a", canonical: "/w/a", state: "running", attempted: true, guard: true},
	)
	scope, err := a.PermissionScope(context.Background(), "ses_b")
	if err != nil {
		t.Fatal(err)
	}
	known, _ := a.PermissionExecutions(context.Background(), scope)
	if got := executionIDs(known); len(got) != 1 || got[0] != "x_b" {
		t.Fatalf("executions = %v, want [x_b]", got)
	}
}
