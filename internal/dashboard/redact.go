package dashboard

import (
	"encoding/json"
	"errors"
	"regexp"
)

// redactedText replaces a redacted value (ADR-023 section 3).
const redactedText = "[redacted]"

// Operator-pattern bounds: a pattern is code the operator wrote, so it is
// bounded and compiled with RE2 (linear time, no backtracking).
const (
	maxExtraPatterns   = 16
	maxExtraPatternLen = 256
)

// defaultRedactionPatterns is the shipped default list of ADR-023 section 3:
// `sk-ant-`, `ghp_`, `github_pat_`, `AKIA[0-9A-Z]{16}`, a private-key block (an
// unterminated block is redacted to the end of the text) and a bearer token or
// Authorization header. It is active with no configuration (D17 Q5).
var defaultRedactionPatterns = []string{
	`sk-ant-[A-Za-z0-9_-]{8,}`,
	`ghp_[A-Za-z0-9]{20,}`,
	`github_pat_[A-Za-z0-9_]{20,}`,
	`AKIA[0-9A-Z]{16}`,
	`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`,
	`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*`,
	`(?i)authorization:[ \t]*bearer[ \t]+[^\s]+`,
	`(?i)\bbearer[ \t]+[A-Za-z0-9._~+/=-]{8,}`,
}

// Redactor replaces values that match its patterns. It is a convenience, not a
// guarantee: an arbitrary secret that matches no pattern is still displayed to
// whoever can open the dashboard, which is why the transport is local only.
type Redactor struct {
	rules []*regexp.Regexp
}

// NewRedactor returns the default list plus the operator's extra patterns.
func NewRedactor(extra []string) (*Redactor, error) {
	if len(extra) > maxExtraPatterns {
		return nil, errors.New("dashboard: too many redaction patterns")
	}
	r := &Redactor{}
	for _, p := range append(append([]string(nil), defaultRedactionPatterns...), extra...) {
		if len(p) == 0 || len(p) > maxExtraPatternLen && !isDefault(p) {
			return nil, errors.New("dashboard: a redaction pattern is empty or too long")
		}
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, errors.New("dashboard: invalid redaction pattern")
		}
		r.rules = append(r.rules, re)
	}
	return r, nil
}

func isDefault(p string) bool {
	for _, d := range defaultRedactionPatterns {
		if d == p {
			return true
		}
	}
	return false
}

// DefaultRedactor is the shipped list with no operator patterns.
func DefaultRedactor() *Redactor {
	r, err := NewRedactor(nil)
	if err != nil {
		panic(err) // the shipped patterns compile: a test pins it
	}
	return r
}

// Redact returns s with every match replaced, and how many replacements were
// made. The count is displayed, never logged with content.
func (r *Redactor) Redact(s string) (string, int) {
	n := 0
	for _, re := range r.rules {
		s = re.ReplaceAllStringFunc(s, func(string) string { n++; return redactedText })
	}
	return s, n
}

// EnvRedactExtra names the operator's additional redaction patterns (D17 Q5): a JSON
// array of RE2 patterns, for example BRIDGE_DASHBOARD_REDACT_EXTRA='["corp-[0-9a-f]{16}"]'.
// The patterns are additive: the shipped list stays on. Unset means the shipped list only.
const EnvRedactExtra = "BRIDGE_DASHBOARD_REDACT_EXTRA"

// RedactorFromEnv builds the redactor from BRIDGE_DASHBOARD_REDACT_EXTRA. An invalid
// value (not a JSON array of strings, too many or too long patterns, a pattern that does
// not compile) is an error, so the Bridge fails closed at startup. No error carries the
// value or any pattern text: the configuration is never echoed or logged.
func RedactorFromEnv(env func(string) string) (*Redactor, error) {
	raw := env(EnvRedactExtra)
	if raw == "" {
		return NewRedactor(nil)
	}
	var extra []string
	if err := json.Unmarshal([]byte(raw), &extra); err != nil || extra == nil { // "null" decodes to nil without error: not an array
		return nil, errors.New(EnvRedactExtra + " must be a JSON array of strings")
	}
	r, err := NewRedactor(extra)
	if err != nil {
		return nil, errors.New(EnvRedactExtra + " is invalid: at most 16 patterns of 1 to 256 bytes that compile as RE2")
	}
	return r, nil
}
