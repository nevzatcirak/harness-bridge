package lifecycle

import (
	"bytes"
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
		case section == "core" && key == "bare" && (!hasValue || truthy(value)):
			return causeConfigBare
		case section == "extensions" && key == "refstorage" && (!hasValue || strings.ToLower(strings.Trim(value, `"' `)) != "files"):
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

func truthy(v string) bool {
	switch strings.ToLower(strings.Trim(v, `"' `)) {
	case "", "true", "yes", "on", "1":
		return true
	}
	return false
}
