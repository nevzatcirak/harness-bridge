package execution

import (
	"encoding/json"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-018, accepted 2026-11-06: option (d) built on (a). After a native
// permission rejection the execution state stays as (a) decides it (unknown,
// no ended_at, guard held). A report-only permission_rejection block names the
// rejected tool call. It is never proof of an outcome: it never changes state,
// error, unresolved, ended_at, the guard or idempotency, and the supervisor must
// not act on it automatically.

// PermissionRejection is the report-only block on get_execution_status and
// get_execution_result.
type PermissionRejection struct {
	// ObservedAt is the Bridge time of the probe that produced this block.
	ObservedAt time.Time `json:"observed_at"`
	// Tool and ToolCallID name the rejected native tool part. No pattern,
	// metadata or native text is reported.
	Tool       string `json:"tool"`
	ToolCallID string `json:"tool_call_id"`
	// Basis is "native_event" when this process's permission observer saw the
	// verified ask and a native reject reply for that tool call (ADR-018 §3(b)
	// proof items 2-4), or "persisted_text_only" when only the persisted native
	// message shape holds (items 2-3). persisted_text_only is untrusted: the
	// same text can come from instance disposal or from any tool's error.
	Basis string `json:"basis"`
}

// RejectionWitness reports, from in-memory state only, whether a native
// permission reject reply was observed for the given tool call and attributed
// to the given execution (ADR-018 §3(b) item 4). It must perform no I/O and
// must answer false whenever its evidence is incomplete, gapped, fenced,
// conflicting or unattributed. A nil witness never yields native_event.
// It is called with run.mu held, so it must not call back into the Manager.
type RejectionWitness interface {
	VerifiedRejection(session, executionID, toolMessageID, toolCallID string) bool
}

// The opencode permission observer provides this view (in memory, no I/O).
var _ RejectionWitness = (*opencode.PermissionObserver)(nil)

type witnessBox struct{ w RejectionWitness }

// SetRejectionWitness installs (or with nil removes) the in-memory witness used
// for basis native_event. It adds no native reads.
func (m *Manager) SetRejectionWitness(w RejectionWitness) {
	if w == nil {
		m.witness.Store(nil)
		return
	}
	m.witness.Store(&witnessBox{w})
}

type rejectionWitness = atomic.Pointer[witnessBox]

// nativeRejectedText is OpenCode 1.19.4 PermissionV1.RejectedError's message
// (packages/core/src/v1/permission.ts:7-11), the only text the processor
// persists on the tool part (session/processor.ts:186-199). Byte-equal only:
// CorrectedError ("… with the following feedback: …") never matches.
const nativeRejectedText = "The user rejected permission to use this specific tool call."

var (
	safeTool   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	safeCallID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
)

// rejectedPart checks ADR-018 §3(b) items 2-3 on the correlated assistants
// of one probe: the latest assistant is completed with finish "tool-calls" and
// no error, no other assistant is as late or unfinished, and it has exactly one
// tool part in state "error" whose error is byte-equal to nativeRejectedText,
// with no pending or running tool part. Anything else, including unparseable
// or out-of-charset native values, yields ok false.
func rejectedPart(assistants []opencode.Message, unfinished bool) (messageID, tool, callID string, ok bool) {
	if unfinished || len(assistants) == 0 {
		return "", "", "", false
	}
	last := 0
	for i, a := range assistants {
		if a.Info.Time.Created > assistants[last].Info.Time.Created {
			last = i
		}
	}
	latest := assistants[last]
	for i, a := range assistants {
		if i != last && (a.Info.Time.Created >= latest.Info.Time.Created || a.Info.Time.Completed > latest.Info.Time.Completed) {
			return "", "", "", false
		}
	}
	if latest.Info.Time.Completed <= 0 || latest.Info.Finish != "tool-calls" || (len(latest.Info.Error) > 0 && string(latest.Info.Error) != "null") {
		return "", "", "", false
	}
	errored := 0
	for _, raw := range latest.Parts {
		var p struct {
			Type   string `json:"type"`
			Tool   string `json:"tool"`
			CallID string `json:"callID"`
			State  struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"state"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return "", "", "", false
		}
		if p.Type != "tool" {
			continue
		}
		switch p.State.Status {
		case "pending", "running":
			return "", "", "", false
		case "error":
			errored++
			if p.State.Error != nativeRejectedText || !safeTool.MatchString(p.Tool) || !safeCallID.MatchString(p.CallID) {
				return "", "", "", false
			}
			tool, callID = p.Tool, p.CallID
		}
	}
	if errored != 1 {
		return "", "", "", false
	}
	return latest.Info.ID, tool, callID, true
}

// assessRejection builds the report-only block from reads the probe already
// made plus the in-memory witness. Caller holds run.mu; it reads no native
// state and changes nothing but its return value.
func (m *Manager) assessRejection(x *run, assistants []opencode.Message, unfinished bool, now time.Time) *PermissionRejection {
	messageID, tool, callID, ok := rejectedPart(assistants, unfinished)
	if !ok {
		return nil
	}
	basis := "persisted_text_only"
	if b := m.witness.Load(); b != nil && b.w.VerifiedRejection(x.native, x.view.ID, messageID, callID) {
		basis = "native_event"
	}
	return &PermissionRejection{ObservedAt: now, Tool: tool, ToolCallID: callID, Basis: basis}
}

func copyRejection(r *PermissionRejection) *PermissionRejection {
	if r == nil {
		return nil
	}
	v := *r
	return &v
}
