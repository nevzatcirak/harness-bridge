// Package lifecycle holds the operator-configured lifecycle root allowlist of
// ADR-022 (section 3.1). Slice S1 is the parser and the startup validation only:
// no tool, no native call, and nothing here is wired into startup yet.
//
// The configuration is a trust statement by the operator: because opening a
// directory in OpenCode executes its plugins, a root asserts that everything an
// instance will load under it is trusted. It is therefore read once, from a file
// the service user owns with mode 0600, and every refusal is fail closed.
package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EnvConfig names the environment variable holding the config file path.
const EnvConfig = "BRIDGE_LIFECYCLE_CONFIG"

// Modes a root may grant (ADR-022 3.1).
const (
	ModeCreate  = "create"
	ModeArchive = "archive"
)

// Defaults (ADR-022 section 8, accepted as proposed by D16 Q3).
const (
	DefaultCreatePerHour   = 10
	DefaultArchivePerHour  = 10
	DefaultMaxSessions     = 20
	DefaultMaxTotalSession = 50
)

// maxConfigBytes bounds the file read; a lifecycle config is a handful of roots.
const maxConfigBytes = 64 << 10

// maxRoots bounds the number of configured roots.
const maxRoots = 64

// Refusal reasons: a closed set, so callers and tests can tell refusals apart.
const (
	ReasonConfigPath      = "config_path"     // not an absolute, clean path
	ReasonConfigMissing   = "config_missing"  // the file cannot be inspected or opened
	ReasonConfigType      = "config_type"     // not a regular file (symlink, directory, ...)
	ReasonConfigOwner     = "config_owner"    // not owned by the service user
	ReasonConfigMode      = "config_mode"     // mode is not exactly 0600
	ReasonConfigSize      = "config_size"     // larger than the bound
	ReasonConfigChanged   = "config_changed"  // the opened file is not the inspected one
	ReasonSyntax          = "syntax"          // not exactly one JSON object of the known shape
	ReasonNoRootPath      = "root_path_empty" // a root without a path
	ReasonRootNotAbsolute = "root_not_absolute"
	ReasonRootNotCanon    = "root_not_canonical" // not clean, or a symlink alias exists in the path
	ReasonRootIsSlash     = "root_is_slash"
	ReasonRootMissing     = "root_missing"
	ReasonRootNotDir      = "root_not_directory"
	ReasonRootOwner       = "root_owner" // owned by neither the service user nor root
	ReasonRootOverlap     = "root_overlap"
	ReasonModes           = "modes" // empty, unknown or duplicate mode
	ReasonLimit           = "limit" // a cap that is not a positive integer
	ReasonTooManyRoots    = "too_many_roots"
)

// Error is a startup refusal. Reason is one of the Reason constants.
type Error struct {
	Reason string
	Path   string
	Detail string
}

func (e *Error) Error() string {
	s := "lifecycle config refused: " + e.Reason
	if e.Path != "" {
		s += " (" + e.Path + ")"
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

func refuse(reason, path, detail string) error {
	return &Error{Reason: reason, Path: path, Detail: detail}
}

// Root is one validated lifecycle root.
type Root struct {
	Path        string
	Modes       []string
	MaxSessions int
	// MaxDepth is how far below the root creation is allowed; absent means 0
	// (only the root directory itself).
	MaxDepth int
	// ExistingTools is the per-root operator flag for ADR-022 O2 (D16 Q7). It is
	// parsed and recorded only: it grants nothing until the ADR-012 amendment
	// (slice S9) is accepted and implemented.
	ExistingTools bool
}

// Allows reports whether the root grants mode.
func (r Root) Allows(mode string) bool {
	for _, m := range r.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Config is the validated lifecycle configuration.
type Config struct {
	Roots            []Root
	CreatePerHour    int
	ArchivePerHour   int
	MaxTotalSessions int
}

// Enabled reports whether any lifecycle root is configured. A nil or empty
// configuration registers no lifecycle tool (default off, ADR-022 3.1).
func (c *Config) Enabled() bool { return c != nil && len(c.Roots) > 0 }

// validator carries the service identity and the ownership probe, so tests can
// exercise the owner refusals regardless of the user running them.
type validator struct {
	uid   uint32
	owner func(fs.FileInfo) (uint32, bool)
	// afterLstat is a test seam: it runs between the inspection of the config
	// file and the opening of it, so a test can swap the file there. Nil in
	// production.
	afterLstat func(path string)
}

// FromEnv loads the configuration named by BRIDGE_LIFECYCLE_CONFIG. An unset or
// empty variable disables lifecycle and returns (nil, nil). uid is the service
// user (os.Geteuid()).
func FromEnv(getenv func(string) string, uid int) (*Config, error) {
	path := getenv(EnvConfig)
	if path == "" {
		return nil, nil
	}
	return Load(path, uid)
}

// Load reads and validates the config file at path. It never rewrites it.
func Load(path string, uid int) (*Config, error) {
	return validator{uid: uint32(uid), owner: ownerOf}.load(path)
}

// Parse validates raw config bytes (the file checks do not apply).
func Parse(data []byte, uid int) (*Config, error) {
	return validator{uid: uint32(uid), owner: ownerOf}.parse(data)
}

func (v validator) load(path string) (*Config, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, refuse(ReasonConfigPath, path, "BRIDGE_LIFECYCLE_CONFIG must be a clean absolute path")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, refuse(ReasonConfigMissing, path, "cannot inspect the file")
	}
	if !before.Mode().IsRegular() {
		return nil, refuse(ReasonConfigType, path, "must be a regular file, not a symlink or directory")
	}
	// P3-2 (review #215): the path itself must be canonical, so a symlink in
	// ANY component, an ancestor directory included, is refused, not only a
	// final-component symlink (that one is the config_type refusal above).
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, refuse(ReasonConfigMissing, path, "cannot resolve the file")
	}
	if resolved != path {
		return nil, refuse(ReasonConfigPath, path, "the path must be canonical: no symlink in any component")
	}
	if err := v.checkFile(path, before); err != nil {
		return nil, err
	}
	if v.afterLstat != nil {
		v.afterLstat(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, refuse(ReasonConfigMissing, path, "cannot open the file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, refuse(ReasonConfigChanged, path, "the opened file is not the inspected file")
	}
	if err := v.checkFile(path, opened); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, refuse(ReasonConfigMissing, path, "cannot read the file")
	}
	if len(data) > maxConfigBytes {
		return nil, refuse(ReasonConfigSize, path, fmt.Sprintf("larger than %d bytes", maxConfigBytes))
	}
	return v.parse(data)
}

func (v validator) checkFile(path string, info fs.FileInfo) error {
	if uid, ok := v.owner(info); !ok || uid != v.uid {
		return refuse(ReasonConfigOwner, path, "must be owned by the service user")
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return refuse(ReasonConfigMode, path, "mode must be exactly 0600")
	}
	return nil
}

// rawConfig is the on-disk shape. Unknown fields are rejected. Pointers
// distinguish an absent cap (default) from an explicit zero (refused).
type rawConfig struct {
	Roots            []rawRoot `json:"roots"`
	CreatePerHour    *int      `json:"create_per_hour"`
	ArchivePerHour   *int      `json:"archive_per_hour"`
	MaxTotalSessions *int      `json:"max_total_sessions"`
}

type rawRoot struct {
	Path          *string  `json:"path"`
	Modes         []string `json:"modes"`
	MaxSessions   *int     `json:"max_sessions"`
	MaxDepth      *int     `json:"max_depth"`
	ExistingTools bool     `json:"existing_tools"`
}

func (v validator) parse(data []byte) (*Config, error) {
	// P3-3 (review #215): exact, unique keys. Go's decoder matches field names
	// case-insensitively and keeps the last of a duplicate key; both would let two
	// readers of the same file disagree, so both are refused.
	if err := checkStrictKeys(data); err != nil {
		return nil, refuse(ReasonSyntax, "", err.Error())
	}
	var raw rawConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, refuse(ReasonSyntax, "", "not a JSON object of the expected shape")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse(ReasonSyntax, "", "trailing data after the JSON object")
	}
	cfg := &Config{
		CreatePerHour:    DefaultCreatePerHour,
		ArchivePerHour:   DefaultArchivePerHour,
		MaxTotalSessions: DefaultMaxTotalSession,
	}
	for _, c := range []struct {
		name string
		in   *int
		out  *int
	}{
		{"create_per_hour", raw.CreatePerHour, &cfg.CreatePerHour},
		{"archive_per_hour", raw.ArchivePerHour, &cfg.ArchivePerHour},
		{"max_total_sessions", raw.MaxTotalSessions, &cfg.MaxTotalSessions},
	} {
		if c.in == nil {
			continue
		}
		if *c.in < 1 {
			return nil, refuse(ReasonLimit, "", c.name+" must be a positive integer")
		}
		*c.out = *c.in
	}
	if len(raw.Roots) > maxRoots {
		return nil, refuse(ReasonTooManyRoots, "", fmt.Sprintf("at most %d roots", maxRoots))
	}
	for _, r := range raw.Roots {
		root, err := v.root(r)
		if err != nil {
			return nil, err
		}
		cfg.Roots = append(cfg.Roots, root)
	}
	if err := checkOverlap(cfg.Roots); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (v validator) root(r rawRoot) (Root, error) {
	if r.Path == nil || *r.Path == "" {
		return Root{}, refuse(ReasonNoRootPath, "", "every root needs a path")
	}
	path := *r.Path
	if err := v.checkRootPath(path); err != nil {
		return Root{}, err
	}
	modes, err := checkModes(path, r.Modes)
	if err != nil {
		return Root{}, err
	}
	out := Root{Path: path, Modes: modes, MaxSessions: DefaultMaxSessions, ExistingTools: r.ExistingTools}
	if r.MaxSessions != nil {
		if *r.MaxSessions < 1 {
			return Root{}, refuse(ReasonLimit, path, "max_sessions must be a positive integer")
		}
		out.MaxSessions = *r.MaxSessions
	}
	if r.MaxDepth != nil {
		if *r.MaxDepth < 0 {
			return Root{}, refuse(ReasonLimit, path, "max_depth must not be negative")
		}
		out.MaxDepth = *r.MaxDepth
	}
	return out, nil
}

// checkRootPath applies the startup rules of ADR-022 3.1 to one root: it must
// exist, be canonical (no symlink alias in its own path, the ADR-014 rule), be a
// directory owned by the service user or root, and not be "/".
func (v validator) checkRootPath(path string) error {
	if !filepath.IsAbs(path) {
		return refuse(ReasonRootNotAbsolute, path, "root path must be absolute")
	}
	if filepath.Clean(path) != path {
		return refuse(ReasonRootNotCanon, path, "root path must be clean (no '..', '.', duplicate or trailing slash)")
	}
	if path == string(filepath.Separator) {
		return refuse(ReasonRootIsSlash, path, "'/' cannot be a lifecycle root")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return refuse(ReasonRootMissing, path, "root does not exist or cannot be resolved")
	}
	if resolved != path {
		return refuse(ReasonRootNotCanon, path, "root path contains a symlink alias; configure the canonical path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return refuse(ReasonRootMissing, path, "root cannot be inspected")
	}
	if !info.IsDir() {
		return refuse(ReasonRootNotDir, path, "root must be a directory")
	}
	if uid, ok := v.owner(info); !ok || (uid != v.uid && uid != 0) {
		return refuse(ReasonRootOwner, path, "root must be owned by the service user or root")
	}
	return nil
}

func checkModes(path string, modes []string) ([]string, error) {
	if len(modes) == 0 {
		return nil, refuse(ReasonModes, path, "a root needs at least one mode (create, archive)")
	}
	seen := map[string]bool{}
	for _, m := range modes {
		if m != ModeCreate && m != ModeArchive {
			return nil, refuse(ReasonModes, path, "unknown mode")
		}
		if seen[m] {
			return nil, refuse(ReasonModes, path, "duplicate mode")
		}
		seen[m] = true
	}
	return append([]string(nil), modes...), nil
}

// checkOverlap refuses two roots that are equal or where one contains the other.
// Both paths are already canonical, so a component-wise prefix test is exact.
func checkOverlap(roots []Root) error {
	for i := range roots {
		for j := i + 1; j < len(roots); j++ {
			if contains(roots[i].Path, roots[j].Path) || contains(roots[j].Path, roots[i].Path) {
				return refuse(ReasonRootOverlap, roots[j].Path, "overlaps root "+roots[i].Path)
			}
		}
	}
	return nil
}

// contains reports whether child is parent or lies below it, on component
// boundaries (so "/a/b" does not contain "/a/bc").
func contains(parent, child string) bool {
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
}

var (
	topKeys  = map[string]string{"roots": "roots", "create_per_hour": "scalar", "archive_per_hour": "scalar", "max_total_sessions": "scalar"}
	rootKeys = map[string]string{"path": "scalar", "modes": "scalar", "max_sessions": "scalar", "max_depth": "scalar", "existing_tools": "scalar"}
)

// checkStrictKeys walks the JSON tokens and refuses a duplicate key (exact), a
// key whose case differs from the canonical lower-case name, and a key that is
// not known. It does not validate values: the decode that follows does.
func checkStrictKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return errors.New("not a JSON object of the expected shape")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil // the decode reports the wrong top-level shape
	}
	return scanObject(dec, topKeys)
}

func scanObject(dec *json.Decoder, allowed map[string]string) error {
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errors.New("malformed JSON")
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("malformed JSON")
		}
		kind, known := allowed[key]
		if !known {
			if _, folded := allowed[strings.ToLower(key)]; folded {
				return errors.New("key differs from its canonical lower-case name only by case")
			}
			return errors.New("unknown key")
		}
		if seen[key] {
			return errors.New("duplicate key")
		}
		seen[key] = true
		if kind == "roots" {
			if err := scanRoots(dec); err != nil {
				return err
			}
			continue
		}
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return errors.New("malformed JSON")
	}
	return nil
}

func scanRoots(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("malformed JSON")
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim || d != '[' {
		if isDelim { // an object or other container where an array belongs: skip it, the decode refuses it
			return skipOpen(dec, d)
		}
		return nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errors.New("malformed JSON")
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' {
				if err := scanObject(dec, rootKeys); err != nil {
					return err
				}
				continue
			}
			if err := skipOpen(dec, d); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("malformed JSON")
	}
	return nil
}

// skipValue consumes one JSON value.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return errors.New("malformed JSON")
	}
	if d, ok := tok.(json.Delim); ok {
		return skipOpen(dec, d)
	}
	return nil
}

// skipOpen consumes the rest of a container whose opening delimiter was read.
func skipOpen(dec *json.Decoder, open json.Delim) error {
	if open != '{' && open != '[' {
		return nil
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return errors.New("malformed JSON")
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}
