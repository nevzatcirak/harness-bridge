package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// The unit's environment (ADR-027 section 3 item 6 and section 5): `doctor --from-unit <name>` reads what a
// systemd unit would run with, not the operator's shell. It parses `systemctl show -p Environment -p
// EnvironmentFiles -p UnsetEnvironment` and the files it names, merges them with systemd's order
// (EnvironmentFile= overrides Environment=; UnsetEnvironment= removes) and returns the paths of the files it
// read. A missing file is skipped only when its entry is "-"-prefixed (ignore_errors=yes); an unreadable or
// unparseable file, a line that is not NAME=VALUE, or an unexpanded systemd specifier (%d, %t, ...) is an ERROR,
// never an empty source or a guess. No value is ever included in an error or in the list of paths.

var (
	envAssign     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	envName       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	specifierLike = regexp.MustCompile(`%[A-Za-z]`)
)

// unitEnvironment is the merged environment of a unit.
type unitEnvironment struct {
	Vars      map[string]string
	FilesRead []string // paths of the EnvironmentFile entries actually read, in order
	Skipped   []string // "-"-prefixed entries that did not exist
}

// openFile reads a whole environment file; a variable so tests use temp files and nothing else.
type fileReader func(path string) ([]byte, error)

func osReadEnvFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// parseSystemctlShow reads the text of `systemctl show -p Environment -p EnvironmentFiles -p UnsetEnvironment`.
// The exact property formats were taken from systemd's documentation of `show`, not from a live host: UNVERIFIED.
func parseSystemctlShow(text string) (assign []string, files []envFileEntry, unset []string, err error) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, nil, nil, errors.New("systemctl show printed a line that is not a property")
		}
		switch k {
		case "Environment":
			words, werr := splitShellWords(v)
			if werr != nil {
				return nil, nil, nil, fmt.Errorf("Environment= of the unit: %w", werr)
			}
			assign = append(assign, words...)
		case "UnsetEnvironment":
			words, werr := splitShellWords(v)
			if werr != nil {
				return nil, nil, nil, fmt.Errorf("UnsetEnvironment= of the unit: %w", werr)
			}
			unset = append(unset, words...)
		case "EnvironmentFiles":
			fs, ferr := parseEnvironmentFiles(v)
			if ferr != nil {
				return nil, nil, nil, ferr
			}
			files = append(files, fs...)
		default:
			return nil, nil, nil, fmt.Errorf("systemctl show printed the unexpected property %s", k)
		}
	}
	return assign, files, unset, nil
}

// envFileEntry is one EnvironmentFile= entry. Optional is the "-" prefix: a missing file is skipped.
type envFileEntry struct {
	Path     string
	Optional bool
}

// parseEnvironmentFiles reads "EnvironmentFiles=/a (ignore_errors=no) /b (ignore_errors=yes)".
func parseEnvironmentFiles(v string) ([]envFileEntry, error) {
	var out []envFileEntry
	rest := strings.TrimSpace(v)
	for rest != "" {
		end := strings.Index(rest, " (ignore_errors=")
		if end < 0 {
			if strings.TrimSpace(rest) == "" {
				break
			}
			return nil, errors.New("EnvironmentFiles= of the unit is not in the form PATH (ignore_errors=yes|no)")
		}
		path := strings.TrimSpace(rest[:end])
		after := rest[end+len(" (ignore_errors="):]
		close := strings.Index(after, ")")
		if close < 0 {
			return nil, errors.New("EnvironmentFiles= of the unit has an unterminated ignore_errors")
		}
		flag := after[:close]
		if flag != "yes" && flag != "no" {
			return nil, errors.New("EnvironmentFiles= of the unit has an unknown ignore_errors value")
		}
		if specifierLike.MatchString(path) {
			return nil, errors.New("an EnvironmentFile= path of the unit holds an unexpanded systemd specifier")
		}
		if !strings.HasPrefix(path, "/") {
			return nil, errors.New("an EnvironmentFile= path of the unit is not absolute")
		}
		out = append(out, envFileEntry{Path: path, Optional: flag == "yes"})
		rest = strings.TrimSpace(after[close+1:])
	}
	return out, nil
}

// splitShellWords splits systemd's printed Environment= value: words separated by spaces, a word may be wrapped
// in double quotes (a backslash escapes a quote or a backslash).
func splitShellWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	in, started := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case in && c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '"':
			in, started = !in, true
		case c == ' ' && !in:
			if started {
				words = append(words, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if in {
		return nil, errors.New("an unterminated quote")
	}
	if started {
		words = append(words, cur.String())
	}
	return words, nil
}

// parseEnvironmentFile parses the lines of an EnvironmentFile: NAME=VALUE, comments (# or ;) and blank lines.
// A value may be wrapped in single or double quotes. A line that is not an assignment, or a value that still holds
// a systemd specifier, is an error that names the line number, never the line.
func parseEnvironmentFile(data []byte) (map[string]string, error) {
	out := map[string]string{}
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if !envAssign.MatchString(line) {
			return nil, fmt.Errorf("line %d is not a NAME=VALUE assignment", n+1)
		}
		k, v, _ := strings.Cut(line, "=")
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if strings.Contains(strings.ReplaceAll(v, "%%", ""), "%") && specifierLike.MatchString(strings.ReplaceAll(v, "%%", "")) {
			return nil, fmt.Errorf("line %d holds an unexpanded systemd specifier", n+1)
		}
		out[k] = strings.ReplaceAll(v, "%%", "%")
	}
	return out, nil
}

// loadUnitEnvironment merges Environment=, then the EnvironmentFile= files (which override), then removes
// UnsetEnvironment=. read is the file reader (a fake in tests).
func loadUnitEnvironment(assign []string, files []envFileEntry, unset []string, read fileReader) (*unitEnvironment, error) {
	u := &unitEnvironment{Vars: map[string]string{}}
	for _, a := range assign {
		if !envAssign.MatchString(a) {
			return nil, errors.New("Environment= of the unit holds a word that is not NAME=VALUE")
		}
		k, v, _ := strings.Cut(a, "=")
		if specifierLike.MatchString(strings.ReplaceAll(v, "%%", "")) {
			return nil, errors.New("Environment= of the unit holds an unexpanded systemd specifier")
		}
		u.Vars[k] = strings.ReplaceAll(v, "%%", "%")
	}
	for _, f := range files {
		data, err := read(f.Path)
		switch {
		case err == nil:
		case errors.Is(err, os.ErrNotExist) && f.Optional:
			u.Skipped = append(u.Skipped, f.Path)
			continue
		case errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("EnvironmentFile %s does not exist and is not marked optional", f.Path)
		default:
			return nil, fmt.Errorf("EnvironmentFile %s is unreadable: %s (run as root, or as a user that can read every file)", f.Path, unwrapErr(err))
		}
		vars, err := parseEnvironmentFile(data)
		if err != nil {
			return nil, fmt.Errorf("EnvironmentFile %s: %w", f.Path, err)
		}
		for k, v := range vars {
			u.Vars[k] = v
		}
		u.FilesRead = append(u.FilesRead, f.Path)
	}
	for _, name := range unset {
		n, _, _ := strings.Cut(name, "=")
		if !envName.MatchString(n) {
			return nil, errors.New("UnsetEnvironment= of the unit names something that is not a variable")
		}
		delete(u.Vars, n)
	}
	return u, nil
}

// getenv serves the unit's environment alone: the doctor then reads the unit, never the operator's shell.
func (u *unitEnvironment) getenv(k string) string { return u.Vars[k] }

// environ is the NAME=VALUE list for the unknown-name scan (values stay in memory; only names are reported).
func (u *unitEnvironment) environ() []string {
	names := make([]string, 0, len(u.Vars))
	for k := range u.Vars {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		out = append(out, k+"="+u.Vars[k])
	}
	return out
}

// envFileSource reads one explicit --env-file as a unit-style environment source.
func envFileSource(path string, read fileReader) (*unitEnvironment, error) {
	return loadUnitEnvironment(nil, []envFileEntry{{Path: path}}, nil, read)
}
