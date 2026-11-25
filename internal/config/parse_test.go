package config

import (
	"os"
	"strings"
	"testing"
)

const goodUUID = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"

func TestParseAcceptsTheDocumentedShape(t *testing.T) {
	f, err := Parse([]byte(`{
	  "server": {"listen": "127.0.0.1:8080", "read_only": false, "public_origin": "", "principal": "", "tls": {"cert_file": "", "key_file": ""}},
	  "token": {"file": "/etc/cirak/token", "credential": "", "expires": "2026-11-28T13:07:47Z"},
	  "harness": {"opencode": {"url": "http://127.0.0.1:4096", "username": "", "password_file": "/etc/cirak/opencode-password", "password_credential": ""},
	              "claude": {"command": "", "sessions_file": "", "versions": "", "history_dir": "", "home": "", "model": "", "lang": "C.UTF-8"}},
	  "acl": {"roots": ["/opt/harness-bridge-test/workspace"], "sessions": ["ses_example", "claudecode:` + goodUUID + `"]},
	  "writer": {"lock_dir": "/var/lib/cirak/writer-locks", "admin_socket": "/run/cirak-admin/admin.sock"},
	  "lifecycle": {"config_file": "/etc/cirak/lifecycle.json"}, "overlay": {"dir": "/var/lib/cirak/overlay"},
	  "permission": {"relay": "off", "incarnation": "off"},
	  "dashboard": {"socket": "/run/cirak-dashboard/dash.sock", "addr": "", "redact_extra": []},
	  "limits": {"token_max_lifetime": "168h", "claude_max_turns": 20, "claude_max_budget_usd": "", "claude_run_max": "15m"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Values) != len(Keys) {
		t.Fatalf("the documented example sets every key: %d of %d", len(f.Values), len(Keys))
	}
	for path, want := range map[string]string{
		"server.read_only": "0", "limits.claude_max_turns": "20", "acl.sessions": "ses_example,claudecode:" + goodUUID,
		"acl.roots": "/opt/harness-bridge-test/workspace", "dashboard.redact_extra": "", "token.expires": "2026-11-28T13:07:47Z",
	} {
		if got := f.EnvForm(path); got != want {
			t.Errorf("EnvForm(%s) = %q, want %q", path, got, want)
		}
	}
	if !f.Has("server.public_origin") || f.EnvForm("server.public_origin") != "" {
		t.Error("an empty string is a present explicit unset")
	}
	if f.Has("nope") || (*File)(nil).Has("server.listen") {
		t.Error("Has on a missing key or a nil file")
	}
}

func TestParseRefusals(t *testing.T) {
	big := `{"server":{"listen":"` + strings.Repeat("a", MaxFileSize) + `"}}`
	cases := []struct{ name, in, want string }{
		{"unknown top key", `{"surprise":{}}`, `unknown config key "surprise"`},
		{"unknown nested key", `{"server":{"nope":1}}`, `unknown config key "server.nope"`},
		{"inline token key", `{"token":{"value":"x"}}`, `unknown config key "token.value"`},
		{"inline password key", `{"harness":{"opencode":{"password":"x"}}}`, `unknown config key "harness.opencode.password"`},
		{"duplicate top key", `{"server":{},"server":{}}`, `repeats the key "server"`},
		{"duplicate nested key", `{"server":{"listen":"a","listen":"b"}}`, `repeats the key "listen"`},
		{"wrong type string", `{"server":{"listen":1}}`, "must be a string"},
		{"wrong type bool", `{"server":{"read_only":"true"}}`, "must be true or false"},
		{"wrong type int", `{"limits":{"claude_max_turns":"20"}}`, "must be an integer"},
		{"float int", `{"limits":{"claude_max_turns":2.5}}`, "must be an integer"},
		{"wrong type list", `{"acl":{"roots":"/a"}}`, "must be an array of strings"},
		{"list element type", `{"acl":{"roots":[1]}}`, "must be an array of strings"},
		{"null", `{"server":{"listen":null}}`, "must be a string"},
		{"section not object", `{"server":"x"}`, `config key "server" must be an object`},
		{"key is an object", `{"server":{"listen":{}}}`, "must be a string"},
		{"trailing document", `{} {}`, "data after the JSON object"},
		{"trailing garbage", `{}x`, "data after the JSON object"},
		{"not an object", `[]`, "must be one JSON object"},
		{"empty", ``, "not valid JSON"},
		{"comment", "{ // c\n}", "not valid JSON"},
		{"truncated", `{"server":`, "not valid JSON"},
		{"over 1 MiB", big, "larger than 1 MiB"},
		{"relative path", `{"token":{"file":"token"}}`, "absolute clean path"},
		{"unclean path", `{"token":{"file":"/etc/../etc/token"}}`, "absolute clean path"},
		{"relative root", `{"acl":{"roots":["relative"]}}`, "absolute clean path"},
		{"control character", `{"server":{"principal":"a\nb"}}`, "control character"},
		{"empty list entry", `{"acl":{"sessions":[""]}}`, "empty entry"},
		{"separator in session", `{"acl":{"sessions":["ses_a,ses_b"]}}`, `containing ","`},
		{"separator in root", `{"acl":{"roots":["/a:/b"]}}`, `containing ":"`},
		{"opencode prefix", `{"acl":{"sessions":["opencode:ses_abc"]}}`, "bare native ID"},
		{"uppercase claude uuid", `{"acl":{"sessions":["claudecode:0A1B2C3D-4E5F-4A6B-8C7D-9E0F1A2B3C4D"]}}`, "lower-case uuid"},
		{"unknown id shape", `{"acl":{"sessions":["thread_1"]}}`, "ses_... or claudecode"},
		{"too deep", strings.Repeat(`{"a":`, 20) + `1` + strings.Repeat(`}`, 20), "nested too deeply"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

// A refusal names keys, never a value: the value of a rejected secret-bearing key stays out of the message.
func TestParseErrorsCarryNoValue(t *testing.T) {
	canary := "CANARY-VALUE-0123456789abcdef"
	for _, in := range []string{
		`{"token":{"value":"` + canary + `"}}`, `{"server":{"listen":["` + canary + `"]}}`, `{"limits":{"claude_max_turns":"` + canary + `"}}`,
		`{"acl":{"sessions":["` + canary + `"]}}`, `{"token":{"file":"` + canary + `"}}`, `{"server":{"principal":"` + canary + `\n"}}`,
	} {
		if _, err := Parse([]byte(in)); err == nil || strings.Contains(err.Error(), canary) {
			t.Errorf("%s: %v", in, err)
		}
	}
}

func TestEveryKeyIsInTheSchemaOnceWithADistinctEnvName(t *testing.T) {
	seenPath, seenEnv := map[string]bool{}, map[string]bool{}
	for _, k := range Keys {
		if seenPath[k.Path] || seenEnv[k.Env] {
			t.Fatalf("duplicate %s / %s", k.Path, k.Env)
		}
		seenPath[k.Path], seenEnv[k.Env] = true, true
		if got, ok := Lookup(k.Path); !ok || got.Env != k.Env {
			t.Errorf("Lookup(%s)", k.Path)
		}
	}
	if len(Keys) != 35 {
		t.Errorf("ADR-027 section 1.3 has 35 keys, the schema has %d", len(Keys))
	}
	// Exactly one key is display-only (section 1.2).
	var open []string
	for _, k := range Keys {
		if !k.Guarded {
			open = append(open, k.Path)
		}
	}
	if len(open) != 1 || open[0] != "harness.claude.lang" {
		t.Errorf("the only unguarded key must be harness.claude.lang, got %v", open)
	}
	if _, ok := Lookup("token.value"); ok {
		t.Error("an inline-secret key exists")
	}
}

func TestListFormsMatchTheRuntime(t *testing.T) {
	f, err := Parse([]byte(`{"dashboard":{"redact_extra":["a","b\"c"]},"acl":{"roots":["/a","/b"],"sessions":["ses_a","ses_b"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.EnvForm("acl.roots"); got != "/a"+string(os.PathListSeparator)+"/b" {
		t.Errorf("roots: %q", got)
	}
	if got := f.EnvForm("acl.sessions"); got != "ses_a,ses_b" {
		t.Errorf("sessions: %q", got)
	}
	if got := f.EnvForm("dashboard.redact_extra"); got != `["a","b\"c"]` {
		t.Errorf("redact_extra is a JSON array: %q", got)
	}
}
