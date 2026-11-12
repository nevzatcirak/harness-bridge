package claudecode

import (
	"sort"
	"strings"
)

// This file is ADR-021 §3 as a pure function: events, exit status and the
// Bridge's own records (interrupts it sent, deadlines that fired) in, a verdict
// out. It never does I/O, reads no clock and starts nothing.

// Config is what the verdict depends on besides the stream.
type Config struct {
	RequestedUUID   string   // exact session the Bridge resumed
	CanonicalRoot   string   // the session's approved root, after ADR-014 checks
	AllowedVersions []string // exact claude_code_version strings (§8.5)
	Tools           []string // the configured built-in tool profile
}

// StepKind is the kind of one input to the machine, in the order it happened.
type StepKind int

const (
	StepLine                StepKind = iota + 1 // one stdout line (Event)
	StepStreamBroken                            // decoder bound or read error: the stream is unusable
	StepEOF                                     // stdout reached EOF
	StepExit                                    // the child was waited (ExitCode / ExitSignal / ExitUnknown)
	StepUserInterrupt                           // interrupt_execution sent SIGINT (reason user)
	StepCap                                     // the wall-clock cap fired
	StepGraceExpired                            // the exit grace after a result ended
	StepShutdown                                // Bridge Close while the run is live
	StepEscalationExhausted                     // SIGINT and SIGTERM waits ended with the child alive
	StepSpawnFailed                             // fork/exec failed
	StepStdinFailed                             // the stdin write failed; Accepted bytes were taken
)

// Step is one input. Only the fields of its Kind are read.
type Step struct {
	Kind        StepKind
	Event       Event  // StepLine
	ExitCode    int    // StepExit
	ExitSignal  string // StepExit: non-empty when the child died of a signal
	ExitUnknown bool   // StepExit: wait failed or the status could not be obtained
	Accepted    int    // StepStdinFailed
}

// Action is what the Bridge must do after a Feed.
type Action struct {
	// Interrupt is the reason of an interrupt (SIGINT, then the §4 escalation)
	// the Bridge must send now: "latch", "cap", "grace" or "shutdown".
	Interrupt string
}

// Verdict is the machine's output. State is one of running, submitting,
// completed, failed, interrupted, unknown.
type Verdict struct {
	Decided bool
	Row     string // the §3 row that decided: 0 9 6a 9s 6b 1 1s 2 8 3 4 5 6 7 10
	State   string
	Code    string // failure code for failed
	Reason  string // unresolved reason for unknown
	Detail  string // enum string for audit, never free text

	GuardReleased    bool
	ChildUnconfirmed bool
	Retryable        bool

	InterruptReasons []string // ordered, one entry per interrupt the Bridge sent
	LateLines        int      // late_lines_after_verdict
	LateResult       bool     // late_result_seen
	ForeignEvents    int      // events of another session, counted only
	Violations       []string // init verification failures (row 0)

	Subtype           string
	TerminalReason    string
	NumTurns          int
	ExitClass         string // zero, nonzero, signal, unknown, none
	CostUSD           float64
	PermissionDenials []Denial
}

// ignorableTrailers are event types that do NOT count as an event after the final
// result. It starts EMPTY and is extended only from a live multi-run capture of
// the post-result stream (a slice 1 operator acceptance item, ADR-021 §3 row 8).
var ignorableTrailers = map[string]struct{}{}

func ignorable(e Event) bool {
	if e.Undecodable {
		return false
	}
	key := e.Type
	if e.Subtype != "" {
		key += "/" + e.Subtype
	}
	_, ok := ignorableTrailers[key]
	return ok
}

var hookEvents = map[string]bool{"hook_started": true, "hook_progress": true, "hook_response": true, "plugin_install": true}

// Machine accumulates steps. Use Decide for the one-shot form.
type Machine struct {
	cfg Config

	decided, latched bool
	v                Verdict

	initSeen, initOK bool
	results          int
	last             *ResultInfo
	after            int
	abortedSeen      bool
	userInterrupt    bool
	broken           bool
	eof, exited      bool
	exit             Step
	interrupts       []string
	foreign          int
}

// NewMachine starts a run that has been spawned and fed its prompt.
func NewMachine(cfg Config) *Machine { return &Machine{cfg: cfg} }

// Decide feeds every step and returns the verdict (Decided is false if the steps
// do not settle the run).
func Decide(cfg Config, steps []Step) Verdict {
	m := NewMachine(cfg)
	for _, s := range steps {
		m.Feed(s)
	}
	return m.Verdict()
}

// Verdict returns the current verdict; a run that is not decided is reported as
// submitting (no init yet) or running (init correlated).
func (m *Machine) Verdict() Verdict {
	v := m.v
	v.InterruptReasons = append([]string(nil), m.interrupts...)
	v.ForeignEvents = m.foreign
	if v.ExitClass == "" {
		v.ExitClass = "none"
	}
	if !m.decided {
		v.State = "submitting"
		if m.initOK {
			v.State = "running"
		}
	}
	return v
}

// Feed advances the machine by one step.
func (m *Machine) Feed(s Step) Action {
	if m.decided {
		m.audit(s)
		return Action{}
	}
	switch s.Kind {
	case StepLine:
		return m.line(s.Event)
	case StepStreamBroken:
		m.broken = true
	case StepEOF:
		m.eof = true
		m.settle()
	case StepExit:
		m.recordExit(s)
		if s.ExitUnknown && m.results > 0 {
			m.latch("6a", "unknown", "EXIT_UNCONFIRMED", "exit_status_unavailable")
			m.v.ChildUnconfirmed = true
			return Action{}
		}
		m.exited = true
		m.settle()
	case StepUserInterrupt:
		if !m.userInterrupt {
			m.userInterrupt = true
			m.interrupts = append(m.interrupts, "user")
		}
	case StepCap:
		m.interrupts = append(m.interrupts, "cap")
		m.latch("9", "unknown", "MONITOR_EXPIRED", "wall_clock_cap")
		return Action{Interrupt: "cap"}
	case StepGraceExpired:
		if m.results > 0 {
			m.interrupts = append(m.interrupts, "grace")
			m.latch("6a", "unknown", "EXIT_UNCONFIRMED", "exit_grace_expired")
			m.v.ChildUnconfirmed = true
			return Action{Interrupt: "grace"}
		}
	case StepShutdown:
		m.interrupts = append(m.interrupts, "shutdown")
		m.latch("9s", "unknown", "MANAGER_CLOSED", "bridge_shutdown")
		return Action{Interrupt: "shutdown"}
	case StepEscalationExhausted:
		if m.userInterrupt && !m.abortedSeen {
			m.latch("6b", "unknown", "ABORT_UNCONFIRMED", "user_interrupt_escalation_exhausted")
			m.v.ChildUnconfirmed = true
		}
	case StepSpawnFailed:
		m.decide("1", "failed", "PRE_SUBMISSION_UNAVAILABLE", "", "spawn_failed", true)
	case StepStdinFailed:
		if s.Accepted <= 0 {
			m.decide("1", "failed", "PRE_SUBMISSION_UNAVAILABLE", "", "stdin_rejected_all_bytes", true)
		} else {
			m.latch("1s", "unknown", "SUBMISSION_UNCONFIRMED", "stdin_failed_after_partial_write")
		}
	}
	return Action{}
}

func (m *Machine) correlated(e Event) bool { return e.SessionID == m.cfg.RequestedUUID }

func (m *Machine) line(e Event) Action {
	if e.Undecodable {
		m.countAfter(e)
		return Action{}
	}
	if !m.initSeen && e.Type == "system" && hookEvents[e.Subtype] {
		m.v.Violations = append(m.v.Violations, "pre_init_hook_event")
		m.interrupts = append(m.interrupts, "latch")
		m.latch("0", "unknown", "IDENTITY_MISMATCH", "init_verification")
		return Action{Interrupt: "latch"}
	}
	if e.Type == "system" && e.Subtype == "init" && !m.initSeen {
		m.initSeen = true
		if viol := m.checkInit(e.Init); len(viol) > 0 {
			m.v.Violations = viol
			m.interrupts = append(m.interrupts, "latch")
			m.latch("0", "unknown", "IDENTITY_MISMATCH", "init_verification")
			return Action{Interrupt: "latch"}
		}
		m.initOK = true
		return Action{}
	}
	if e.Type == "result" {
		if e.Result == nil || !m.correlated(e) {
			m.foreign++
			m.countAfter(e)
			return Action{}
		}
		m.results++
		m.last = e.Result
		if r := e.Result; r.IsError != nil && *r.IsError && strings.HasPrefix(r.TerminalReason, "aborted_") {
			m.abortedSeen = true
		}
		return Action{}
	}
	if e.SessionID != "" && !m.correlated(e) {
		m.foreign++
	}
	m.countAfter(e)
	return Action{}
}

// countAfter counts a line that arrived after the final correlated result: every
// non-empty line counts, decodable or not, unless its type is on the (empty)
// ignorable list.
func (m *Machine) countAfter(e Event) {
	if m.results > 0 && !ignorable(e) {
		m.after++
	}
}

func (m *Machine) checkInit(i *InitInfo) []string {
	if i == nil {
		return []string{"init_unreadable"}
	}
	var v []string
	if i.PermissionMode != "default" {
		v = append(v, "permission_mode")
	}
	if i.APIKeySource != "none" {
		v = append(v, "api_key_source")
	}
	if i.CWD != m.cfg.CanonicalRoot || m.cfg.CanonicalRoot == "" {
		v = append(v, "cwd")
	}
	if i.SessionID != m.cfg.RequestedUUID {
		v = append(v, "session_id")
	}
	if !contains(m.cfg.AllowedVersions, i.Version) {
		v = append(v, "claude_code_version")
	}
	if !i.MCPServersKnown || i.MCPServers != 0 {
		v = append(v, "mcp_servers")
	}
	if !i.PluginsKnown || i.Plugins != 0 {
		v = append(v, "plugins")
	}
	if !i.ToolsKnown || !sameSet(i.Tools, m.cfg.Tools) {
		v = append(v, "tools")
	}
	return v
}

func contains(list []string, s string) bool {
	if s == "" {
		return false
	}
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] || (i > 0 && x[i] == x[i-1]) {
			return false
		}
	}
	return true
}

func (m *Machine) recordExit(s Step) {
	m.exit = s
	switch {
	case s.ExitUnknown:
		m.v.ExitClass = "unknown"
	case s.ExitSignal != "":
		m.v.ExitClass = "signal"
	case s.ExitCode == 0:
		m.v.ExitClass = "zero"
	default:
		m.v.ExitClass = "nonzero"
	}
}

// audit records what arrives after the verdict was decided. A latched verdict
// keeps counting late lines and results; nothing can change the verdict.
func (m *Machine) audit(s Step) {
	switch s.Kind {
	case StepLine:
		if !m.latched {
			return
		}
		m.v.LateLines++
		if s.Event.Type == "result" && !s.Event.Undecodable {
			m.v.LateResult = true
		}
	case StepExit:
		if m.latched {
			m.recordExit(s)
		}
	case StepEscalationExhausted:
		if m.latched && (m.v.Row == "0" || m.v.Row == "9" || m.v.Row == "6a") {
			m.v.ChildUnconfirmed = true
		}
	}
}

func (m *Machine) fillResult() {
	if r := m.last; r != nil {
		m.v.Subtype, m.v.TerminalReason, m.v.CostUSD = r.Subtype, r.TerminalReason, r.CostUSD
		if r.NumTurns != nil {
			m.v.NumTurns = *r.NumTurns
		}
		m.v.PermissionDenials = append([]Denial(nil), r.Denials...)
	}
}

func (m *Machine) decide(row, state, code, reason, detail string, released bool) {
	m.fillResult()
	m.decided = true
	m.v.Decided, m.v.Row, m.v.State, m.v.Code, m.v.Reason, m.v.Detail, m.v.GuardReleased = true, row, state, code, reason, detail, released
}

// latch decides at the moment the event happens; later input is audit-only.
func (m *Machine) latch(row, state, reason, detail string) {
	m.decide(row, state, "", reason, detail, false)
	m.latched = true
}

func (m *Machine) exitZero() bool {
	return m.exited && !m.exit.ExitUnknown && m.exit.ExitSignal == "" && m.exit.ExitCode == 0
}
func (m *Machine) exitNonZero() bool {
	return m.exited && !m.exit.ExitUnknown && (m.exit.ExitSignal != "" || m.exit.ExitCode != 0)
}

// settle runs the non-latched part of the table once stdout reached EOF and the
// child was waited. Order: 1 2 8 3 4 5 6 7 10 (rows 0 9 6a 9s 6b are latches).
func (m *Machine) settle() {
	if !m.eof || !m.exited {
		return
	}
	r := m.last
	isErr := r != nil && r.IsError != nil && *r.IsError
	aborted := isErr && strings.HasPrefix(r.TerminalReason, "aborted_")
	switch {
	case m.exit.ExitUnknown:
		m.decide("10", "unknown", "", "", "exit_status_unavailable", false)
	case m.row2():
		m.decide("2", "failed", "NOT_FOUND", "", "session_not_found", true)
	case m.results >= 2:
		m.decide("8", "unknown", "", "", "multiple_results", false)
	case m.results >= 1 && m.after > 0:
		m.decide("8", "unknown", "", "", "event_after_final_result", false)
	case m.results == 1 && m.initOK && r.Subtype == "success" && r.IsError != nil && !*r.IsError && m.exitZero():
		if r.TerminalReason == "completed" {
			m.decide("3", "completed", "", "", "", true)
		} else {
			m.decide("10", "unknown", "", "", "terminal_reason_unlisted", false)
		}
	case m.results == 1 && isErr && len(m.interrupts) == 0 && (m.exitNonZero() || aborted):
		m.decide("4", "failed", failureCode(r.Subtype), "", "", true)
	case m.results == 1 && m.userInterrupt && aborted:
		m.decide("5", "interrupted", "", "", "", true)
	case m.results == 0:
		reason := "PROCESS_ENDED_WITHOUT_RESULT"
		if m.userInterrupt {
			reason = "ABORT_UNCONFIRMED"
		}
		m.decide("6", "unknown", "", reason, "no_result", false)
	case m.results == 1 && r.Subtype == "success" && r.IsError != nil && !*r.IsError && m.exitNonZero():
		m.decide("7", "unknown", "", "", "success_result_exit_nonzero", false)
	default:
		m.decide("10", "unknown", "", "", "default", false)
	}
}

// row2 is the OBSERVED unknown-session shape (spike step 4): no init, one result
// of error_during_execution with num_turns 0 and the not-found message, exit 1,
// and no event after it.
func (m *Machine) row2() bool {
	r := m.last
	if m.initSeen || m.results != 1 || m.after != 0 || r == nil || r.Subtype != "error_during_execution" {
		return false
	}
	if r.NumTurns == nil || *r.NumTurns != 0 || m.exit.ExitSignal != "" || m.exit.ExitCode != 1 {
		return false
	}
	for _, e := range r.Errors {
		if strings.Contains(e, "No conversation found with session ID") {
			return true
		}
	}
	return false
}

func failureCode(subtype string) string {
	if subtype == "" || len(subtype) > 64 {
		return "ERROR_UNKNOWN"
	}
	var b strings.Builder
	for _, c := range strings.ToUpper(subtype) {
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
