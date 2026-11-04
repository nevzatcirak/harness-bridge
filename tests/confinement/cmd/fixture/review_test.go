//go:build linux

package main

// Unprivileged regressions for R2-PR5-FIXTURE-REVIEW F-A, F-B, F-C, F-F and the
// PR #15 mount API seccomp observation. None of these tests runs run-fixture.sh,
// the run/setup modes as root, or the privileged integration test.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// --- F-A: root writes into owner-controlled directories ---------------------

type canary struct{ file, dir, sentinel string }

func newCanary(t *testing.T) canary {
	t.Helper()
	outside := t.TempDir()
	c := canary{file: filepath.Join(outside, "victim-file"), dir: filepath.Join(outside, "victim-dir")}
	if e := os.WriteFile(c.file, []byte("HOST_FILE_MUST_NOT_CHANGE"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(c.file, 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(c.dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(c.dir, 0755); e != nil {
		t.Fatal(e)
	}
	c.sentinel = filepath.Join(c.dir, "sentinel")
	if e := os.WriteFile(c.sentinel, []byte("HOST_DIR_ENTRY"), 0644); e != nil {
		t.Fatal(e)
	}
	return c
}

func (c canary) verify(t *testing.T) {
	t.Helper()
	b, e := os.ReadFile(c.file)
	if e != nil || string(b) != "HOST_FILE_MUST_NOT_CHANGE" {
		t.Errorf("host file followed through planted symlink: %q %v", b, e)
	}
	if fi, e := os.Lstat(c.file); e != nil || fi.Mode().Perm() != 0644 {
		t.Errorf("host file mode changed through planted symlink: %v %v", fi.Mode(), e)
	}
	if fi, e := os.Lstat(c.dir); e != nil || fi.Mode().Perm() != 0755 {
		t.Errorf("host directory mode changed through planted symlink: %v %v", fi, e)
	}
	if _, e := os.Lstat(c.sentinel); e != nil {
		t.Errorf("host directory entry removed through planted symlink: %v", e)
	}
}

// laterNames are every name root creates inside a probe-owned directory.
var laterNames = []string{"own-bootstrap-credential", "imported-hardlink", "allowed-program", "absolute-escape", "relative-escape", "host-path-escape", "credential-escape", "home", "data", "state"}

// attack acts as a concurrent owner-uid process that wins every race in dir:
// it swaps each existing entry for a symlink to the host canary and plants
// symlinks under every name root may create later.
func (c canary) attack(t *testing.T, dir string) {
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatalf("attacker cannot list owned dir %s: %v", dir, e)
	}
	for _, entry := range entries {
		p := filepath.Join(dir, entry.Name())
		target := c.file
		if entry.IsDir() {
			target = c.dir
		}
		if e := os.Rename(p, p+".moved"); e != nil {
			t.Fatalf("attacker rename: %v", e)
		}
		if e := os.Symlink(target, p); e != nil {
			t.Fatalf("attacker symlink: %v", e)
		}
	}
	for _, name := range laterNames {
		p := filepath.Join(dir, name)
		if _, e := os.Lstat(p); errors.Is(e, os.ErrNotExist) {
			if e := os.Symlink(c.file, p); e != nil {
				t.Fatalf("attacker plant: %v", e)
			}
		}
	}
}

func catch(fn func()) (failure any) {
	defer func() { failure = recover() }()
	fn()
	return nil
}

func TestPrepareOwnerRaceCannotRedirectRootWrites(t *testing.T) {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for _, target := range []string{"workspace", "state", "state/home", "state/data", "state/state", "cache", "tmp"} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			c := newCanary(t)
			base := filepath.Join(t.TempDir(), "base")
			if e := os.Mkdir(base, 0755); e != nil {
				t.Fatal(e)
			}
			attacked := false
			failure := catch(func() {
				prepare(base, exe, os.Getuid(), func(rel string) {
					if rel == target {
						attacked = true
						c.attack(t, filepath.Join(base, rel))
					}
				})
			})
			if !attacked {
				t.Fatalf("handoff of %s never happened (prepare failure: %v)", target, failure)
			}
			// Cleanup must not follow the attacker's symlinks either.
			if e := os.RemoveAll(base); e != nil {
				t.Errorf("cleanup: %v", e)
			}
			c.verify(t)
		})
	}
}

func TestPrepareWithoutAttackerBuildsTree(t *testing.T) {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	base := filepath.Join(t.TempDir(), "base")
	if e := os.Mkdir(base, 0755); e != nil {
		t.Fatal(e)
	}
	var handed []string
	prepare(base, exe, os.Getuid(), func(rel string) { handed = append(handed, rel) })
	if len(handed) != 7 {
		t.Fatalf("handed off %v", handed)
	}
	for _, rel := range handed {
		if fi, e := os.Lstat(filepath.Join(base, rel)); e != nil || !fi.IsDir() || fi.Mode().Perm() != 0700 {
			t.Errorf("%s: %v %v", rel, fi, e)
		}
	}
	for rel, mode := range map[string]os.FileMode{"SYNTHETIC_ONLY": 0600, "state/own-bootstrap-credential": 0600, "host/outside/public": 0666, "workspace/allowed-program": 0755} {
		if fi, e := os.Lstat(filepath.Join(base, rel)); e != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v", rel, fi, e)
		}
	}
	if b, e := os.ReadFile(filepath.Join(base, "SYNTHETIC_ONLY")); e != nil || string(b) != "NO_PRODUCTION_DATA" {
		t.Errorf("marker: %q %v", b, e)
	}
	if l, e := os.Readlink(filepath.Join(base, "workspace/credential-escape")); e != nil || l != "/etc/harness-bridge/private/test-token" {
		t.Errorf("escape symlink: %q %v", l, e)
	}
}

// --- F-C: cleanup skipped on failure ----------------------------------------

// os.Exit skips deferred cleanup. It may appear only as the final call of main.
func TestOsExitOnlyInMain(t *testing.T) {
	fset := token.NewFileSet()
	file, e := parser.ParseFile(fset, "main.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.FuncLit); ok && fn.Name.Name == "main" {
				ast.Inspect(lit.Body, func(m ast.Node) bool {
					if isOsExit(m) {
						t.Errorf("%s: os.Exit inside a closure in main", fset.Position(m.Pos()))
					}
					return true
				})
				return false
			}
			if isOsExit(n) && fn.Name.Name != "main" {
				t.Errorf("%s: os.Exit in %s skips its deferred cleanup", fset.Position(n.Pos()), fn.Name.Name)
			}
			return true
		})
	}
}

func isOsExit(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os" && sel.Sel.Name == "Exit"
}

func readScript(t *testing.T) []string {
	t.Helper()
	b, e := os.ReadFile("../../run-fixture.sh")
	if e != nil {
		t.Fatal(e)
	}
	return strings.Split(string(b), "\n")
}

// firstLine returns the index of the first non-comment line matching re, or -1.
func firstLine(lines []string, re string) int {
	r := regexp.MustCompile(re)
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") && r.MatchString(l) {
			return i
		}
	}
	return -1
}

func TestRunFixtureScriptCleanupOnEveryExit(t *testing.T) {
	lines := readScript(t)
	trapExit := firstLine(lines, `^trap .*\bEXIT\s*$`)
	if trapExit < 0 || !strings.Contains(lines[trapExit], "cleanup") {
		t.Fatal("no EXIT trap running cleanup")
	}
	for _, sig := range []string{"INT", "TERM", "HUP"} {
		if firstLine(lines, `^trap .*\b`+sig+`\s*$`) < 0 {
			t.Errorf("no %s trap converting the signal into a trapped exit", sig)
		}
	}
	for _, create := range []string{`mktemp -d /tmp/hb-confinement-build\.`, `mktemp -d /tmp/hb-confinement-fixture-`} {
		i := firstLine(lines, create)
		if i < 0 || i < trapExit {
			t.Errorf("%s must be created after the cleanup trap is installed (line %d, trap %d)", create, i, trapExit)
		}
	}
	if firstLine(lines, `^\s*exec\s`) >= 0 {
		t.Error("exec would replace the shell and skip its EXIT trap")
	}
	body := strings.Join(lines, "\n")
	cleanup := regexp.MustCompile(`(?s)cleanup\(\) \{(.*?)\n\}`).FindStringSubmatch(body)
	if cleanup == nil {
		t.Fatal("no cleanup function")
	}
	// Idempotent: each removal is guarded and tolerant of already-removed paths.
	for _, v := range []string{"work_dir", "build_dir"} {
		if !regexp.MustCompile(`\[ -n "\$`+v+`" \]`).MatchString(cleanup[1]) || !strings.Contains(cleanup[1], `rm -rf -- "$`+v+`"`) {
			t.Errorf("cleanup does not idempotently remove $%s: %s", v, cleanup[1])
		}
	}
	if strings.Contains(cleanup[1], "rmdir") {
		t.Error("rmdir fails when the directory is already gone; cleanup is not idempotent")
	}
}

// --- F-B: production-host runs ----------------------------------------------

const ackName = "HB_CONFINEMENT_DISPOSABLE_HOST"

func TestRunFixtureScriptRequiresDisposableHostAck(t *testing.T) {
	lines := readScript(t)
	guard := firstLine(lines, `\$\{`+ackName+`:-\}" != "\$ack_value"`)
	if guard < 0 {
		t.Fatal("no fail-closed disposable-host acknowledgement check")
	}
	if !strings.Contains(strings.Join(lines[guard:guard+4], "\n"), "exit 2") {
		t.Error("acknowledgement check does not exit non-zero")
	}
	value := firstLine(lines, `^ack_value=[a-z-]{20,}$`)
	if value < 0 || value > guard {
		t.Error("acknowledgement value must be a fixed literal defined before the check")
	}
	for _, effect := range []string{`mktemp`, `go_bin" build`, `unshare`, `^cd `, `chmod`} {
		if i := firstLine(lines, effect); i >= 0 && i < guard {
			t.Errorf("%q at line %d runs before the acknowledgement guard (line %d)", effect, i+1, guard+1)
		}
	}
	body := strings.Join(lines, "\n")
	for _, marker := range []string{"/etc/harness-bridge", "/var/lib/harness-bridge", "/etc/systemd/system/harness-bridge*", "/etc/systemd/system/opencode*", "harness-bridge*|opencode*)"} {
		if !strings.Contains(body, marker) {
			t.Errorf("production-host accident guard does not check %s", marker)
		}
	}
	// The script may forward the caller's value to the fixture, never invent it.
	for i, l := range lines {
		if strings.Contains(l, ackName+"=") && !strings.Contains(l, ackName+`="$`+ackName+`"`) {
			t.Errorf("line %d assigns %s: %s", i+1, ackName, l)
		}
	}
}

func TestCIDoesNotSetDisposableHostAck(t *testing.T) {
	b, e := os.ReadFile("../../../../.github/workflows/ci.yml")
	if e != nil {
		t.Fatal(e)
	}
	ci := string(b)
	if regexp.MustCompile(ackName + `\s*[:=]`).MatchString(ci) {
		t.Error("CI sets the disposable-host acknowledgement")
	}
	if !strings.Contains(ci, `${`+ackName+`+set}`) {
		t.Error("CI does not fail when the disposable-host acknowledgement is set")
	}
	if strings.Count(ci, "-u "+ackName+" ") < 3 {
		t.Error("CI confinement vet/build/test steps must unset the acknowledgement")
	}
}

func TestReadmeStatesDisposableHostOnly(t *testing.T) {
	b, e := os.ReadFile("../../README.md")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"Never run it on a host that runs the Bridge or OpenCode", ackName} {
		if !strings.Contains(string(b), want) {
			t.Errorf("README missing %q", want)
		}
	}
}

func buildFixture(t *testing.T) string {
	t.Helper()
	ctx, end := context.WithTimeout(context.Background(), 60*time.Second)
	defer end()
	binary := filepath.Join(t.TempDir(), "fixture")
	if out, e := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, out)
	}
	return binary
}

// The fixture binary itself refuses run mode without the acknowledgement,
// before any privilege or namespace check, so a direct invocation is guarded too.
func TestFixtureRunRequiresDisposableHostAck(t *testing.T) {
	binary := buildFixture(t)
	for _, tc := range []struct {
		name, ack, want string
	}{
		{"missing", "", "disposable-host acknowledgement"},
		{"wrong", "yes", "disposable-host acknowledgement"},
		{"acknowledged_nonroot", "", "root required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("unprivileged guard test; root would proceed to namespace checks")
			}
			ctx, end := context.WithTimeout(context.Background(), 30*time.Second)
			defer end()
			cmd := exec.CommandContext(ctx, binary, "run", "net:[0]", "mnt:[0]", "/tmp/hb-confinement-fixture-absent")
			env := []string{"PATH=/usr/bin:/bin"}
			if tc.name == "acknowledged_nonroot" {
				env = append(env, ackName+"="+fixtureAckValue(t))
			} else if tc.ack != "" {
				env = append(env, ackName+"="+tc.ack)
			}
			cmd.Env = env
			out, e := cmd.CombinedOutput()
			if e == nil || !strings.Contains(string(out), "BLOCKED/FAIL:") || !strings.Contains(string(out), tc.want) {
				t.Fatalf("want refusal containing %q, got %v %s", tc.want, e, out)
			}
		})
	}
}

func fixtureAckValue(t *testing.T) string {
	t.Helper()
	i := firstLine(readScript(t), `^ack_value=`)
	if i < 0 {
		t.Fatal("run-fixture.sh defines no ack_value")
	}
	return strings.TrimPrefix(readScript(t)[i], "ack_value=")
}

// --- F-F: go build inherits the caller's Go environment ---------------------

func TestRunFixtureScriptBuildIsHermetic(t *testing.T) {
	lines := readScript(t)
	envLine := firstLine(lines, `^build_env='`)
	if envLine < 0 {
		t.Fatal("no fixed build_env assignment list")
	}
	assignments := strings.Fields(strings.Trim(strings.TrimPrefix(lines[envLine], "build_env="), "'"))
	for _, want := range []string{"GOENV=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "CGO_ENABLED=0"} {
		found := false
		for _, a := range assignments {
			found = found || a == want
		}
		if !found {
			t.Errorf("build_env missing %s", want)
		}
	}
	build := firstLine(lines, `"\$go_bin" build`)
	if build < 0 {
		t.Fatal("no build through the resolved go binary")
	}
	for _, want := range []string{"/usr/bin/env -i ", "$build_env", "-buildvcs=false", `GOCACHE="$build_dir/`, `GOPATH="$build_dir/`, `HOME="$build_dir/`} {
		if !strings.Contains(lines[build], want) {
			t.Errorf("build line lacks %q: %s", want, lines[build])
		}
	}
	// Behavior: with exactly this cleared environment the build succeeds even
	// though the caller's environment would sabotage an inheriting build.
	goBin, e := exec.LookPath("go")
	if e != nil {
		t.Fatal(e)
	}
	work := t.TempDir()
	args := append([]string{"-i", "PATH=/usr/bin:/bin", "HOME=" + work + "/home", "GOCACHE=" + work + "/gocache", "GOPATH=" + work + "/gopath"}, assignments...)
	args = append(args, goBin, "build", "-trimpath", "-buildvcs=false", "-o", filepath.Join(work, "fixture"), "./cmd/fixture")
	ctx, end := context.WithTimeout(context.Background(), 120*time.Second)
	defer end()
	cmd := exec.CommandContext(ctx, "/usr/bin/env", args...)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "GOFLAGS=-toolexec=/bin/false", "GOTOOLCHAIN=go1.99.0", "GOPROXY=https://invalid.example")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("hermetic build failed: %v %s", e, out)
	}
}

// --- PR #15 observation: new mount API syscalls -----------------------------

var mountAPI = map[string]uint32{"open_tree": sysOpenTree, "move_mount": sysMoveMount, "fsopen": sysFsopen, "fsconfig": sysFsconfig, "fsmount": sysFsmount, "fspick": sysFspick, "mount_setattr": sysMountSetattr, "open_tree_attr": sysOpenTreeAttr}

func nativeArch(t *testing.T) uint32 {
	switch runtime.GOARCH {
	case "arm64":
		return 0xc00000b7
	case "amd64":
		return 0xc000003e
	}
	t.Skip("fixture supports only amd64/arm64")
	return 0
}

// evalFilter interprets the classic-BPF subset that buildFilter emits against
// seccomp_data{nr, arch, ip, args[6]} (little-endian low words).
func evalFilter(t *testing.T, f []syscall.SockFilter, nr, arch uint32, args [6]uint64) uint32 {
	t.Helper()
	word := func(off uint32) uint32 {
		switch {
		case off == 0:
			return nr
		case off == 4:
			return arch
		case off >= 16 && off < 64 && off%8 == 0:
			return uint32(args[(off-16)/8])
		case off >= 16 && off < 64 && off%8 == 4:
			return uint32(args[(off-16)/8] >> 32)
		}
		t.Fatalf("unsupported load offset %d", off)
		return 0
	}
	var a uint32
	for pc := 0; pc < len(f); pc++ {
		in := f[pc]
		switch in.Code {
		case 0x20:
			a = word(in.K)
		case 0x54:
			a &= in.K
		case 0x15:
			if a == in.K {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case 0x45:
			if a&in.K != 0 {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case 0x06:
			return in.K
		default:
			t.Fatalf("unsupported BPF opcode %#x at %d", in.Code, pc)
		}
	}
	t.Fatal("filter fell off the end")
	return 0
}

func TestSeccompProgramDeniesMountAPI(t *testing.T) {
	arch := nativeArch(t)
	const deny = 0x00050000 | uint32(syscall.EPERM)
	const allow = 0x7fff0000
	for _, denyUnix := range []bool{false, true} {
		f := buildFilter(denyUnix)
		for name, nr := range mountAPI {
			if got := evalFilter(t, f, nr, arch, [6]uint64{}); got != deny {
				t.Errorf("denyUnix=%v %s(%d) -> %#x, want EPERM deny", denyUnix, name, nr, got)
			}
		}
		for name, nr := range map[string]uint32{"mount": uint32(syscall.SYS_MOUNT), "umount2": uint32(syscall.SYS_UMOUNT2), "pivot_root": uint32(syscall.SYS_PIVOT_ROOT), "chroot": uint32(syscall.SYS_CHROOT), "ptrace": uint32(syscall.SYS_PTRACE), "unshare": uint32(syscall.SYS_UNSHARE), "setns": uint32(sysSetns)} {
			if got := evalFilter(t, f, nr, arch, [6]uint64{}); got != deny {
				t.Errorf("denyUnix=%v existing deny %s -> %#x", denyUnix, name, got)
			}
		}
		for name, nr := range map[string]uint32{"read": uint32(syscall.SYS_READ), "write": uint32(syscall.SYS_WRITE), "openat": uint32(syscall.SYS_OPENAT), "exit_group": uint32(syscall.SYS_EXIT_GROUP)} {
			if got := evalFilter(t, f, nr, arch, [6]uint64{}); got != allow {
				t.Errorf("denyUnix=%v %s -> %#x, want allow", denyUnix, name, got)
			}
		}
		if got := evalFilter(t, f, uint32(syscall.SYS_READ), arch^1, [6]uint64{}); got != 0x80000000 {
			t.Errorf("foreign arch -> %#x, want kill", got)
		}
	}
}

// Real kernel check of the per-arch numbers: a child installs the fixture's
// filter (unprivileged via no_new_privs) and the mount API calls return EPERM.
// open_tree(AT_FDCWD, "/", 0) needs no privilege, so its unfiltered success
// proves 428 is open_tree on this architecture.
//
// Limitation (R2-PR18-REVIEW R-2): in the caller's namespaces an unfiltered
// kernel already returns EPERM for fsopen, fsmount, fspick and move_mount (they
// check CAP_SYS_ADMIN over the mount namespace first), so there the kernel check
// distinguishes only open_tree, fsconfig, mount_setattr and open_tree_attr (on
// kernel 6.8: ok, EINVAL, EINVAL, ENOSYS). The other four numbers are covered by
// TestSeccompProgramDeniesMountAPI (BPF evaluation) and the per-arch table.
// Where unprivileged user namespaces are allowed, a second pair of children
// runs in a new user+mount namespace with invalid flags: unfiltered, those four
// fail with EINVAL after the capability check, so EPERM there can only come
// from the filter and all eight numbers are distinguished. Where they are not
// allowed (for example Ubuntu's apparmor_restrict_unprivileged_userns=1), the
// test logs "userns discriminator unavailable" and the limitation stands.
const seccompChildEnv = "HB_FIXTURE_SECCOMP_CHILD"

func unsafePointer(p *byte) unsafe.Pointer { return unsafe.Pointer(p) }

func mountAPIProbe(invalidFlags bool) map[string]string {
	root, _ := syscall.BytePtrFromString("/")
	empty, _ := syscall.BytePtrFromString("")
	ext, _ := syscall.BytePtrFromString("ext4")
	at := ^uintptr(99) // AT_FDCWD (-100), sign-extended
	defer runtime.KeepAlive([]*byte{root, empty, ext})
	calls := map[string][6]uintptr{
		"open_tree":      {at, uintptr(unsafePointer(root)), 0},
		"move_mount":     {^uintptr(0), uintptr(unsafePointer(empty)), ^uintptr(0), uintptr(unsafePointer(empty)), 0},
		"fsopen":         {uintptr(unsafePointer(ext)), 0},
		"fsconfig":       {^uintptr(0), 0, 0, 0, 0},
		"fsmount":        {^uintptr(0), 0, 0},
		"fspick":         {^uintptr(0), uintptr(unsafePointer(empty)), 0},
		"mount_setattr":  {^uintptr(0), uintptr(unsafePointer(empty)), 0, 0, 0},
		"open_tree_attr": {at, uintptr(unsafePointer(root)), 0, 0, 0},
	}
	if invalidFlags {
		// Rejected with EINVAL right after the capability check; nothing is created.
		const bad = 0x80000000
		calls["fsopen"] = [6]uintptr{uintptr(unsafePointer(ext)), bad}
		calls["fsmount"] = [6]uintptr{^uintptr(0), bad, 0}
		calls["fspick"] = [6]uintptr{^uintptr(0), uintptr(unsafePointer(empty)), bad}
		calls["move_mount"] = [6]uintptr{^uintptr(0), uintptr(unsafePointer(empty)), ^uintptr(0), uintptr(unsafePointer(empty)), bad}
	}
	out := map[string]string{}
	for name, a := range calls {
		r, _, errno := syscall.Syscall6(uintptr(mountAPI[name]), a[0], a[1], a[2], a[3], a[4], a[5])
		if errno == 0 {
			syscall.Close(int(r))
			out[name] = "ok"
		} else {
			out[name] = errno.Error()
		}
	}
	return out
}

func TestSeccompKernelDeniesMountAPI(t *testing.T) {
	if mode := os.Getenv(seccompChildEnv); mode != "" {
		runtime.LockOSThread()
		if mode == "filtered" || mode == "userns-filtered" {
			if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, 38, 1, 0); errno != 0 {
				fmt.Println(`{"error":"no_new_privs"}`)
				os.Exit(3)
			}
			if failure := catch(func() { installFilter(false) }); failure != nil {
				fmt.Printf("{\"error\":%q}\n", fmt.Sprint(failure))
				os.Exit(3)
			}
		}
		json.NewEncoder(os.Stdout).Encode(mountAPIProbe(strings.HasPrefix(mode, "userns-")))
		os.Exit(0)
	}
	nativeArch(t)
	child := func(mode string, userns bool) (map[string]string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSeccompKernelDeniesMountAPI$", "-test.count=1")
		cmd.Env = append(os.Environ(), seccompChildEnv+"="+mode)
		if userns {
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
				UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
				GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
			}
		}
		out, e := cmd.Output()
		if e != nil {
			var exit *exec.ExitError
			if !errors.As(e, &exit) {
				return nil, &childStartError{mode: mode, err: e}
			}
			return nil, fmt.Errorf("%s child: %v %s", mode, e, out)
		}
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		r := map[string]string{}
		if e := json.Unmarshal([]byte(line), &r); e != nil {
			return nil, fmt.Errorf("%s child output %q: %v", mode, out, e)
		}
		return r, nil
	}
	results := map[string]map[string]string{}
	for _, mode := range []string{"control", "filtered"} {
		r, e := child(mode, false)
		if e != nil {
			t.Fatal(e)
		}
		results[mode] = r
	}
	eperm := syscall.EPERM.Error()
	for name := range mountAPI {
		if results["filtered"][name] != eperm {
			t.Errorf("filtered %s = %s (control %s), want %s", name, results["filtered"][name], results["control"][name], eperm)
		}
	}
	if results["control"]["open_tree"] != "ok" {
		t.Errorf("unfiltered open_tree(AT_FDCWD, \"/\", 0) = %s; syscall number %d unverified on %s", results["control"]["open_tree"], sysOpenTree, runtime.GOARCH)
	}
	distinguished := []string{}
	for name := range mountAPI {
		if results["control"][name] != eperm {
			distinguished = append(distinguished, name)
		}
	}
	sort.Strings(distinguished)
	t.Logf("control=%v filtered=%v", results["control"], results["filtered"])

	// User+mount namespace discriminator for the numbers EPERM hides above. It is
	// its own subtest so that an environment without it is reported as SKIP with
	// the reason, never as a silent PASS; a child that starts but fails is a FAIL.
	t.Run("userns_discriminator", func(t *testing.T) {
		usernsControl, e := child("userns-control", true)
		var start *childStartError
		if errors.As(e, &start) {
			t.Skipf("unprivileged user+mount namespace not creatable here (%v); kernel-distinguished %d/8 in the parent: %v; the rest rely on BPF evaluation and the per-arch table", start.err, len(distinguished), distinguished)
		}
		if e != nil {
			t.Fatal(e)
		}
		if usernsControl["fsopen"] == eperm {
			t.Skipf("user namespace grants no mount capability (unfiltered fsopen = %s, e.g. AppArmor userns restriction; control=%v); kernel-distinguished %d/8 in the parent: %v; the rest rely on BPF evaluation and the per-arch table", eperm, usernsControl, len(distinguished), distinguished)
		}
		usernsFiltered, e := child("userns-filtered", true)
		if e != nil {
			t.Fatal(e)
		}
		einval := syscall.EINVAL.Error()
		for _, name := range []string{"fsopen", "fsmount", "fspick", "move_mount"} {
			if usernsControl[name] != einval {
				t.Errorf("userns unfiltered %s(invalid flags) = %s, want %s; syscall number %d unverified on %s", name, usernsControl[name], einval, mountAPI[name], runtime.GOARCH)
			}
		}
		for name := range mountAPI {
			if usernsFiltered[name] != eperm {
				t.Errorf("userns filtered %s = %s (unfiltered %s), want %s", name, usernsFiltered[name], usernsControl[name], eperm)
			}
		}
		if !t.Failed() {
			t.Logf("userns discriminator: control=%v filtered=%v; kernel-distinguished 8/8", usernsControl, usernsFiltered)
		}
	})
}

// childStartError means the seccomp test child could not be started at all, for
// example because the kernel refused the requested user+mount namespace.
type childStartError struct {
	mode string
	err  error
}

func (e *childStartError) Error() string { return e.mode + " child did not start: " + e.err.Error() }
