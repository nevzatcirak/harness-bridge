package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode/runner"
	"github.com/nevzatcirak/harness-bridge/internal/claudewire"
)

// ADR-021 wiring, default off. The adapter exists only when BRIDGE_CLAUDE_COMMAND is set;
// then every required BRIDGE_CLAUDE_* value must be present and valid or the Bridge refuses to
// start (no partial enablement). With none of them set, no Executor is constructed and a
// claudecode: session ID keeps the "not enabled" refusal.
//
//	BRIDGE_CLAUDE_COMMAND       absolute path of the root-owned wrapper (required to enable)
//	BRIDGE_CLAUDE_SESSIONS      path of the allowlist JSON (mode 0600, ADR-021 section 6)
//	BRIDGE_CLAUDE_VERSIONS      exact claude_code_version list, comma separated (no default)
//	BRIDGE_CLAUDE_HISTORY_DIR   private directory of the history records
//	BRIDGE_CLAUDE_HOME          HOME of the Claude service user (the child's environment)
//	BRIDGE_CLAUDE_MODEL         model name
//	BRIDGE_CLAUDE_LANG          optional child LANG (default C.UTF-8)
//	BRIDGE_CLAUDE_MAX_TURNS     optional, 1..1000 (default 20)
//	BRIDGE_CLAUDE_MAX_BUDGET_USD optional decimal
//	BRIDGE_CLAUDE_RUN_MAX       optional wall-clock cap per child, a Go duration (default 15m)
//	BRIDGE_WRITER_LOCK_DIR      the existing writer lock directory (shared with OpenCode)
//	BRIDGE_ALLOWED_ROOTS / BRIDGE_ALLOWED_SESSIONS  the existing lists; every claudecode:
//	                            session must be in both the allowlist file and the ACL.
var claudeEnvNames = []string{"BRIDGE_CLAUDE_COMMAND", "BRIDGE_CLAUDE_SESSIONS", "BRIDGE_CLAUDE_VERSIONS", "BRIDGE_CLAUDE_HISTORY_DIR",
	"BRIDGE_CLAUDE_HOME", "BRIDGE_CLAUDE_MODEL", "BRIDGE_CLAUDE_LANG", "BRIDGE_CLAUDE_MAX_TURNS", "BRIDGE_CLAUDE_MAX_BUDGET_USD", "BRIDGE_CLAUDE_RUN_MAX"}

// claudeConfig resolves the environment. It returns (nil, nil) when the adapter is not
// configured, and an error for any partial or invalid configuration.
func claudeConfig(env func(string) string, allowedRoots, allowedSessions []string) (*claudewire.Config, error) {
	if env("BRIDGE_CLAUDE_COMMAND") == "" {
		for _, n := range claudeEnvNames[1:] {
			if env(n) != "" {
				return nil, fmt.Errorf("%s is set but BRIDGE_CLAUDE_COMMAND is not: the Claude Code adapter is configured completely or not at all", n)
			}
		}
		return nil, nil
	}
	cfg := &claudewire.Config{
		Command: env("BRIDGE_CLAUDE_COMMAND"), SessionsFile: env("BRIDGE_CLAUDE_SESSIONS"), Versions: env("BRIDGE_CLAUDE_VERSIONS"),
		HistoryDir: env("BRIDGE_CLAUDE_HISTORY_DIR"), LockDir: env("BRIDGE_WRITER_LOCK_DIR"), Home: env("BRIDGE_CLAUDE_HOME"),
		Lang: env("BRIDGE_CLAUDE_LANG"), Model: env("BRIDGE_CLAUDE_MODEL"), MaxBudgetUSD: env("BRIDGE_CLAUDE_MAX_BUDGET_USD"),
		AllowedRoots: allowedRoots, AllowedSessions: allowedSessions, ServiceUID: uint32(os.Geteuid()), MaxTurns: 20,
	}
	for name, v := range map[string]string{"BRIDGE_CLAUDE_SESSIONS": cfg.SessionsFile, "BRIDGE_CLAUDE_VERSIONS": cfg.Versions, "BRIDGE_CLAUDE_HISTORY_DIR": cfg.HistoryDir,
		"BRIDGE_CLAUDE_HOME": cfg.Home, "BRIDGE_CLAUDE_MODEL": cfg.Model, "BRIDGE_WRITER_LOCK_DIR": cfg.LockDir} {
		if v == "" {
			return nil, fmt.Errorf("%s is required when BRIDGE_CLAUDE_COMMAND is set", name)
		}
	}
	for name, p := range map[string]string{"BRIDGE_CLAUDE_COMMAND": cfg.Command, "BRIDGE_CLAUDE_SESSIONS": cfg.SessionsFile, "BRIDGE_CLAUDE_HISTORY_DIR": cfg.HistoryDir, "BRIDGE_CLAUDE_HOME": cfg.Home} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return nil, fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	if v := env("BRIDGE_CLAUDE_MAX_TURNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return nil, fmt.Errorf("BRIDGE_CLAUDE_MAX_TURNS must be 1..1000")
		}
		cfg.MaxTurns = n
	}
	if v := env("BRIDGE_CLAUDE_RUN_MAX"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 24*time.Hour {
			return nil, fmt.Errorf("BRIDGE_CLAUDE_RUN_MAX must be a duration between 1s and 24h")
		}
		cfg.Runner = runner.Config{RunMax: d}
	}
	return cfg, nil
}

// openClaude builds the adapter when it is configured and this process is a writer. A
// read-only ingress takes no writer locks and starts no child, so it never enables the
// adapter (the configuration is still checked for consistency first).
func openClaude(env func(string) string, allowedRoots, allowedSessions []string, readOnly bool) (*claudewire.Harness, error) {
	cfg, err := claudeConfig(env, allowedRoots, allowedSessions)
	if err != nil || cfg == nil {
		return nil, err
	}
	if readOnly {
		log.Printf("BRIDGE_CLAUDE_COMMAND ignored: read-only ingress starts no child and takes no writer locks")
		return nil, nil
	}
	h, err := claudewire.Open(*cfg)
	if err != nil {
		return nil, err
	}
	log.Printf("Claude Code adapter enabled (%d allowlisted session(s))", len(h.Sessions()))
	return h, nil
}

func splitList(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
