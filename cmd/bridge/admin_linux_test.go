//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/admin"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

var _ admin.Releaser = (*execution.Manager)(nil)

type countingReleaser struct{ calls int }

func (c *countingReleaser) ReleaseGuard(context.Context, string, string, execution.ReleaseOptions) (execution.ReleaseEvidence, error) {
	c.calls++
	return execution.ReleaseEvidence{NativeRead: true, NativeState: "idle", CorrelatedUser: "found", SettleMS: 5000}, nil
}

func adminDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hbcli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// ADR-020 group 9: the admin socket is never opened in read-only mode.
func TestAdminSocketNotOpenedReadOnly(t *testing.T) {
	path := filepath.Join(adminDir(t), "admin.sock")
	srv, err := openAdmin(envOf(map[string]string{"BRIDGE_ADMIN_SOCKET": path}), true, &countingReleaser{})
	if err != nil || srv != nil {
		t.Fatalf("read-only: srv=%v err=%v", srv, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket created in read-only mode: %v", err)
	}
	if srv, err := openAdmin(envOf(nil), false, &countingReleaser{}); err != nil || srv != nil {
		t.Fatalf("unset socket: srv=%v err=%v", srv, err)
	}
}

func TestAdminSocketUnsafeDirectoryFailsStartup(t *testing.T) {
	dir := adminDir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if srv, err := openAdmin(envOf(map[string]string{"BRIDGE_ADMIN_SOCKET": filepath.Join(dir, "admin.sock")}), false, &countingReleaser{}); err == nil {
		srv.Close()
		t.Fatal("0755 directory accepted")
	}
}

func TestAdminCLIReleaseGuard(t *testing.T) {
	path := filepath.Join(adminDir(t), "admin.sock")
	runs := &countingReleaser{}
	srv, err := openAdmin(envOf(map[string]string{"BRIDGE_ADMIN_SOCKET": path}), false, runs)
	if err != nil || srv == nil {
		t.Fatalf("writer: srv=%v err=%v", srv, err)
	}
	defer srv.Close()
	env := envOf(map[string]string{"BRIDGE_ADMIN_SOCKET": path})
	var out, errOut bytes.Buffer
	code := runAdmin([]string{"release-guard", "--session", "opencode:ses_example", "--execution", "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "--ticket", "OPS-1234"}, env, &out, &errOut)
	if code != 0 || runs.calls != 1 {
		t.Fatalf("exit %d calls %d stderr %q", code, runs.calls, errOut.String())
	}
	var resp admin.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Outcome != "released" || resp.Evidence == nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	// A refused request exits 1.
	out.Reset()
	code = runAdmin([]string{"release-guard", "--session", "ses_bare", "--execution", "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "--ticket", "OPS-1234"}, env, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), `"refused"`) || runs.calls != 1 {
		t.Fatalf("refusal: exit %d calls %d out %q", code, runs.calls, out.String())
	}
}

func TestAdminCLIUsage(t *testing.T) {
	env := envOf(map[string]string{"BRIDGE_ADMIN_SOCKET": "/nonexistent/admin.sock"})
	for name, args := range map[string][]string{
		"no_subcommand":  {},
		"unknown":        {"restart"},
		"missing_ticket": {"release-guard", "--session", "opencode:ses_x", "--execution", "0f1e2d3c4b5a69788796a5b4c3d2e1f0"},
		"bad_ticket":     {"release-guard", "--session", "opencode:ses_x", "--execution", "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "--ticket", "has space"},
		"reason_flag":    {"release-guard", "--session", "opencode:ses_x", "--execution", "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "--ticket", "OPS-1", "--reason", "free text"},
		"extra_args":     {"release-guard", "--session", "opencode:ses_x", "--execution", "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "--ticket", "OPS-1", "free", "text"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runAdmin(args, env, &out, &errOut); code != 2 {
				t.Fatalf("exit %d, want 2 (stderr %q)", code, errOut.String())
			}
		})
	}
	var out, errOut bytes.Buffer
	if code := runAdmin([]string{"release-guard", "--session", "opencode:ses_x", "--execution", "0f1e2d3c4b5a69788796a5b4c3d2e1f0", "--ticket", "OPS-1"}, env, &out, &errOut); code != 1 {
		t.Fatalf("unreachable socket: exit %d", code)
	}
}
