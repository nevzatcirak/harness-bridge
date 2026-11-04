//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// R2-PR22-REVIEW P3-1: setup receives run's mount namespace as fd 3 without
// close-on-exec and later replaces itself with the probe via syscall.Exec. The
// guard must close fd 3 first, or the probe inherits a namespace descriptor.
//
// Unprivileged replica of that sequence: a child process receives its own mount
// namespace as an inheritable fd 3 (ExtraFiles), passes the guard (the /proc
// paths are chosen so the passing path runs without privileges), then execs
// itself exactly as setup execs the probe. The new image reports every
// inherited descriptor that still names a mount namespace.
const fd3ChildEnv = "HB_FIXTURE_FD3_CHILD"

func withoutEnv(env []string, name string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func TestSetupGuardClosesNamespaceFD(t *testing.T) {
	self := []string{os.Args[0], "-test.run=^TestSetupGuardClosesNamespaceFD$", "-test.count=1"}
	switch os.Getenv(fd3ChildEnv) {
	case "guard":
		if failure := catch(func() {
			requirePrivateMountNamespaceAt(setupNamespaceFD, "/proc/self/ns/net", "/proc/self", "/proc/self/ns/ipc")
		}); failure != nil {
			fmt.Printf("GUARD_REFUSED %v\n", failure)
			os.Exit(3)
		}
		env := append(withoutEnv(os.Environ(), fd3ChildEnv), fd3ChildEnv+"=list")
		e := syscall.Exec(os.Args[0], self, env)
		fmt.Printf("EXEC_FAILED %v\n", e)
		os.Exit(3)
	case "list":
		entries, e := os.ReadDir("/proc/self/fd")
		if e != nil {
			fmt.Printf("LIST_FAILED %v\n", e)
			os.Exit(3)
		}
		for _, entry := range entries {
			target, e := os.Readlink("/proc/self/fd/" + entry.Name())
			if e == nil && strings.HasPrefix(target, "mnt:[") {
				fmt.Printf("LEAKED fd %s -> %s\n", entry.Name(), target)
			}
		}
		fmt.Println("FDS_CHECKED")
		os.Exit(0)
	}

	mnt, e := os.Open("/proc/self/ns/mnt")
	if e != nil {
		t.Fatal(e)
	}
	defer mnt.Close()
	cmd := exec.Command(self[0], self[1:]...)
	cmd.Env = append(withoutEnv(os.Environ(), fd3ChildEnv), fd3ChildEnv+"=guard")
	cmd.ExtraFiles = []*os.File{mnt} // becomes fd 3, inheritable, as in setupCommand
	out, e := cmd.CombinedOutput()
	text := string(out)
	if e != nil || !strings.Contains(text, "FDS_CHECKED") {
		t.Fatalf("fd-3 replica did not complete: %v\n%s", e, text)
	}
	if strings.Contains(text, "LEAKED") {
		t.Errorf("mount namespace descriptor survived the guard and the exec into the probe:\n%s", text)
	}
}
