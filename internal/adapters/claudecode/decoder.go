package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// Stream bounds (ADR-021 §1).
const (
	defaultMaxLine  = 1 << 20  // 1 MiB per stdout line
	defaultMaxTotal = 64 << 20 // 64 MiB per child
	maxDenialInput  = 256
	// maxRetryDelayMS bounds an untrusted retry_delay_ms so the receive clock
	// arithmetic cannot overflow; a delay beyond the Claude RUN_MAX cap is
	// irrelevant to a run anyway.
	maxRetryDelayMS = int64(24 * time.Hour / time.Millisecond)
)

var (
	// ErrLineTooLong ends the stream: one line exceeded the per-line bound.
	ErrLineTooLong = errors.New("claudecode: stdout line exceeds the bound")
	// ErrStreamTooLarge ends the stream: total stdout exceeded the bound.
	ErrStreamTooLarge = errors.New("claudecode: stdout exceeds the total bound")
)

// Limits bounds the decoder; zero fields take the ADR defaults.
type Limits struct {
	MaxLine  int64
	MaxTotal int64
}

func (l Limits) withDefaults() Limits {
	if l.MaxLine <= 0 {
		l.MaxLine = defaultMaxLine
	}
	if l.MaxTotal <= 0 {
		l.MaxTotal = defaultMaxTotal
	}
	return l
}

// Denial is one entry of result.permission_denials, reduced to a bounded and
// redacted summary. Denied inputs are Bridge data, not authority (§7).
type Denial struct {
	ToolName     string
	ToolUseID    string
	InputSummary string
}

// InitInfo is the system/init content the state machine verifies (§8.3 item 4).
// A list that is absent or not a list is reported as unknown, never as empty.
type InitInfo struct {
	SessionID, CWD, PermissionMode, APIKeySource, Version string
	MCPServers, Plugins                                   int
	MCPServersKnown, PluginsKnown                         bool
	Tools                                                 []string
	ToolsKnown                                            bool
}

// ResultInfo is the result line content the state machine uses. Pointer fields
// stay nil when the CLI omitted them: nothing is defaulted into success.
type ResultInfo struct {
	Subtype        string
	IsError        *bool
	NumTurns       *int
	Errors         []string
	TerminalReason string
	CostUSD        float64
	Denials        []Denial
}

// APIRetry is one system/api_retry event (ADR-017 A2.7). The field set is
// documented upstream (code.claude.com/docs/en/headless) and UNVERIFIED on the
// pinned 2.1.325 binary. Every field is lenient: a malformed or unknown shape
// leaves the zero value and never fails the run (D32 Q6).
type APIRetry struct {
	Attempt    int64
	HasAttempt bool
	MaxRetries int64
	HasMax     bool
	// RetryDelayMS is the documented delay before the next attempt.
	RetryDelayMS int64
	// ErrorStatus is the HTTP status when the event carries a valid integer;
	// ErrorStatusNull records an explicit null (a connection-level signal).
	ErrorStatus     *int
	ErrorStatusNull bool
	// Error is the documented enum string (open in practice).
	Error string
	// NoResponse is true when the no_response object is present (network).
	NoResponse            bool
	NoResponseWaitedMS    int64
	NoResponseRetryWaitMS int64
}

// ToolResult is one tool_result content block of a user event, bounded; the
// history store redacts it again on write.
type ToolResult struct {
	ToolUseID string
	Content   string
}

// Event is one decoded stdout line. Blocks and ToolResults carry what the history
// records (§5): assistant text and tool_use blocks, and tool_result blocks.
type Event struct {
	Type, Subtype, SessionID string
	Undecodable              bool
	Init                     *InitInfo
	Result                   *ResultInfo
	Retry                    *APIRetry
	Blocks                   []Block
	ToolResults              []ToolResult
}

const (
	maxBlockText  = 32 << 10 // per text block; the store's record bound applies on top
	maxBlockInput = 4 << 10  // per tool_use input summary
	maxBlocks     = 64       // per event
)

// Decoder reads NDJSON lines with per-line and total bounds. Blank lines are
// skipped. After a bound error every further call returns the same error.
type Decoder struct {
	r     *bufio.Reader
	lim   Limits
	total int64
	err   error
}

// NewDecoder wraps r.
func NewDecoder(r io.Reader, lim Limits) *Decoder {
	return &Decoder{r: bufio.NewReaderSize(r, 64<<10), lim: lim.withDefaults()}
}

// Next returns the next non-blank line as an Event, io.EOF at the end, or a
// bound error. A final line without a newline is returned as a line.
func (d *Decoder) Next() (Event, error) {
	for {
		if d.err != nil {
			return Event{}, d.err
		}
		raw, err := d.readLine()
		if err != nil {
			d.err = err
			return Event{}, err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		return DecodeLine(raw), nil
	}
}

func (d *Decoder) readLine() ([]byte, error) {
	var line []byte
	for {
		frag, err := d.r.ReadSlice('\n')
		d.total += int64(len(frag))
		if d.total > d.lim.MaxTotal {
			return nil, ErrStreamTooLarge
		}
		content := len(frag)
		if content > 0 && frag[content-1] == '\n' {
			content--
		}
		if int64(len(line))+int64(content) > d.lim.MaxLine {
			return nil, ErrLineTooLong
		}
		line = append(line, frag[:content]...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		default:
			return nil, err
		}
	}
}

func rawString(m map[string]json.RawMessage, k string) string {
	var s string
	if json.Unmarshal(m[k], &s) != nil {
		return ""
	}
	return s
}

func rawCount(m map[string]json.RawMessage, k string) (int, bool) {
	var a []json.RawMessage
	if m[k] == nil || json.Unmarshal(m[k], &a) != nil || a == nil {
		return 0, false
	}
	return len(a), true
}

// DecodeLine decodes one non-blank line. Anything that is not exactly one JSON
// object is an undecodable event (it still counts as a line for row 8).
func DecodeLine(b []byte) Event {
	b = bytes.TrimSpace(b)
	var m map[string]json.RawMessage
	if len(b) == 0 || b[0] != '{' || json.Unmarshal(b, &m) != nil || m == nil {
		return Event{Undecodable: true}
	}
	e := Event{Type: rawString(m, "type"), Subtype: rawString(m, "subtype"), SessionID: rawString(m, "session_id")}
	switch {
	case e.Type == "system" && e.Subtype == "init":
		i := &InitInfo{SessionID: e.SessionID, CWD: rawString(m, "cwd"), PermissionMode: rawString(m, "permissionMode"),
			APIKeySource: rawString(m, "apiKeySource"), Version: rawString(m, "claude_code_version")}
		i.MCPServers, i.MCPServersKnown = rawCount(m, "mcp_servers")
		i.Plugins, i.PluginsKnown = rawCount(m, "plugins")
		if json.Unmarshal(m["tools"], &i.Tools) == nil && m["tools"] != nil && i.Tools != nil {
			i.ToolsKnown = true
		}
		e.Init = i
	case e.Type == "result":
		r := &ResultInfo{Subtype: e.Subtype, TerminalReason: rawString(m, "terminal_reason")}
		var isErr bool
		if json.Unmarshal(m["is_error"], &isErr) == nil && m["is_error"] != nil {
			r.IsError = &isErr
		}
		var n int
		if json.Unmarshal(m["num_turns"], &n) == nil && m["num_turns"] != nil {
			r.NumTurns = &n
		}
		_ = json.Unmarshal(m["errors"], &r.Errors)
		_ = json.Unmarshal(m["total_cost_usd"], &r.CostUSD)
		var denials []struct {
			ToolName  string          `json:"tool_name"`
			ToolUseID string          `json:"tool_use_id"`
			Input     json.RawMessage `json:"tool_input"`
		}
		_ = json.Unmarshal(m["permission_denials"], &denials)
		for _, d := range denials {
			r.Denials = append(r.Denials, Denial{ToolName: boundedASCII(d.ToolName, 128), ToolUseID: boundedASCII(d.ToolUseID, 128), InputSummary: summarize(d.Input)})
		}
		e.Result = r
	case e.Type == "system" && e.Subtype == "api_retry":
		e.Retry = decodeAPIRetry(m)
	case e.Type == "assistant":
		e.Blocks = decodeBlocks(m["message"])
	case e.Type == "user":
		e.ToolResults = decodeToolResults(m["message"])
	}
	return e
}

type contentItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func contentOf(msg json.RawMessage) []contentItem {
	var m struct {
		Content []contentItem `json:"content"`
	}
	if len(msg) == 0 || json.Unmarshal(msg, &m) != nil {
		return nil
	}
	if len(m.Content) > maxBlocks {
		m.Content = m.Content[:maxBlocks]
	}
	return m.Content
}

func decodeBlocks(msg json.RawMessage) []Block {
	var out []Block
	for _, c := range contentOf(msg) {
		switch c.Type {
		case "text":
			out = append(out, Block{Type: "text", Text: boundedASCII(c.Text, maxBlockText)})
		case "tool_use":
			out = append(out, Block{Type: "tool_use", ID: boundedASCII(c.ID, 128), Name: boundedASCII(c.Name, 128), Input: compactBounded(c.Input, maxBlockInput)})
		}
	}
	return out
}

func decodeToolResults(msg json.RawMessage) []ToolResult {
	var out []ToolResult
	for _, c := range contentOf(msg) {
		if c.Type != "tool_result" {
			continue
		}
		text := ""
		var asString string
		var parts []contentItem
		switch {
		case json.Unmarshal(c.Content, &asString) == nil && c.Content != nil:
			text = asString
		case json.Unmarshal(c.Content, &parts) == nil:
			for _, p := range parts {
				if p.Type == "text" {
					text += p.Text
				}
			}
		}
		out = append(out, ToolResult{ToolUseID: boundedASCII(c.ToolUseID, 128), Content: boundedASCII(text, maxBlockText)})
	}
	return out
}

// decodeAPIRetry reads the system/api_retry fields leniently. The event type is
// known even when a field is malformed; the liveness block then reports the
// field as unknown/absent instead of failing the run (D32 Q6).
func decodeAPIRetry(m map[string]json.RawMessage) *APIRetry {
	r := &APIRetry{}
	if raw := m["attempt"]; raw != nil {
		var n int64
		if json.Unmarshal(raw, &n) == nil && n >= 0 {
			r.Attempt, r.HasAttempt = n, true
		}
	}
	if raw := m["max_retries"]; raw != nil {
		var n int64
		if json.Unmarshal(raw, &n) == nil && n >= 0 {
			r.MaxRetries, r.HasMax = n, true
		}
	}
	if raw := m["retry_delay_ms"]; raw != nil {
		var n int64
		if json.Unmarshal(raw, &n) == nil && n >= 0 && n <= maxRetryDelayMS {
			r.RetryDelayMS = n
		}
	}
	if raw := m["error_status"]; raw != nil {
		if string(bytes.TrimSpace(raw)) == "null" {
			r.ErrorStatusNull = true
		} else {
			var n int
			if json.Unmarshal(raw, &n) == nil {
				v := n
				r.ErrorStatus = &v
			}
		}
	}
	r.Error = rawString(m, "error")
	if raw := m["no_response"]; raw != nil && string(bytes.TrimSpace(raw)) != "null" {
		r.NoResponse = true
		var nr struct {
			WaitedMS    int64 `json:"waited_ms"`
			RetryWaitMS int64 `json:"retry_wait_ms"`
		}
		if json.Unmarshal(raw, &nr) == nil {
			r.NoResponseWaitedMS, r.NoResponseRetryWaitMS = nr.WaitedMS, nr.RetryWaitMS
		}
	}
	return r
}

// compactBounded compacts a JSON value and bounds it, without redacting (the
// store redacts every record it writes).
func compactBounded(in json.RawMessage, max int) string {
	if len(in) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, in) != nil {
		return ""
	}
	s := buf.String()
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func boundedASCII(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	return strings.ToValidUTF8(s, "?")
}

// summarize compacts, redacts and bounds a denied tool input.
func summarize(in json.RawMessage) string {
	if len(in) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, in) != nil {
		return ""
	}
	s := Redact(buf.String())
	if len(s) <= maxDenialInput {
		return s
	}
	cut := maxDenialInput
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
