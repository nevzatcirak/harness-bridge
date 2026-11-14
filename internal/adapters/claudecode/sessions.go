package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// MaxSessions is the ADR-012 ACL bound, reused for the allowlist (§6).
const MaxSessions = 1000

const maxSessionsFile = 1 << 20

// Session is one allowlist entry of BRIDGE_CLAUDE_SESSIONS (§6): an exact session
// ID, its approved root, and an optional label.
type Session struct {
	ID    string `json:"session_id"`
	Root  string `json:"root"`
	Label string `json:"label,omitempty"`
}

// UUID is the lowercase UUID of the entry (the ID was validated at load).
func (s Session) UUID() string { u, _ := ParseSessionID(s.ID); return u }

// ParseSessions decodes and validates the allowlist document: strict JSON (no
// unknown field, no key in another letter case, no duplicate key, no null, one document), at most MaxSessions entries, canonical
// `claudecode:<uuid>` IDs, no duplicates, absolute clean roots inside one of
// allowedRoots (BRIDGE_ALLOWED_ROOTS), printable labels. Any violation fails the
// whole file: startup refuses, nothing is partially loaded.
func ParseSessions(data []byte, allowedRoots []string) ([]Session, error) {
	if len(data) > maxSessionsFile {
		return nil, errors.New("claudecode: sessions file too large")
	}
	if err := strictSessionsDocument(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var list []Session
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("claudecode: sessions file: %w", err)
	}
	if dec.More() {
		return nil, errors.New("claudecode: sessions file has trailing data")
	}
	if len(list) > MaxSessions {
		return nil, fmt.Errorf("claudecode: more than %d sessions", MaxSessions)
	}
	for _, r := range allowedRoots {
		if cleanAbs(r) != nil {
			return nil, errors.New("claudecode: an allowed root is not absolute and clean")
		}
	}
	seen := map[string]bool{}
	for i, s := range list {
		uuid, err := ParseSessionID(s.ID)
		if err != nil || s.ID != SessionPrefix+uuid {
			return nil, fmt.Errorf("claudecode: session %d: invalid session_id", i)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("claudecode: session %d: duplicate session_id", i)
		}
		seen[s.ID] = true
		if cleanAbs(s.Root) != nil || s.Root == "/" || strings.ContainsAny(s.Root, "\x00\n\r") {
			return nil, fmt.Errorf("claudecode: session %d: root must be absolute and clean", i)
		}
		if !insideAny(s.Root, allowedRoots) {
			return nil, fmt.Errorf("claudecode: session %d: root is outside the allowed roots", i)
		}
		if len(s.Label) > 64 || s.Label != boundedASCII(s.Label, 64) || strings.ContainsAny(s.Label, "\x00\n\r\t") {
			return nil, fmt.Errorf("claudecode: session %d: invalid label", i)
		}
	}
	return list, nil
}

func insideAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}

// CheckSessionACL requires every allowlisted ID to also be in the per-session ACL
// (BRIDGE_ALLOWED_SESSIONS); the principal check is unchanged (§6).
func CheckSessionACL(list []Session, allowedSessions []string) error {
	acl := map[string]bool{}
	for _, a := range allowedSessions {
		acl[a] = true
	}
	for _, s := range list {
		if !acl[s.ID] {
			return fmt.Errorf("claudecode: %s is not in the allowed sessions", s.ID)
		}
	}
	return nil
}

// ValidateConfigFile applies the operator-file rule to a path before it is read:
// absolute and clean, a regular file that is not a symlink, owned by the service
// user, mode exactly 0600, one link, in directories (every ancestor) owned by
// root or the service user that others cannot write to.
func ValidateConfigFile(fsys FS, p string, serviceUID uint32) error {
	if err := cleanAbs(p); err != nil || p == "/" {
		return errors.New("claudecode: config file path must be absolute and clean")
	}
	info, err := fsys.Lstat(p)
	if err != nil {
		return fmt.Errorf("claudecode: config file: %w", err)
	}
	switch {
	case info.isSymlink():
		return errors.New("claudecode: config file is a symlink")
	case !info.isRegular():
		return errors.New("claudecode: config file is not a regular file")
	case info.UID != serviceUID:
		return errors.New("claudecode: config file is not owned by the service user")
	case info.Mode.Perm() != 0o600:
		return errors.New("claudecode: config file mode must be exactly 0600")
	case info.Nlink != 1:
		return errors.New("claudecode: config file has more than one link")
	}
	for _, d := range ancestors(path.Clean(p)) {
		di, err := fsys.Lstat(d)
		if err != nil {
			return fmt.Errorf("claudecode: config file ancestor: %w", err)
		}
		if di.isSymlink() || !di.isDir() {
			return errors.New("claudecode: config file ancestor is a symlink or not a directory")
		}
		if di.UID != 0 && di.UID != serviceUID {
			return errors.New("claudecode: config file ancestor has a foreign owner")
		}
		if di.Mode.Perm()&0o022 != 0 && !(di.Mode&fs.ModeSticky != 0 && di.UID == 0) {
			return errors.New("claudecode: config file ancestor is writable by others")
		}
	}
	return nil
}

var sessionKeys = map[string]bool{"session_id": true, "root": true, "label": true}

// strictSessionsDocument closes what encoding/json leaves open for a security allowlist
// (#236 P3-6, the class of ADR-022 review #215 P3-3): Go matches struct keys
// case-insensitively and lets the last duplicate win, and accepts `null` as an empty list.
// Here the document must be a JSON array (never null) of objects whose keys are exactly
// session_id, root and label, each at most once, spelled exactly; an entry must be an object.
func strictSessionsDocument(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("claudecode: sessions file: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return errors.New("claudecode: sessions file must be a JSON array (not null, not an object)")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("claudecode: sessions file: %w", err)
		}
		if d, ok := tok.(json.Delim); !ok || d != '{' {
			return errors.New("claudecode: every sessions entry must be an object")
		}
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("claudecode: sessions file: %w", err)
			}
			key, _ := kt.(string)
			if !sessionKeys[key] {
				return fmt.Errorf("claudecode: sessions entry has an unknown or mis-cased key")
			}
			if seen[key] {
				return fmt.Errorf("claudecode: sessions entry repeats a key")
			}
			seen[key] = true
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return fmt.Errorf("claudecode: sessions file: %w", err)
			}
		}
		if _, err := dec.Token(); err != nil { // the closing brace
			return fmt.Errorf("claudecode: sessions file: %w", err)
		}
	}
	if _, err := dec.Token(); err != nil { // the closing bracket
		return fmt.Errorf("claudecode: sessions file: %w", err)
	}
	return nil
}
