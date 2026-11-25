package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Source says where an effective value came from.
type Source string

const (
	FromFlag    Source = "flag"
	FromEnv     Source = "environment"
	FromFile    Source = "config file"
	FromDefault Source = "default"
)

// Input is everything Resolve decides from. Nothing is read from the process.
type Input struct {
	// Env is the environment with the ADR-026 rule already applied (canonical CIRAK_* names; an empty value
	// counts as unset). It also supplies the names that are not keys (CREDENTIALS_DIRECTORY, inline secrets).
	Env func(string) string
	// File is the parsed config file, nil when none is in use.
	File *File
	// Flags holds the flag values by key path, in the environment form (a bool flag is "1").
	Flags map[string]string
}

// Result is the effective configuration.
type Result struct {
	// Get is the environment function the runtime validators read: the effective value of every key under
	// its canonical name, and any other name from the environment unchanged.
	Get func(string) string
	// Source is the winning source of every key, by key path.
	Source map[string]Source
	// Warnings are one line each and never contain a value.
	Warnings []string
	// Unresolved lists the credential keys that name a systemd credential while $CREDENTIALS_DIRECTORY is
	// unset: a check outside systemd cannot read them (unverified), a service start refuses.
	Unresolved []string
}

var credentialName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)

// Resolve applies the precedence of ADR-027 section 1.2 per key: flag, then environment, then file, then the
// default (the runtime's own). A guarded key set in both the environment and the file refuses to resolve when
// the values differ, naming the key and the two sources, never a value; equal values warn once. The one key
// that is not guarded, harness.claude.lang, is display-only and lets the environment win.
func Resolve(in Input) (*Result, error) {
	env := in.Env
	if env == nil {
		env = func(string) string { return "" }
	}
	values := make(map[string]string, len(Keys))
	res := &Result{Source: make(map[string]Source, len(Keys))}
	var conflicts []string
	for _, k := range Keys {
		ev := env(k.Env)
		has := in.File.Has(k.Path)
		fv := in.File.EnvForm(k.Path)
		flag, flagged := in.Flags[k.Path]
		switch {
		case flagged:
			values[k.Env], res.Source[k.Path] = flag, FromFlag
		case ev != "" && has:
			switch {
			case !k.Guarded:
				values[k.Env], res.Source[k.Path] = ev, FromEnv
			case sameValue(k, ev, in.File.Values[k.Path]):
				values[k.Env], res.Source[k.Path] = ev, FromEnv
				res.Warnings = append(res.Warnings, fmt.Sprintf("config key %s is set in both %s and the config file with the same value; set it in one place", k.Path, k.Env))
			default:
				conflicts = append(conflicts, fmt.Sprintf("config key %s: %s and the config file are both set with different values; set it in only one place", k.Path, k.Env))
			}
		case ev != "":
			values[k.Env], res.Source[k.Path] = ev, FromEnv
		case has:
			values[k.Env], res.Source[k.Path] = fv, FromFile
		default:
			values[k.Env], res.Source[k.Path] = "", FromDefault
		}
	}
	if len(conflicts) > 0 {
		return nil, errors.New(strings.Join(conflicts, "; "))
	}
	if err := secretSource(env, values, res, "token", "CIRAK_TOKEN_FILE", "CIRAK_TOKEN_CREDENTIAL", EnvInlineToken, "token.credential"); err != nil {
		return nil, err
	}
	if err := secretSource(env, values, res, "harness.opencode.password", "OPENCODE_SERVER_PASSWORD_FILE", "CIRAK_OPENCODE_PASSWORD_CREDENTIAL", EnvInlinePassword, "harness.opencode.password_credential"); err != nil {
		return nil, err
	}
	res.Get = func(name string) string {
		if v, ok := values[name]; ok {
			return v
		}
		return env(name)
	}
	return res, nil
}

// secretSource enforces "a file, a credential and an inline secret are mutually exclusive" over the
// EFFECTIVE values and turns a credential name into the file the existing readSecret rule reads:
// $CREDENTIALS_DIRECTORY/<name>, only when systemd set the directory, never anywhere else.
func secretSource(env func(string) string, values map[string]string, res *Result, what, fileEnv, credEnv, inlineEnv, credPath string) error {
	set := 0
	for _, v := range []string{values[fileEnv], values[credEnv], env(inlineEnv)} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		return fmt.Errorf("%s: the file, the credential and the inline %s are mutually exclusive", what, inlineEnv)
	}
	name := values[credEnv]
	if name == "" {
		return nil
	}
	if !credentialName.MatchString(name) || strings.Trim(name, ".") == "" {
		return fmt.Errorf("config key %s must be a credential name of [A-Za-z0-9._-]", credPath)
	}
	dir := env(EnvCredentialsDir)
	if dir == "" {
		res.Unresolved = append(res.Unresolved, credPath)
		return nil
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New(EnvCredentialsDir + " must be an absolute canonical path")
	}
	values[fileEnv] = filepath.Join(dir, name)
	return nil
}

// sameValue compares an environment string with a file value the way the runtime reads it: lists element by
// element (a separator inside the environment string splits it), everything else exactly (no trimming, no
// case folding). It never looks at the other key.
func sameValue(k Key, ev string, fv Value) bool {
	switch k.Kind {
	case List:
		if k.List == JSONArray {
			var got []string
			if json.Unmarshal([]byte(ev), &got) != nil {
				return false
			}
			return equalLists(got, fv.List)
		}
		var got []string
		for _, p := range strings.Split(ev, k.separator()) {
			if p != "" {
				got = append(got, p)
			}
		}
		return equalLists(got, fv.List)
	case Bool:
		return (ev == "1" && fv.Bool) || (ev == "0" && !fv.Bool)
	case Int:
		return ev == strconv.FormatInt(fv.Int, 10)
	}
	return ev == fv.Str
}

func equalLists(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Effective is one line of print-effective: the key, where it came from, and the value, except that a secret
// reference is shown as "set" or "unset" and nothing else.
type Effective struct {
	Key    string
	Source Source
	Value  string
}

// Describe lists every key in schema order. A secret key shows only whether it is set.
func (r *Result) Describe() []Effective {
	out := make([]Effective, 0, len(Keys))
	for _, k := range Keys {
		v := r.Get(k.Env)
		if k.Secret {
			if v == "" {
				v = "unset"
			} else {
				v = "set"
			}
		} else if v == "" {
			v = k.Default
			if v == "" {
				v = "unset"
			}
		} else if k.URL {
			v = redactURL(v)
		}
		out = append(out, Effective{Key: k.Path, Source: r.Source[k.Path], Value: displayValue(v)})
	}
	return out
}

// redactURL is the only form in which a URL-valued key is listed: scheme and host, with any userinfo shown as
// "***@", and a note when a path, query or fragment was left out (they can carry a token). A value that is not
// a plain URL is never echoed.
func redactURL(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return "set (not shown: not a valid URL)"
	}
	out := u.Scheme + "://"
	if u.User != nil {
		out += "***@"
	}
	out += u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		out += " (path or query not shown)"
	}
	return out
}

// displayValue makes a value safe to put on one line of a report: a value with a control character, an
// unprintable character or a line separator (a value from the environment is not checked like the file's)
// is shown quoted, so it cannot forge a line.
func displayValue(v string) string {
	for _, r := range v {
		if !unicode.IsPrint(r) {
			return strconv.Quote(v)
		}
	}
	return v
}

// SortedUnresolved is the unresolved list in a stable order.
func (r *Result) SortedUnresolved() []string {
	u := append([]string(nil), r.Unresolved...)
	sort.Strings(u)
	return u
}
