package claudecode

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T, mod func(*StoreConfig)) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hist")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultStoreConfig()
	n := 0
	cfg.Now = func() time.Time { n++; return time.Date(2026, 11, 12, 10, 0, n, 0, time.UTC) }
	if mod != nil {
		mod(&cfg)
	}
	s, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func segName(uuid string, n int) string {
	h := sha256.Sum256([]byte("claudecode:" + uuid))
	return hex.EncodeToString(h[:]) + "-" + padSeg(n) + ".ndjson"
}

func padSeg(n int) string { return strings.Repeat("0", 6-len(itoa(n))) + itoa(n) }
func itoa(n int) string   { b, _ := json.Marshal(n); return string(b) }

func TestStoreWritesAndReadsATurn(t *testing.T) {
	s, dir := newStore(t, nil)
	if err := s.BeginTurn(testUUID, "exec-1", TurnStart{Prompt: "hello there", Principal: "operator", KeyHashPrefix: "ab12cd34"}); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, s, "exec-1", Record{Kind: KindAssistantBlock, Block: &Block{Type: "text", Text: "hi"}})
	mustAppend(t, s, "exec-1", Record{Kind: KindAssistantBlock, Block: &Block{Type: "tool_use", Name: "Read", Input: `{"file_path":"/r/a"}`}})
	mustAppend(t, s, "exec-1", Record{Kind: KindToolResult, ToolUseID: "toolu_1", Content: "file text"})
	if err := s.EndTurn(testUUID, "exec-1", TurnEnd{State: "completed", Row: "3", Subtype: "success", TerminalReason: "completed", NumTurns: 1, ExitClass: "zero", CostUSD: 0.09}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Read(testUUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 5 {
		t.Fatalf("records = %d", len(res.Records))
	}
	for i, r := range res.Records {
		if r.Seq != int64(i+1) || r.V != 1 || r.ExecutionID != "exec-1" || r.At.IsZero() {
			t.Fatalf("record %d = %+v", i, r)
		}
	}
	if res.Records[0].Kind != KindTurnStart || res.Records[0].Prompt != "hello there" || res.Records[4].Kind != KindTurnEnd || res.Records[4].State != "completed" {
		t.Fatalf("kinds: %+v / %+v", res.Records[0], res.Records[4])
	}
	// Directory layout: one entry per segment, named by the session hash.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != segName(testUUID, 1) {
		t.Fatalf("entries = %v, want %s", ents, segName(testUUID, 1))
	}
	st, _ := os.Stat(filepath.Join(dir, ents[0].Name()))
	if st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
		t.Fatalf("segment mode %v", st.Mode())
	}
	// One write per record, one JSON object per line, no prompt in the file name.
	f, _ := os.Open(filepath.Join(dir, ents[0].Name()))
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil || m["v"] == nil || m["seq"] == nil || m["kind"] == nil {
			t.Fatalf("bad line %q", sc.Text())
		}
		n++
	}
	if n != 5 {
		t.Fatalf("lines = %d", n)
	}
}

func mustAppend(t *testing.T, s *Store, exec string, r Record) {
	t.Helper()
	if err := s.Append(testUUID, exec, r); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRedactsBeforeTheFirstByte(t *testing.T) {
	s, dir := newStore(t, nil)
	secret := "sk-ant-api03-AbCdEfGhIjKlMnOp"
	if err := s.BeginTurn(testUUID, "e", TurnStart{Prompt: "use " + secret + " and BRIDGE_TOKEN=hunter2value", Principal: "op"}); err != nil {
		t.Fatal(err)
	}
	mustAppend(t, s, "e", Record{Kind: KindToolResult, ToolUseID: "t", Content: "Authorization: Bearer abcdef123456"})
	raw, _ := os.ReadFile(filepath.Join(dir, segName(testUUID, 1)))
	for _, bad := range []string{"AbCdEfGhIjKlMnOp", "hunter2value", "abcdef123456"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("%q reached the file", bad)
		}
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatal("no redaction marker")
	}
}

func TestStoreBounds(t *testing.T) {
	t.Run("a prompt over 32768 bytes is refused", func(t *testing.T) {
		s, _ := newStore(t, nil)
		if err := s.BeginTurn(testUUID, "e", TurnStart{Prompt: strings.Repeat("p", 32769)}); !errors.Is(err, ErrPromptTooLong) {
			t.Fatalf("err = %v", err)
		}
		if err := s.BeginTurn(testUUID, "e", TurnStart{Prompt: strings.Repeat("p", 32768)}); err != nil {
			t.Fatalf("a prompt at the limit: %v", err)
		}
	})
	t.Run("one record over 64 KiB becomes a truncated marker with the original size", func(t *testing.T) {
		s, _ := newStore(t, nil)
		s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"})
		mustAppend(t, s, "e", Record{Kind: KindToolResult, ToolUseID: "t", Content: strings.Repeat("x", 100000)})
		res, _ := s.Read(testUUID)
		last := res.Records[len(res.Records)-1]
		if last.Kind != KindTruncated || last.OriginalKind != KindToolResult || last.OriginalSize < 100000 || last.Content != "" {
			t.Fatalf("record = %+v", last)
		}
	})
	t.Run("a turn over its budget gets one truncated record, turn_end is still written", func(t *testing.T) {
		s, _ := newStore(t, func(c *StoreConfig) { c.MaxTurnBytes = 4096 })
		s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"})
		for i := 0; i < 20; i++ {
			mustAppend(t, s, "e", Record{Kind: KindToolResult, ToolUseID: "t", Content: strings.Repeat("y", 1000)})
		}
		if err := s.EndTurn(testUUID, "e", TurnEnd{State: "completed"}); err != nil {
			t.Fatal(err)
		}
		res, _ := s.Read(testUUID)
		trunc, results := 0, 0
		for _, r := range res.Records {
			switch r.Kind {
			case KindTruncated:
				trunc++
			case KindToolResult:
				results++
			}
		}
		if trunc != 1 || results >= 20 || res.Records[len(res.Records)-1].Kind != KindTurnEnd {
			t.Fatalf("trunc=%d results=%d last=%s", trunc, results, res.Records[len(res.Records)-1].Kind)
		}
	})
	t.Run("a full segment rolls to the next, never rewrites", func(t *testing.T) {
		s, dir := newStore(t, func(c *StoreConfig) { c.MaxSegmentBytes = 2048 })
		s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"})
		for i := 0; i < 12; i++ {
			mustAppend(t, s, "e", Record{Kind: KindToolResult, ToolUseID: "t", Content: strings.Repeat("z", 500)})
		}
		ents, _ := os.ReadDir(dir)
		if len(ents) < 3 {
			t.Fatalf("segments = %d", len(ents))
		}
		for _, e := range ents {
			if fi, _ := e.Info(); fi.Size() > 2048+700 {
				t.Fatalf("segment %s is %d bytes", e.Name(), fi.Size())
			}
		}
		res, err := s.Read(testUUID)
		if err != nil || len(res.Records) != 13 {
			t.Fatalf("read across segments: %d %v", len(res.Records), err)
		}
		for i, r := range res.Records {
			if r.Seq != int64(i+1) {
				t.Fatalf("seq %d at %d", r.Seq, i)
			}
		}
	})
	t.Run("a session at its total budget refuses NEW executions, keeps old records", func(t *testing.T) {
		s, _ := newStore(t, func(c *StoreConfig) { c.MaxSessionBytes = 3000; c.MaxSegmentBytes = 1500 })
		s.BeginTurn(testUUID, "e1", TurnStart{Prompt: "p"})
		for i := 0; i < 10; i++ {
			mustAppend(t, s, "e1", Record{Kind: KindToolResult, ToolUseID: "t", Content: strings.Repeat("q", 400)})
		}
		s.EndTurn(testUUID, "e1", TurnEnd{State: "completed"})
		before, _ := s.Read(testUUID)
		if err := s.BeginTurn(testUUID, "e2", TurnStart{Prompt: "again"}); !errors.Is(err, ErrSessionHistoryFull) {
			t.Fatalf("err = %v", err)
		}
		after, _ := s.Read(testUUID)
		if len(after.Records) != len(before.Records) {
			t.Fatal("refusing a new turn changed the history")
		}
		// Another session is unaffected.
		if err := s.BeginTurn("00000000-0000-4000-8000-000000000001", "e3", TurnStart{Prompt: "x"}); err != nil {
			t.Fatalf("other session: %v", err)
		}
	})
}

func TestStoreReaderTornTailAndCorruption(t *testing.T) {
	s, dir := newStore(t, nil)
	s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"})
	mustAppend(t, s, "e", Record{Kind: KindAssistantBlock, Block: &Block{Type: "text", Text: "a"}})
	seg := filepath.Join(dir, segName(testUUID, 1))
	// A torn final line (crash mid-write) is ignored.
	f, _ := os.OpenFile(seg, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"v":1,"seq":3,"kind":"assistant_blo`)
	f.Close()
	res, err := s.Read(testUUID)
	if err != nil || len(res.Records) != 2 || res.Corrupt {
		t.Fatalf("torn tail: %d records corrupt=%v err=%v", len(res.Records), res.Corrupt, err)
	}
	// A torn line in the MIDDLE is reported, and reading stops there.
	raw, _ := os.ReadFile(seg)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	bad := lines[0] + "\n" + `{"v":1,"seq":2,"kind":"assist` + "\n" + lines[1] + "\n"
	os.WriteFile(seg, []byte(bad), 0o600)
	res, err = s.Read(testUUID)
	if !errors.Is(err, ErrHistoryCorrupt) || !res.Corrupt || len(res.Records) != 1 {
		t.Fatalf("mid-file corruption: %d records corrupt=%v err=%v", len(res.Records), res.Corrupt, err)
	}
	// A sequence gap is detectable and reported.
	os.WriteFile(seg, []byte(lines[0]+"\n"+strings.Replace(lines[1], `"seq":2`, `"seq":5`, 1)+"\n"), 0o600)
	res, err = s.Read(testUUID)
	if !errors.Is(err, ErrHistoryCorrupt) {
		t.Fatalf("seq gap: %v", err)
	}
}

func TestStoreRestartGap(t *testing.T) {
	s, dir := newStore(t, nil)
	s.BeginTurn(testUUID, "exec-open", TurnStart{Prompt: "p"})
	mustAppend(t, s, "exec-open", Record{Kind: KindAssistantBlock, Block: &Block{Type: "text", Text: "partial"}})
	s.BeginTurn(testUUID, "exec-done", TurnStart{Prompt: "q"})
	s.EndTurn(testUUID, "exec-done", TurnEnd{State: "completed"})
	// Restart: a new Store on the same dir.
	cfg := DefaultStoreConfig()
	s2, err := OpenStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	gaps, err := s2.RecoverOpenTurns(testUUID)
	if err != nil || len(gaps) != 1 || gaps[0] != "exec-open" {
		t.Fatalf("gaps = %v %v", gaps, err)
	}
	res, _ := s2.Read(testUUID)
	last := res.Records[len(res.Records)-1]
	if last.Kind != KindRestartGap || last.ExecutionID != "exec-open" {
		t.Fatalf("last = %+v", last)
	}
	// Seq continues strictly increasing after a restart, and a second recovery is a no-op.
	if err := s2.BeginTurn(testUUID, "exec-new", TurnStart{Prompt: "r"}); err != nil {
		t.Fatal(err)
	}
	res, _ = s2.Read(testUUID)
	for i, r := range res.Records {
		if r.Seq != int64(i+1) {
			t.Fatalf("seq %d at %d after restart", r.Seq, i)
		}
	}
	if err := s2.EndTurn(testUUID, "exec-new", TurnEnd{State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if gaps, _ := s2.RecoverOpenTurns(testUUID); len(gaps) != 0 {
		t.Fatalf("recovery is not idempotent: %v", gaps)
	}
}

func TestStoreRefusesUnsafeDirectoryAndFiles(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "h")
	os.Mkdir(dir, 0o755)
	if _, err := OpenStore(dir, DefaultStoreConfig()); err == nil {
		t.Fatal("a 0755 history dir was accepted")
	}
	os.Chmod(dir, 0o700)
	s, err := OpenStore(dir, DefaultStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	// A pre-planted symlink at the segment name is never followed.
	target := filepath.Join(base, "victim")
	os.WriteFile(target, nil, 0o600)
	os.Symlink(target, filepath.Join(dir, segName(testUUID, 1)))
	if err := s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"}); err == nil {
		t.Fatal("wrote through a symlink")
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Fatalf("victim modified: %q", b)
	}
	os.Remove(filepath.Join(dir, segName(testUUID, 1)))
	// A hard-linked or wrongly-moded segment is refused.
	p := filepath.Join(dir, segName(testUUID, 1))
	os.WriteFile(p, nil, 0o644)
	if err := s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"}); err == nil {
		t.Fatal("a 0644 segment was accepted")
	}
	os.Remove(p)
	os.WriteFile(p, nil, 0o600)
	os.Link(p, filepath.Join(base, "alias"))
	if err := s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"}); err == nil {
		t.Fatal("a hard-linked segment was accepted")
	}
	// Bad session ids never reach the file system.
	if err := s.BeginTurn("../../etc/passwd", "e", TurnStart{Prompt: "p"}); err == nil {
		t.Fatal("bad session id accepted")
	}
}

func TestStoreConcurrentAppendsKeepSeqStrict(t *testing.T) {
	s, _ := newStore(t, nil)
	s.BeginTurn(testUUID, "e", TurnStart{Prompt: "p"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := s.Append(testUUID, "e", Record{Kind: KindAssistantBlock, Block: &Block{Type: "text", Text: "t"}}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	res, err := s.Read(testUUID)
	if err != nil || len(res.Records) != 161 {
		t.Fatalf("%d %v", len(res.Records), err)
	}
	for i, r := range res.Records {
		if r.Seq != int64(i+1) {
			t.Fatalf("seq %d at %d", r.Seq, i)
		}
	}
}
