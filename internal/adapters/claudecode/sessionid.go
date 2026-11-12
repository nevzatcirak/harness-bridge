package claudecode

import (
	"errors"
	"regexp"
	"strings"
)

// SessionPrefix is the harness namespace of a Claude Code session ID (ADR-021 §9).
const SessionPrefix = "claudecode:"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidUUID reports whether s is a lowercase 8-4-4-4-12 hex UUID.
func ValidUUID(s string) bool { return len(s) == 36 && uuidPattern.MatchString(s) }

// ParseSessionID returns the UUID of a `claudecode:<uuid>` session ID. Anything
// else, including uppercase hex, surrounding space or a trailing newline, is
// refused: the UUID reaches argv and file names.
func ParseSessionID(id string) (string, error) {
	rest, ok := strings.CutPrefix(id, SessionPrefix)
	if !ok || !ValidUUID(rest) {
		return "", errors.New("claudecode: invalid session id")
	}
	return rest, nil
}

// FormatSessionID is the inverse of ParseSessionID.
func FormatSessionID(uuid string) (string, error) {
	if !ValidUUID(uuid) {
		return "", errors.New("claudecode: invalid session uuid")
	}
	return SessionPrefix + uuid, nil
}
