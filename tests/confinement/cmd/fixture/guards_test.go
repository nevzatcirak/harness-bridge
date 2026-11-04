//go:build linux

package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FIX-FIXTURE-03 R-1 (R2-PR18-REVIEW): the setup/setup-compat and
// control/probe/probe-compat modes must refuse direct host invocations before
// any side effect. These tests run the built fixture as a non-root user against
// temp directories only; they never run a mode as root.

// tree records every entry under dir with its mode and size.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	e := filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		fi, e := os.Lstat(p)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fmt.Sprintf("%v %d", fi.Mode(), fi.Size())
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for k, v := range after {
		if before[k] != v {
			t.Errorf("%s side effect: %s = %s (was %q)", what, k, v, before[k])
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			t.Errorf("%s side effect: %s removed", what, k)
		}
	}
}

// guardCase runs binary with mode and checks refusal containing want and no
// change under any of dirs.
func guardCase(t *testing.T, binary, cwd string, env []string, extra []*os.File, want string, args []string, dirs ...string) {
	t.Helper()
	before := map[string]map[string]string{}
	for _, d := range dirs {
		before[d] = tree(t, d)
	}
	ctx, end := context.WithTimeout(context.Background(), 30*time.Second)
	defer end()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.ExtraFiles = extra
	out, e := cmd.CombinedOutput()
	if e == nil || !strings.Contains(string(out), "BLOCKED/FAIL:") || !strings.Contains(string(out), want) {
		t.Errorf("%v: want refusal containing %q, got %v %s", args, want, e, out)
	}
	for _, d := range dirs {
		sameTree(t, d, before[d], tree(t, d))
	}
}

// syntheticBase is a temp fixture root that setup would accept as synthetic.
func syntheticBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if e := os.WriteFile(filepath.Join(base, "SYNTHETIC_ONLY"), []byte("NO_PRODUCTION_DATA"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, d := range []string{"root", "host/outside", "workspace", "state", "cache", "tmp"} {
		if e := os.MkdirAll(filepath.Join(base, d), 0755); e != nil {
			t.Fatal(e)
		}
	}
	return base
}

func TestSetupModesRequireAckAndPrivateMountNamespace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("unprivileged guard test; never run setup modes as root outside the disposable fixture")
	}
	binary := buildFixture(t)
	ack := ackName + "=" + fixtureAckValue(t)
	ownMnt, e := os.Open("/proc/self/ns/mnt")
	if e != nil {
		t.Fatal(e)
	}
	defer ownMnt.Close()
	plain, e := os.Open(filepath.Join(syntheticBase(t), "SYNTHETIC_ONLY"))
	if e != nil {
		t.Fatal(e)
	}
	defer plain.Close()
	for _, mode := range []string{"setup", "setup-compat"} {
		for _, tc := range []struct {
			name  string
			env   []string
			extra []*os.File
			want  string
		}{
			{"missing_ack", nil, nil, "disposable-host acknowledgement"},
			{"wrong_ack", []string{ackName + "=yes"}, nil, "disposable-host acknowledgement"},
			{"wrong_ack_with_ns_fd", []string{ackName + "=yes"}, []*os.File{ownMnt}, "disposable-host acknowledgement"},
			{"ack_without_ns_fd", []string{ack}, nil, "private mount namespace"},
			{"ack_ns_fd_is_not_namespace", []string{ack}, []*os.File{plain}, "private mount namespace"},
			// Same mount namespace as the caller: this is a host invocation,
			// refused before the euid/PID-1 check (wrong euid included).
			{"ack_same_mount_namespace_wrong_euid", []string{ack}, []*os.File{ownMnt}, "private mount namespace"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				base, cwd := syntheticBase(t), t.TempDir()
				env := append([]string{"PATH=/usr/bin:/bin"}, tc.env...)
				guardCase(t, binary, cwd, env, tc.extra, tc.want, []string{mode, base, "@hb-guard-test-absent", "127.0.0.1:1"}, base, cwd)
			})
		}
	}
}

func TestProbeModesRequireProbeUID(t *testing.T) {
	switch os.Geteuid() {
	case 0:
		t.Skip("unprivileged guard test; root is a wrong euid but probe modes are never run as root here")
	case uid:
		t.Skip("NOT_VERIFIED here: the test euid equals the probe uid 65534, so no wrong euid is available; run as another non-root uid (CI runs as 1001)")
	}
	binary := buildFixture(t)
	ack := ackName + "=" + fixtureAckValue(t)
	for _, mode := range []string{"control", "probe", "probe-compat"} {
		for name, env := range map[string][]string{"missing_ack": nil, "wrong_ack": {ackName + "=yes"}, "with_ack": {ack}} {
			t.Run(mode+"/"+name+"_wrong_euid", func(t *testing.T) {
				base, cwd := syntheticBase(t), t.TempDir()
				// Planted so an unguarded control/probe would write here.
				if e := os.WriteFile(filepath.Join(base, "host/outside/public"), []byte("SYNTHETIC_PUBLIC"), 0644); e != nil {
					t.Fatal(e)
				}
				guardCase(t, binary, cwd, append([]string{"PATH=/usr/bin:/bin"}, env...), nil, "probe uid 65534", []string{mode, base, "@hb-guard-test-absent", "127.0.0.1:1"}, base, cwd)
			})
		}
	}
}

// run hands setup the acknowledgement and its own mount namespace as fd 3;
// without both, the new setup guard would refuse the real fixture run.
func TestSetupCommandCarriesAckAndNamespace(t *testing.T) {
	mnt, e := os.Open("/proc/self/ns/mnt")
	if e != nil {
		t.Fatal(e)
	}
	defer mnt.Close()
	for _, mode := range []string{"setup", "setup-compat"} {
		cmd := setupCommand(context.Background(), "/x/fixture", mode, "/tmp/base", "@a", "127.0.0.1:1", mnt)
		if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != mnt {
			t.Errorf("%s: ExtraFiles = %v, want the caller's mount namespace as fd 3", mode, cmd.ExtraFiles)
		}
		acks := 0
		for _, kv := range cmd.Env {
			if strings.HasPrefix(kv, ackName+"=") {
				acks++
				if kv != ackName+"="+ackValue {
					t.Errorf("%s: %s", mode, kv)
				}
			}
		}
		if acks != 1 {
			t.Errorf("%s: env %v carries %d acknowledgements, want 1", mode, cmd.Env, acks)
		}
		for _, kv := range cleanEnv {
			if strings.HasPrefix(kv, ackName+"=") {
				t.Errorf("cleanEnv carries the acknowledgement to probe modes: %s", kv)
			}
		}
		args := strings.Join(cmd.Args, " ")
		if !strings.Contains(args, "--mount ") || !strings.Contains(args, " /x/fixture "+mode+" /tmp/base @a 127.0.0.1:1") || strings.Contains(args, "--net") != (mode == "setup") {
			t.Errorf("%s: args %q", mode, args)
		}
	}
}

// F-E: the README states the run-mode namespace check trusts caller values.
func TestReadmeDocumentsRunNamespaceTrust(t *testing.T) {
	b, e := os.ReadFile("../../README.md")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"caller-provided", "acceptable only on a disposable host"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("README missing %q", want)
		}
	}
}

// Each layer of the setup mount-namespace guard refuses on its own. The real
// /proc paths cannot isolate the layers unprivileged (the /proc/1 comparison
// already refuses every host-namespace call), so this calls the check with a
// fake /proc/self link that reads "1", as it does for setup under
// unshare --mount-proc.
func TestPrivateMountNamespaceLayers(t *testing.T) {
	ownPath := "/proc/self/ns/mnt"
	own, e := os.Open(ownPath)
	if e != nil {
		t.Fatal(e)
	}
	defer own.Close()
	plain, e := os.Open(filepath.Join(syntheticBase(t), "SYNTHETIC_ONLY"))
	if e != nil {
		t.Fatal(e)
	}
	defer plain.Close()
	net, e := os.Open("/proc/self/ns/net")
	if e != nil {
		t.Fatal(e)
	}
	defer net.Close()
	pid1 := filepath.Join(t.TempDir(), "self")
	if e := os.Symlink("1", pid1); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name     string
		fd       int
		procSelf string
	}{
		{"same_mount_namespace_as_pid1", int(own.Fd()), pid1},           // inode layer
		{"regular_file_as_pid1", int(plain.Fd()), pid1},                 // NS_GET_NSTYPE layer
		{"network_namespace_as_pid1", int(net.Fd()), pid1},              // NS_GET_NSTYPE layer
		{"closed_fd_as_pid1", 1 << 20, pid1},                            // EBADF
		{"same_mount_namespace_real_proc", int(own.Fd()), "/proc/self"}, // inode and /proc/1 layers
	} {
		if privateMountNamespace(tc.fd, ownPath, tc.procSelf, ownPath) {
			t.Errorf("%s accepted", tc.name)
		}
	}
	// /proc/1 layer, isolated by standing in a regular file for "this
	// process's namespace": the real mount namespace fd then differs from it.
	// The check refuses when /proc/1 resolves to the same object and accepts
	// otherwise (positive control for the whole chain).
	base := syntheticBase(t)
	fakeOwn, fakeOther := filepath.Join(base, "SYNTHETIC_ONLY"), filepath.Join(base, "root")
	if privateMountNamespace(int(own.Fd()), fakeOwn, "/proc/self", fakeOwn) {
		t.Error("/proc/1 in this namespace accepted")
	}
	if !privateMountNamespace(int(own.Fd()), fakeOwn, "/proc/self", fakeOther) {
		t.Error("distinct namespaces refused")
	}
	if !privateMountNamespace(int(own.Fd()), fakeOwn, pid1, fakeOwn) {
		t.Error("as PID 1 the /proc/1 comparison must be skipped")
	}
}
