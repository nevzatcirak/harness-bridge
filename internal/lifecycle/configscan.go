package lifecycle

import (
	"bytes"
	"strconv"
	"strings"
)

// Bounds of the git config scan (D16 Q9 option (i)). Anything over a bound is
// refused: a config the scan cannot read completely is not known to be safe.
const (
	maxGitConfigBytes = 256 << 10
	maxGitConfigLine  = 4096
	maxGitConfigLines = 8192
)

// Causes reported by scanGitConfig; closed set, used for tests and audit.
const (
	causeConfigWorktree = "config_worktree" // core.worktree present
	causeConfigInclude  = "config_include"  // an include or includeIf section
	causeConfigBare     = "config_bare"     // core.bare true: not a work tree
	causeConfigReftable = "config_reftable" // extensions.refstorage is not "files"
	causeConfigMalform  = "config_malformed"
	causeConfigBounds   = "config_bounds"
)

// scanGitConfig is the bounded, include-free scan of a git config file's text
// that ADR-022 section 3.2 and D16 Q9 (i) call for. It is not a git config
// parser: it looks only for the settings that can move or hide the work tree
// the Bridge decided on from the files it read, and it refuses on any of them.
//
//   - core.worktree (any value, or the key with no value);
//   - any include or includeIf section (the scan never follows an include);
//   - core.bare set to true, and extensions.refstorage other than "files"
//     (a bare or reftable repository is a layout the bounded reader does not
//     support);
//   - NUL bytes, over-long lines, too many lines or too large a file.
//
// It returns "" for a config with none of them, else the cause. Names are
// matched case-insensitively and a key on the same line as its section header
// is seen. Every physical line is inspected, including one that a backslash
// continuation would make part of a value: that can only add refusals.
func scanGitConfig(data []byte) string {
	if len(data) > maxGitConfigBytes {
		return causeConfigBounds
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return causeConfigMalform
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxGitConfigLines {
		return causeConfigBounds
	}
	section := ""
	for _, line := range lines {
		if len(line) > maxGitConfigLine {
			return causeConfigBounds
		}
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return causeConfigMalform
			}
			section = sectionName(line[1:end])
			if section == "include" || section == "includeif" {
				return causeConfigInclude
			}
			line = strings.TrimSpace(line[end+1:])
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
		}
		key, value, hasValue := splitKey(line)
		switch {
		case section == "core" && key == "worktree":
			return causeConfigWorktree
		case section == "core" && key == "bare":
			// A key with no value is true in git. Any value is classified like git's
			// own boolean reader; one that cannot be classified refuses (fail closed).
			if !hasValue {
				return causeConfigBare
			}
			switch gitBool(value) {
			case boolTrue:
				return causeConfigBare
			case boolUnknown:
				return causeConfigMalform
			}
		case section == "extensions" && key == "refstorage" && (!hasValue || strings.ToLower(strings.Trim(stripInlineComment(value), `"' \t`)) != "files"):
			return causeConfigReftable
		}
	}
	return ""
}

// sectionName is the lower-cased section name of a header body: the text up to
// the first space, quote or dot (the deprecated [section.subsection] form).
func sectionName(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.IndexAny(h, " \t\".'"); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

// splitKey splits "key = value" or a bare "key". The key is lower-cased.
func splitKey(line string) (key, value string, hasValue bool) {
	i := strings.IndexAny(line, "= \t")
	if i < 0 {
		return strings.ToLower(line), "", false
	}
	key = strings.ToLower(line[:i])
	rest := strings.TrimSpace(line[i:])
	if strings.HasPrefix(rest, "=") {
		return key, strings.TrimSpace(rest[1:]), true
	}
	if rest == "" || rest[0] == '#' || rest[0] == ';' {
		return key, "", false
	}
	return key, rest, false
}

type boolVerdict int

const (
	boolFalse boolVerdict = iota
	boolTrue
	boolUnknown // cannot be classified: the caller refuses
)

// gitBool classifies a config value the way git reads a boolean: an unquoted
// inline comment ("#" or ";") is dropped, the value may be double-quoted, the
// words true/yes/on and false/no/off are case-insensitive, and anything else is an
// integer in decimal, octal or hex with an optional k/m/g suffix, true when it is
// not zero. An empty value is classified true here (git reads it as false): the
// scan only ever errs towards refusing. A value that is none of these is unknown.
func gitBool(v string) boolVerdict {
	v = strings.TrimSpace(stripInlineComment(v))
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	switch strings.ToLower(v) {
	case "", "true", "yes", "on":
		return boolTrue
	case "false", "no", "off":
		return boolFalse
	}
	n := strings.ToLower(v)
	if last := n[len(n)-1]; last == 'k' || last == 'm' || last == 'g' {
		n = n[:len(n)-1]
	}
	if n == "" {
		return boolUnknown
	}
	// strconv.ParseInt with base 0 also reads spellings that git's own integer parser
	// does not (a digit separator "0_0", "0x0_0", "0b0_0") or reads only on some C
	// libraries (a "0b" or "0o" prefix). Anything the scan cannot be sure git accepts is
	// unclassifiable and refuses (review #247 P3-1).
	if strings.Contains(n, "_") {
		return boolUnknown
	}
	if digits := strings.TrimLeft(n, "+-"); strings.HasPrefix(digits, "0b") || strings.HasPrefix(digits, "0o") {
		return boolUnknown
	}
	if i, err := strconv.ParseInt(n, 0, 64); err == nil {
		if i == 0 {
			return boolFalse
		}
		return boolTrue
	}
	return boolUnknown
}

// stripInlineComment cuts v at the first "#" or ";" that is not inside double
// quotes. A backslash escapes the next character inside quotes.
func stripInlineComment(v string) string {
	inQuote := false
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c == '\\' && inQuote:
			i++
		case c == '"':
			inQuote = !inQuote
		case (c == '#' || c == ';') && !inQuote:
			return v[:i]
		}
	}
	return v
}
