//go:build linux

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/admin"
)

// ADR-021 wiring: default off, complete-or-nothing configuration.

func claudeEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	return map[string]string{
		"BRIDGE_CLAUDE_COMMAND": "/usr/local/libexec/hb-claude", "BRIDGE_CLAUDE_SESSIONS": filepath.Join(dir, "sessions.json"), "BRIDGE_CLAUDE_VERSIONS": "2.1.322",
		"BRIDGE_CLAUDE_HISTORY_DIR": filepath.Join(dir, "history"), "BRIDGE_CLAUDE_HOME": "/home/claude", "BRIDGE_CLAUDE_MODEL": "claude-sonnet-5-5",
		"BRIDGE_WRITER_LOCK_DIR": filepath.Join(dir, "locks"),
	}
}

func TestClaudeIsOffWithoutConfiguration(t *testing.T) {
	cfg, err := claudeConfig(envOf(nil), []string{"/srv"}, []string{"ses_a"})
	if cfg != nil || err != nil {
		t.Fatalf("cfg %v err %v: no configuration must mean no Executor", cfg, err)
	}
	h, err := openClaude(envOf(nil), nil, nil, false)
	if h != nil || err != nil {
		t.Fatalf("openClaude with no configuration: %v %v", h, err)
	}
}

// Any BRIDGE_CLAUDE_* value without BRIDGE_CLAUDE_COMMAND is a configuration error: complete or
// not at all, never a silent partial enablement.
func TestAPartialClaudeConfigurationFailsClosed(t *testing.T) {
	full := claudeEnv(t)
	for name := range full {
		if name == "BRIDGE_CLAUDE_COMMAND" || name == "BRIDGE_WRITER_LOCK_DIR" {
			continue
		}
		env := map[string]string{name: full[name]}
		if _, err := claudeConfig(envOf(env), nil, nil); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("only %s set: err %v", name, err)
		}
	}
	// COMMAND set but a required value missing
	for _, missing := range []string{"BRIDGE_CLAUDE_SESSIONS", "BRIDGE_CLAUDE_VERSIONS", "BRIDGE_CLAUDE_HISTORY_DIR", "BRIDGE_CLAUDE_HOME", "BRIDGE_CLAUDE_MODEL", "BRIDGE_WRITER_LOCK_DIR"} {
		env := claudeEnv(t)
		delete(env, missing)
		if _, err := claudeConfig(envOf(env), nil, nil); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %s: err %v", missing, err)
		}
	}
	for name, mod := range map[string]func(map[string]string){
		"relative command":  func(e map[string]string) { e["BRIDGE_CLAUDE_COMMAND"] = "hb-claude" },
		"unclean sessions":  func(e map[string]string) { e["BRIDGE_CLAUDE_SESSIONS"] = "/a/../b/s.json" },
		"relative home":     func(e map[string]string) { e["BRIDGE_CLAUDE_HOME"] = "home" },
		"turns zero":        func(e map[string]string) { e["BRIDGE_CLAUDE_MAX_TURNS"] = "0" },
		"turns text":        func(e map[string]string) { e["BRIDGE_CLAUDE_MAX_TURNS"] = "many" },
		"turns huge":        func(e map[string]string) { e["BRIDGE_CLAUDE_MAX_TURNS"] = "100000" },
		"run max text":      func(e map[string]string) { e["BRIDGE_CLAUDE_RUN_MAX"] = "soon" },
		"run max too short": func(e map[string]string) { e["BRIDGE_CLAUDE_RUN_MAX"] = "10ms" },
		"run max too long":  func(e map[string]string) { e["BRIDGE_CLAUDE_RUN_MAX"] = "48h" },
	} {
		env := claudeEnv(t)
		mod(env)
		if _, err := claudeConfig(envOf(env), nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a valid one
	env := claudeEnv(t)
	env["BRIDGE_CLAUDE_MAX_TURNS"], env["BRIDGE_CLAUDE_RUN_MAX"] = "7", "90s"
	cfg, err := claudeConfig(envOf(env), []string{"/srv"}, []string{"claudecode:x"})
	if err != nil || cfg == nil || cfg.MaxTurns != 7 || cfg.Runner.RunMax != 90*time.Second || cfg.Command != env["BRIDGE_CLAUDE_COMMAND"] || len(cfg.AllowedRoots) != 1 {
		t.Fatalf("cfg %+v err %v", cfg, err)
	}
}

// An invalid configuration stops the start with an error and constructs nothing.
func TestAnInvalidClaudeConfigurationStopsTheStart(t *testing.T) {
	env := claudeEnv(t) // the allowlist file, history and lock directories do not exist
	h, err := openClaude(envOf(env), []string{"/srv"}, []string{"claudecode:00000000-0000-4000-8000-000000000001"}, false)
	if err == nil || h != nil {
		t.Fatalf("openClaude = %v, %v: an unusable configuration must fail, not half-enable", h, err)
	}
}

func TestReadOnlyIngressNeverEnablesClaude(t *testing.T) {
	h, err := openClaude(envOf(claudeEnv(t)), []string{"/srv"}, nil, true)
	if h != nil || err != nil {
		t.Fatalf("read-only: %v %v", h, err)
	}
	// still an inconsistent configuration is an error even read-only
	if _, err := openClaude(envOf(map[string]string{"BRIDGE_CLAUDE_MODEL": "x"}), nil, nil, true); err == nil {
		t.Fatal("a partial configuration passed in read-only mode")
	}
}

type claudeStub struct{ calls int }

func (c *claudeStub) ReleaseGuard(_ context.Context, _, _ string, _ claudecode.ReleaseOptions) (claudecode.ReleaseEvidence, error) {
	c.calls++
	return claudecode.ReleaseEvidence{Harness: "claudecode", ChildExited: true, GroupEmpty: true, Rechecked: true}, nil
}

// The admin channel gets the Claude releaser only when the adapter runs: a nil adapter keeps
// the "not enabled" refusal, a running one releases through it.
func TestAdminGetsTheClaudeReleaserOnlyWhenEnabled(t *testing.T) {
	const session, exec = "claudecode:00000000-0000-4000-8000-000000000001", "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	for name, rel := range map[string]admin.ClaudeReleaser{"off": nil, "on": &claudeStub{}} {
		path := filepath.Join(adminDir(t), "admin.sock")
		srv, err := openAdmin(envOf(map[string]string{"BRIDGE_ADMIN_SOCKET": path}), false, &countingReleaser{}, rel)
		if err != nil || srv == nil {
			t.Fatalf("%s: %v", name, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := admin.Call(ctx, path, admin.Request{Op: admin.OpReleaseGuard, SessionID: session, ExecutionID: exec, Ticket: "OPS-1"})
		cancel()
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "off":
			if resp.Outcome != "refused" || resp.Error == nil || !strings.Contains(resp.Error.Message, "not enabled") {
				t.Errorf("off: %+v", resp)
			}
		case "on":
			if resp.Outcome != "released" || resp.ClaudeEvidence == nil || !resp.ClaudeEvidence.Rechecked {
				t.Errorf("on: %+v", resp)
			}
		}
	}
}
