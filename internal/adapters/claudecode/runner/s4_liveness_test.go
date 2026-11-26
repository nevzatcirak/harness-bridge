package runner

// ADR-017 A2 S4: liveness on a real Executor over fabricated Claude streams.
// No live CLI, no host action.

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
	"github.com/nevzatcirak/cirak/internal/execution"
)

type s4Clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *s4Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *s4Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newS4Harness(t *testing.T, clk *s4Clock, mod func(*Config)) *harness {
	t.Helper()
	return newHarness(t, func(c *Config, _ *claudecode.StoreConfig) {
		c.Now = clk.Now
		c.NoProgressS = 120
		c.ToolLongS = 600
		c.RunMax = 10 * time.Minute
		if mod != nil {
			mod(c)
		}
	})
}

func s4Get(t *testing.T, h *harness, id string) execution.Execution {
	t.Helper()
	x, err := h.ex.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func s4Until(t *testing.T, h *harness, id, what string, ok func(execution.Execution) bool) execution.Execution {
	t.Helper()
	var last execution.Execution
	until(t, what, func() bool {
		x, err := h.ex.Get(id)
		if err != nil {
			return false
		}
		last = x
		return ok(x)
	})
	return last
}

func s4ToolUse(n int, id, name string) string {
	return js(map[string]any{"type": "assistant", "session_id": uuidN(n), "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{"command": "ls"}},
	}}})
}

func s4ToolResult(n int, id string) string {
	return js(map[string]any{"type": "user", "session_id": uuidN(n), "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"},
	}}})
}

func s4Retry(n int, attempt int, maxRetries int, delayMS int, status any, enum string) string {
	m := map[string]any{"type": "system", "subtype": "api_retry", "session_id": uuidN(n), "attempt": attempt, "max_retries": maxRetries,
		"retry_delay_ms": delayMS, "error": enum}
	if status != nil {
		m["error_status"] = status
	}
	return js(m)
}

// A running tool is exported, never suppresses NO_PROGRESS, gains the
// explanation at tool_long_s, and a matching tool_result clears it; a terminal
// verdict clears the block. State and guard are untouched by the report.
func TestS4ClaudeBusyToolNoProgressAndExplanation(t *testing.T) {
	clk := &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}
	h := newS4Harness(t, clk, nil)
	x := h.mustSend(1, "do the thing", "key-s4-tool-0001")
	c := h.next(1)
	c.emit(h.initLine(1))
	got := s4Until(t, h, x.ID, "running liveness", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.NativeStatus == "busy"
	})
	if got.Liveness.ProgressSource != "bridge" || got.Liveness.Thresholds == nil ||
		got.Liveness.Thresholds.NoProgressS != 120 || got.Liveness.Thresholds.ToolLongS != 600 {
		t.Fatalf("block %+v", got.Liveness)
	}
	c.emit(s4ToolUse(1, "toolu_01", "Bash"))
	got = s4Until(t, h, x.ID, "tool object", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.Tool != nil
	})
	if got.Liveness.Tool.ToolName != "Bash" || got.Liveness.StallSuspected {
		t.Fatalf("fresh tool %+v", got.Liveness)
	}
	// 61 s: not yet. 121 s: A1's NO_PROGRESS, never suppressed by the tool.
	clk.Advance(61 * time.Second)
	if got := s4Get(t, h, x.ID); got.Liveness.StallSuspected {
		t.Fatalf("suspected at 61 s: %+v", got.Liveness)
	}
	clk.Advance(60 * time.Second)
	got = s4Until(t, h, x.ID, "NO_PROGRESS with a tool", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.StallSuspected && x.Liveness.StallReason == "NO_PROGRESS"
	})
	if got.Liveness.Tool == nil || got.Liveness.Explanation != "" {
		t.Fatalf("tool/explanation %+v", got.Liveness)
	}
	clk.Advance(479 * time.Second) // 600 s from the tool start
	got = s4Until(t, h, x.ID, "TOOL_RUNNING_LONG", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.Explanation == "TOOL_RUNNING_LONG"
	})
	if !got.Liveness.StallSuspected || got.Liveness.Tool == nil {
		t.Fatalf("explanation %+v", got.Liveness)
	}
	c.emit(s4ToolResult(1, "toolu_01"))
	got = s4Until(t, h, x.ID, "tool cleared", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.Tool == nil && !x.Liveness.StallSuspected
	})
	if got.Liveness.Explanation != "" || got.State != "running" {
		t.Fatalf("after tool_result %+v", got)
	}
	if held, _ := h.ex.SessionGuard(h.sessions[0].ID); !held {
		t.Fatal("the guard was released by liveness")
	}
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if x.State != "completed" || x.Liveness != nil {
		t.Fatalf("terminal view %+v", x)
	}
	h.sp.mu.Lock()
	defer h.sp.mu.Unlock()
	if len(h.sp.specs) != 1 {
		t.Fatalf("child starts = %d", len(h.sp.specs))
	}
}

// api_retry is degradation, not a stall while healthy; a later event clears it;
// a wedged retry is NO_PROGRESS. The cause comes from the structured fields.
func TestS4ClaudeAPIRetry(t *testing.T) {
	clk := &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}
	h := newS4Harness(t, clk, nil)
	x := h.mustSend(1, "do the thing", "key-s4-retry-0001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(s4Retry(1, 3, 5, int((5 * time.Minute).Milliseconds()), 429, "rate_limit"))
	got := s4Until(t, h, x.ID, "provider retrying", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.DegradationReason != ""
	})
	l := got.Liveness
	if l.DegradationReason != "PROVIDER_RETRYING" || l.StallSuspected || l.Degraded != true {
		t.Fatalf("healthy retry %+v", l)
	}
	if l.RetryAttempt == nil || *l.RetryAttempt != 3 || l.RetryMax == nil || *l.RetryMax != 5 ||
		l.CauseHint != "rate_limit" || l.CauseSource != "structured" || l.RetryNextAt == nil {
		t.Fatalf("retry fields %+v", l)
	}
	if got.RetryAttemptSince == nil {
		t.Fatal("retry_attempt_since not exposed")
	}
	// A later assistant event is progress and clears the retry.
	c.emit(h.assistantLine(1, "working"))
	got = s4Until(t, h, x.ID, "retry cleared", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.DegradationReason == "" && x.RetryAttemptSince == nil
	})
	// A wedged retry: a young attempt whose next is 10 s past but no progress
	// past no_progress_s is NO_PROGRESS and not overdue.
	clk.Advance(111 * time.Second)
	c.emit(s4Retry(1, 4, 5, 0, 500, "server_error"))
	// Wait until the retry event was processed before advancing: a snapshot can
	// legitimately see the prior event while the line is in flight.
	_ = s4Until(t, h, x.ID, "retry event processed", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.DegradationReason == "PROVIDER_RETRYING"
	})
	clk.Advance(10 * time.Second)
	got = s4Until(t, h, x.ID, "wedged retry", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.StallReason == "NO_PROGRESS" && x.Liveness.DegradationReason != ""
	})
	if got.Liveness.DegradationReason != "PROVIDER_RETRYING" || got.Liveness.RetryOverdue {
		t.Fatalf("wedged retry %+v", got.Liveness)
	}
	c.emit(s4ToolResult(1, "toolu_none"))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if x.Liveness != nil {
		t.Fatalf("terminal kept liveness %+v", x.Liveness)
	}
}

// A raw stdout read error while the child is alive is the only Claude class
// that counts; 120 s makes HARNESS_UNREACHABLE, and the terminal verdict clears
// it. A decoder bound is unavailable but never counts.
func TestS4ClaudeReaderFailure(t *testing.T) {
	clk := &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}
	h := newS4Harness(t, clk, nil)
	x := h.mustSend(1, "do the thing", "key-s4-reader-0001")
	c := h.next(1)
	c.emit(h.initLine(1))
	_ = s4Until(t, h, x.ID, "running liveness", func(x execution.Execution) bool { return x.Liveness != nil })
	_ = c.stdoutW.CloseWithError(errors.New("stdout read failed"))
	got := s4Until(t, h, x.ID, "native unavailable", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.NativeStatus == "unavailable"
	})
	if got.Liveness.StallSuspected || got.UnavailableSince == nil {
		t.Fatalf("reader failure %+v since=%v", got.Liveness, got.UnavailableSince)
	}
	clk.Advance(60 * time.Second)
	if got := s4Get(t, h, x.ID); got.Liveness.StallSuspected || got.UnavailableSince == nil {
		t.Fatalf("at 60 s %+v", got.Liveness)
	}
	clk.Advance(60 * time.Second)
	got = s4Until(t, h, x.ID, "HARNESS_UNREACHABLE", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.StallReason == "HARNESS_UNREACHABLE"
	})
	if !got.Liveness.StallSuspected {
		t.Fatalf("unreachable %+v", got.Liveness)
	}
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if x.Liveness != nil || x.UnavailableSince != nil {
		t.Fatalf("terminal kept the failure memory: %+v %v", x.Liveness, x.UnavailableSince)
	}

	// A decoder bound is a reader failure but never counts.
	h2 := newS4Harness(t, &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}, nil)
	x2 := h2.mustSend(1, "do the thing", "key-s4-bound-0001")
	c2 := h2.next(1)
	c2.emit(h2.initLine(1))
	_ = s4Until(t, h2, x2.ID, "running liveness", func(x execution.Execution) bool { return x.Liveness != nil })
	c2.emitRaw(strings.Repeat("x", (1<<20)+10) + "\n")
	got = s4Until(t, h2, x2.ID, "bound unavailable", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.NativeStatus == "unavailable"
	})
	if got.UnavailableSince != nil || got.Liveness.StallSuspected {
		t.Fatalf("decoder bound counted: %+v %v", got.Liveness, got.UnavailableSince)
	}
	c2.finish(Exit{Code: 0})
	_ = h2.wait(x2.ID)
}

// A result is terminal evidence: the block is cleared at once, before the
// verdict waits for EOF and the exit.
func TestS4ClaudeResultClearsBeforeExit(t *testing.T) {
	clk := &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}
	h := newS4Harness(t, clk, nil)
	x := h.mustSend(1, "do the thing", "key-s4-result-0001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(s4ToolUse(1, "toolu_01", "Bash"))
	_ = s4Until(t, h, x.ID, "running liveness", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.Tool != nil
	})
	c.emit(h.resultLine(1, nil))
	got := s4Until(t, h, x.ID, "result cleared", func(x execution.Execution) bool { return x.Liveness == nil })
	if got.State == "unknown" || got.State == "failed" {
		t.Fatalf("the result was not the deciding evidence: %+v", got)
	}
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if x.State != "completed" || x.Liveness != nil {
		t.Fatalf("terminal view %+v", x)
	}
}

// After the cap latch the execution is unknown and never busy/tool-running.
func TestS4ClaudeCapLatchClearsLiveness(t *testing.T) {
	clk := &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}
	h := newS4Harness(t, clk, func(c *Config) { c.RunMax = 120 * time.Millisecond })
	x := h.mustSend(1, "do the thing", "key-s4-cap-0001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(s4ToolUse(1, "toolu_01", "Bash"))
	_ = s4Until(t, h, x.ID, "running liveness", func(x execution.Execution) bool { return x.Liveness != nil })
	got := s4Until(t, h, x.ID, "cap latch", func(x execution.Execution) bool { return x.State == "unknown" })
	if got.Liveness != nil {
		t.Fatalf("cap latch kept a report: %+v", got.Liveness)
	}
	_ = c
}

// D32 Q12: the effective tool threshold is capped at RUN_MAX and echoed.
func TestS4RunMaxCapsToolLong(t *testing.T) {
	clk := &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}
	h := newS4Harness(t, clk, func(c *Config) { c.RunMax = 90 * time.Second; c.ToolLongS = 600; c.NoProgressS = 120 })
	if h.ex.cfg.ToolLongS != 90 || h.ex.cfg.NoProgressS != 120 {
		t.Fatalf("effective thresholds %d/%d", h.ex.cfg.NoProgressS, h.ex.cfg.ToolLongS)
	}
	x := h.mustSend(1, "do the thing", "key-s4-cap2-0001")
	c := h.next(1)
	c.emit(h.initLine(1))
	got := s4Until(t, h, x.ID, "threshold echo", func(x execution.Execution) bool { return x.Liveness != nil })
	if got.Liveness.Thresholds == nil || got.Liveness.Thresholds.ToolLongS != 90 {
		t.Fatalf("thresholds %+v", got.Liveness.Thresholds)
	}
	c.finish(Exit{Code: 0})
	_ = h.wait(x.ID)
}

// ADR-017 A2.7: tool_progress (and any unrelated system event) is not progress.
// A wedged tool that only emits heartbeats must still reach A1's NO_PROGRESS
// after no_progress_s, and last_native_progress_at must stay at the last real
// event (the assistant message).
func TestS4ClaudeToolProgressIsNotProgress(t *testing.T) {
	clk := &s4Clock{t: time.UnixMilli(1_700_000_000_000).UTC()}
	h := newS4Harness(t, clk, nil)
	x := h.mustSend(1, "do the thing", "key-s4-heartbeat-01")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(s4ToolUse(1, "toolu_hb", "Bash"))
	got := s4Until(t, h, x.ID, "tool object", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.Tool != nil && x.Liveness.LastNativeProgressAt != nil
	})
	baseline := *got.Liveness.LastNativeProgressAt

	heartbeat := js(map[string]any{"type": "system", "subtype": "tool_progress", "session_id": uuidN(1)})
	for _, at := range []time.Duration{60 * time.Second, 59 * time.Second} {
		clk.Advance(at)
		c.emit(heartbeat)
	}
	clk.Advance(2 * time.Second) // 121 s after the last real progress

	got = s4Until(t, h, x.ID, "NO_PROGRESS despite the heartbeats", func(x execution.Execution) bool {
		return x.Liveness != nil && x.Liveness.StallSuspected && x.Liveness.StallReason == "NO_PROGRESS"
	})
	if got.Liveness.LastNativeProgressAt == nil || !got.Liveness.LastNativeProgressAt.Equal(baseline) {
		t.Fatalf("the heartbeats moved last_native_progress_at: %v -> %v", baseline, got.Liveness.LastNativeProgressAt)
	}
	if got.Liveness.Tool == nil || got.Liveness.Explanation != "" {
		t.Fatalf("tool/explanation at 121 s: %+v", got.Liveness)
	}
	if held, _ := h.ex.SessionGuard(h.sessions[0].ID); !held {
		t.Fatal("the guard was released by liveness")
	}
}
