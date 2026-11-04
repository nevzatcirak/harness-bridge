# Synthetic filesystem-confinement candidate

This is an offline Linux kernel fixture, not a harness launcher or an installed service. It never launches OpenCode, reads native history, loads actual credentials, connects to production sockets, or acquires a Bridge writer lock. Run only reviewed source on a disposable test host with root/CAP_SYS_ADMIN. The test temporarily creates its own mount, PID, IPC and network namespaces; it does not relax production confinement.

## Disposable host only

The privileged run executes as root and writes a synthetic tree, including directories owned by UID 65534, under the host's `/tmp`. **Never run it on a host that runs the Bridge or OpenCode**, or on any host whose data or services matter. Use a throwaway VM or machine with no production credentials, sessions or routes, and record which host produced any evidence.

`run-fixture.sh` and the fixture's `run` mode fail closed unless `HB_CONFINEMENT_DISPOSABLE_HOST=disposable-host-without-bridge-or-opencode` is set explicitly by the operator for that one invocation. The script also refuses, as an accident guard only, when it sees `/etc/harness-bridge`, `/var/lib/harness-bridge`, `harness-bridge*`/`opencode*` systemd units, or running `harness-bridge*`/`opencode*` processes. Absence of these markers does not prove a host is disposable. CI must never set the acknowledgement; it fails if it is set.

```sh
go build ./...
go vet ./...
go test ./... -count=1
# Privileged, disposable host only:
HB_CONFINEMENT_DISPOSABLE_HOST=disposable-host-without-bridge-or-opencode HB_CONFINEMENT_INTEGRATION=1 go test ./... -count=1 -v
HB_CONFINEMENT_DISPOSABLE_HOST=disposable-host-without-bridge-or-opencode HB_CONFINEMENT_INTEGRATION=1 go test -race ./... -count=1 -v
HB_CONFINEMENT_DISPOSABLE_HOST=disposable-host-without-bridge-or-opencode ./run-fixture.sh
```

Ordinary tests deliberately skip the privileged integration test. Explicit integration requests fail if privileges/namespaces are unavailable. Direct invocation of the driver's `run` mode without the acknowledgement or outside its namespaces is refused. The `setup`/`setup-compat` modes also require the acknowledgement and refuse, before any mount or other side effect, unless file descriptor 3 names a mount namespace that differs from their own, and from `/proc/1`'s when `/proc/1` is another process (comparing with `/proc/1/ns/mnt` alone is vacuous there, because under `unshare --mount-proc` setup is itself PID 1); `run` passes its own mount namespace as fd 3. This is an accident guard, not proof that fd 3 is the invoking `run` process's namespace: a root caller who deliberately supplies another mount namespace passes it. Setup closes fd 3 once the guard passes, so the probe it later executes never inherits a namespace descriptor. The `control`/`probe`/`probe-compat` modes refuse unless the effective uid is the fixture probe uid 65534, before any file is written. The `run` namespace check compares `/proc/self/ns/{net,mnt}` with caller-provided values: `run-fixture.sh` passes the invoking shell's namespaces, but a direct caller can pass any strings, so the check catches accidental host invocations rather than proving isolation from the host. That is acceptable only on a disposable host, where the acknowledgement, root and PID-1 checks and the private namespaces created by the runner are accident guards and a mistake cannot reach a Bridge, OpenCode or production data. The runner builds a static fixture with the installed Go toolchain and standard library under a cleared environment (`env -i`, `GOENV=off`, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly`, `GOPROXY=off`, `GOWORK=off`, `-buildvcs=false` so root never runs git in the checkout, build cache inside its temporary build directory), so it neither downloads packages or toolchains nor inherits the caller's `GOFLAGS`/toolexec. The runner creates the fixture root and build directory and removes both on every shell exit path (success, failure, INT/TERM/HUP); the fixture also removes its root on failed checks and panics. Only SIGKILL of the runner shell can leave them behind. No service manager operation occurs.

Root never writes through a path inside a UID-65534-owned directory: every synthetic entry is created (`O_EXCL|O_NOFOLLOW`, mode/owner via the descriptor) while all directories are still root-owned, and ownership is handed over last, children before parents, through `O_NOFOLLOW` directory descriptors.

## Actual boundary

The root setup code creates a new, read-only root filesystem and binds only these resources. Probes run with UID/GID 65534, no supplementary groups, no capabilities, no-new-privileges and an inherited seccomp filter. All workspace/state/cache files below are newly generated synthetic files, including the directory that has the approved workspace's spelling.

| Guest resource | Access | Source |
|---|---|---|
| `/opt/harness-bridge-test/workspace` | read/write/execute | synthetic workspace only |
| `/state`, `/cache`, `/tmp` | read/write, direct execution denied | separate synthetic directories |
| `/usr/bin/fixture-runtime` | read/execute | freshly built static probe |
| `/usr/bin/sh`, loader, libc | read/execute | explicit read-only binds of public host runtime files |
| `/dev/null` | read/write | this single standard device |
| `/proc` | private PID namespace, hidepid/subset | new proc mount |
| all other host paths | absent | no host root bind |

The filter denies mount/namespace manipulation (including the new mount API: `open_tree`, `move_mount`, `fsopen`, `fsconfig`, `fsmount`, `fspick`, `mount_setattr`, `open_tree_attr`), ptrace and privilege escalation primitives tested here; it is not a complete syscall allowlist or kernel-exploit defense. The host setup process is trusted and privileged. Workspace execution is intentionally permitted. Root-owned runtime files and bindings must never be selected by repository/client input.

Two variants are actually exercised: `setup` has another private network namespace and cannot reach the synthetic outer TCP/abstract sockets. `setup-compat` shares only the **synthetic outer** network, allows AF_INET/AF_INET6, denies AF_UNIX socket creation, and reaches its synthetic TCP endpoint. Neither connects to the host's actual Docker socket or OpenCode server. The baseline proves the synthetic outside files/programs/sockets would otherwise be accessible.

The tests include workspace read/write/rename, native executable and real shell-script execution; outside read/write/execute denial; absolute/relative symlinks; credential paths; other workspace; Docker-shaped pathname and abstract sockets; proc magic links; namespace escalation; and state/cache/tmp execution behavior. Results explicitly retain residuals instead of equating every expected check with full security.

## Deliberately unresolved

* A credential readable by the same UID inside allowed state remains readable by a tool. The live service's own LoadCredential access is a separate finding; hiding the host credential directory does not solve same-UID credential separation. API/provider secrets also need a separately designed privilege boundary, not an environment-only workaround.
* `noexec` blocks direct execution; an allowed shell can interpret a script in `/tmp`. This was tested with a real shell. This profile is a filesystem visibility boundary, not code provenance enforcement.
* Network-compatible mode permits TCP and therefore TCP exfiltration/proxy paths. AF_UNIX denial alone is not Docker/API/network authorization. A separately reviewed egress design is required.
* A pre-imported hard link in the allowed workspace exposes and can modify its outside shared inode; this was actively reproduced.
* Allowed workspace/state access exposes everything inside those grants. Imported hard links, inherited descriptors, delegated sockets and shared service identity need separate controls. This fixture does not establish all application-level paths are safe.
* OpenCode/Bun startup, provider networking, real builds, native tools, dependency closure and session continuity were **not** exercised. Only ARM64 kernel execution was tested. Go race success concerns the Go test harness, not arbitrary native tools or every kernel race.

`profiles/*.conf` are review-only systemd candidates, not deployed drop-ins. Their `/proc` handling differs from the tested private PID namespace and is unverified. The fixed bootstrap and reviewed rootfs do not yet exist. See [ROLLOUT.md](ROLLOUT.md). **Synthetic boundary: PASS. Production installation: NO-GO pending the listed gates.**
