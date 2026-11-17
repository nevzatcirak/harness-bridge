//go:build linux

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// ADR021-OBSERVER (#372): the history check of ADR-021 §8.3 ("the decoy content appears nowhere in the
// stream, the history record or the result"). The history record is the Bridge's own (ADR-021 §5: the
// only readable history is built from the stream-json events of runs the Bridge starts; nothing reads
// the CLI's own transcript files). The tap writes the DENIED runs' streams through the Bridge's store
// into a dedicated 0700 directory and reads them back through the store's no-follow read path.

const histUUID = "11111111-2222-4333-8444-555555555555"

func histLine(m map[string]any) string { b, _ := json.Marshal(m); return string(b) + "\n" }

func deniedStream(session, text string) []byte {
	var b strings.Builder
	b.WriteString(histLine(map[string]any{"type": "system", "subtype": "init", "session_id": session, "cwd": "/srv/ws", "permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.325", "mcp_servers": []any{}, "plugins": []any{}, "tools": []any{"Read", "Glob", "Grep"}}))
	b.WriteString(histLine(map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": text},
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{"file_path": "/srv/runhome/.claude/.credentials.decoy.json"}}}}}))
	b.WriteString(histLine(map[string]any{"type": "user", "session_id": session, "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "permission denied by rule"}}}}))
	b.WriteString(histLine(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "num_turns": 2, "terminal_reason": "completed", "permission_denials": []any{}}))
	return []byte(b.String())
}

func histDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(realDir(t), "hist")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHistoryTapRecordsTheDeniedStreamAndReadsItBack(t *testing.T) {
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	if err := tap.Record(deniedStream(histUUID, "VISIBLE-TEXT")); err != nil {
		t.Fatal(err)
	}
	rec, ok := tap.Read()
	if !ok || !bytes.Contains(rec, []byte("VISIBLE-TEXT")) || !bytes.Contains(rec, []byte("permission denied by rule")) {
		t.Fatalf("history not read back: ok=%v %q", ok, rec)
	}
}

// A decoy marker that reached the stream is in the history record: that is what the verdict compares.
func TestHistoryTapShowsALeakedMarker(t *testing.T) {
	marker, err := claudecode.NewCanaryMarker(bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	if err := tap.Record(deniedStream(histUUID, "the file says "+marker)); err != nil {
		t.Fatal(err)
	}
	rec, ok := tap.Read()
	if !ok || !bytes.Contains(rec, []byte(marker)) {
		t.Fatalf("a marker in the stream is not visible in the history: ok=%v", ok)
	}
}

func TestHistoryTapWithNothingRecordedIsUnobserved(t *testing.T) {
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	if rec, ok := tap.Read(); ok {
		t.Fatalf("an empty history was reported as an observation: %q", rec)
	}
}

func TestHistoryTapIsUnobservedOnAnyFailure(t *testing.T) {
	// no session id in the stream: cannot be attributed
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := tap.Record(deniedStream("not-a-uuid", "x")); err == nil {
		t.Fatal("a stream without a valid session id was recorded")
	}
	if _, ok := tap.Read(); ok {
		t.Fatal("a failed record still reads as observed")
	}
	tap.Close()

	// the store refuses (session total at its bound)
	cfg := claudecode.DefaultStoreConfig()
	cfg.MaxSessionBytes = 1
	tap2, err := NewHistoryTapWith(histDir(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tap2.Close()
	_ = tap2.Record(deniedStream(histUUID, "a"))
	if err := tap2.Record(deniedStream(histUUID, "b")); err == nil {
		t.Fatal("a full session was recorded again")
	}
	if _, ok := tap2.Read(); ok {
		t.Fatal("a refused record still reads as observed")
	}

	// a segment replaced by a symlink is refused by the store's no-follow read
	dir := histDir(t)
	tap3, err := NewHistoryTap(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tap3.Close()
	if err := tap3.Record(deniedStream(histUUID, "c")); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("segments: %v", ents)
	}
	seg := filepath.Join(dir, ents[0].Name())
	victim := filepath.Join(realDir(t), "victim.ndjson")
	if err := os.WriteFile(victim, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(seg); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, seg); err != nil {
		t.Fatal(err)
	}
	if rec, ok := tap3.Read(); ok {
		t.Fatalf("a symlinked segment was read: %q", rec)
	}

	// a corrupt segment is unobserved, never a partial "clean" history
	dir4 := histDir(t)
	tap4, err := NewHistoryTap(dir4)
	if err != nil {
		t.Fatal(err)
	}
	defer tap4.Close()
	if err := tap4.Record(deniedStream(histUUID, "d")); err != nil {
		t.Fatal(err)
	}
	ents4, _ := os.ReadDir(dir4)
	f, err := os.OpenFile(filepath.Join(dir4, ents4[0].Name()), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("this is not json\n{\"v\":1}\n")
	f.Close()
	if rec, ok := tap4.Read(); ok {
		t.Fatalf("a corrupt history reads as observed: %q", rec)
	}
}

// The store opens the directory once, without following a symlink, and every later operation is
// relative to that descriptor: nothing is written through a path swapped in afterwards.
func TestHistoryTapNeverFollowsAPath(t *testing.T) {
	root := realDir(t)
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHistoryTap(link); err == nil {
		t.Error("a symlinked history directory was accepted")
	}
	loose := filepath.Join(root, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHistoryTap(loose); err == nil {
		t.Error("a history directory with mode 0755 was accepted")
	}
	if _, err := NewHistoryTap("relative/dir"); err == nil {
		t.Error("a relative history directory was accepted")
	}
	// swap after open
	dir := histDir(t)
	tap, err := NewHistoryTap(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	other := filepath.Join(realDir(t), "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	swapForSymlink(t, dir, other)
	if err := tap.Record(deniedStream(histUUID, "SWAPPED")); err != nil {
		t.Fatalf("Record after a path swap: %v", err)
	}
	if es, _ := os.ReadDir(other); len(es) != 0 {
		t.Fatalf("the history was written through the swapped symlink: %v", es)
	}
	if rec, ok := tap.Read(); !ok || !bytes.Contains(rec, []byte("SWAPPED")) {
		t.Fatalf("the original directory was not used: ok=%v", ok)
	}
}

func TestHistoryTapBoundsAnOversizedStream(t *testing.T) {
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	var b bytes.Buffer
	b.Write(deniedStream(histUUID, strings.Repeat("A", 200<<10)))
	for i := 0; i < 5000; i++ {
		b.WriteString(histLine(map[string]any{"type": "assistant", "session_id": histUUID, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("B", 1024) + fmt.Sprint(i)}}}}))
	}
	if err := tap.Record(b.Bytes()); err != nil {
		t.Fatalf("an oversized stream: %v", err)
	}
	rec, ok := tap.Read()
	if !ok {
		t.Fatal("an oversized stream made the history unobserved")
	}
	if len(rec) > 8<<20 {
		t.Fatalf("the history record is not bounded: %d bytes", len(rec))
	}
	if !bytes.Contains(rec, []byte("truncated")) {
		t.Fatal("the bound left no truncated marker")
	}
}

type cannedRunner struct {
	out   []byte
	err   error
	calls []string
}

func (c *cannedRunner) Run(_ context.Context, inv Invocation) (Result, error) {
	c.calls = append(c.calls, inv.Label)
	if c.err != nil {
		return Result{}, c.err
	}
	return Result{Stdout: c.out, ExitCode: 0, InitAt: -1, ResultAt: -1, ExitAt: -1}, nil
}

func TestRecordingRunnerRecordsOnlyTheDeniedRuns(t *testing.T) {
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	inner := &cannedRunner{out: deniedStream(histUUID, "FROM-RUN")}
	rr := RecordingRunner{Inner: inner, Tap: tap}
	for _, label := range []string{"bootstrap", "canary_control_read_decoy", "lag", "canary_denied_read_decoy"} {
		res, err := rr.Run(context.Background(), Invocation{Label: label})
		if err != nil || !bytes.Equal(res.Stdout, inner.out) {
			t.Fatalf("%s: result changed or error: %v", label, err)
		}
	}
	rec, ok := tap.Read()
	if !ok || bytes.Count(rec, []byte("FROM-RUN")) != 1 {
		t.Fatalf("exactly the one denied run must be recorded: ok=%v count=%d", ok, bytes.Count(rec, []byte("FROM-RUN")))
	}
	// an inner error is passed through and nothing is recorded for it
	bad := RecordingRunner{Inner: &cannedRunner{err: errors.New("boom")}, Tap: tap}
	if _, err := bad.Run(context.Background(), Invocation{Label: "canary_denied_x"}); err == nil {
		t.Fatal("the inner error was swallowed")
	}
	// a nil tap changes nothing
	if _, err := (RecordingRunner{Inner: inner}).Run(context.Background(), Invocation{Label: "canary_denied_x"}); err != nil {
		t.Fatal(err)
	}
}

// A recording failure never fails the run (the verdict logic still has the stream) but makes the
// history unobserved.
func TestRecordingRunnerFailureMakesTheHistoryUnobserved(t *testing.T) {
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	rr := RecordingRunner{Inner: &cannedRunner{out: deniedStream("not-a-uuid", "x")}, Tap: tap}
	if _, err := rr.Run(context.Background(), Invocation{Label: "canary_denied_read_decoy"}); err != nil {
		t.Fatalf("a recording failure failed the run: %v", err)
	}
	if _, ok := tap.Read(); ok {
		t.Fatal("a failed recording still reads as observed")
	}
}

// A history the store could only read in part (older segments left out at its read bound) is not an
// observation: the decoy marker could sit in the part that was not read.
func TestHistoryTapIsUnobservedWhenTheStoreReadsOnlyPartOfIt(t *testing.T) {
	cfg := claudecode.DefaultStoreConfig()
	cfg.MaxSegmentBytes = 2000
	cfg.MaxReadBytes = 3000
	tap, err := NewHistoryTapWith(histDir(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tap.Close()
	for i := 0; i < 40; i++ {
		if err := tap.Record(deniedStream(histUUID, fmt.Sprintf("run %d", i))); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if rec, ok := tap.Read(); ok {
		t.Fatalf("a partial read was reported as an observation (%d bytes)", len(rec))
	}
}
