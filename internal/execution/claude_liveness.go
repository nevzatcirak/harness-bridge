package execution

// ADR-017 A2 S4: the report-only liveness reducer for a Claude Code execution.
// The Claude stream has no status endpoint; this reducer is fed the correlated
// events the runner already decodes, and Assess is called on every snapshot. It
// never touches the verdict, the state, the guard or a native call. It reuses the
// same closed fields, thresholds and tool object as the OpenCode side.

import (
	"time"
)

// ClaudeTool is one live tool_use observed on the Claude receive clock.
type ClaudeTool struct {
	CallID string
	Name   string
	Since  time.Time
}

type claudeRetry struct {
	attempt      int64
	hasAttempt   bool
	maxRetries   int64
	hasMax       bool
	next         time.Time
	hint, source string
}

// ClaudeLiveness is the A2 S4 reducer. It is not safe for concurrent use; the
// runner serializes every call under its run lock.
type ClaudeLiveness struct {
	start, progress time.Time
	started         bool
	cleared         bool

	retry        *claudeRetry
	attemptSince time.Time
	attemptSeen  int64

	tools map[string]ClaudeTool

	unavailable      bool
	unavailableSince time.Time

	noProgress, toolLong time.Duration
}

// NewClaudeLiveness builds the reducer with the applied A2.6 thresholds; zero
// fields use the defaults. The caller passes the effective tool_long_s (capped
// at the Claude RUN_MAX by the executor, D32 Q12).
func NewClaudeLiveness(noProgressS, toolLongS int) *ClaudeLiveness {
	noProgress := defaultStallThreshold
	if noProgressS > 0 {
		noProgress = time.Duration(noProgressS) * time.Second
	}
	toolLong := defaultToolLongThreshold
	if toolLongS > 0 {
		toolLong = time.Duration(toolLongS) * time.Second
	}
	return &ClaudeLiveness{noProgress: noProgress, toolLong: toolLong, tools: map[string]ClaudeTool{}}
}

// Init records the correlated system/init: the run is busy and the receive
// clock starts. A second init keeps the first start and only advances progress.
func (c *ClaudeLiveness) Init(now time.Time) {
	if c.cleared {
		return
	}
	if !c.started {
		c.started = true
		c.start = now
	}
	if now.After(c.progress) {
		c.progress = now
	}
	c.endRetry()
}

// Progress records a correlated event that is native progress for the Bridge
// (a full assistant message, a user/tool_result event, the init). A later event
// clears the retry state (A2.7).
func (c *ClaudeLiveness) Progress(now time.Time) {
	if c.cleared || !c.started {
		return
	}
	if now.After(c.progress) {
		c.progress = now
	}
	c.endRetry()
}

// OtherEvent clears the retry state without counting as progress (for example a
// tool_progress heartbeat or an unrelated system event).
func (c *ClaudeLiveness) OtherEvent() {
	if c.cleared || !c.started {
		return
	}
	c.endRetry()
}

// Assistant records one correlated assistant message: it is progress, and its
// tool_use blocks (with their ids) become the live tools of the newest
// correlated assistant. A tool without an id cannot be paired with a result and
// is ignored for the tool object.
func (c *ClaudeLiveness) Assistant(now time.Time, tools []ClaudeTool) {
	if c.cleared || !c.started {
		return
	}
	c.Progress(now)
	c.tools = map[string]ClaudeTool{}
	for _, t := range tools {
		if t.CallID == "" {
			continue
		}
		t.Name = sanitizeToolName(t.Name)
		if t.Since.IsZero() {
			t.Since = now
		}
		c.tools[t.CallID] = t
	}
}

// ToolResult records one correlated user/tool_result event: progress, and the
// matching call is no longer running.
func (c *ClaudeLiveness) ToolResult(now time.Time, callIDs []string) {
	if c.cleared || !c.started {
		return
	}
	c.Progress(now)
	for _, id := range callIDs {
		delete(c.tools, id)
	}
}

// Retry records one correlated system/api_retry event. It is not progress: a
// wedged retry must still reach NO_PROGRESS. next is the receive clock plus the
// documented delay.
func (c *ClaudeLiveness) Retry(now time.Time, attempt int64, hasAttempt bool, maxRetries int64, hasMax bool, delayMS int64, hint, source string) {
	if c.cleared || !c.started {
		return
	}
	key := int64(-1)
	if hasAttempt {
		key = attempt
	}
	if c.attemptSince.IsZero() || c.attemptSeen != key {
		c.attemptSince = now
		c.attemptSeen = key
	}
	next := now
	if delayMS > 0 {
		next = now.Add(time.Duration(delayMS) * time.Millisecond)
	}
	c.retry = &claudeRetry{attempt: attempt, hasAttempt: hasAttempt, maxRetries: maxRetries, hasMax: hasMax,
		next: next, hint: hint, source: source}
}

func (c *ClaudeLiveness) endRetry() {
	c.retry = nil
	c.attemptSince = time.Time{}
	c.attemptSeen = 0
}

// Unavailable records a stream/read failure. counting is true only for a stdout
// read error while the child is alive and the run is not decided; a decoder
// bound is a reader failure but never counts toward HARNESS_UNREACHABLE.
func (c *ClaudeLiveness) Unavailable(now time.Time, counting bool) {
	if c.cleared || !c.started {
		return
	}
	c.unavailable = true
	c.endRetry()
	if !counting {
		c.unavailableSince = time.Time{}
		return
	}
	if c.unavailableSince.IsZero() {
		c.unavailableSince = now
	}
}

// Clear ends the report: a terminal verdict, or a cap/exit-grace latch, never
// carries busy or a running tool.
func (c *ClaudeLiveness) Clear() {
	c.cleared = true
	c.started = false
	c.tools = nil
	c.retry = nil
	c.attemptSince = time.Time{}
	c.attemptSeen = 0
	c.unavailable = false
	c.unavailableSince = time.Time{}
}

// UnavailableSince is the report-only memory for the execution record.
func (c *ClaudeLiveness) UnavailableSince() *time.Time {
	if c.cleared || c.unavailableSince.IsZero() {
		return nil
	}
	t := c.unavailableSince
	return &t
}

// RetryAttemptSince is the report-only memory for retry_overdue.
func (c *ClaudeLiveness) RetryAttemptSince() *time.Time {
	if c.cleared || c.retry == nil || c.attemptSince.IsZero() {
		return nil
	}
	t := c.attemptSince
	return &t
}

// Assess returns the current liveness block, or nil before init and after a
// clear. It is advisory and never influences the verdict.
func (c *ClaudeLiveness) Assess(now time.Time) *Liveness {
	if c.cleared || !c.started {
		return nil
	}
	l := &Liveness{
		NativeStatus:   "busy",
		ProgressSource: "bridge",
		ObservedAt:     now,
		Thresholds: &Thresholds{
			NoProgressS: int(c.noProgress / time.Second),
			ToolLongS:   int(c.toolLong / time.Second),
		},
	}
	if !c.progress.IsZero() {
		p := c.progress
		l.LastNativeProgressAt = &p
	}
	if c.unavailable {
		l.NativeStatus = "unavailable"
		if !c.unavailableSince.IsZero() && now.Sub(c.unavailableSince) >= unavailableThreshold {
			l.StallReason = "HARNESS_UNREACHABLE"
			l.StallSuspected = true
		}
		return l
	}
	healthyRetry := false
	if c.retry != nil {
		r := c.retry
		if r.hasAttempt {
			a := r.attempt
			l.RetryAttempt = &a
		}
		if r.hasMax {
			m := r.maxRetries
			l.RetryMax = &m
		}
		next := r.next
		l.RetryNextAt = &next
		// retry_overdue: next past by more than max(30 s, no_progress_s) and the
		// current attempt observed for at least no_progress_s.
		grace := 30 * time.Second
		if c.noProgress > grace {
			grace = c.noProgress
		}
		if next.Before(now) && now.Sub(next) > grace && !c.attemptSince.IsZero() && now.Sub(c.attemptSince) >= c.noProgress {
			l.RetryOverdue = true
		}
		if l.RetryOverdue {
			l.DegradationReason = "PROVIDER_RETRY_OVERDUE"
		} else {
			l.DegradationReason = "PROVIDER_RETRYING"
		}
		l.CauseHint, l.CauseSource = r.hint, r.source
		healthyRetry = next.After(now)
	}
	l.Degraded = l.DegradationReason != ""
	baseline := c.start
	if c.progress.After(baseline) {
		baseline = c.progress
	}
	if !healthyRetry && now.Sub(baseline) > c.noProgress {
		l.StallReason = "NO_PROGRESS"
	}
	// The tool object is display data of a busy execution or a wedged retry.
	if c.retry == nil || (c.retry.next.Before(now) && l.StallReason == "NO_PROGRESS") {
		if tool, ok := c.oldestTool(); ok {
			l.Tool = &Tool{ToolName: tool.Name, RunningSince: tool.Since}
			if now.Sub(tool.Since) >= c.toolLong {
				l.Explanation = explanationToolRunningLong
			}
		}
	}
	l.StallSuspected = l.StallReason != ""
	return l
}

func (c *ClaudeLiveness) oldestTool() (ClaudeTool, bool) {
	var best ClaudeTool
	found := false
	for _, t := range c.tools {
		if !found || t.Since.Before(best.Since) {
			best, found = t, true
		}
	}
	return best, found
}

// ClaudeRetryCause maps the structured system/api_retry fields to the A2.5
// closed cause words; the source is always structured. A null error_status or a
// present no_response is the documented connection-level signal (network).
func ClaudeRetryCause(status *int, statusNull bool, errorEnum string, noResponse bool) (hint, source string) {
	if status != nil {
		switch *status {
		case 401, 403:
			return "auth", "structured"
		case 429:
			return "rate_limit", "structured"
		case 408, 500, 502, 503, 504, 529:
			return "server", "structured"
		default:
			return "unknown", "structured"
		}
	}
	if statusNull || noResponse {
		return "network", "structured"
	}
	switch errorEnum {
	case "rate_limit":
		return "rate_limit", "structured"
	case "authentication_failed":
		return "auth", "structured"
	case "server_error", "overloaded":
		return "server", "structured"
	default:
		return "unknown", "structured"
	}
}
