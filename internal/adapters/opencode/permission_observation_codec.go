package opencode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

const permissionPayloadLimit = 64 << 10
const permissionSnapshotLimit = 1 << 20

type nativePermissionTool struct {
	MessageID string `json:"messageID"`
	CallID    string `json:"callID"`
}
type nativePermissionRequest struct {
	ID         string                `json:"id"`
	SessionID  string                `json:"sessionID"`
	Permission string                `json:"permission"`
	Patterns   []string              `json:"patterns"`
	Metadata   json.RawMessage       `json:"metadata"`
	Always     []string              `json:"always"`
	Tool       *nativePermissionTool `json:"tool,omitempty"`
}
type nativePermissionEvent struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}
type nativePermissionReply struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
	Reply     string `json:"reply"`
}

// strictNativeJSON bounds depth and rejects duplicate keys at EVERY object level.
// Native metadata remains opaque data, never an authoritative Go map model.
func strictNativeJSON(raw []byte, limit int) error {
	if len(raw) == 0 || len(raw) > limit {
		return ErrPermissionLimit
	}
	if !utf8.Valid(raw) {
		return ErrPermissionMalformed
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return ErrPermissionLimit
		}
		t, err := d.Token()
		if err != nil {
			return ErrPermissionMalformed
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return ErrPermissionMalformed
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return ErrPermissionMalformed
				}
				seen[key] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			if end, e := d.Token(); e != nil || end != json.Delim('}') {
				return ErrPermissionMalformed
			}
		case json.Delim('['):
			for d.More() {
				if e := value(depth + 1); e != nil {
					return e
				}
			}
			if end, e := d.Token(); e != nil || end != json.Delim(']') {
				return ErrPermissionMalformed
			}
		case json.Delim('}'), json.Delim(']'):
			return ErrPermissionMalformed
		}
		return nil
	}
	if e := value(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrPermissionMalformed
	}
	return nil
}
func opaquePermissionID(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, b := range []byte(s) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("_-.:", rune(b))) {
			return false
		}
	}
	return true
}
func decodePermissionRequest(raw []byte) (nativePermissionRequest, string, error) {
	var r nativePermissionRequest
	if e := strictNativeJSON(raw, permissionPayloadLimit); e != nil {
		return r, "", e
	}
	// Exact versioned request field set; metadata's arbitrary JSON is data only.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return r, "", ErrPermissionMalformed
	}
	for _, k := range []string{"id", "sessionID", "permission", "patterns", "metadata", "always"} {
		if v, ok := fields[k]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return r, "", ErrPermissionMalformed
		}
	}
	if len(fields) != 6 && len(fields) != 7 || len(fields) == 7 && fields["tool"] == nil {
		return r, "", ErrPermissionMalformed
	}
	// encoding/json silently converts null array members to an empty Go string.
	// Validate the wire types before that coercion can collapse scope evidence.
	for _, key := range []string{"patterns", "always"} {
		var members []json.RawMessage
		if json.Unmarshal(fields[key], &members) != nil || members == nil {
			return r, "", ErrPermissionMalformed
		}
		for _, member := range members {
			if raw := bytes.TrimSpace(member); len(raw) == 0 || raw[0] != '"' {
				return r, "", ErrPermissionMalformed
			}
		}
	}
	if rawTool := fields["tool"]; rawTool != nil {
		tool, err := permissionProjection(rawTool, []string{"messageID", "callID"}, []string{"messageID", "callID"})
		if err != nil || len(tool) != 2 {
			return r, "", ErrPermissionMalformed
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || !ValidID(r.ID, "per") || !ValidID(r.SessionID, "ses") || len(r.Permission) == 0 || len(r.Permission) > 128 || r.Patterns == nil || r.Always == nil || len(r.Patterns) > 64 || len(r.Always) > 64 {
		return r, "", ErrPermissionMalformed
	}
	if r.Tool != nil && (!ValidID(r.Tool.MessageID, "msg") || !opaquePermissionID(r.Tool.CallID)) || fields["tool"] != nil && r.Tool == nil {
		return r, "", ErrPermissionMalformed
	}
	for _, s := range append(append([]string{}, r.Patterns...), r.Always...) {
		if len(s) > 4096 {
			return r, "", ErrPermissionLimit
		}
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(r.Metadata, &meta) != nil || meta == nil {
		return r, "", ErrPermissionMalformed
	}
	// Sorting JSON object members gives deterministic scope bytes; array order
	// and json.Number spellings are preserved (alternate numbers may be stale).
	var data any
	md := json.NewDecoder(bytes.NewReader(r.Metadata))
	md.UseNumber()
	if md.Decode(&data) != nil {
		return r, "", ErrPermissionMalformed
	}
	r.Metadata, _ = json.Marshal(data)
	canonical, _ := json.Marshal(r)
	h := sha256.Sum256(append([]byte("HB-OPENCODE-PERMISSION-OBS-v1\x00"), canonical...))
	return r, hex.EncodeToString(h[:]), nil
}
func safePermissionKind(s string) string {
	switch s {
	case "read", "edit", "write", "bash", "external_directory", "doom_loop", "webfetch", "websearch", "task", "glob", "grep":
		return s
	default:
		return "unrecognized"
	}
}

// Projection decoders may ignore native fields irrelevant to correlation, but
// never accept encoding/json's case-folded aliases for identity-bearing keys.
func permissionProjection(raw []byte, required, critical []string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, ErrPermissionMalformed
	}
	for _, k := range required {
		if v, ok := m[k]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, ErrPermissionMalformed
		}
	}
	for k := range m {
		for _, identity := range critical {
			if k != identity && strings.EqualFold(k, identity) {
				return nil, ErrPermissionMalformed
			}
		}
	}
	return m, nil
}

func validatePermissionMessage(raw []byte) error {
	m, err := permissionProjection(raw, []string{"info", "parts"}, []string{"info", "parts"})
	if err != nil {
		return err
	}
	info, err := permissionProjection(m["info"], []string{"id", "role", "sessionID"}, []string{"id", "role", "sessionID", "parentID", "path"})
	if err != nil {
		return err
	}
	if p := info["path"]; p != nil {
		if _, err = permissionProjection(p, []string{"cwd"}, []string{"cwd", "root"}); err != nil {
			return err
		}
	}
	var parts []json.RawMessage
	if json.Unmarshal(m["parts"], &parts) != nil || parts == nil || len(parts) > 256 {
		return ErrPermissionMalformed
	}
	for _, raw := range parts {
		p, e := permissionProjection(raw, []string{"type"}, []string{"id", "sessionID", "messageID", "type", "callID", "tool", "state"})
		if e != nil {
			return e
		}
		var kind string
		if json.Unmarshal(p["type"], &kind) != nil {
			return ErrPermissionMalformed
		}
		if kind == "tool" {
			p, e = permissionProjection(raw, []string{"id", "sessionID", "messageID", "callID", "tool", "state"}, []string{})
			if e != nil {
				return e
			}
			if _, e = permissionProjection(p["state"], []string{"status"}, []string{"status"}); e != nil {
				return e
			}
		}
	}
	return nil
}
