package runner

import (
	"errors"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// Locks holds the per-session writer locks of the allowlisted Claude sessions
// for the whole writer lifetime (§2). It uses the same directory, the same
// validation and the same flock as the OpenCode ACL (execution.AcquireOwnership);
// the namespace is the full string "claudecode:<uuid>", hashed into the lock file
// name, so it cannot collide with an "opencode:ses_..." ID.
type Locks struct {
	own *execution.Ownership
	ids map[string]bool
}

// AcquireLocks takes the lock of every session or fails (and releases what it
// took) if any is held by another writer.
func AcquireLocks(dir string, sessions []claudecode.Session) (*Locks, error) {
	l := &Locks{ids: map[string]bool{}}
	if len(sessions) == 0 {
		return l, nil
	}
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if _, err := claudecode.ParseSessionID(s.ID); err != nil {
			return nil, errors.New("runner: invalid session id in the allowlist")
		}
		ids = append(ids, s.ID)
	}
	own, err := execution.AcquireOwnership(dir, ids)
	if err != nil {
		return nil, err
	}
	l.own = own
	for _, id := range ids {
		l.ids[id] = true
	}
	return l, nil
}

// Held reports whether this process holds the lock of the session.
func (l *Locks) Held(id string) bool { return l != nil && l.ids[id] }

// Close releases every lock.
func (l *Locks) Close() {
	if l != nil && l.own != nil {
		l.own.Close()
		l.own = nil
		l.ids = map[string]bool{}
	}
}
