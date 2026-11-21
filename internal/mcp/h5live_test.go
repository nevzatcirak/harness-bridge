package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/lifecycle"
)

// unflagged is o without the replay flag, to compare a replay with the first answer.
func unflagged(o CreateOutput) CreateOutput {
	o.Replay = false
	return o
}

// H5 live findings of task #460. Every case is fabricated: a fake native server and temporary
// directories; nothing live.

// The only refusal cause that reaches a client is "unavailable": live, a filesystem permission
// problem made every create_session FORBIDDEN with a message that did not say a filesystem check
// failed. Every other cause stays internal (ADR-022 3.2: causes are for the audit line and tests).
func TestAuthRefusalNamesTheCauseOnlyForUnavailable(t *testing.T) {
	base := "the directory is not authorized for create_session"
	unavailable := authRefusal(&lifecycle.AuthError{Code: lifecycle.CodeForbidden, Cause: "unavailable"})
	if codeOf(unavailable) != "FORBIDDEN" {
		t.Fatalf("code %q", codeOf(unavailable))
	}
	msg := envelope(unavailable).Message
	if !strings.HasPrefix(msg, base) || !strings.Contains(msg, "unavailable") || !strings.Contains(msg, "filesystem check") {
		t.Errorf("unavailable: %q", msg)
	}
	if strings.ContainsAny(msg, "/\\") {
		t.Errorf("a path-like text in %q", msg)
	}
	// every other cause keeps the previous message exactly.
	for _, cause := range []string{"no_root", "mode_not_granted", "not_canonical", "root_replaced", "symlink", "not_directory", "changed",
		"no_top_level", "ascent_bound", "dot_git", "gitdir", "pointer", "layout", ""} {
		err := authRefusal(&lifecycle.AuthError{Code: lifecycle.CodeForbidden, Cause: cause})
		if got := envelope(err).Message; got != base {
			t.Errorf("cause %q leaked into %q", cause, got)
		}
	}
	// the reasons are unchanged next to it.
	if got := envelope(authRefusal(&lifecycle.AuthError{Code: lifecycle.CodeForbidden, Reason: lifecycle.ReasonNonGit, Cause: "no_top_level"})).Message; got != base+" (non_git)" {
		t.Errorf("non_git: %q", got)
	}
	// a NOT_FOUND keeps its own message exactly, whatever the cause.
	nf := authRefusal(&lifecycle.AuthError{Code: lifecycle.CodeNotFound, Cause: "not_directory"})
	if codeOf(nf) != "NOT_FOUND" || envelope(nf).Message != "the directory does not exist (create_session never creates directories)" {
		t.Errorf("NOT_FOUND changed: %v", nf)
	}
}

// End to end through create_session: a path component that is a regular file makes the directory
// check fail with ENOTDIR, which the authorizer classifies as `unavailable`.
func TestCreateRefusedByAFilesystemCheckSaysSo(t *testing.T) {
	e := newCreateEnv(t, nil)
	if err := os.WriteFile(filepath.Join(e.rootB, "afile"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.create(args{Root: e.rootB, Subdirectory: "afile/x", IdempotencyKey: "key-h5live-unav1"})
	if codeOf(err) != "FORBIDDEN" || !strings.Contains(envelope(err).Message, "unavailable") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(envelope(err).Message, e.rootB) {
		t.Fatalf("the message names the path: %v", err)
	}
	if n := len(e.requests()); n != 0 {
		t.Fatalf("%d native requests for a refused path", n)
	}
}

// The recorded outcome of a create is still returned verbatim on a replay (created stays true), and
// a replay now says so with an additive `replay` field; the first answer and a new key never carry it.
func TestCreateReplayIsFlagged(t *testing.T) {
	e := newCreateEnv(t, nil)
	first := e.mustCreate(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-h5live-rep01"})
	if first.Replay {
		t.Fatal("the first answer is flagged as a replay")
	}
	again := e.mustCreate(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-h5live-rep01"})
	if !again.Replay {
		t.Fatalf("a replay is not flagged: %+v", again)
	}
	again.Replay = false
	if again != first || !again.Created {
		t.Fatalf("the recorded output changed: %+v vs %+v", again, first)
	}
	third := e.mustCreate(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-h5live-rep01"})
	if !third.Replay {
		t.Fatal("a second replay is not flagged")
	}
	if e.count("POST") != 1 {
		t.Fatalf("%d POSTs", e.count("POST"))
	}
	other := e.mustCreate(args{Root: e.rootA, Title: "same", IdempotencyKey: "key-h5live-rep02"})
	if other.Replay || other.SessionID == first.SessionID {
		t.Fatalf("a new key: %+v", other)
	}
}

// list_catalog rows always carry `archived`, false when the session is not archived (it used to be
// omitted, so a client could not tell "not archived" from "field not supported").
func TestCatalogRowsAlwaysCarryArchived(t *testing.T) {
	for name, ov := range map[string]ArchiveOverlay{
		"an overlay with one archived session": &fakeOverlay{archived: map[string]bool{"opencode:ses_b": true}},
		"no overlay":                           nil,
	} {
		e := newCatalogEnv(t, ov)
		c := e.mustCall(args{IncludeArchived: true})
		if len(c.Sessions) == 0 {
			t.Fatalf("%s: no rows", name)
		}
		for _, row := range c.Sessions {
			b, _ := json.Marshal(row)
			want := `"archived":false`
			if row.Archived {
				want = `"archived":true`
			}
			if !strings.Contains(string(b), want) {
				t.Errorf("%s: row %s lacks %s: %s", name, row.SessionRef.SessionID, want, b)
			}
		}
	}
}
