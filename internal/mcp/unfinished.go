package mcp

import (
	"context"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/bounded"
)

// F-5: OpenCode keeps no status entry for a session whose assistant message was
// left without time.completed (for example by an OpenCode restart during a tool
// call), so the session looks idle while send_prompt admission refuses it. A
// session whose state would be idle is therefore checked against its native
// history with the predicate admission uses (opencode.UnfinishedAssistant).
//
// get_session reads the full history, exactly as admission does, and is the
// authoritative view before a mutation. list_sessions and find_sessions read
// only the latest unfinishedPage messages through OpenCode's bounded
// latest-page route (GET /session/{id}/message?limit=N, OpenCode 1.19.4), so an
// unfinished assistant message older than that page is not seen there.
const (
	unfinishedLimit   = 8
	unfinishedTimeout = 3 * time.Second
	unfinishedPage    = 10
)

// unfinishedBudget bounds a get_session check and the whole list/find history
// phase. A check that does not finish within it leaves the session unknown.
var unfinishedBudget = unfinishedTimeout

type historyKey struct {
	instance, directory, session string
	limit                        int // 0: full history
}

type unfinishedResult struct {
	ok, unfinished bool
}

// The native reads are HTTP, but they share the F-3 policy: one in-flight read
// per key, at most unfinishedLimit running, every caller bounded. ErrBusy and
// ErrTimeout both leave ok false, so the session is reported unknown.
var unfinishedChecks = bounded.NewGroup[historyKey, unfinishedResult](unfinishedLimit, unfinishedTimeout)

// historyEvidence records a completed history check for one session.
type historyEvidence struct {
	checked, unfinished bool
}

// wouldBeIdle reports whether native evidence alone would make v idle: status
// (observed idle or inferred from absence) and permissions both read, and no
// pending permission. The guard is applied later and still wins.
func wouldBeIdle(v opencode.Session, d directoryEvidence) bool {
	if workspaceBound(v) || !d.statusOK || !d.permissionsOK || d.permissions[v.ID] {
		return false
	}
	native, listed := d.status[v.ID]
	return !listed || native.Type == "idle"
}

// guardWindowHook runs, in tests only, after the needsHistory decisions and
// before view, so a test can release a guard inside that window (#95 finding 1).
// It is nil in production.
var guardWindowHook func()

func runGuardWindowHook() {
	if guardWindowHook != nil {
		guardWindowHook()
	}
}

// needsHistory reports whether v's state depends on a history check: native
// evidence alone would make it idle and this Bridge holds no guard for it. A
// held guard decides the state anyway (view), so its history is not read. If
// the guard is released between this check and view, the session has no
// completed check and is reported unknown (fail closed).
func (s *Server) needsHistory(v opencode.Session, d directoryEvidence) bool {
	if !wouldBeIdle(v, d) {
		return false
	}
	if s.Runs != nil {
		if held, _ := s.Runs.SessionGuard(v.ID); held {
			return false
		}
	}
	return true
}

// checkUnfinished reads v's history (the latest page when limit > 0) through
// unfinishedChecks. ok is false when the read failed, timed out or was refused.
func (s *Server) checkUnfinished(ctx context.Context, v opencode.Session, limit int) historyEvidence {
	c := s.Client.WithDirectory(v.Directory)
	key := historyKey{instance: s.Client.InstanceID(), directory: v.Directory, session: v.ID, limit: limit}
	r, e := unfinishedChecks.Do(ctx, key, func(work context.Context) unfinishedResult {
		var msgs []opencode.Message
		var err error
		if limit == 0 {
			msgs, err = c.History(work, v.ID)
		} else {
			msgs, _, err = c.HistoryPage(work, v.ID, limit, "")
		}
		// A 2xx JSON null decodes to a nil slice without error; it is not an
		// empty history and proves nothing (as for /session/status, F-4).
		if err != nil || msgs == nil {
			return unfinishedResult{}
		}
		return unfinishedResult{ok: true, unfinished: opencode.UnfinishedAssistant(msgs)}
	})
	if e != nil || !r.ok {
		return historyEvidence{}
	}
	return historyEvidence{checked: true, unfinished: r.unfinished}
}
