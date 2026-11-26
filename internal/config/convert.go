package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The environment form of a key and its config file form are the same setting written two ways. `cirak config
// import` (ADR-027 section 5) converts the first into the second; these two functions are the whole of that
// conversion, kept beside the schema so the rules cannot drift from Parse and Resolve.

// JSONValue converts the environment string of a key into the value a config file holds for it: a bool for
// "1"/"0", an integer, a list of strings split the way the runtime reads it, or the string itself. A value the
// runtime would not read the same way is an error that names the key, never the value.
func JSONValue(k Key, ev string) (any, error) {
	switch k.Kind {
	case Bool:
		switch ev {
		case "1":
			return true, nil
		case "0":
			return false, nil
		}
		return nil, fmt.Errorf("%s must be 0 or 1 to be written as %s", k.Env, k.Path)
	case Int:
		n, err := strconv.ParseInt(ev, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s must be an integer to be written as %s", k.Env, k.Path)
		}
		return n, nil
	case List:
		var out []string
		if k.List == JSONArray {
			if err := json.Unmarshal([]byte(ev), &out); err != nil {
				return nil, fmt.Errorf("%s must be a JSON array of strings to be written as %s", k.Env, k.Path)
			}
			return out, nil
		}
		for _, p := range strings.Split(ev, k.separator()) {
			if p != "" {
				out = append(out, p)
			}
		}
		return out, nil
	}
	return ev, nil
}

// SameValue reports whether an environment string and the value the file holds for the same key are the same
// setting, the way Resolve compares them (the guard of a differing env/file pair). The key must be present in
// the file; for a missing key it reports false.
func SameValue(k Key, ev string, f *File) bool {
	if !f.Has(k.Path) {
		return false
	}
	return sameValue(k, ev, f.Values[k.Path])
}
