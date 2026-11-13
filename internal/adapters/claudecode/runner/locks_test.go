package runner

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// The per-session flock namespace: claudecode:<uuid> is held for the writer
// lifetime, in the shared lock directory, under a name that cannot collide with an
// OpenCode ID.
func TestClaudeSessionsAreFlocked(t *testing.T) {
	dir := private(t, "locks")
	sessions := []claudecode.Session{{ID: "claudecode:" + uuidN(1), Root: "/r"}, {ID: "claudecode:" + uuidN(2), Root: "/r"}}
	first, err := AcquireLocks(dir, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Held("claudecode:"+uuidN(1)) || first.Held("claudecode:"+uuidN(3)) || first.Held("opencode:ses_x") {
		t.Fatal("Held reports the wrong set")
	}
	// A second writer for the same sessions is refused while the first lives.
	if _, err := AcquireLocks(dir, sessions[:1]); err == nil {
		t.Fatal("a second writer acquired a held Claude session")
	}
	if _, err := execution.AcquireOwnership(dir, []string{"claudecode:" + uuidN(2)}); err == nil {
		t.Fatal("the OpenCode ownership mechanism acquired a held Claude session")
	}
	// The lock file name is the hash of the full namespaced string.
	name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte("claudecode:"+uuidN(1))))
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("expected lock file %s: %v", name, err)
	}
	bare := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(uuidN(1))))
	if _, err := os.Stat(filepath.Join(dir, bare)); err == nil {
		t.Fatal("a lock exists for the bare UUID: the namespace is missing")
	}
	// An OpenCode ID is a different lock: no collision.
	oc, err := execution.AcquireOwnership(dir, []string{"opencode:ses_abc"})
	if err != nil {
		t.Fatalf("an OpenCode session must not collide: %v", err)
	}
	oc.Close()
	first.Close()
	again, err := AcquireLocks(dir, sessions)
	if err != nil {
		t.Fatalf("after Close the sessions are free: %v", err)
	}
	again.Close()
}

func TestAcquireLocksRefusesBadInput(t *testing.T) {
	dir := private(t, "locks")
	if _, err := AcquireLocks(dir, []claudecode.Session{{ID: "opencode:ses_x"}}); err == nil {
		t.Error("a non-Claude ID must be refused")
	}
	if _, err := AcquireLocks("relative", []claudecode.Session{{ID: "claudecode:" + uuidN(1)}}); err == nil {
		t.Error("a relative lock directory must be refused")
	}
	empty, err := AcquireLocks(dir, nil)
	if err != nil || empty.Held("claudecode:"+uuidN(1)) {
		t.Errorf("no sessions, no locks: %v", err)
	}
	empty.Close()
	var nilLocks *Locks
	if nilLocks.Held("x") {
		t.Error("a nil Locks holds nothing")
	}
	nilLocks.Close()
}
