package config

import (
	"os"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func fileOf(t *testing.T, body string) *File {
	t.Helper()
	f, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// sample is a valid file value, a different valid file value, and the environment strings that read as those.
type sample struct {
	json1, json2 string // the JSON of the key's value
	env1, env2   string // the environment form of the same values
}

func sampleFor(k Key) sample {
	switch k.Kind {
	case Bool:
		return sample{"true", "false", "1", "0"}
	case Int:
		return sample{"20", "30", "20", "30"}
	case List:
		switch k.List {
		case JSONArray:
			return sample{`["a"]`, `["b"]`, `["a"]`, `["b"]`}
		case Comma:
			return sample{`["ses_a"]`, `["ses_b"]`, "ses_a", "ses_b"}
		}
		return sample{`["/a"]`, `["/b"]`, "/a", "/b"}
	}
	if k.AbsPath {
		return sample{`"/a/one"`, `"/a/two"`, "/a/one", "/a/two"}
	}
	return sample{`"one"`, `"two"`, "one", "two"}
}

func fileWith(t *testing.T, k Key, jsonValue string) *File {
	t.Helper()
	// build {"a":{"b":{"c": value}}} from the dotted path
	parts := strings.Split(k.Path, ".")
	body := jsonValue
	for i := len(parts) - 1; i >= 0; i-- {
		body = `{"` + parts[i] + `":` + body + `}`
	}
	return fileOf(t, body)
}

// ADR-027 section 1.2, the empty-value table, for every guarded key and for the display-only one.
func TestPrecedenceTableForEveryKey(t *testing.T) {
	for _, k := range Keys {
		s := sampleFor(k)
		empty := `""`
		if k.Kind == List {
			empty = `[]`
		}
		if k.Kind == Bool || k.Kind == Int {
			empty = ""
		}
		get := func(env map[string]string, f *File) (string, Source, error) {
			r, err := Resolve(Input{Env: envMap(env), File: f})
			if err != nil {
				return "", "", err
			}
			return r.Get(k.Env), r.Source[k.Path], nil
		}
		t.Run(k.Path, func(t *testing.T) {
			// env unset or empty, file missing: the default
			for _, e := range []map[string]string{nil, {k.Env: ""}} {
				if v, src, err := get(e, nil); err != nil || v != "" || src != FromDefault {
					t.Fatalf("default: %q %q %v", v, src, err)
				}
			}
			// env unset or empty, file value: the file value
			for _, e := range []map[string]string{nil, {k.Env: ""}} {
				if v, src, err := get(e, fileWith(t, k, s.json1)); err != nil || v != s.env1 || src != FromFile {
					t.Fatalf("file only: %q %q %v", v, src, err)
				}
			}
			// env value, file missing: the env value
			if v, src, err := get(map[string]string{k.Env: s.env1}, nil); err != nil || v != s.env1 || src != FromEnv {
				t.Fatalf("env only: %q %q %v", v, src, err)
			}
			if empty != "" {
				// env unset, file "" or []: an explicit unset
				if v, src, err := get(nil, fileWith(t, k, empty)); err != nil || v != "" || src != FromFile {
					t.Fatalf("explicit unset: %q %q %v", v, src, err)
				}
				// env value, file "" or []: a refusal on a guarded key, the env value otherwise
				v, _, err := get(map[string]string{k.Env: s.env1}, fileWith(t, k, empty))
				if k.Guarded && err == nil {
					t.Fatalf("a guarded key accepted an env value against an explicit file unset: %q", v)
				}
				if !k.Guarded && (err != nil || v != s.env1) {
					t.Fatalf("display-only key: %q %v", v, err)
				}
			}
			// env value, file value, equal: the value with one warning (guarded); the env value (display-only)
			r, err := Resolve(Input{Env: envMap(map[string]string{k.Env: s.env1}), File: fileWith(t, k, s.json1)})
			if err != nil || r.Get(k.Env) != s.env1 {
				t.Fatalf("equal pair: %v", err)
			}
			if k.Guarded && len(r.Warnings) != 1 {
				t.Fatalf("equal pair must warn exactly once: %v", r.Warnings)
			}
			if !k.Guarded && len(r.Warnings) != 0 {
				t.Fatalf("display-only key warned: %v", r.Warnings)
			}
			// env value, file value, different: a refusal naming the key and the env name, never a value
			r, err = Resolve(Input{Env: envMap(map[string]string{k.Env: s.env2}), File: fileWith(t, k, s.json1)})
			if !k.Guarded {
				if err != nil || r.Get(k.Env) != s.env2 {
					t.Fatalf("display-only key must keep env-wins: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a differing environment/file pair was accepted")
			}
			for _, want := range []string{k.Path, k.Env} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
			for _, v := range []string{s.env1, s.env2} {
				if len(v) > 3 && strings.Contains(err.Error(), v) {
					t.Errorf("the refusal contains a value %q: %v", v, err)
				}
			}
		})
	}
}

func TestListsReplaceNeverMerge(t *testing.T) {
	r, err := Resolve(Input{Env: envMap(nil), File: fileOf(t, `{"acl":{"roots":["/a","/b"]}}`)})
	if err != nil || r.Get("CIRAK_ALLOWED_ROOTS") != "/a"+string(os.PathListSeparator)+"/b" {
		t.Fatalf("%q %v", r.Get("CIRAK_ALLOWED_ROOTS"), err)
	}
	// the same list written as an env string is equal, so it only warns; a subset or superset is a refusal
	sep := string(os.PathListSeparator)
	if _, err := Resolve(Input{Env: envMap(map[string]string{"CIRAK_ALLOWED_ROOTS": "/a" + sep + "/b"}), File: fileOf(t, `{"acl":{"roots":["/a","/b"]}}`)}); err != nil {
		t.Fatalf("an equal list: %v", err)
	}
	for _, env := range []string{"/a", "/a" + sep + "/b" + sep + "/c", "/b" + sep + "/a"} {
		if _, err := Resolve(Input{Env: envMap(map[string]string{"CIRAK_ALLOWED_ROOTS": env}), File: fileOf(t, `{"acl":{"roots":["/a","/b"]}}`)}); err == nil {
			t.Errorf("env list %q merged with or replaced the file list silently", env)
		}
	}
	// an env session list never takes elements from the file
	r, err = Resolve(Input{Env: envMap(map[string]string{"CIRAK_ALLOWED_SESSIONS": "ses_x"})})
	if err != nil || r.Get("CIRAK_ALLOWED_SESSIONS") != "ses_x" {
		t.Fatal(err)
	}
}

func TestEqualRedactListIsRecognised(t *testing.T) {
	f := fileOf(t, `{"dashboard":{"redact_extra":["a","b"]}}`)
	if r, err := Resolve(Input{Env: envMap(map[string]string{"CIRAK_DASHBOARD_REDACT_EXTRA": `["a", "b"]`}), File: f}); err != nil || len(r.Warnings) != 1 {
		t.Fatalf("a JSON array that differs only in spacing is equal: %v %v", err, r)
	}
	if _, err := Resolve(Input{Env: envMap(map[string]string{"CIRAK_DASHBOARD_REDACT_EXTRA": `not json`}), File: f}); err == nil {
		t.Fatal("an unparseable env list equals the file list")
	}
}

func TestFlagsWinOverEverything(t *testing.T) {
	env := envMap(map[string]string{"CIRAK_LISTEN": "127.0.0.1:2"})
	f := fileOf(t, `{"server":{"listen":"127.0.0.1:3"}}`)
	r, err := Resolve(Input{Env: env, File: f, Flags: map[string]string{"server.listen": "127.0.0.1:1"}})
	if err != nil || r.Get("CIRAK_LISTEN") != "127.0.0.1:1" || r.Source["server.listen"] != FromFlag {
		t.Fatalf("%q %v", r.Get("CIRAK_LISTEN"), err)
	}
	// without a flag the same env and file are a refusal: the flag is the operator's explicit choice, not a way around the guard
	if _, err := Resolve(Input{Env: env, File: f}); err == nil {
		t.Fatal("the differing pair was accepted")
	}
}

func TestNonKeyNamesPassThrough(t *testing.T) {
	r, err := Resolve(Input{Env: envMap(map[string]string{"CIRAK_TOKEN": "inline", "CREDENTIALS_DIRECTORY": "/run/credentials/x", "HOME": "/h"})})
	if err != nil || r.Get("CIRAK_TOKEN") != "inline" || r.Get("HOME") != "/h" {
		t.Fatalf("%v", err)
	}
	if r2, _ := Resolve(Input{}); r2.Get("ANYTHING") != "" {
		t.Fatal("nil environment")
	}
}

func TestSecretSourcesAreMutuallyExclusive(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		file string
	}{
		{"file+credential in the file", nil, `{"token":{"file":"/a/t","credential":"token"}}`},
		{"env file + file credential", map[string]string{"CIRAK_TOKEN_FILE": "/a/t"}, `{"token":{"credential":"token"}}`},
		{"inline + file file", map[string]string{"CIRAK_TOKEN": "inline"}, `{"token":{"file":"/a/t"}}`},
		{"inline + credential", map[string]string{"CIRAK_TOKEN": "inline", "CREDENTIALS_DIRECTORY": "/run/credentials/u"}, `{"token":{"credential":"token"}}`},
		{"opencode file+credential", nil, `{"harness":{"opencode":{"password_file":"/a/p","password_credential":"pw"}}}`},
		{"opencode inline + credential", map[string]string{"OPENCODE_SERVER_PASSWORD": "x"}, `{"harness":{"opencode":{"password_credential":"pw"}}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Resolve(Input{Env: envMap(c.env), File: fileOf(t, c.file)})
			if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
				t.Fatalf("%v", err)
			}
		})
	}
	// An empty value is unset: the H5 idiom of an empty inline twin next to a file is not a conflict.
	if _, err := Resolve(Input{Env: envMap(map[string]string{"CIRAK_TOKEN": "", "CIRAK_TOKEN_FILE": "/a/t"})}); err != nil {
		t.Fatalf("an empty inline token with a token file: %v", err)
	}
}

func TestCredentialResolvesOnlyUnderCredentialsDirectory(t *testing.T) {
	env := map[string]string{"CREDENTIALS_DIRECTORY": "/run/credentials/cirak.service"}
	r, err := Resolve(Input{Env: envMap(env), File: fileOf(t, `{"token":{"credential":"token"},"harness":{"opencode":{"password_credential":"opencode-pw"}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Get("CIRAK_TOKEN_FILE"); got != "/run/credentials/cirak.service/token" {
		t.Errorf("token path %q", got)
	}
	if got := r.Get("OPENCODE_SERVER_PASSWORD_FILE"); got != "/run/credentials/cirak.service/opencode-pw" {
		t.Errorf("password path %q", got)
	}
	for _, name := range []string{"../token", "a/b", "/abs", "..", ".", "...", "tok en", "tok\nen", "", strings.Repeat("a", 256), "tökén"} {
		body := `{"token":{"credential":"` + strings.NewReplacer("\n", `\n`, `"`, `\"`).Replace(name) + `"}}`
		f, perr := Parse([]byte(body))
		if perr != nil {
			continue
		}
		res, err := Resolve(Input{Env: envMap(env), File: f})
		if name == "" {
			if err != nil || res.Get("CIRAK_TOKEN_FILE") != "" {
				t.Errorf("an empty credential is an explicit unset: %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("credential name %q accepted: resolved to %q", name, res.Get("CIRAK_TOKEN_FILE"))
		}
	}
	for _, dir := range []string{"relative", "/run/credentials/../x", "/run//credentials"} {
		if _, err := Resolve(Input{Env: envMap(map[string]string{"CREDENTIALS_DIRECTORY": dir}), File: fileOf(t, `{"token":{"credential":"token"}}`)}); err == nil {
			t.Errorf("CREDENTIALS_DIRECTORY %q accepted", dir)
		}
	}
	// no systemd directory: unresolved, never guessed from anywhere else
	r, err = Resolve(Input{Env: envMap(nil), File: fileOf(t, `{"token":{"credential":"token"}}`)})
	if err != nil || r.Get("CIRAK_TOKEN_FILE") != "" || len(r.SortedUnresolved()) != 1 || r.SortedUnresolved()[0] != "token.credential" {
		t.Fatalf("unresolved credential: %q %v %v", r.Get("CIRAK_TOKEN_FILE"), r.Unresolved, err)
	}
}

func TestDescribeShowsSecretsOnlyAsSetOrUnset(t *testing.T) {
	canary := "/secret/path/CANARY-0123456789"
	r, err := Resolve(Input{Env: envMap(map[string]string{"CIRAK_TOKEN_FILE": canary, "OPENCODE_SERVER_PASSWORD_FILE": canary + "2", "CIRAK_TLS_KEY": canary + "3"}), File: fileOf(t, `{"server":{"listen":"127.0.0.1:7"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]Effective{}
	for _, e := range r.Describe() {
		seen[e.Key] = e
		if strings.Contains(e.Value, "CANARY") {
			t.Errorf("%s shows a secret reference: %q", e.Key, e.Value)
		}
	}
	if len(seen) != len(Keys) {
		t.Fatalf("Describe lists %d of %d keys", len(seen), len(Keys))
	}
	if seen["token.file"].Value != "set" || seen["token.credential"].Value != "unset" || seen["server.listen"].Value != "127.0.0.1:7" || seen["server.listen"].Source != FromFile {
		t.Errorf("%+v %+v %+v", seen["token.file"], seen["token.credential"], seen["server.listen"])
	}
	if seen["limits.claude_max_turns"].Value != "20" || seen["limits.claude_max_turns"].Source != FromDefault {
		t.Errorf("default shown: %+v", seen["limits.claude_max_turns"])
	}
}
