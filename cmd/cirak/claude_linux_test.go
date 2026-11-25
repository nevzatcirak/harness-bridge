//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
	"github.com/nevzatcirak/cirak/internal/admin"
	"github.com/nevzatcirak/cirak/internal/execution"
)

// ADR-021 wiring: default off, complete-or-nothing configuration.

func claudeEnv(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	return map[string]string{
		"CIRAK_CLAUDE_COMMAND": "/usr/local/libexec/hb-claude", "CIRAK_CLAUDE_SESSIONS": filepath.Join(dir, "sessions.json"), "CIRAK_CLAUDE_VERSIONS": "2.1.322",
		"CIRAK_CLAUDE_HISTORY_DIR": filepath.Join(dir, "history"), "CIRAK_CLAUDE_HOME": "/home/claude", "CIRAK_CLAUDE_MODEL": "claude-sonnet-5-5",
		"CIRAK_WRITER_LOCK_DIR": filepath.Join(dir, "locks"),
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

// Any CIRAK_CLAUDE_* value without CIRAK_CLAUDE_COMMAND is a configuration error: complete or
// not at all, never a silent partial enablement.
func TestAPartialClaudeConfigurationFailsClosed(t *testing.T) {
	full := claudeEnv(t)
	for name := range full {
		if name == "CIRAK_CLAUDE_COMMAND" || name == "CIRAK_WRITER_LOCK_DIR" {
			continue
		}
		env := map[string]string{name: full[name]}
		if _, err := claudeConfig(envOf(env), nil, nil); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("only %s set: err %v", name, err)
		}
	}
	// COMMAND set but a required value missing
	for _, missing := range []string{"CIRAK_CLAUDE_SESSIONS", "CIRAK_CLAUDE_VERSIONS", "CIRAK_CLAUDE_HISTORY_DIR", "CIRAK_CLAUDE_HOME", "CIRAK_CLAUDE_MODEL", "CIRAK_WRITER_LOCK_DIR"} {
		env := claudeEnv(t)
		delete(env, missing)
		if _, err := claudeConfig(envOf(env), nil, nil); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %s: err %v", missing, err)
		}
	}
	for name, mod := range map[string]func(map[string]string){
		"relative command":  func(e map[string]string) { e["CIRAK_CLAUDE_COMMAND"] = "hb-claude" },
		"unclean sessions":  func(e map[string]string) { e["CIRAK_CLAUDE_SESSIONS"] = "/a/../b/s.json" },
		"relative home":     func(e map[string]string) { e["CIRAK_CLAUDE_HOME"] = "home" },
		"turns zero":        func(e map[string]string) { e["CIRAK_CLAUDE_MAX_TURNS"] = "0" },
		"turns text":        func(e map[string]string) { e["CIRAK_CLAUDE_MAX_TURNS"] = "many" },
		"turns huge":        func(e map[string]string) { e["CIRAK_CLAUDE_MAX_TURNS"] = "100000" },
		"run max text":      func(e map[string]string) { e["CIRAK_CLAUDE_RUN_MAX"] = "soon" },
		"run max too short": func(e map[string]string) { e["CIRAK_CLAUDE_RUN_MAX"] = "10ms" },
		"run max too long":  func(e map[string]string) { e["CIRAK_CLAUDE_RUN_MAX"] = "48h" },
	} {
		env := claudeEnv(t)
		mod(env)
		if _, err := claudeConfig(envOf(env), nil, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a valid one
	env := claudeEnv(t)
	env["CIRAK_CLAUDE_MAX_TURNS"], env["CIRAK_CLAUDE_RUN_MAX"] = "7", "90s"
	cfg, err := claudeConfig(envOf(env), []string{"/srv"}, []string{"claudecode:x"})
	if err != nil || cfg == nil || cfg.MaxTurns != 7 || cfg.Runner.RunMax != 90*time.Second || cfg.Command != env["CIRAK_CLAUDE_COMMAND"] || len(cfg.AllowedRoots) != 1 {
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
	if _, err := openClaude(envOf(map[string]string{"CIRAK_CLAUDE_MODEL": "x"}), nil, nil, true); err == nil {
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
		srv, err := openAdmin(envOf(map[string]string{"CIRAK_ADMIN_SOCKET": path}), false, &countingReleaser{}, rel)
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

// #267 P3-4: the OpenCode writer-lock step of main, called by main and by this test. The
// OpenCode IDs are locked, the claudecode: ones are left to the adapter.
func TestOpenCodeOwnershipTakesOnlyTheNonClaudeSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const claude = "claudecode:00000000-0000-4000-8000-000000000001"
	free := func(id string) bool {
		o, err := execution.AcquireOwnership(dir, []string{id})
		if err != nil {
			return false
		}
		o.Close()
		return true
	}
	owner, err := acquireOpenCodeOwnership(dir, []string{"ses_a", claude, "ses_b"})
	if err != nil || owner == nil {
		t.Fatalf("owner %v err %v", owner, err)
	}
	if free("ses_a") || free("ses_b") {
		t.Fatal("an OpenCode session lock was not taken")
	}
	if !free(claude) {
		t.Fatal("the OpenCode writer took the Claude session's lock")
	}
	owner.Close()
	if !free("ses_a") {
		t.Fatal("Close did not release")
	}
	// only Claude entries: nothing to lock, no error, no owner
	if o, err := acquireOpenCodeOwnership(dir, []string{claude}); o != nil || err != nil {
		t.Fatalf("claude-only ACL: %v %v", o, err)
	}
	// a held lock is refused (a second writer)
	held, err := acquireOpenCodeOwnership(dir, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := acquireOpenCodeOwnership(dir, []string{"ses_a"}); err == nil {
		t.Fatal("a second owner of the same session was accepted")
	}
}
