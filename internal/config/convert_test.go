package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// JSONValue and SameValue are the whole conversion of `cirak config import`: the environment form of a key and
// its file form are the same setting, written two ways.

func convFile(t *testing.T, k Key, v any) *File {
	t.Helper()
	tree := map[string]any{}
	parts := strings.Split(k.Path, ".")
	m := tree
	for _, p := range parts[:len(parts)-1] {
		next := map[string]any{}
		m[p] = next
		m = next
	}
	m[parts[len(parts)-1]] = v
	b, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(b)
	if err != nil {
		t.Fatalf("%s: %v\n%s", k.Path, err, b)
	}
	return f
}

// sampleEnv is an environment string of every kind of key, in the form the runtime reads.
func sampleEnv(k Key) string {
	switch k.Kind {
	case Bool:
		return "1"
	case Int:
		return "42"
	case List:
		switch k.List {
		case JSONArray:
			return `["a","b"]`
		case Comma:
			return "ses_a,ses_b"
		}
		return "/opt/a" + string(os.PathListSeparator) + "/opt/b"
	}
	switch {
	case k.AbsPath:
		return "/opt/x/value"
	case k.Path == "server.public_origin":
		return "https://bridge.example"
	}
	return "value"
}

func TestJSONValueRoundTripsEveryKeyThroughTheFile(t *testing.T) {
	for _, k := range Keys {
		ev := sampleEnv(k)
		v, err := JSONValue(k, ev)
		if err != nil {
			t.Errorf("%s: %v", k.Path, err)
			continue
		}
		f := convFile(t, k, v)
		if got := f.EnvForm(k.Path); got != ev {
			t.Errorf("%s: env %q became file form %q", k.Path, ev, got)
		}
		if !SameValue(k, ev, f) {
			t.Errorf("%s: SameValue is false for the value that was just converted", k.Path)
		}
	}
}

func TestJSONValueRefusesAValueTheFileCannotHoldTheSameWay(t *testing.T) {
	get := func(path string) Key { k, _ := Lookup(path); return k }
	for _, tc := range []struct {
		path, ev string
	}{
		{"server.read_only", "YES-CANARY"}, {"server.read_only", "true"}, {"server.read_only", "2"},
		{"limits.claude_max_turns", "NOT-AN-INT-CANARY"}, {"limits.claude_max_turns", "1.5"},
		{"dashboard.redact_extra", "NOT-JSON-CANARY"}, {"dashboard.redact_extra", `[1,2]`},
	} {
		if _, err := JSONValue(get(tc.path), tc.ev); err == nil {
			t.Errorf("%s=%q was accepted", tc.path, tc.ev)
		} else if len(tc.ev) > 4 && strings.Contains(err.Error(), tc.ev) {
			t.Errorf("%s: the error echoes the value: %v", tc.path, err)
		}
	}
	// empty parts of a list are dropped the way the runtime drops them
	v, err := JSONValue(get("acl.sessions"), "ses_a,,ses_b,")
	if got, _ := v.([]string); err != nil || len(got) != 2 || got[0] != "ses_a" || got[1] != "ses_b" {
		t.Errorf("list parts: %v %v", v, err)
	}
	if v, _ := JSONValue(get("server.read_only"), "0"); v != false {
		t.Errorf("0 is %v, want false", v)
	}
}

func TestSameValueIsFalseForAKeyTheFileDoesNotHold(t *testing.T) {
	k, _ := Lookup("server.listen")
	if SameValue(k, "127.0.0.1:1", &File{Values: map[string]Value{}}) || SameValue(k, "127.0.0.1:1", nil) {
		t.Error("a missing key compared equal")
	}
	f := convFile(t, k, "127.0.0.1:1")
	if !SameValue(k, "127.0.0.1:1", f) || SameValue(k, "127.0.0.1:2", f) {
		t.Error("the comparison is wrong")
	}
	// an explicit unset in the file ("" or []) is a value that differs from a set environment value
	lk, _ := Lookup("acl.roots")
	if SameValue(lk, "/opt/a", convFile(t, lk, []string{})) {
		t.Error("an explicit unset equals a set value")
	}
}
