//go:build linux

// This privileged, disposable fixture is NOT a launcher to install or expose
// through MCP. Its only input is its own generated synthetic fixture directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const workspace = "/opt/harness-bridge-test/workspace"
const uid = 65534

var cleanEnv = []string{"PATH=/usr/bin", "HOME=/state/home", "XDG_DATA_HOME=/state/data", "XDG_STATE_HOME=/state/state", "XDG_CACHE_HOME=/cache", "TMPDIR=/tmp", "SYNTHETIC_ONLY=1"}

type check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}
type report struct {
	ExecErrors map[string]string `json:"exec_errors,omitempty"`
	Mode       string            `json:"mode"`
	Checks     []check           `json:"checks"`
	Residuals  []string          `json:"residuals,omitempty"`
}

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
func main() {
	defer func() {
		if e := recover(); e != nil {
			fmt.Fprintln(os.Stderr, "BLOCKED/FAIL:", e)
			os.Exit(2)
		}
	}()
	require(len(os.Args) >= 2, "fixture mode required")
	switch os.Args[1] {
	case "noop":
		fmt.Println("FIXTURE_EXEC_OK")
	case "run":
		run()
	case "setup", "setup-compat":
		require(len(os.Args) == 5, "setup input")
		setup(os.Args[2], os.Args[3], os.Args[4], os.Args[1] == "setup-compat")
	case "control", "probe", "probe-compat":
		require(len(os.Args) == 5, "probe input")
		probe(os.Args[1], os.Args[2], os.Args[3], os.Args[4])
	default:
		panic("unknown fixture mode")
	}
}
func write(path string, data []byte, mode os.FileMode) {
	must(os.WriteFile(path, data, mode))
	must(os.Chmod(path, mode))
}
func copyFile(from, to string, mode os.FileMode) {
	f, e := os.Open(from)
	must(e)
	defer f.Close()
	g, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	must(e)
	_, e = io.Copy(g, f)
	must(e)
	must(g.Close())
	must(os.Chmod(to, mode))
}
func serve(l net.Listener) {
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}()
}
func command(ctx context.Context, path string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, path, args...)
	c.Env = cleanEnv
	return c
}
func run() {
	require(os.Geteuid() == 0, "root required only in disposable fixture setup")
	require(len(os.Args) == 4 && os.Getpid() == 1, "run only under outer disposable namespaces")
	netns, e := os.Readlink("/proc/self/ns/net")
	must(e)
	mntns, e := os.Readlink("/proc/self/ns/mnt")
	must(e)
	require(netns != os.Args[2] && mntns != os.Args[3], "outer namespaces must differ from invoking host")
	// Bring up only the disposable outer loopback: no host routing/interface change.
	loop := exec.Command("/usr/sbin/ip", "link", "set", "lo", "up")
	loop.Env = cleanEnv
	must(loop.Run())
	exe, e := os.Executable()
	must(e)
	base, e := os.MkdirTemp("/tmp", "hb-confinement-fixture-")
	must(e)
	must(os.Chmod(base, 0755))
	defer os.RemoveAll(base)
	write(filepath.Join(base, "SYNTHETIC_ONLY"), []byte("NO_PRODUCTION_DATA"), 0600)
	for _, dir := range []string{"root", "host/outside", "host/credentials", "host/other-workspace", "host/run", "workspace", "state/home", "state/data", "state/state", "cache", "tmp"} {
		must(os.MkdirAll(filepath.Join(base, dir), 0755))
	}
	for _, dir := range []string{"workspace", "state", "state/home", "state/data", "state/state", "cache", "tmp"} {
		must(os.Chown(filepath.Join(base, dir), uid, uid))
		must(os.Chmod(filepath.Join(base, dir), 0700))
	}
	write(filepath.Join(base, "host/outside/public"), []byte("SYNTHETIC_PUBLIC"), 0666)
	write(filepath.Join(base, "host/credentials/test-token"), []byte("NOT_A_REAL_CREDENTIAL"), 0666)
	write(filepath.Join(base, "host/other-workspace/public"), []byte("SYNTHETIC_OTHER_PROJECT"), 0666)
	write(filepath.Join(base, "host/outside/hardlink-target"), []byte("SYNTHETIC_SHARED_INODE"), 0666)
	must(os.Link(filepath.Join(base, "host/outside/hardlink-target"), filepath.Join(base, "workspace/imported-hardlink")))
	copyFile(exe, filepath.Join(base, "host/outside/program"), 0755)
	copyFile(exe, filepath.Join(base, "workspace/allowed-program"), 0755)
	write(filepath.Join(base, "state/own-bootstrap-credential"), []byte("SYNTHETIC_SAME_UID_CREDENTIAL"), 0600)
	must(os.Chown(filepath.Join(base, "state/own-bootstrap-credential"), uid, uid))
	must(os.Symlink("/outside/public", filepath.Join(base, "workspace/absolute-escape")))
	must(os.Symlink("../../../../outside/public", filepath.Join(base, "workspace/relative-escape")))
	must(os.Symlink(filepath.Join(base, "host/outside/public"), filepath.Join(base, "workspace/host-path-escape")))
	must(os.Symlink("/etc/harness-bridge/private/test-token", filepath.Join(base, "workspace/credential-escape")))
	socket := filepath.Join(base, "host/run/docker.sock")
	l, e := net.Listen("unix", socket)
	must(e)
	defer l.Close()
	must(os.Chmod(socket, 0666))
	serve(l)
	abstract := "@hb-synthetic-" + filepath.Base(base)
	a, e := net.Listen("unix", abstract)
	must(e)
	defer a.Close()
	serve(a)
	tcpListener, e := net.Listen("tcp4", "127.0.0.1:0")
	must(e)
	defer tcpListener.Close()
	serve(tcpListener)
	tcp := tcpListener.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	control := command(ctx, exe, "control", base, abstract, tcp)
	control.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{}, NoSetGroups: false}}
	raw, e := control.Output()
	must(e)
	var baseline report
	must(json.Unmarshal(raw, &baseline))
	for _, c := range baseline.Checks {
		require(c.Pass, "baseline must prove synthetic outside access: "+c.Name)
	}
	// Every bind source below is generated by this program. No actual workspace,
	// credentials, OpenCode executable, Docker socket or runtime state is mounted.
	variants := map[string]report{}
	all := true
	for _, mode := range []string{"setup", "setup-compat"} {
		args := []string{"--mount", "--ipc", "--pid", "--fork", "--kill-child", "--mount-proc"}
		if mode == "setup" {
			args = append(args, "--net")
		}
		args = append(args, exe, mode, base, abstract, tcp)
		sandbox := command(ctx, "/usr/bin/unshare", args...)
		var stderr strings.Builder
		sandbox.Stderr = &stderr
		raw, e = sandbox.Output()
		if e != nil {
			panic("private profile failed: " + stderr.String())
		}
		var confined report
		must(json.Unmarshal(raw, &confined))
		variants[mode] = confined
		for _, c := range confined.Checks {
			if !c.Pass {
				all = false
			}
		}
	}
	result := struct {
		SyntheticOnly               bool              `json:"synthetic_only"`
		Baseline                    report            `json:"baseline"`
		Variants                    map[string]report `json:"variants"`
		RequiredChecksPass          bool              `json:"required_checks_pass"`
		CredentialIsolationComplete bool              `json:"credential_isolation_complete"`
		ProductionReady             bool              `json:"production_ready"`
		ProductionTargetsUsed       bool              `json:"production_targets_used"`
	}{true, baseline, variants, all, false, false, false}
	encoded, e := json.MarshalIndent(result, "", "  ")
	must(e)
	fmt.Println(string(encoded))
	if !all {
		os.Exit(1)
	}
}
func bind(source, target string, ro, noexec bool) {
	must(syscall.Mount(source, target, "", syscall.MS_BIND, ""))
	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | syscall.MS_NODEV)
	if ro {
		flags |= syscall.MS_RDONLY
	}
	if noexec {
		flags |= syscall.MS_NOEXEC
	}
	must(syscall.Mount("", target, "", flags, ""))
}
func setup(base, abstract, tcp string, compatible bool) {
	runtime.LockOSThread()
	require(os.Geteuid() == 0 && os.Getpid() == 1, "setup only as PID1 in disposable namespace")
	require(filepath.IsAbs(base) && filepath.Clean(base) == base && strings.HasPrefix(base, "/tmp/hb-confinement-fixture-"), "invalid synthetic root")
	info, e := os.Lstat(base)
	must(e)
	require(info.IsDir() && info.Sys().(*syscall.Stat_t).Uid == 0, "untrusted fixture root")
	marker, e := os.ReadFile(filepath.Join(base, "SYNTHETIC_ONLY"))
	must(e)
	require(string(marker) == "NO_PRODUCTION_DATA", "not synthetic fixture")
	must(syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""))
	root := filepath.Join(base, "root")
	must(syscall.Mount("tmpfs", root, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "mode=0755,size=32m"))
	for _, dir := range []string{"usr/bin", strings.TrimPrefix(workspace, "/"), "state", "cache", "tmp", "proc", "etc/harness-bridge/private", "run/credentials", "var/run", "opt/other-workspace", "sys", "dev"} {
		must(os.MkdirAll(filepath.Join(root, dir), 0755))
	}
	runtimePath := filepath.Join(root, "usr/bin/fixture-runtime")
	write(runtimePath, nil, 0555)
	exe, e := os.Executable()
	must(e)
	bind(exe, runtimePath, true, false)
	for _, entry := range []struct {
		src, dst string
		noexec   bool
	}{{"workspace", workspace, false}, {"state", "/state", true}, {"cache", "/cache", true}, {"tmp", "/tmp", true}} {
		bind(filepath.Join(base, entry.src), filepath.Join(root, entry.dst), false, entry.noexec)
	}
	// Explicit public runtime closure for a real shell, not a host /usr bind.
	// Unsupported distro/architecture paths fail instead of broadening the mounts.
	runtimeFiles := []struct{ source, target string }{{"/bin/dash", "/usr/bin/sh"}}
	switch runtime.GOARCH {
	case "arm64":
		runtimeFiles = append(runtimeFiles, struct{ source, target string }{"/lib/ld-linux-aarch64.so.1", "/lib/ld-linux-aarch64.so.1"}, struct{ source, target string }{"/lib/aarch64-linux-gnu/libc.so.6", "/lib/aarch64-linux-gnu/libc.so.6"})
	case "amd64":
		runtimeFiles = append(runtimeFiles, struct{ source, target string }{"/lib64/ld-linux-x86-64.so.2", "/lib64/ld-linux-x86-64.so.2"}, struct{ source, target string }{"/lib/x86_64-linux-gnu/libc.so.6", "/lib/x86_64-linux-gnu/libc.so.6"})
	default:
		panic("unsupported shell runtime architecture")
	}
	for _, file := range runtimeFiles {
		target := filepath.Join(root, file.target)
		must(os.MkdirAll(filepath.Dir(target), 0755))
		write(target, nil, 0555)
		bind(file.source, target, true, false)
	}
	// Minimal runtime device, not a bind of the host /dev tree. os/exec
	// requires /dev/null even for a static binary with no dynamic libraries.
	null := filepath.Join(root, "dev/null")
	write(null, nil, 0600)
	must(syscall.Mount("/dev/null", null, "", syscall.MS_BIND, ""))
	must(syscall.Mount("", null, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_NOSUID|syscall.MS_NOEXEC, ""))
	must(syscall.Mount("proc", filepath.Join(root, "proc"), "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "hidepid=2,subset=pid"))
	must(syscall.Mount("", root, "tmpfs", syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""))
	must(syscall.Chroot(root))
	must(os.Chdir(workspace))
	for capability := 0; capability <= 40; capability++ {
		_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, 24, uintptr(capability), 0)
		if errno != 0 && errno != syscall.EINVAL {
			must(errno)
		}
	}
	_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, 38, 1, 0)
	mustErrno(errno)
	must(syscall.Setgroups([]int{}))
	must(syscall.Setgid(uid))
	must(syscall.Setuid(uid))
	installFilter(compatible)
	// Exec discards setup threads and their inherited state. Every new worker
	// thread/child inherits the filter, empty bounding set and no_new_privs.
	must(syscall.Exec("/usr/bin/fixture-runtime", []string{"fixture-runtime", func() string {
		if compatible {
			return "probe-compat"
		}
		return "probe"
	}(), base, abstract, tcp}, cleanEnv))
}
func mustErrno(e syscall.Errno) {
	if e != 0 {
		panic(e)
	}
}
func installFilter(denyUnix bool) {
	arch := uint32(0)
	switch runtime.GOARCH {
	case "arm64":
		arch = 0xc00000b7
	case "amd64":
		arch = 0xc000003e
	default:
		panic("unsupported native seccomp architecture")
	}
	const ld = 0x20
	const jeq = 0x15
	const jset = 0x45
	const ret = 0x06
	const and = 0x54
	const allow = 0x7fff0000
	const deny = 0x00050000 | uint32(syscall.EPERM)
	f := []syscall.SockFilter{{Code: ld, K: 4}, {Code: jeq, K: arch, Jt: 1}, {Code: ret, K: 0x80000000}, {Code: ld, K: 0}}
	if runtime.GOARCH == "amd64" {
		f = append(f, syscall.SockFilter{Code: jset, K: 0x40000000, Jf: 1}, syscall.SockFilter{Code: ret, K: deny})
	}
	// clone3's flags live behind a pointer, so reject it with ENOSYS to
	// preserve documented libc/Go clone fallback, then inspect classic clone flags.
	f = append(f, syscall.SockFilter{Code: jeq, K: 435, Jf: 1}, syscall.SockFilter{Code: ret, K: 0x00050000 | uint32(syscall.ENOSYS)})
	for _, number := range []uint32{uint32(syscall.SYS_UNSHARE), uint32(syscall.SYS_SETNS), uint32(syscall.SYS_MOUNT), uint32(syscall.SYS_UMOUNT2), uint32(syscall.SYS_PIVOT_ROOT), uint32(syscall.SYS_CHROOT), uint32(syscall.SYS_PTRACE)} {
		f = append(f, syscall.SockFilter{Code: jeq, K: number, Jf: 1}, syscall.SockFilter{Code: ret, K: deny})
	}
	if denyUnix {
		// Match systemd's inet-only address-family policy, including socketpair.
		for _, nr := range []uint32{uint32(syscall.SYS_SOCKET), uint32(syscall.SYS_SOCKETPAIR)} {
			f = append(f, syscall.SockFilter{Code: jeq, K: nr, Jf: 5}, syscall.SockFilter{Code: ld, K: 16}, syscall.SockFilter{Code: jeq, K: syscall.AF_INET, Jt: 1}, syscall.SockFilter{Code: jeq, K: syscall.AF_INET6, Jf: 1}, syscall.SockFilter{Code: ret, K: allow}, syscall.SockFilter{Code: ret, K: 0x00050000 | uint32(syscall.EAFNOSUPPORT)})
		}
	}
	f = append(f, syscall.SockFilter{Code: jeq, K: uint32(syscall.SYS_CLONE), Jf: 4}, syscall.SockFilter{Code: ld, K: 16}, syscall.SockFilter{Code: and, K: 0x7e020080}, syscall.SockFilter{Code: jeq, K: 0, Jt: 1}, syscall.SockFilter{Code: ret, K: deny}, syscall.SockFilter{Code: ret, K: allow})
	program := syscall.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, 22, 2, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(f)
	mustErrno(errno)
}
func probe(mode, base, abstract, tcp string) {
	r := report{Mode: mode, Checks: []check{}}
	add := func(name string, pass bool) { r.Checks = append(r.Checks, check{Name: name, Pass: pass}) }
	read := func(p string) bool { _, e := os.ReadFile(p); return e == nil }
	writeAt := func(p string) bool { return os.WriteFile(p, []byte("SYNTHETIC_WRITE"), 0600) == nil }
	execErr := map[string]error{}
	execute := func(p string) bool {
		ctx, end := context.WithTimeout(context.Background(), 3*time.Second)
		defer end()
		out, e := exec.CommandContext(ctx, p, "noop").Output()
		if e != nil {
			if r.ExecErrors == nil {
				r.ExecErrors = map[string]string{}
			}
			r.ExecErrors[p] = e.Error()
			execErr[p] = e
		}
		return e == nil && strings.TrimSpace(string(out)) == "FIXTURE_EXEC_OK"
	}
	dial := func(p string) bool {
		c, e := net.DialTimeout("unix", p, time.Second)
		if e != nil {
			return false
		}
		c.Close()
		return true
	}
	outside := filepath.Join(base, "host/outside/public")
	cred := filepath.Join(base, "host/credentials/test-token")
	other := filepath.Join(base, "host/other-workspace/public")
	docker := filepath.Join(base, "host/run/docker.sock")
	if mode == "control" {
		add("synthetic_outside_read_allowed_without_profile", read(outside))
		add("synthetic_outside_write_allowed_without_profile", writeAt(outside))
		add("synthetic_outside_exec_allowed_without_profile", execute(filepath.Join(base, "host/outside/program")))
		add("synthetic_credential_read_allowed_without_profile", read(cred))
		add("synthetic_other_workspace_read_allowed_without_profile", read(other))
		add("synthetic_docker_socket_connect_allowed_without_profile", dial(docker))
		add("synthetic_abstract_socket_connect_allowed_without_profile", dial(abstract))
		c, e := net.DialTimeout("tcp", tcp, time.Second)
		add("synthetic_tcp_control", e == nil)
		if e == nil {
			c.Close()
		}
		json.NewEncoder(os.Stdout).Encode(r)
		return
	}
	add("nonroot_uid_gid", os.Geteuid() == uid && os.Getegid() == uid)
	status, e := os.ReadFile("/proc/self/status")
	must(e)
	for _, key := range []string{"CapEff", "CapPrm", "CapBnd", "CapAmb"} {
		add(key+"_zero", strings.Contains(string(status), key+":\t0000000000000000"))
	}
	add("no_new_privileges", strings.Contains(string(status), "NoNewPrivs:\t1"))
	add("seccomp_filter", strings.Contains(string(status), "Seccomp:\t2"))
	cwd, e := os.Getwd()
	add("original_style_workspace_cwd", e == nil && cwd == workspace)
	add("workspace_write_read", writeAt("work.txt") && read("work.txt"))
	must(os.MkdirAll("subdir", 0700))
	add("workspace_rename", os.Rename("work.txt", "subdir/renamed.txt") == nil)
	add("workspace_exec", execute("./allowed-program"))
	add("approved_runtime_exec", execute("/usr/bin/fixture-runtime"))
	add("approved_runtime_read", read("/usr/bin/fixture-runtime"))
	shell := func(args ...string) bool {
		ctx, end := context.WithTimeout(context.Background(), 3*time.Second)
		defer end()
		return exec.CommandContext(ctx, "/usr/bin/sh", args...).Run() == nil
	}
	add("allowed_workspace_shell_script", os.WriteFile("workspace.sh", []byte("test -r ./allowed-program\n"), 0700) == nil && shell("./workspace.sh"))
	for _, dir := range []string{"/state/data", "/cache", "/tmp"} {
		add("required_rw_"+dir, writeAt(dir+"/data") && read(dir+"/data"))
		os.Remove(dir + "/program")
		copyFile("/usr/bin/fixture-runtime", dir+"/program", 0755)
		add("direct_exec_denied_"+dir, !execute(dir+"/program") && errors.Is(execErr[dir+"/program"], syscall.EACCES))
	}
	add("unapproved_host_read_denied", !read(outside))
	add("unapproved_host_write_denied", !writeAt(outside))
	add("unapproved_host_exec_denied", !execute(filepath.Join(base, "host/outside/program")))
	add("outside_guest_read_write_denied", !read("/outside/public") && !writeAt("/outside/public"))
	add("root_readonly", !writeAt("/etc/created"))
	add("root_mkdir_denied", os.Mkdir("/outside", 0777) != nil)
	for _, name := range []string{"absolute-escape", "relative-escape", "host-path-escape", "credential-escape"} {
		add("symlink_denied_"+name, !read(name) && !writeAt(name))
	}
	add("other_workspace_read_write_denied", !read(other) && !writeAt(other) && !read("/opt/other-workspace/public"))
	add("host_credential_paths_denied", !read(cred) && !read("/etc/harness-bridge/private/test-token") && !read("/run/credentials/another.service/test-token"))
	add("docker_socket_path_connect_denied", !dial(docker) && !dial("/var/run/docker.sock") && !dial("/run/docker.sock"))
	add("abstract_socket_denied", !dial(abstract))
	c, te := net.DialTimeout("tcp", tcp, time.Second)
	if te == nil {
		c.Close()
	}
	if mode == "probe-compat" {
		add("tcp_transport_works_in_synthetic_shared_network", te == nil)
		r.Residuals = append(r.Residuals, "inet-compatible variant permits TCP peers in its shared network; not network isolation")
	} else {
		add("private_network_blocks_outer_tcp", te != nil)
	}
	add("proc_magic_link_escape_denied", !read("/proc/1/root"+outside))
	_, e = os.Stat("/proc/sys")
	add("proc_subset_pid", errors.Is(e, os.ErrNotExist))
	add("uid_escalation_denied", syscall.Setuid(0) != nil && os.Geteuid() == uid)
	add("new_user_namespace_denied", syscall.Unshare(syscall.CLONE_NEWUSER) == syscall.EPERM)
	script := "/tmp/interpreted.sh"
	must(os.WriteFile(script, []byte("exit 0\n"), 0700))
	interpreted := shell(script)
	add("noexec_interpreter_residual_detected", interpreted)
	if interpreted {
		r.Residuals = append(r.Residuals, "noexec blocks direct exec only; an allowed shell still interprets readable code from writable /tmp")
	}
	// Deliberately demonstrate the limit: this profile does not protect the
	// harness's own credentials from tools sharing its UID/state/environment.
	own := read("/state/own-bootstrap-credential")
	add("own_credential_residual_detected", own)
	if own {
		r.Residuals = append(r.Residuals, "same-UID tools can read credentials intentionally present in their allowed state; privilege separation still required")
	}
	// A path namespace cannot hide an inode already linked into an allowed bind.
	linked := read("imported-hardlink") && writeAt("imported-hardlink")
	add("imported_hardlink_residual_detected", linked)
	if linked {
		r.Residuals = append(r.Residuals, "an imported hard link exposes and modifies its shared outside inode; bind-root confinement does not enforce file provenance")
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
