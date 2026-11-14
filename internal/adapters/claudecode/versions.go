package claudecode

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// BRIDGE_CLAUDE_VERSIONS (ADR-021 §8.5, D14 Q7): an allowlist of EXACT claude_code_version
// strings. There is no range, no wildcard, no prefix and no default: until a slice-3 live
// acceptance has validated a version, the list is empty and the adapter refuses to start
// (fail closed). The machine compares the init version with the list by equality only.

// EnvVersions is the name of the operator setting. Reading it is wiring (a later slice);
// this package only parses its value.
const EnvVersions = "BRIDGE_CLAUDE_VERSIONS"

// MaxVersions bounds the list.
const MaxVersions = 16

var versionPattern = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

// ErrNoVersions is returned for an empty or missing BRIDGE_CLAUDE_VERSIONS.
var ErrNoVersions = errors.New("claudecode: BRIDGE_CLAUDE_VERSIONS is empty: no version is allowed")

// ParseVersions parses the comma-separated value of BRIDGE_CLAUDE_VERSIONS. Every entry
// must be exactly MAJOR.MINOR.PATCH (digits only); an empty entry, a duplicate, a
// wildcard, a range, a prefix, a pre-release suffix or whitespace inside an entry is an
// error. Surrounding ASCII spaces of an entry are not accepted either: the value is an
// operator file, not free text.
func ParseVersions(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, ErrNoVersions
	}
	parts := strings.Split(s, ",")
	if len(parts) > MaxVersions {
		return nil, fmt.Errorf("claudecode: more than %d versions", MaxVersions)
	}
	seen := map[string]bool{}
	for i, p := range parts {
		if !versionPattern.MatchString(p) {
			return nil, fmt.Errorf("claudecode: version %d is not an exact MAJOR.MINOR.PATCH string", i)
		}
		if seen[p] {
			return nil, fmt.Errorf("claudecode: version %d is a duplicate", i)
		}
		seen[p] = true
	}
	return parts, nil
}

// CheckVersions applies the same rules to an already split list (a Config value).
func CheckVersions(list []string) error {
	if len(list) == 0 {
		return ErrNoVersions
	}
	if len(list) > MaxVersions {
		return fmt.Errorf("claudecode: more than %d versions", MaxVersions)
	}
	// Each element on its own (#249 P3-2): joining first would let one element "a,b" pass as
	// two versions.
	seen := map[string]bool{}
	for i, v := range list {
		if !versionPattern.MatchString(v) {
			return fmt.Errorf("claudecode: version %d is not an exact MAJOR.MINOR.PATCH string", i)
		}
		if seen[v] {
			return fmt.Errorf("claudecode: version %d is a duplicate", i)
		}
		seen[v] = true
	}
	return nil
}
