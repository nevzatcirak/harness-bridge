# H3 local systemd/OpenCode validation — 2026-10-24 UTC

Result: actual local MCP integration and real two-turn test-agent continuation
PASS. This is not ordinary Claude Chat E2E or a public HTTPS deployment.
Implementation commit: `2c97334` (`fix(security): validate systemd credential ACLs without relaxing file privacy`).

## Credential failure and fix

systemd `255 (255.4-1ubuntu8.18)` supplies LoadCredential files as root:root
regular files with mode 0440. Their access ACL is exactly owner read, named
service UID read, owning group none, mask read, other none. The directory is
root:root 0550 with the corresponding owner/named-user read+execute ACL.
The mode's group-read bit represents the ACL mask, not access for the root group.
Both were observed in the real Bridge namespace and in a metadata-only transient
probe using the same service user. Credential mount: read-only tmpfs with
nosuid,nodev,noexec,nosymfollow,noswap. There was no credential symlink.

The old `Perm() & 0077` rejected that legitimate ACL as group-readable. Do NOT
chmod systemd's read-only credential files or replace them with shared 0640 files.
The [systemd 255 credential interface](https://www.freedesktop.org/software/systemd/man/255/systemd.exec.html#Credentials)
and [systemd v255 programming interface](https://github.com/systemd/systemd-stable/blob/v255-stable/docs/CREDENTIALS.md)
define CREDENTIALS_DIRECTORY and `%d`; observed ACL/mount details are local
runtime evidence, not assumptions about every systemd installation.

The Linux reader opens with O_NOFOLLOW/O_NONBLOCK/O_CLOEXEC, checks and reads the
same descriptor, bounds reads to 16384 bytes, and retains strict normal-file
permissions and mutually exclusive inline/file configuration. A narrow exception
for 0440 files requires canonical direct membership in CREDENTIALS_DIRECTORY
under `/run/credentials`, root:root ownership, exact directory/file modes,
read-only tmpfs/ramfs, matching device/inode and exact ACL entries granting only
root and the current service UID. Extra users/groups, write grants or other
access fail closed. Setting CREDENTIALS_DIRECTORY to an arbitrary directory does
not authorize group-readable files. No secret values appear in errors.
Non-Linux builds retain private-file input but have no systemd ACL exception;
other systemd credential layouts fail closed if they do not match this policy.
Existing pinned x/sys was promoted to a direct dependency; no version change.

Tests cover ordinary private files, inline configuration/conflicts, size/content,
symlink, directory, FIFO, real group/world readability, forged directory,
wrong/modified/extra ACL entries and a separately invoked genuine systemd
LoadCredential positive test. The positive test used a synthetic fixture, not a
production credential, as User/Group=harness-bridge with ProtectSystem=strict and
NoNewPrivileges. `TestSystemdManagedCredential` passed; transient service exited
0 and was collected. The normal suite explicitly skips that opt-in test.

## Service changes and installed binary

Existing workdir drop-in retained: `/var/lib/harness-bridge` (0700,
harness-bridge:harness-bridge), resolving the earlier CHDIR error. No permissions
were broadened to make the workspace the Bridge process's cwd.

Only new persistent systemd change:
`/etc/systemd/system/harness-bridge-test.service.d/validation.conf`:

```ini
[Service]
# Test validation is operator-controlled; fail once rather than looping.
Restart=no
```

Ran daemon-reload and started the previously inactive Bridge test service once.
No restart loops, hardening reductions, OpenCode restarts, credential changes,
workspace permission changes or other service configuration changes occurred.
Original hardening remains: unprivileged User/Group=harness-bridge,
NoNewPrivileges=yes, empty CapabilityBoundingSet, ProtectSystem=strict,
ProtectHome=yes, PrivateTmp=yes. Source credential directory remains root:root
0700; source files root:root 0600. `%d` resolves to
`/run/credentials/harness-bridge-test.service`; both files loaded successfully.

After all required checks passed, built the committed source, backed up the old
binary to `/var/lib/harness-bridge/harness-bridge.before-h3-credential-fix`, and
installed root:root 0755 `/usr/local/bin/harness-bridge`. Installed and tested
binary SHA256 both:
`15c03c4bcaeea5c40e3536b153a2f72b42463f2532facb1d49d18ee4f05fdf0d`.

Observed service/listeners after startup and the two-turn run:

| Service | User | PID | Listener | Restarts |
|---|---|---:|---|---:|
| harness-bridge-test | harness-bridge | 3690330 | 127.0.0.1:8080 | 0 |
| opencode-hb-test | opencode-hb | 3679520 | 127.0.0.1:4097 | 0 |
| Original OpenCode (unchanged) | root | 3489290 | 127.0.0.1:4096 | untouched |

Both test services active/running. Original PID/start-time ticks
`3489290 / 1354613519` matched before and after; it was not contacted with a
credential, restarted, reconfigured, migrated or used as the Bridge target.
The running Bridge's selected nonsecret environment confirms exclusively
`OPENCODE_URL=http://127.0.0.1:4097`, username `opencode`, loopback :8080,
only the supplied test session and `/opt/harness-bridge-test/workspace` root.
No inline token/password environment values were present in the Bridge process.
Successful SDK metadata/history/continuation proves the loaded Basic Auth works.

## Actual MCP and agent evidence

Used the official Go SDK through the separately running systemd Bridge; the
OpenCode backend is the real existing non-root service, not a fixture. The smoke
utility now supports `H2_READ_ONLY=1` and explicit `H2_ALLOW_EMPTY_HISTORY=1`.
Default H1/H2 seeded-history requirements remain unchanged without that opt-in.
Test credentials were read only for authorized local checks and passed in the
child's environment, never printed, stored in a script, or placed in arguments.
The launcher contains paths and test configuration only.

Read-only run exited 0:

```text
native idle preflight verified; no pending permissions or unfinished assistant
existing session selected; native ID and directory redacted; history before prompts: 0 messages
missing and incorrect MCP bearer tokens rejected with HTTP 401
official SDK initialize, seven tools, exact-session discovery, metadata/history and unauthorized session denials verified
PASS: read-only local MCP authentication and session policy checks; no prompt submitted
```

SDK discovery returned exactly one session, internally compared to the full
operator-supplied test ID. Metadata directory and history matched native API
results. get_session, read_session_history and send_prompt against a synthetic
non-allowlisted session all returned typed FORBIDDEN errors before native access.
No negative prompt was submitted to a native session.

The existing provider API reported connected provider `opencode`, default model
`big-pickle`. No model/provider credentials were copied or configured. The real
continuation run exited 0: turn 1 and turn 2 each completed, assistant parentID,
context marker and cwd matched, final history had 4 messages. Both controlled
prompts instructed no tools and no file changes; independent history inspection
found zero tool parts. Second prompt did not repeat the random marker and its
response matched the first exactly. Same-key replay returned the same execution;
changed payload with that key was rejected, without duplicate native messages.

Independent native history verification (IDs and output withheld):

| Evidence | Turn 1 | Turn 2 |
|---|---|---|
| Native session matches supplied test session | true | true |
| Assistant parent matches corresponding prompt | true | true |
| Cwd matches allowed workspace | true | true |
| Completion Unix milliseconds | 1792830025733 | 1792830028003 |
| Native finish | stop | stop |
| Native error present | false | false |
| Tool parts | 0 | 0 |

The session already existed but had **zero** prior messages. History grew 0→4;
this proves continuity between these two turns, not retention of a pre-existing
nonempty conversation. No replacement session/server was created. No live
provider error, timeout or permission wait was deliberately induced; those
behaviors were checked through deterministic tests, not claimed as live failures.

## Required checks and precise scope

Go 1.27.2 linux/arm64, SDK v1.8.0. Each command exited 0 before binary installation:

```sh
gofmt -w cmd internal
go build ./...
go vet ./...
go test ./...
go test -race ./...
git diff --check
```

Final full test and race runs: cmd/bridge, adapters/opencode, execution, mcp and
security all `ok (cached)`; cmd/mcpsmoke, cmd/smoke and mcpclient had no test files.
Formatting/build/vet/diff check silent. An initial build found an unused import
during implementation; it was fixed before successful checks and installation.
An earlier uncached credential-source test/race run passed cmd/bridge in
0.013s/1.038s. A separate actual systemd fixture test passed as described above.

`go test ./internal/execution -count=1 -v` exited 0 (0.071s):
TwoTurnsCorrelationAndResults, DuplicateConflictBusyConcurrent,
UnknownSubmissionNeverRetried, DisconnectAndMissingStatusRecoverFromProof,
TimeoutAndShutdownRetainGuard, CancellationBeforeAndAfterAcceptance,
PermissionErrorAndExplicitInterrupt, NativeBusyAndValidation and
MessageIDChronological all PASS. These include error, timeout, uncertain outcome
and permission/interrupt semantics without risking the live native session.

## Filesystem limits and next gate

Workspace directory ACL: owner opencode-hb rwx; named harness-bridge --x;
owning group none; mask --x; other none. README.md is opencode-hb:opencode-hb
0664 with other read. Thus the Bridge can traverse a known path and read that
file. Both direct runuser and a check with the Bridge mount namespace/service
UID returned WORKSPACE_FILE_READABLE. The failed negative test is explained;
no WORKSPACE_FILE_PROTECTED claim and no filesystem-isolation claim is made.
No ACL/file modes were changed in this work. Non-root processes can also read
other OS-permitted paths; application session ACLs do not confine native agent
filesystem access. Existing systemd hardening is not a complete read sandbox.

Next: review and implement a separately authorized filesystem/OS confinement
policy for the test agents, then validate the approved private HTTPS backend
and Traefik certificate-verification path. No public routing, DNS/firewall/Swarm
changes or deployment-repository changes were performed here. Ordinary Claude
Chat authentication availability and actual two-turn E2E remain pending.
Neither PR was merged.
