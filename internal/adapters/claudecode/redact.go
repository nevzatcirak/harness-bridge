package claudecode

import "regexp"

// RedactionRulesVersion is the version of the rule list below. It is bumped when
// a rule is added or changed so history written under an older list is
// distinguishable (ADR-021 §5: a versioned list in code with tests).
const RedactionRulesVersion = 1

const redacted = "[REDACTED]"

// The rules are best effort and cover known credential shapes only (§5). Tool
// results can still contain secrets that match no rule.
var redactionRules = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)authorization:[^\r\n]*`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{6,}`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`\b(?:BRIDGE|ANTHROPIC|OPENCODE)_[A-Za-z0-9_]*\s*=\s*(?:"[^"]*"|'[^']*'|\S+)`),
}

// Redact replaces credential-shaped values with [REDACTED]. It is idempotent.
func Redact(s string) string {
	for _, r := range redactionRules {
		s = r.ReplaceAllString(s, redacted)
	}
	return s
}
