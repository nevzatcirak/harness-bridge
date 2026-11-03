//go:build linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectHostRunRefused(t *testing.T) {
	ctx, end := context.WithTimeout(context.Background(), 30*time.Second)
	defer end()
	binary := filepath.Join(t.TempDir(), "fixture")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, out)
	}
	netns, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	mntns, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	out, e := exec.CommandContext(ctx, binary, "run", netns, mntns).CombinedOutput()
	if e == nil || !strings.Contains(string(out), "BLOCKED/FAIL:") {
		t.Fatalf("unsafe host invocation accepted: %v %s", e, out)
	}
}
func TestIsolatedKernelProfiles(t *testing.T) {
	if os.Getenv("HB_CONFINEMENT_INTEGRATION") != "1" {
		t.Skip("opt-in root/CAP_SYS_ADMIN test; use HB_CONFINEMENT_INTEGRATION=1 go test -run TestIsolatedKernelProfiles -count=1")
	}
	if os.Geteuid() != 0 {
		t.Fatal("BLOCKED: integration explicitly requested without root")
	}
	ctx, end := context.WithTimeout(context.Background(), 60*time.Second)
	defer end()
	raw, e := exec.CommandContext(ctx, "../../run-fixture.sh").CombinedOutput()
	if e != nil {
		t.Fatalf("actual kernel fixture failed: %v %s", e, raw)
	}
	var r struct {
		Synthetic          bool              `json:"synthetic_only"`
		Baseline           report            `json:"baseline"`
		Variants           map[string]report `json:"variants"`
		Pass               bool              `json:"required_checks_pass"`
		CredentialComplete bool              `json:"credential_isolation_complete"`
		Ready              bool              `json:"production_ready"`
		ProductionUsed     bool              `json:"production_targets_used"`
	}
	if e := json.Unmarshal(raw, &r); e != nil {
		t.Fatal(e)
	}
	if !r.Synthetic || !r.Pass || r.CredentialComplete || r.Ready || r.ProductionUsed || len(r.Baseline.Checks) != 8 || len(r.Variants) != 2 {
		t.Fatalf("misleading fixture verdict: %+v", r)
	}
	for name, v := range r.Variants {
		seen := map[string]bool{}
		for _, c := range v.Checks {
			if !c.Pass {
				t.Errorf("%s: failed %s", name, c.Name)
			}
			seen[c.Name] = c.Pass
		}
		for _, needed := range []string{"workspace_write_read", "workspace_rename", "workspace_exec", "allowed_workspace_shell_script", "unapproved_host_read_denied", "unapproved_host_write_denied", "unapproved_host_exec_denied", "symlink_denied_absolute-escape", "symlink_denied_relative-escape", "symlink_denied_host-path-escape", "host_credential_paths_denied", "other_workspace_read_write_denied", "docker_socket_path_connect_denied", "abstract_socket_denied", "proc_magic_link_escape_denied", "new_user_namespace_denied", "direct_exec_denied_/tmp", "own_credential_residual_detected", "noexec_interpreter_residual_detected"} {
			if !seen[needed] {
				t.Errorf("%s missing/failed acceptance %s", name, needed)
			}
		}
		if len(v.Residuals) < 2 {
			t.Errorf("%s hides residual risks", name)
		}
		if !seen["imported_hardlink_residual_detected"] {
			t.Errorf("%s hides imported-hardlink limitation", name)
		}
	}
}
