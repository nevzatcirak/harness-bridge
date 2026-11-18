package acceptance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// HistoryTap is the history check of ADR-021 §8.3 ("the decoy content appears nowhere in the stream, the
// history record or the result"). The history record is the BRIDGE's own (ADR-021 §5, D7: the only
// readable history is built from the stream-json events of runs the Bridge starts; nothing reads the
// CLI's own transcript files). The driver runs the CLI directly, so the tap does what the adapter's
// runner does for a run: it writes the DENIED probe runs' streams through the Bridge's history store
// (blocks and tool results redacted and bounded by the store, one turn per run) into a dedicated
// directory, and Read returns what the store reads back.
//
// The directory is opened by the store once, without following a symlink, and must already be absolute,
// owned by the process, mode exactly 0700 with a safe ancestor chain (claudecode.ValidateHistoryDir);
// every later operation is relative to that descriptor, so nothing is read or written outside it and a
// path swapped in afterwards is never followed. Use a throwaway directory, never BRIDGE_CLAUDE_HISTORY_DIR.
//
// Honesty: this history is derived from the same stream the verdict already scans, after redaction and
// truncation; it is not an independent observation of the CLI's transcript (which ADR-021 §5 declares
// unsupported). It is observed only when something was recorded and every store operation succeeded.
type HistoryTap struct {
	mu       sync.Mutex
	store    *claudecode.Store
	sessions []string
	runs     int
	failed   bool
}

// NewHistoryTap opens the store in dir with the ADR defaults.
func NewHistoryTap(dir string) (*HistoryTap, error) {
	return NewHistoryTapWith(dir, claudecode.DefaultStoreConfig())
}

// NewHistoryTapWith opens the store with explicit bounds (tests).
func NewHistoryTapWith(dir string, cfg claudecode.StoreConfig) (*HistoryTap, error) {
	st, err := claudecode.OpenStore(dir, cfg)
	if err != nil {
		return nil, err
	}
	return &HistoryTap{store: st}, nil
}

func execID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Record writes one run's stdout as one turn of its session. Any failure marks the history
// unobserved for good: a history that missed a run proves nothing.
func (h *HistoryTap) Record(stdout []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.record(stdout); err != nil {
		h.failed = true
		return err
	}
	return nil
}

func (h *HistoryTap) record(stdout []byte) error {
	if h.store == nil {
		return errors.New("acceptance: the history tap is closed")
	}
	dec := claudecode.NewDecoder(bytes.NewReader(stdout), claudecode.Limits{})
	var evs []claudecode.Event
	for {
		ev, err := dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		evs = append(evs, ev)
	}
	uuid := ""
	for _, ev := range evs {
		if claudecode.ValidUUID(ev.SessionID) {
			uuid = ev.SessionID
			break
		}
	}
	if uuid == "" {
		return errors.New("acceptance: the stream names no valid session id")
	}
	exec, err := execID()
	if err != nil {
		return err
	}
	if err := h.store.BeginTurn(uuid, exec, claudecode.TurnStart{Prompt: "acceptance denied run", Principal: "acceptance"}); err != nil {
		return err
	}
	end := claudecode.TurnEnd{State: "unknown", Row: "10", ExitClass: "unknown"}
	for _, ev := range evs {
		if ev.SessionID != uuid {
			continue
		}
		for _, b := range ev.Blocks {
			b := b
			if err := h.store.Append(uuid, exec, claudecode.Record{Kind: claudecode.KindAssistantBlock, Block: &b}); err != nil {
				return err
			}
		}
		for _, tr := range ev.ToolResults {
			if err := h.store.Append(uuid, exec, claudecode.Record{Kind: claudecode.KindToolResult, ToolUseID: tr.ToolUseID, Content: tr.Content}); err != nil {
				return err
			}
		}
		if ev.Result != nil {
			end.Subtype, end.TerminalReason, end.CostUSD = ev.Result.Subtype, ev.Result.TerminalReason, ev.Result.CostUSD
			if ev.Result.NumTurns != nil {
				end.NumTurns = *ev.Result.NumTurns
			}
			end.Denials = len(ev.Result.Denials)
		}
	}
	if err := h.store.EndTurn(uuid, exec, end); err != nil {
		return err // any refusal (an invalid turn end included) leaves the run unrecorded: unobserved
	}
	seen := false
	for _, s := range h.sessions {
		seen = seen || s == uuid
	}
	if !seen {
		h.sessions = append(h.sessions, uuid)
	}
	h.runs++
	return nil
}

// Read returns the recorded history (one JSON record per line, as the store holds it, redacted and
// bounded) and whether it is an observation: nothing recorded, a failed record, a corrupt or partial
// read, or any store error is unobserved.
func (h *HistoryTap) Read() ([]byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed || h.runs == 0 || h.store == nil {
		return nil, false
	}
	var out bytes.Buffer
	for _, uuid := range h.sessions {
		res, err := h.store.Read(uuid)
		if err != nil || res.Corrupt || res.Partial || len(res.Records) == 0 {
			return nil, false // a recorded session that reads back empty (segments removed) is no observation
		}
		for _, r := range res.Records {
			b, err := json.Marshal(r)
			if err != nil {
				return nil, false
			}
			out.Write(b)
			out.WriteByte('\n')
		}
	}
	if out.Len() == 0 {
		return nil, false
	}
	return out.Bytes(), true
}

// Close releases the store's directory descriptor.
func (h *HistoryTap) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.store == nil {
		return nil
	}
	err := h.store.Close()
	h.store = nil
	return err
}

// RecordingRunner tees the stdout of the DENIED probe runs (labels starting `canary_denied`) into a
// HistoryTap. The result is passed through untouched; a recording failure never fails the run (the
// verdict logic still has the stream) but makes the history unobserved.
type RecordingRunner struct {
	Inner Runner
	Tap   *HistoryTap
}

// Run implements Runner.
func (r RecordingRunner) Run(ctx context.Context, inv Invocation) (Result, error) {
	res, err := r.Inner.Run(ctx, inv)
	if err != nil {
		return res, err
	}
	if r.Tap != nil && strings.HasPrefix(inv.Label, "canary_denied") {
		_ = r.Tap.Record(res.Stdout)
	}
	return res, nil
}
