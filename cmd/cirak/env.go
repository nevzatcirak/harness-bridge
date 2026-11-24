package main

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ADR-026 section 2.3: the environment prefix is CIRAK_*; the legacy BRIDGE_* names stay accepted
// for the window of the rule below. cirakEnv is the single reader of the process environment:
// every injected `env func(string) string` of the daemon receives the function it returns, so no
// other code sees a legacy name or has to know the rule.

// envSuffixes are the 31 names of ADR-026 section 2.2 (without the prefix). TOKEN_FILE is the
// _FILE form the daemon reads (through readSecret); OPENCODE_*, CREDENTIALS_DIRECTORY and the
// dev-tool names are not renamed and are not here.
var envSuffixes = []string{
	"ADMIN_SOCKET", "ALLOWED_ROOTS", "ALLOWED_SESSIONS", "CLAUDE_COMMAND", "CLAUDE_HISTORY_DIR", "CLAUDE_HOME",
	"CLAUDE_LANG", "CLAUDE_MAX_BUDGET_USD", "CLAUDE_MAX_TURNS", "CLAUDE_MODEL", "CLAUDE_RUN_MAX", "CLAUDE_SESSIONS",
	"CLAUDE_VERSIONS", "DASHBOARD_ADDR", "DASHBOARD_REDACT_EXTRA", "DASHBOARD_SOCKET", "LIFECYCLE_CONFIG", "LISTEN",
	"OVERLAY_DIR", "PERMISSION_INCARNATION", "PERMISSION_RELAY", "PRINCIPAL", "PUBLIC_ORIGIN", "READ_ONLY",
	"TLS_CERT", "TLS_KEY", "TOKEN", "TOKEN_EXPIRES", "TOKEN_FILE", "TOKEN_MAX_LIFETIME", "WRITER_LOCK_DIR",
}

const (
	envPrefix    = "CIRAK_"
	legacyPrefix = "BRIDGE_"
)

// legacyPhase is where a build stands in the window of the legacy names (ADR-026 section 2.3,
// Q1, decided D29).
type legacyPhase int

const (
	// phaseAccept (v0.1.x and every build that is not a later release): both prefixes are read,
	// a legacy name gets one warning, and two different values refuse to start.
	phaseAccept legacyPhase = iota
	// phaseRefuse (v0.2.x): any known legacy name that is set refuses to start, so a legacy
	// security setting is never silently dropped.
	phaseRefuse
	// phaseIgnore (v0.3.0 and later): legacy names are not read.
	phaseIgnore
)

var releaseVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

// legacyPhaseFor derives the phase from the module version of the binary. A version that is not a
// release number (devel, unknown, empty) is in the first phase; a pseudo-version belongs to the
// release it follows.
func legacyPhaseFor(version string) legacyPhase {
	m := releaseVersion.FindStringSubmatch(version)
	if m == nil {
		return phaseAccept
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	switch {
	case major >= 1 || minor >= 3:
		return phaseIgnore
	case minor == 2:
		return phaseRefuse
	}
	return phaseAccept
}

// cirakEnv applies the rule of ADR-026 section 2.3 to getenv. For a canonical name CIRAK_X with the
// legacy twin BRIDGE_X (empty counts as unset on both sides; values are compared exactly, never
// trimmed or case-folded):
//
//	neither / both empty        -> empty
//	only CIRAK_X                -> its value
//	only BRIDGE_X               -> its value, one warning
//	both, equal                 -> the value, one warning
//	both, different             -> an error naming both variables, never a value
//
// The scan covers every known name, not only the ones a configuration reads. The returned warnings
// are one line per variable and never contain a value; any other name is passed through unchanged.
func cirakEnv(getenv func(string) string, phase legacyPhase) (func(string) string, []string, error) {
	resolved := make(map[string]string, len(envSuffixes))
	var warnings, problems []string
	for _, s := range envSuffixes {
		n, l := envPrefix+s, legacyPrefix+s
		nv, lv := getenv(n), getenv(l)
		switch {
		case phase == phaseIgnore:
			resolved[n] = nv
		case phase == phaseRefuse:
			resolved[n] = nv
			if lv != "" {
				problems = append(problems, l+" is no longer supported; use "+n)
			}
		case lv == "":
			resolved[n] = nv
		case nv == "":
			resolved[n] = lv
			warnings = append(warnings, deprecationWarning(l, n))
		case nv == lv:
			resolved[n] = nv
			warnings = append(warnings, deprecationWarning(l, n))
		default:
			problems = append(problems, l+" and "+n+" are both set with different values; set only "+n)
		}
	}
	if len(problems) > 0 {
		return nil, nil, errors.New(strings.Join(problems, "; "))
	}
	return func(k string) string {
		if v, ok := resolved[k]; ok {
			return v
		}
		return getenv(k)
	}, warnings, nil
}

func deprecationWarning(legacy, canonical string) string {
	return "legacy environment variable " + legacy + " is deprecated; set " + canonical + " instead (not accepted from v0.2.0)"
}
