package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The fixtures are derived from the upstream source at 148842e, not captured live:
// these tests prove the client decodes the documented shapes, nothing more.
func TestMessageDiffStateFromTheFixture(t *testing.T) {
	var msgs []Message
	if err := json.Unmarshal(fixture(t, "messages_summary_diffs_148842e.json"), &msgs); err != nil {
		t.Fatal(err)
	}
	want := map[string]DiffState{
		"msg_user_notcomputed": DiffNotComputed, // info.summary absent
		"msg_user_nodiffskey":  DiffNotComputed, // info.summary present, diffs absent
		"msg_user_empty":       DiffComputedEmpty,
		"msg_user_changes":     DiffChanges,
		"msg_assistant_1":      DiffNotComputed, // only a user message carries a summary
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages: %d", len(msgs))
	}
	for _, m := range msgs {
		if got := m.Info.DiffState(); got != want[m.Info.ID] {
			t.Errorf("%s: %q, want %q", m.Info.ID, got, want[m.Info.ID])
		}
	}
}

func TestDiffStateNeverFailsAndNeverGuessesEmpty(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":         ``,
		"null":          `null`,
		"not an object": `"x"`,
		"array":         `[]`,
		"diffs null":    `{"diffs":null}`,
		"diffs object":  `{"diffs":{}}`,
		"diffs string":  `{"diffs":"[]"}`,
		"diffs number":  `{"diffs":0}`,
		"no diffs":      `{"title":"t"}`,
		"garbage":       `{"diffs":[`,
	} {
		if got := (MessageInfo{Summary: json.RawMessage(raw)}).DiffState(); got != DiffNotComputed {
			t.Errorf("%s: %q, want not computed", name, got)
		}
	}
	if (MessageInfo{Summary: json.RawMessage(`{"diffs":[]}`)}).DiffState() != DiffComputedEmpty {
		t.Error("diffs [] must be computed-empty")
	}
	if (MessageInfo{Summary: json.RawMessage(`{"diffs":[{"additions":1,"deletions":0}]}`)}).DiffState() != DiffChanges {
		t.Error("a non-empty diffs must be changes")
	}
	// A summary of any shape does not stop a history read from decoding.
	var msgs []Message
	if err := json.Unmarshal([]byte(`[{"info":{"id":"msg_x","role":"user","summary":{"diffs":"weird"}},"parts":[]}]`), &msgs); err != nil {
		t.Fatalf("a strange summary broke the message decode: %v", err)
	}
}

func diffServer(t *testing.T, seen *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Encode())
		switch r.URL.Path {
		case "/session/ses_diffs1/diff":
			w.Write(fixture(t, "session_diff_148842e.json"))
		case "/vcs/diff":
			w.Write(fixture(t, "vcs_diff_148842e.json"))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "opencode", "secret")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSessionDiffAndVCSDiffDecodeTheFixtures(t *testing.T) {
	var seen []string
	c := diffServer(t, &seen).WithDirectory("/work dir")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sd, err := c.SessionDiff(ctx, "ses_diffs1", "msg_user_changes")
	if err != nil || len(sd) != 5 {
		t.Fatalf("session diff: %d %v", len(sd), err)
	}
	if sd[0].File == nil || *sd[0].File != "h4/NOTE.md" || sd[0].Patch == nil || sd[0].Additions != 1 || sd[0].Status != "modified" {
		t.Errorf("item 0: %+v", sd[0])
	}
	if sd[3].Patch != nil || sd[3].File == nil {
		t.Errorf("an absent patch must stay absent: %+v", sd[3])
	}
	if sd[4].File != nil {
		t.Errorf("an absent file must stay absent: %+v", sd[4])
	}
	vd, err := c.VCSDiff(ctx, VCSModeGit, -1)
	if err != nil || len(vd) != 3 || vd[2].Additions != 120000 {
		t.Fatalf("vcs diff: %d %v", len(vd), err)
	}
	wantSeen := []string{
		"GET /session/ses_diffs1/diff?directory=%2Fwork+dir&messageID=msg_user_changes",
		"GET /vcs/diff?directory=%2Fwork+dir&mode=git",
	}
	if strings.Join(seen, "|") != strings.Join(wantSeen, "|") {
		t.Fatalf("requests:\n got  %v\n want %v", seen, wantSeen)
	}
	// context is sent only when asked for.
	seen = nil
	if _, err := c.VCSDiff(ctx, VCSModeBranch, 3); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "context=3") || !strings.Contains(seen[0], "mode=branch") {
		t.Fatalf("branch/context: %v", seen)
	}
}

func TestDiffReadsRefuseBadInputWithoutARequest(t *testing.T) {
	var seen []string
	c := diffServer(t, &seen)
	ctx := context.Background()
	for _, tc := range []struct{ id, msg string }{
		{"", "msg_a"}, {"ses_a", ""}, {"x", "msg_a"}, {"ses_a", "x"}, {"ses_a/..", "msg_a"}, {"ses_a", "msg_a&directory=/"}, {"ses_a", "msg_a/../b"},
	} {
		if _, err := c.SessionDiff(ctx, tc.id, tc.msg); err == nil {
			t.Errorf("accepted %+v", tc)
		}
	}
	for _, mode := range []string{"", "GIT", "git&x=1", "raw", "../x"} {
		if _, err := c.VCSDiff(ctx, mode, -1); err == nil {
			t.Errorf("accepted mode %q", mode)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("a refused input still sent %v", seen)
	}
}

// The client bound is 8 MiB; a native answer above it is an error, never a
// truncated list.
func TestVCSDiffAbove8MiBIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"file":"a","patch":"`))
		w.Write([]byte(strings.Repeat("x", 9<<20)))
		w.Write([]byte(`","additions":1,"deletions":0}]`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "opencode", "secret")
	if out, err := c.VCSDiff(context.Background(), VCSModeGit, -1); err == nil || out != nil {
		t.Fatalf("an over-bound answer returned %d items, err %v", len(out), err)
	}
}
