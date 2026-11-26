// Package config is the one configuration file of Cirak (ADR-027 section 1): the closed schema, the strict
// parser, the rules for the file itself, and the precedence of flag, environment, file and default. It reads
// no environment variable and starts nothing: the caller injects the environment, so every rule is testable
// with fakes.
//
// The file only replaces the INPUT surface. Every value ends up as the environment string the runtime
// validators of cmd/cirak already read, so those validators are reused, never copied (ADR-027 section 1.5).
package config

import (
	"os"
	"strings"
)

// Kind is the JSON type of a key.
type Kind int

const (
	// String is a JSON string. Durations, enums and RFC 3339 times are strings whose meaning the runtime
	// validators check.
	String Kind = iota
	// Bool is a JSON boolean; its environment form is "1" or "0".
	Bool
	// Int is a JSON integer; its environment form is the decimal.
	Int
	// List is a JSON array of strings.
	List
)

// ListForm is how a list becomes the environment string the runtime reads.
type ListForm int

const (
	// PathList joins with the OS path-list separator (CIRAK_ALLOWED_ROOTS).
	PathList ListForm = iota
	// Comma joins with "," (CIRAK_ALLOWED_SESSIONS).
	Comma
	// JSONArray is a JSON array of strings (CIRAK_DASHBOARD_REDACT_EXTRA).
	JSONArray
)

// Key is one row of the schema of ADR-027 section 1.3.
type Key struct {
	Path string // "server.tls.cert_file"
	Env  string // the canonical environment name the runtime reads
	Kind Kind
	List ListForm
	// Guarded is true for every key except the display-only harness.claude.lang: a differing environment/file
	// pair is a startup refusal (section 1.2, P2-A).
	Guarded bool
	// Secret marks a file reference or a credential name: output shows it as set/unset, never as a value.
	Secret bool
	// AbsPath marks a key whose non-empty value must be an absolute, clean path.
	AbsPath bool
	// URL marks a key whose value is a URL or an origin: it can carry credentials (userinfo, a query), so the
	// effective listing never prints it as written (see Describe).
	URL bool
	// Default is documentation only (print-effective); the runtime applies its own default.
	Default string
}

// Keys is the schema, in the order of ADR-027 section 1.3.
var Keys = []Key{
	{Path: "server.listen", Env: "CIRAK_LISTEN", Guarded: true, Default: "127.0.0.1:8080"},
	{Path: "server.read_only", Env: "CIRAK_READ_ONLY", Kind: Bool, Guarded: true, Default: "0"},
	{Path: "server.public_origin", Env: "CIRAK_PUBLIC_ORIGIN", Guarded: true, URL: true},
	{Path: "server.principal", Env: "CIRAK_PRINCIPAL", Guarded: true},
	{Path: "server.tls.cert_file", Env: "CIRAK_TLS_CERT", Guarded: true, AbsPath: true},
	{Path: "server.tls.key_file", Env: "CIRAK_TLS_KEY", Guarded: true, Secret: true, AbsPath: true},
	{Path: "token.file", Env: "CIRAK_TOKEN_FILE", Guarded: true, Secret: true, AbsPath: true},
	{Path: "token.credential", Env: "CIRAK_TOKEN_CREDENTIAL", Guarded: true, Secret: true},
	{Path: "token.expires", Env: "CIRAK_TOKEN_EXPIRES", Guarded: true},
	{Path: "harness.opencode.url", Env: "OPENCODE_URL", Guarded: true, URL: true, Default: "http://127.0.0.1:4096"},
	{Path: "harness.opencode.username", Env: "OPENCODE_SERVER_USERNAME", Guarded: true},
	{Path: "harness.opencode.password_file", Env: "OPENCODE_SERVER_PASSWORD_FILE", Guarded: true, Secret: true, AbsPath: true},
	{Path: "harness.opencode.password_credential", Env: "CIRAK_OPENCODE_PASSWORD_CREDENTIAL", Guarded: true, Secret: true},
	{Path: "harness.claude.command", Env: "CIRAK_CLAUDE_COMMAND", Guarded: true, AbsPath: true},
	{Path: "harness.claude.sessions_file", Env: "CIRAK_CLAUDE_SESSIONS", Guarded: true, AbsPath: true},
	{Path: "harness.claude.versions", Env: "CIRAK_CLAUDE_VERSIONS", Guarded: true},
	{Path: "harness.claude.history_dir", Env: "CIRAK_CLAUDE_HISTORY_DIR", Guarded: true, AbsPath: true},
	{Path: "harness.claude.home", Env: "CIRAK_CLAUDE_HOME", Guarded: true, AbsPath: true},
	{Path: "harness.claude.model", Env: "CIRAK_CLAUDE_MODEL", Guarded: true},
	{Path: "harness.claude.lang", Env: "CIRAK_CLAUDE_LANG", Guarded: false, Default: "C.UTF-8"},
	{Path: "acl.roots", Env: "CIRAK_ALLOWED_ROOTS", Kind: List, List: PathList, Guarded: true, AbsPath: true},
	{Path: "acl.sessions", Env: "CIRAK_ALLOWED_SESSIONS", Kind: List, List: Comma, Guarded: true},
	{Path: "writer.lock_dir", Env: "CIRAK_WRITER_LOCK_DIR", Guarded: true, AbsPath: true},
	{Path: "writer.admin_socket", Env: "CIRAK_ADMIN_SOCKET", Guarded: true, AbsPath: true},
	{Path: "lifecycle.config_file", Env: "CIRAK_LIFECYCLE_CONFIG", Guarded: true, AbsPath: true},
	{Path: "overlay.dir", Env: "CIRAK_OVERLAY_DIR", Guarded: true, AbsPath: true},
	{Path: "permission.relay", Env: "CIRAK_PERMISSION_RELAY", Guarded: true, Default: "reject_only"},
	{Path: "permission.incarnation", Env: "CIRAK_PERMISSION_INCARNATION", Guarded: true, Default: "off"},
	{Path: "dashboard.socket", Env: "CIRAK_DASHBOARD_SOCKET", Guarded: true, AbsPath: true},
	{Path: "dashboard.addr", Env: "CIRAK_DASHBOARD_ADDR", Guarded: true},
	{Path: "dashboard.redact_extra", Env: "CIRAK_DASHBOARD_REDACT_EXTRA", Kind: List, List: JSONArray, Guarded: true},
	{Path: "limits.token_max_lifetime", Env: "CIRAK_TOKEN_MAX_LIFETIME", Guarded: true, Default: "24h"},
	{Path: "limits.claude_max_turns", Env: "CIRAK_CLAUDE_MAX_TURNS", Kind: Int, Guarded: true, Default: "20"},
	{Path: "limits.claude_max_budget_usd", Env: "CIRAK_CLAUDE_MAX_BUDGET_USD", Guarded: true},
	{Path: "limits.claude_run_max", Env: "CIRAK_CLAUDE_RUN_MAX", Guarded: true, Default: "15m"},
	// ADR-017 A2 S3 (D32 Q13) amends ADR-027 section 1.3 with the operator liveness thresholds, in
	// seconds. The runtime validator (execution.ParseLivenessEnv) enforces the A2.6 bounds; the file key
	// and its CIRAK_* environment name resolve to the same value under the every-key rule.
	{Path: "liveness.no_progress_s", Env: "CIRAK_LIVENESS_NO_PROGRESS_S", Kind: Int, Guarded: true, Default: "120"},
	{Path: "liveness.tool_long_s", Env: "CIRAK_LIVENESS_TOOL_LONG_S", Kind: Int, Guarded: true, Default: "600"},
}

// Names that are read by the daemon and are not schema keys: the config location, and the
// environment-only inline secrets (the file has no inline-secret key, section 1.3).
const (
	EnvConfig         = "CIRAK_CONFIG"
	EnvInlineToken    = "CIRAK_TOKEN"
	EnvInlinePassword = "OPENCODE_SERVER_PASSWORD"
	EnvCredentialsDir = "CREDENTIALS_DIRECTORY"
	// EnvDashboardLabel is the optional dashboard instance label (ADR-023 A1.8). It is read by the dashboard from
	// the environment and is not a schema key (ADR-027 section 1.3 does not list it), so it is a known name that
	// `config check` must not report as unknown.
	EnvDashboardLabel = "CIRAK_DASHBOARD_LABEL"
)

var byPath = func() map[string]*Key {
	m := make(map[string]*Key, len(Keys))
	for i := range Keys {
		m[Keys[i].Path] = &Keys[i]
	}
	return m
}()

// Lookup returns the key of a path ("" for an unknown one).
func Lookup(path string) (Key, bool) {
	k, ok := byPath[path]
	if !ok {
		return Key{}, false
	}
	return *k, true
}

// KnownEnvNames returns every CIRAK_* name the daemon understands, for the unknown-name warning of config
// check. Its legacy twin shares the suffix (ADR-026).
func KnownEnvNames() map[string]bool {
	m := map[string]bool{EnvConfig: true, EnvInlineToken: true, EnvDashboardLabel: true}
	for _, k := range Keys {
		m[k.Env] = true
	}
	return m
}

// listSeparator is the separator of a PathList.
var listSeparator = string(os.PathListSeparator)

func (k Key) separator() string {
	switch k.List {
	case Comma:
		return ","
	case PathList:
		return listSeparator
	}
	return ""
}

// node is the schema as a tree, for the strict walk of a file.
type node struct {
	children map[string]*node
	key      *Key
}

var tree = func() *node {
	root := &node{children: map[string]*node{}}
	for i := range Keys {
		n := root
		parts := strings.Split(Keys[i].Path, ".")
		for j, p := range parts {
			c := n.children[p]
			if c == nil {
				c = &node{children: map[string]*node{}}
				n.children[p] = c
			}
			if j == len(parts)-1 {
				c.key = &Keys[i]
			}
			n = c
		}
	}
	return root
}()
