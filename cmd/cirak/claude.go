package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode/runner"
	"github.com/nevzatcirak/cirak/internal/claudewire"
)

// ADR-021 wiring, default off. The adapter exists only when CIRAK_CLAUDE_COMMAND is set;
// then every required CIRAK_CLAUDE_* value must be present and valid or the Bridge refuses to
// start (no partial enablement). With none of them set, no Executor is constructed and a
// claudecode: session ID keeps the "not enabled" refusal.
//
//	CIRAK_CLAUDE_COMMAND       absolute path of the root-owned wrapper (required to enable)
//	CIRAK_CLAUDE_SESSIONS      path of the allowlist JSON (mode 0600, ADR-021 section 6)
//	CIRAK_CLAUDE_VERSIONS      exact claude_code_version list, comma separated (no default)
//	CIRAK_CLAUDE_HISTORY_DIR   private directory of the history records
//	CIRAK_CLAUDE_HOME          HOME of the Claude service user (the child's environment)
//	CIRAK_CLAUDE_MODEL         model name
//	CIRAK_CLAUDE_LANG          optional child LANG (default C.UTF-8)
//	CIRAK_CLAUDE_MAX_TURNS     optional, 1..1000 (default 20)
//	CIRAK_CLAUDE_MAX_BUDGET_USD optional decimal
//	CIRAK_CLAUDE_RUN_MAX       optional wall-clock cap per child, a Go duration (default 15m)
//	CIRAK_WRITER_LOCK_DIR      the existing writer lock directory (shared with OpenCode)
//	CIRAK_ALLOWED_ROOTS / CIRAK_ALLOWED_SESSIONS  the existing lists; every claudecode:
//	                            session must be in both the allowlist file and the ACL.
var claudeEnvNames = []string{"CIRAK_CLAUDE_COMMAND", "CIRAK_CLAUDE_SESSIONS", "CIRAK_CLAUDE_VERSIONS", "CIRAK_CLAUDE_HISTORY_DIR",
	"CIRAK_CLAUDE_HOME", "CIRAK_CLAUDE_MODEL", "CIRAK_CLAUDE_LANG", "CIRAK_CLAUDE_MAX_TURNS", "CIRAK_CLAUDE_MAX_BUDGET_USD", "CIRAK_CLAUDE_RUN_MAX"}

// claudeConfig resolves the environment. It returns (nil, nil) when the adapter is not
// configured, and an error for any partial or invalid configuration.
func claudeConfig(env func(string) string, allowedRoots, allowedSessions []string) (*claudewire.Config, error) {
	if env("CIRAK_CLAUDE_COMMAND") == "" {
		for _, n := range claudeEnvNames[1:] {
			if env(n) != "" {
				return nil, fmt.Errorf("%s is set but CIRAK_CLAUDE_COMMAND is not: the Claude Code adapter is configured completely or not at all", n)
			}
		}
		return nil, nil
	}
	cfg := &claudewire.Config{
		Command: env("CIRAK_CLAUDE_COMMAND"), SessionsFile: env("CIRAK_CLAUDE_SESSIONS"), Versions: env("CIRAK_CLAUDE_VERSIONS"),
		HistoryDir: env("CIRAK_CLAUDE_HISTORY_DIR"), LockDir: env("CIRAK_WRITER_LOCK_DIR"), Home: env("CIRAK_CLAUDE_HOME"),
		Lang: env("CIRAK_CLAUDE_LANG"), Model: env("CIRAK_CLAUDE_MODEL"), MaxBudgetUSD: env("CIRAK_CLAUDE_MAX_BUDGET_USD"),
		AllowedRoots: allowedRoots, AllowedSessions: allowedSessions, ServiceUID: uint32(os.Geteuid()), MaxTurns: 20,
	}
	for name, v := range map[string]string{"CIRAK_CLAUDE_SESSIONS": cfg.SessionsFile, "CIRAK_CLAUDE_VERSIONS": cfg.Versions, "CIRAK_CLAUDE_HISTORY_DIR": cfg.HistoryDir,
		"CIRAK_CLAUDE_HOME": cfg.Home, "CIRAK_CLAUDE_MODEL": cfg.Model, "CIRAK_WRITER_LOCK_DIR": cfg.LockDir} {
		if v == "" {
			return nil, fmt.Errorf("%s is required when CIRAK_CLAUDE_COMMAND is set", name)
		}
	}
	for name, p := range map[string]string{"CIRAK_CLAUDE_COMMAND": cfg.Command, "CIRAK_CLAUDE_SESSIONS": cfg.SessionsFile, "CIRAK_CLAUDE_HISTORY_DIR": cfg.HistoryDir, "CIRAK_CLAUDE_HOME": cfg.Home} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return nil, fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	if v := env("CIRAK_CLAUDE_MAX_TURNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			return nil, fmt.Errorf("CIRAK_CLAUDE_MAX_TURNS must be 1..1000")
		}
		cfg.MaxTurns = n
	}
	if v := env("CIRAK_CLAUDE_RUN_MAX"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 24*time.Hour {
			return nil, fmt.Errorf("CIRAK_CLAUDE_RUN_MAX must be a duration between 1s and 24h")
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
		log.Printf("CIRAK_CLAUDE_COMMAND ignored: read-only ingress starts no child and takes no writer locks")
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
