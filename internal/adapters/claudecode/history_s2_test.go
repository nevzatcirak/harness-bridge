package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func beginTurn(t *testing.T, s *Store, exec string) {
	t.Helper()
	if err := s.BeginTurn(testUUID, exec, TurnStart{Prompt: "p", Principal: "alice", KeyHashPrefix: "abcd"}); err != nil {
		t.Fatal(err)
	}
}

// turn_end strings come from a closed set; anything else is replaced and reported,
// and the turn is still closed (never left open).
func TestTurnEndClosedSets(t *testing.T) {
	cases := map[string]TurnEnd{
		"state":    {State: "weird", Row: "3", ExitClass: "zero"},
		"row":      {State: "completed", Row: "99", ExitClass: "zero"},
		"reason":   {State: "unknown", Row: "10", Reason: "free text from a child", ExitClass: "zero"},
		"exit":     {State: "completed", Row: "3", ExitClass: "sigkill"},
		"injected": {State: "completed\nSECRET", Row: "3"},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newStore(t, nil)
			beginTurn(t, s, "e1")
			if err := s.EndTurn(testUUID, "e1", e); err != ErrInvalidTurnEnd {
				t.Fatalf("err = %v, want ErrInvalidTurnEnd", err)
			}
			res, _ := s.Read(testUUID)
			last := res.Records[len(res.Records)-1]
			if last.Kind != KindTurnEnd {
				t.Fatalf("turn not closed: %+v", last)
			}
			if !turnEndStates[last.State] || !turnEndRows[last.Row] || !turnEndReasons[last.Reason] || !turnEndExits[last.ExitClass] {
				t.Fatalf("a field outside its set was stored: %+v", last)
			}
		})
	}
	s, _ := newStore(t, nil)
	beginTurn(t, s, "e2")
	if err := s.EndTurn(testUUID, "e2", TurnEnd{State: "completed", Row: "3", Subtype: "sub type\x00", TerminalReason: strings.Repeat("a", 90), ExitClass: "zero"}); err != nil {
		t.Fatalf("child-reported strings are normalised, not an error: %v", err)
	}
	res, _ := s.Read(testUUID)
	last := res.Records[len(res.Records)-1]
	if last.Subtype != "other" || last.TerminalReason != "other" {
		t.Fatalf("child-reported strings must be short identifiers: %+v", last)
	}
}

// The API projection exposes a fixed set of fields per kind and nothing internal.
func TestHistoryAPIWhitelist(t *testing.T) {
	s, _ := newStore(t, nil)
	beginTurn(t, s, "e1")
	s.Append(testUUID, "e1", Record{Kind: KindAssistantBlock, Block: &Block{Type: "text", Text: "hi"}})
	s.Append(testUUID, "e1", Record{Kind: KindToolResult, ToolUseID: "tu1", Content: "ok"})
	if err := s.EndTurn(testUUID, "e1", TurnEnd{State: "completed", Row: "3", Subtype: "success", TerminalReason: "completed", NumTurns: 1, ExitClass: "zero", CostUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	api, err := s.ReadAPI(testUUID)
	if err != nil || len(api.Records) != 4 {
		t.Fatalf("api = %+v, %v", api, err)
	}
	raw, _ := json.Marshal(api)
	for _, banned := range []string{"alice", "abcd", "principal", "key_hash_prefix", `"row"`, `"v"`} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("internal field %q leaked into the API view: %s", banned, raw)
		}
	}
	if api.Records[0].Prompt != "p" || api.Records[3].ExitClass != "zero" || api.Records[3].CostUSD != 0.5 {
		t.Errorf("whitelisted fields missing: %+v", api.Records)
	}
	// A record carrying fields that do not belong to its kind exposes none of them.
	a := Record{Kind: KindAssistantBlock, Seq: 1, Prompt: "stray", Principal: "x", Content: "stray", State: "stray", Block: &Block{Type: "text"}}.API()
	if a.Prompt != "" || a.Content != "" || a.State != "" {
		t.Errorf("cross-kind field leaked: %+v", a)
	}
	if u := (Record{Kind: "future_kind", Seq: 2, Prompt: "p", Content: "c"}).API(); u.Prompt != "" || u.Content != "" || u.Kind != "future_kind" {
		t.Errorf("an unknown kind must expose no payload: %+v", u)
	}
}

// Read loads at most MaxReadBytes, newest segments first, and says so.
func TestReadIsBounded(t *testing.T) {
	s, dir := newStore(t, func(c *StoreConfig) {
		c.MaxSegmentBytes = 600
		c.MaxReadBytes = 1500
		c.MaxRecordBytes = 400
		c.MaxTurnBytes = 1 << 20
	})
	for i := 0; i < 30; i++ {
		beginTurn(t, s, "e"+strings.Repeat("x", i%5)+string(rune('a'+i%26)))
		_ = s.EndTurn(testUUID, "e"+strings.Repeat("x", i%5)+string(rune('a'+i%26)), TurnEnd{State: "completed", Row: "3", ExitClass: "zero"})
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))
	if len(segs) < 5 {
		t.Fatalf("need several segments, got %d", len(segs))
	}
	res, err := s.Read(testUUID)
	if err != nil || !res.Partial || res.Corrupt {
		t.Fatalf("partial=%v corrupt=%v err=%v", res.Partial, res.Corrupt, err)
	}
	var total int
	for _, r := range res.Records {
		b, _ := json.Marshal(r)
		total += len(b) + 1
	}
	if total > 1500 {
		t.Fatalf("Read loaded %d bytes of records, bound is 1500", total)
	}
	if res.Records[len(res.Records)-1].Seq != 60 {
		t.Fatalf("the newest record must be present, got seq %d", res.Records[len(res.Records)-1].Seq)
	}
	for i := 1; i < len(res.Records); i++ {
		if res.Records[i].Seq != res.Records[i-1].Seq+1 {
			t.Fatalf("a partial read must be a contiguous suffix")
		}
	}
	// Restart recovery is not limited by the read bound: an open turn in an old segment is still found.
	s2, _ := OpenStore(dir, StoreConfig{MaxSegmentBytes: 600, MaxReadBytes: 1500, MaxRecordBytes: 400})
	defer s2.Close()
	if _, err := s2.RecoverOpenTurns(testUUID); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverFindsAnOpenTurnBeyondTheReadBound(t *testing.T) {
	s, dir := newStore(t, func(c *StoreConfig) { c.MaxSegmentBytes = 600; c.MaxReadBytes = 1200; c.MaxRecordBytes = 400 })
	beginTurn(t, s, "old-open")
	for i := 0; i < 25; i++ {
		id := "d" + string(rune('a'+i))
		beginTurn(t, s, id)
		_ = s.EndTurn(testUUID, id, TurnEnd{State: "completed", Row: "3", ExitClass: "zero"})
	}
	s2, err := OpenStore(dir, StoreConfig{MaxSegmentBytes: 600, MaxReadBytes: 1200, MaxRecordBytes: 400})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	marked, err := s2.RecoverOpenTurns(testUUID)
	if err != nil || len(marked) != 1 || marked[0] != "old-open" {
		t.Fatalf("marked = %v, %v", marked, err)
	}
}

// The directory is opened once; replacing the path afterwards does not redirect writes.
func TestStoreStaysOnTheOpenedDirectory(t *testing.T) {
	s, dir := newStore(t, nil)
	beginTurn(t, s, "e1")
	moved := dir + ".moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(filepath.Dir(dir), "decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, dir); err != nil {
		t.Fatal(err)
	}
	if err := s.EndTurn(testUUID, "e1", TurnEnd{State: "completed", Row: "3", ExitClass: "zero"}); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(decoy); len(ents) != 0 {
		t.Fatalf("a write followed the swapped path into the decoy: %v", ents)
	}
	if ents, _ := os.ReadDir(moved); len(ents) == 0 {
		t.Fatal("the write did not reach the opened directory")
	}
	if res, err := s.Read(testUUID); err != nil || len(res.Records) != 2 {
		t.Fatalf("read after swap: %d records, %v", len(res.Records), err)
	}
}

func TestOpenStoreRefusesASymlinkAndABadMode(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	_ = os.Mkdir(real, 0o700)
	link := filepath.Join(base, "link")
	_ = os.Symlink(real, link)
	if _, err := OpenStore(link, DefaultStoreConfig()); err == nil {
		t.Error("a symlinked history dir must be refused")
	}
	loose := filepath.Join(base, "loose")
	_ = os.Mkdir(loose, 0o700)
	_ = os.Chmod(loose, 0o750)
	if _, err := OpenStore(loose, DefaultStoreConfig()); err == nil {
		t.Error("mode 0750 must be refused")
	}
	if s, err := OpenStore(real, DefaultStoreConfig()); err != nil {
		t.Errorf("a private real dir must open: %v", err)
	} else {
		s.Close()
		if _, err := s.Read(testUUID); err == nil {
			t.Error("a closed store must refuse reads")
		}
	}
}
