package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
)

// MaxFileSize is the largest config file accepted (ADR-027 section 1.3).
const MaxFileSize = 1 << 20

const maxDepth = 8

// Value is one key present in the file.
type Value struct {
	Str  string
	Bool bool
	Int  int64
	List []string
}

// File is a parsed, strictly validated config file. A key that is not in Values is missing (the default);
// "" and [] are present: an explicit unset (section 1.2).
type File struct {
	Values map[string]Value
}

// Has reports whether a key is present in the file.
func (f *File) Has(path string) bool {
	if f == nil {
		return false
	}
	_, ok := f.Values[path]
	return ok
}

// EnvForm is the environment string of a present key. A list is joined as the runtime reads it.
func (f *File) EnvForm(path string) string {
	k, ok := byPath[path]
	if !ok || f == nil {
		return ""
	}
	v, ok := f.Values[path]
	if !ok {
		return ""
	}
	switch k.Kind {
	case Bool:
		if v.Bool {
			return "1"
		}
		return "0"
	case Int:
		return strconv.FormatInt(v.Int, 10)
	case List:
		return joinList(*k, v.List)
	}
	return v.Str
}

func joinList(k Key, l []string) string {
	if len(l) == 0 {
		return ""
	}
	if k.List == JSONArray {
		b, _ := json.Marshal(l)
		return string(b)
	}
	return strings.Join(l, k.separator())
}

// Parse validates the bytes of a config file: at most 1 MiB, one JSON object and nothing after it, no
// duplicate key at any depth, no unknown key, no null, and every value of the type of its key. Messages
// name keys, never values.
func Parse(data []byte) (*File, error) {
	if len(data) > MaxFileSize {
		return nil, errors.New("config file is larger than 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	top, err := readValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("config file has data after the JSON object")
	}
	obj, ok := top.(map[string]any)
	if !ok {
		return nil, errors.New("config file must be one JSON object")
	}
	f := &File{Values: map[string]Value{}}
	if err := walk("", obj, tree, f); err != nil {
		return nil, err
	}
	return f, nil
}

// readValue decodes one JSON value, refusing a duplicate key in any object.
func readValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("config file is nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, errors.New("config file is not valid JSON")
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	switch d {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, errors.New("config file is not valid JSON")
			}
			key, ok := kt.(string)
			if !ok {
				return nil, errors.New("config file is not valid JSON")
			}
			if _, dup := obj[key]; dup {
				return nil, fmt.Errorf("config file repeats the key %q", key)
			}
			v, err := readValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			obj[key] = v
		}
		if _, err := dec.Token(); err != nil {
			return nil, errors.New("config file is not valid JSON")
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := readValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, errors.New("config file is not valid JSON")
		}
		return arr, nil
	}
	return nil, errors.New("config file is not valid JSON")
}

func walk(prefix string, obj map[string]any, n *node, f *File) error {
	names := make([]string, 0, len(obj))
	for name := range obj {
		names = append(names, name)
	}
	sort.Strings(names) // a deterministic first error
	for _, name := range names {
		raw := obj[name]
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		child, ok := n.children[name]
		if !ok {
			return fmt.Errorf("unknown config key %q", path)
		}
		if child.key == nil {
			sub, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("config key %q must be an object", path)
			}
			if err := walk(path, sub, child, f); err != nil {
				return err
			}
			continue
		}
		v, err := leaf(*child.key, raw)
		if err != nil {
			return err
		}
		f.Values[path] = v
	}
	return nil
}

func leaf(k Key, raw any) (Value, error) {
	wrong := func(want string) (Value, error) {
		return Value{}, fmt.Errorf("config key %q must be %s", k.Path, want)
	}
	switch k.Kind {
	case String:
		s, ok := raw.(string)
		if !ok {
			return wrong("a string")
		}
		if err := checkText(k, s); err != nil {
			return Value{}, err
		}
		return Value{Str: s}, nil
	case Bool:
		b, ok := raw.(bool)
		if !ok {
			return wrong("true or false")
		}
		return Value{Bool: b}, nil
	case Int:
		num, ok := raw.(json.Number)
		if !ok {
			return wrong("an integer")
		}
		n, err := strconv.ParseInt(num.String(), 10, 64)
		if err != nil {
			return wrong("an integer")
		}
		return Value{Int: n}, nil
	case List:
		arr, ok := raw.([]any)
		if !ok {
			return wrong("an array of strings")
		}
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			s, ok := e.(string)
			if !ok {
				return wrong("an array of strings")
			}
			if s == "" {
				return Value{}, fmt.Errorf("config key %q has an empty entry", k.Path)
			}
			if sep := k.separator(); sep != "" && strings.Contains(s, sep) {
				return Value{}, fmt.Errorf("config key %q has an entry containing %q", k.Path, sep)
			}
			if err := checkText(k, s); err != nil {
				return Value{}, err
			}
			if k.Path == "acl.sessions" {
				if err := ValidACLSession(s); err != nil {
					return Value{}, fmt.Errorf("config key %q: %w", k.Path, err)
				}
			}
			out = append(out, s)
		}
		return Value{List: out}, nil
	}
	return wrong("a supported type")
}

// checkText applies the rules that hold for every text of the file: no control character, and an absolute
// clean path where the key is a path (section 1.3).
func checkText(k Key, s string) error {
	if strings.ContainsAny(s, "\x00\r\n") {
		return fmt.Errorf("config key %q contains a control character", k.Path)
	}
	if k.AbsPath && s != "" && (!filepath.IsAbs(s) || filepath.Clean(s) != s) {
		return fmt.Errorf("config key %q must be an absolute clean path", k.Path)
	}
	return nil
}

// ValidACLSession checks an ACL session entry: the bare native ID. An OpenCode ID is ses_..., a Claude Code
// ID is claudecode:<uuid>; an opencode: prefix is not an ACL ID (the policy compares the bare ID, so such an
// entry would silently match nothing, ADR-027 section 1.3, P3-2). The two validators are the harness packages'.
func ValidACLSession(id string) error {
	switch {
	case strings.HasPrefix(id, "opencode:"):
		return errors.New("an ACL session ID is the bare native ID (ses_...), not opencode:ses_...")
	case strings.HasPrefix(id, claudecode.SessionPrefix):
		if _, err := claudecode.ParseSessionID(id); err != nil {
			return errors.New("a Claude Code ACL session ID must be claudecode:<lower-case uuid>")
		}
		return nil
	case opencode.ValidID(id, "ses_"):
		return nil
	}
	return errors.New("an ACL session ID must be ses_... or claudecode:<uuid>")
}
