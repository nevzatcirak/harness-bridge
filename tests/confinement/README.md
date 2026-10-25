# Synthetic filesystem-confinement candidate

This is an offline Linux kernel fixture, not a harness launcher or an installed service. It never launches OpenCode, reads native history, loads actual credentials, connects to production sockets, or acquires a Bridge writer lock. Run only reviewed source on a disposable test host with root/CAP_SYS_ADMIN. The test temporarily creates its own mount, PID, IPC and network namespaces; it does not relax production confinement.

```sh
go build ./...
go vet ./...
go test ./... -count=1
HB_CONFINEMENT_INTEGRATION=1 go test ./... -count=1 -v
HB_CONFINEMENT_INTEGRATION=1 go test -race ./... -count=1 -v
./run-fixture.sh
```

Ordinary tests deliberately skip the privileged integration test. Explicit integration requests fail if privileges/namespaces are unavailable. Direct invocation of the driver's `run` mode outside its namespaces is refused. The runner builds a static fixture with the installed Go toolchain and standard library; it downloads no packages. Generated directories are removed on normal completion. No service manager operation occurs.

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

The filter denies mount/namespace manipulation, ptrace and privilege escalation primitives tested here; it is not a complete syscall allowlist or kernel-exploit defense. The host setup process is trusted and privileged. Workspace execution is intentionally permitted. Root-owned runtime files and bindings must never be selected by repository/client input.

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
