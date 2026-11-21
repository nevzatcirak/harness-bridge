# ADR-026: product name "Cirak", distribution channels and release pipeline

Date: 2026-11-21. Status: **PROPOSED**. Maintainer decisions **D25** (distribution) and **D26** (rename) were
taken on 2026-11-21 and are recorded by the Planning Desk; this ADR designs how to carry them out, and is
accepted by the Planning Desk under D19 only after the mandatory review (B). Written for planner task #461 by
Worker C. **Docs only: nothing here renames, builds or publishes anything.** Every statement about the current
code was measured at `main` `e2ac772` (2026-11-21); every statement about an upstream tool is marked
**verified (docs read 2026-11-21)** or **UNVERIFIED**.

**What the maintainer already decided (not re-opened here).**
- **D25, distribution, all four channels:** (1) GitHub Releases plus `install.sh`; (2) a Homebrew tap
  (`brew install nevzatcirak/tap/cirak`); (3) an npm wrapper (`npx cirak`, `npm i -g cirak`) that only downloads
  and verifies the platform Go binary (Node is never a backend); (4) `.deb` and `.rpm` packages shipping a
  systemd unit. The backend stays one Go binary.
- **D26, a full rename to Cirak:** repository `nevzatcirak/cirak` (the GitHub rename is a maintainer action,
  done last), Go module path, binary `cirak`, MCP server name, docs, dashboard title; environment prefix
  `CIRAK_*`; the legacy `BRIDGE_*` names stay accepted for one minor release with a single startup warning per
  variable; if both forms are set with different values the binary refuses to start; equal values are accepted.

**What this ADR does not touch.** ADR-010 to ADR-025, the MCP tool contract, the security invariants of
`AGENTS.md`, and the roles: the Coding Agent never tags, publishes or releases (§3.2 makes every publication a
maintainer step).

## 1. Evidence base

### 1.1 Measured in the repository (`main` `e2ac772`)

| Fact | Value |
|---|---|
| Module path | `github.com/nevzatcirak/harness-bridge` (`go.mod`, `go 1.25.0`; CI and the H5 build use `go1.27.2`) |
| Occurrences of the module path | 281 in 137 Go files, plus 147 occurrences in docs, workflows, scripts and JSON (all of `internal/` is non-importable by third parties: only the `cmd/*` binaries are installable) |
| `cmd/` | `bridge` (the daemon and its `admin` subcommand), `hb-claude-acceptance`, `mcpsmoke`, `privatecheck`, `smoke` |
| Environment variables read by the daemon | **31** `BRIDGE_*` names (table in §2.2) plus `OPENCODE_URL`, `OPENCODE_SERVER_USERNAME`, `OPENCODE_SERVER_PASSWORD` (and `_FILE`), and the systemd-provided `CREDENTIALS_DIRECTORY`; the dev tools read `H1_*`/`H2_*` |
| How the daemon reads them | 34 literal reads go through an injected `env func(string) string`; `cmd/bridge/main.go` also calls `os.Getenv` directly (lines 40, 51 and 59: `BRIDGE_ALLOWED_ROOTS`, `BRIDGE_ALLOWED_SESSIONS`, `BRIDGE_WRITER_LOCK_DIR`) and passes `os.Getenv` itself as the injected function in places; two exported constants carry names into error messages (`lifecycle.EnvConfig`, `dashboard.EnvRedactExtra`); the dev tool `cmd/mcpsmoke` reads `BRIDGE_TOKEN`. A **single choke point is feasible** |
| Hard-coded paths | **none** in the daemon: `/etc/harness-bridge/...` appears only as a flag default of `cmd/privatecheck` and in the confinement test fixture; every runtime path is an operator-set variable |
| Product-name strings in code | MCP server `Implementation.Name` `"harness-bridge"` (`internal/mcp/server.go:321`, `Version: "0.2.0"` hard-coded), probe clients `harness-bridge-local-client` and `harness-bridge-private-probe` (`internal/mcpclient`), `WWW-Authenticate` realm `harness-bridge` (`internal/mcp/server.go:294`, `internal/security/remote.go:120`), dashboard title `Harness Bridge dashboard` (4 strings in `internal/dashboard`) |
| Names that are contract, not branding | tool names and fields (including `created_by_bridge`), the native session metadata labels `hb_origin` / `hb_key` (`internal/mcp/create.go:403`; the value `bridge`), AUDIT-01 `event=` names and attributes, error codes |
| Version stamping | a binary built with `-buildvcs=true` carries the module version (`mod github.com/nevzatcirak/harness-bridge v0.0.0-20261121034852-e2ac772bf94c` in the H5 build, H5 evidence #454/#455), so a tagged build needs no `-X` flag |
| Licence | Apache-2.0 (`LICENSE`) |

### 1.2 Cross-compilation probe (`CGO_ENABLED=0 go build -trimpath ./cmd/bridge`, go1.27.2)

| Target | Result |
|---|---|
| linux/amd64, linux/arm64 | builds |
| darwin/amd64, darwin/arm64 | builds, **but** the Linux-only features fail closed: the dashboard unix socket (`internal/dashboard/listen_other.go`), the admin socket (`internal/admin/socket_other.go`, needs `SO_PEERCRED`), the Claude Code runner (`runner/spawn_other.go`) and every lifecycle metadata read (`internal/lifecycle/readfile_other.go`, so `create_session` is refused) |
| windows/amd64 | **does not compile**: `syscall.Flock` and `syscall.LOCK_EX` are undefined in `internal/overlay/overlay.go:182` (the writer lock) |
| freebsd/amd64 | builds (not a target) |

No test was run on darwin: macOS runtime behaviour is **UNVERIFIED** (CI has only `ubuntu-24.04`).

### 1.3 Names (public read-only requests, 2026-11-21)

`registry.npmjs.org/cirak` and `registry.npmjs.org/@nevzatcirak%2fcirak`: **404** (not taken);
`formulae.brew.sh/api/formula/cirak.json`: 404 (no `cirak` formula in homebrew-core); GitHub
`nevzatcirak/cirak` and `nevzatcirak/homebrew-tap`: **404** (do not exist; the rename target is free);
`pkg.go.dev` has no page for the new path. Whether npm's similarity check would refuse `cirak`, whether the
npm account owns the scope `nevzatcirak`, and any trademark or name clash for "Cirak" are **UNVERIFIED**.

### 1.4 Upstream facts used (docs read by a read-only fetch on 2026-11-21; the sources are public pages)

| Claim | Status |
|---|---|
| GoReleaser `builds`: `flags` accepts `-trimpath`; `env` takes `CGO_ENABLED=0`; the **default** ldflags are `-s -w -X main.version=... -X main.commit=... -X main.date=... -X main.builtBy=goreleaser`; `mod_timestamp: "{{ .CommitTimestamp }}"` is the documented reproducibility setting | verified |
| Whether a user-set `ldflags` list **replaces** that default (never mentions `-buildid=`), whether `-buildvcs=true` may be passed in `flags`, whether a dirty tree stops GoReleaser, and how `dist/` interacts with Go's `vcs.modified` stamp | **UNVERIFIED**; §3.1 makes each a gate |
| GoReleaser `checksum` default `sha256`, `{{ .ProjectName }}_{{ .Version }}_checksums.txt`; `sboms` default tool Syft (`spdx-json`); `signs` with cosign keyless (`sign-blob --bundle=${signature} ... --yes`, `artifacts: checksum`); `id-token: write` for cosign OIDC; the action is `goreleaser/goreleaser-action` (`args: release --clean`, `contents: write`) | verified |
| GoReleaser `brews` (Homebrew **formula**) is **deprecated since v2.10 in favour of `homebrew_casks`**; a cross-repo push needs a separate token with content write on the tap, the default action token cannot do it | verified |
| `nfpms`: `formats` include `deb` and `rpm`; `contents` entries with `type: config` / `config\|noreplace` (rpm: `%config(noreplace)`); `scripts:` `preinstall`/`postinstall`/`preremove`/`postremove` as file paths | verified |
| How a systemd unit and a system user are shipped through nfpm | **UNVERIFIED** in the pages read (a unit is an ordinary `contents` file and a user is created by the `preinstall` script: design in §4.4, proven in S4) |
| `release.draft` ("all GitHub releases start as drafts while artifacts are uploaded"); `changelog` `use: git` with `groups` (RE2 `regexp` on the first line) and `filters` | verified (the documented bug example regexp does not match `fix:`: our groups name `fix` explicitly) |
| GitHub artifact attestations: `actions/attest` (v4; `actions/attest-build-provenance` is now "simply a wrapper"), permissions `id-token: write`, `contents: read`, `attestations: write`; consumer check `gh attestation verify <file> -R <owner>/<repo>`; available for public repositories on all plans | verified |
| cosign keyless: `cosign sign-blob <file> --bundle <b>.sigstore.json --yes` needs `id-token: write`; `cosign verify-blob <file> --bundle ... --certificate-identity=... --certificate-oidc-issuer=https://token.actions.githubusercontent.com`; `sigstore/cosign-installer` | verified |
| npm `publish --provenance`: npm >= 9.5.0, a GitHub-hosted runner, `permissions: id-token: write`, a public repository, `--access public` on first publish. **Trusted publishing (OIDC)** removes the long-lived token (npm CLI >= 11.5.1, Node >= 22.14; the trusted publisher is configured per package on npmjs.com; provenance is then automatic) | verified; whether a package that does not exist yet can have a trusted publisher configured is **UNVERIFIED** |
| esbuild pattern: the main package "depends on a separate optional package for each platform-specific binary"; with `--no-optional` the install script downloads the binary; with `--ignore-scripts --no-optional` "cannot work at all" | verified (the `os`/`cpu` package.json fields are standard npm but were not on the page read: UNVERIFIED by fetch) |
| Homebrew third-party tap: the repository should be named `homebrew-<name>` (a recommendation), `brew install user/repo/formula`; non-official taps need explicit trust | verified |

## 2. The rename

### 2.1 Every surface, and what happens to it

| Surface | Now | After | Notes |
|---|---|---|---|
| GitHub repository | `nevzatcirak/harness-bridge` | `nevzatcirak/cirak` | maintainer action, **last** (§2.4) |
| Go module path | `github.com/nevzatcirak/harness-bridge` | `github.com/nevzatcirak/cirak` | mechanical; 137 Go files |
| Daemon package | `cmd/bridge` | `cmd/cirak` | the structural tests of `main.go` move with it |
| Binary | `harness-bridge-<tag>` (host-installed names) | `cirak` | the live host keeps its installed binary until it adopts a release |
| Dev and acceptance tools | `cmd/smoke`, `mcpsmoke`, `privatecheck`, `hb-claude-acceptance` | **names kept** (only their imports change) | not product surfaces; the runbooks name them |
| Host artifacts | `hb-claude-wrapper`, user `hbclaude`, `/opt/harness-bridge-test` | **kept** | operator-owned; renaming is host work with no benefit |
| Environment prefix | `BRIDGE_*` (31 names) | `CIRAK_*` | table in §2.2; `OPENCODE_*` and `CREDENTIALS_DIRECTORY` are **not** renamed (upstream and systemd names) |
| MCP server name | `harness-bridge` | `cirak` | also `Version`, today the literal `"0.2.0"`: derive it from the build info (§5) |
| Probe client names | `harness-bridge-local-client`, `...-private-probe` | `cirak-local-client`, `cirak-private-probe` | internal tools |
| `WWW-Authenticate` realm | `harness-bridge` | `cirak` | check with a live connector at the first adoption (open question 4) |
| Dashboard title | `Harness Bridge dashboard` (4 strings) | `Cirak dashboard` | tests that assert the title change with it |
| Docs | "Harness Bridge" and `harness-bridge` throughout | "Cirak" / `cirak` | historical evidence files and accepted ADR texts keep their wording (dated records); live runbooks and the README are updated. `HARNESS_BRIDGE_SPEC.md` keeps its file name in S1 (`AGENTS.md` names it as the source of truth): renaming it is open question 11 |
| systemd units | host unit `harness-bridge-private.service` | the **package** unit is `cirak.service`; the host unit is untouched | no collision |
| Paths | `/etc/harness-bridge`, `/var/lib/harness-bridge`, `/run/harness-bridge-*` | the **packages** use `/etc/cirak`, `/var/lib/cirak`, `/run/cirak*` | **decision: the binary reads no fixed path (measured), so there is nothing to keep reading**: a host that keeps its old paths needs no change; no dual-path logic is added |

**What stays unchanged, on purpose** (a journal grep, a client or a stored record keeps working):
tool names and their input/output fields (including `created_by_bridge`); the native metadata labels `hb_origin`
(value `bridge`) and `hb_key`, which are persisted in existing native sessions and read back as provenance;
AUDIT-01 `event=` names and attribute names; error codes and reasons; the text of existing log lines apart from
the product name; the agent/planner protocol markers `<!-- harness-bridge-agent -->` and
`<!-- harness-bridge-planner -->` (they are the trust protocol of `AGENTS.md`, changed only by a separate
maintainer decision); ADR and decision identifiers; the git history.

### 2.2 The environment table (generated from the code)

The new name is `CIRAK_` plus the suffix; the `_FILE` form follows (`CIRAK_TOKEN_FILE`). The third column is a
file that reads the name (the first one, there may be more).

| Legacy | New | Read in |
|---|---|---|
| `BRIDGE_ADMIN_SOCKET` | `CIRAK_ADMIN_SOCKET` | `cmd/bridge/admin.go` |
| `BRIDGE_ALLOWED_ROOTS` | `CIRAK_ALLOWED_ROOTS` | `cmd/bridge/claude.go`, `main.go` |
| `BRIDGE_ALLOWED_SESSIONS` | `CIRAK_ALLOWED_SESSIONS` | `cmd/bridge/claude.go`, `main.go` |
| `BRIDGE_CLAUDE_COMMAND` | `CIRAK_CLAUDE_COMMAND` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_HISTORY_DIR` | `CIRAK_CLAUDE_HISTORY_DIR` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_HOME` | `CIRAK_CLAUDE_HOME` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_LANG` | `CIRAK_CLAUDE_LANG` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_MAX_BUDGET_USD` | `CIRAK_CLAUDE_MAX_BUDGET_USD` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_MAX_TURNS` | `CIRAK_CLAUDE_MAX_TURNS` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_MODEL` | `CIRAK_CLAUDE_MODEL` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_RUN_MAX` | `CIRAK_CLAUDE_RUN_MAX` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_SESSIONS` | `CIRAK_CLAUDE_SESSIONS` | `cmd/bridge/claude.go` |
| `BRIDGE_CLAUDE_VERSIONS` | `CIRAK_CLAUDE_VERSIONS` | `cmd/bridge/claude.go` |
| `BRIDGE_DASHBOARD_ADDR` | `CIRAK_DASHBOARD_ADDR` | `cmd/bridge/dashboard.go` |
| `BRIDGE_DASHBOARD_REDACT_EXTRA` | `CIRAK_DASHBOARD_REDACT_EXTRA` | `internal/dashboard/redact.go` |
| `BRIDGE_DASHBOARD_SOCKET` | `CIRAK_DASHBOARD_SOCKET` | `cmd/bridge/dashboard.go` |
| `BRIDGE_LIFECYCLE_CONFIG` | `CIRAK_LIFECYCLE_CONFIG` | `cmd/bridge/lifecycle.go` |
| `BRIDGE_LISTEN` | `CIRAK_LISTEN` | `cmd/bridge/ingress.go` |
| `BRIDGE_OVERLAY_DIR` | `CIRAK_OVERLAY_DIR` | `cmd/bridge/lifecycle.go` |
| `BRIDGE_PERMISSION_INCARNATION` | `CIRAK_PERMISSION_INCARNATION` | `cmd/bridge/main.go` |
| `BRIDGE_PERMISSION_RELAY` | `CIRAK_PERMISSION_RELAY` | `cmd/bridge/main.go` |
| `BRIDGE_PRINCIPAL` | `CIRAK_PRINCIPAL` | `cmd/bridge/ingress.go` |
| `BRIDGE_PUBLIC_ORIGIN` | `CIRAK_PUBLIC_ORIGIN` | `cmd/bridge/ingress.go` |
| `BRIDGE_READ_ONLY` | `CIRAK_READ_ONLY` | `cmd/bridge/ingress.go` |
| `BRIDGE_TLS_CERT` | `CIRAK_TLS_CERT` | `cmd/bridge/ingress.go` |
| `BRIDGE_TLS_KEY` | `CIRAK_TLS_KEY` | `cmd/bridge/ingress.go` |
| `BRIDGE_TOKEN` | `CIRAK_TOKEN` | `cmd/bridge/startup.go`, `main.go` |
| `BRIDGE_TOKEN_EXPIRES` | `CIRAK_TOKEN_EXPIRES` | `cmd/bridge/ingress.go` |
| `BRIDGE_TOKEN_FILE` | `CIRAK_TOKEN_FILE` | `cmd/bridge/startup.go` (via `readSecret`) |
| `BRIDGE_TOKEN_MAX_LIFETIME` | `CIRAK_TOKEN_MAX_LIFETIME` | `cmd/bridge/ingress.go` |
| `BRIDGE_WRITER_LOCK_DIR` | `CIRAK_WRITER_LOCK_DIR` | `cmd/bridge/claude.go`, `main.go` |

Names that appear in docs or test scripts but are not read by the daemon (`BRIDGE_CLAUDE_HISTORY_MAX_AGE`, not
wired; `BRIDGE_PERMISSION_POLICY`, refused at startup; `BRIDGE_BINARY`, `BRIDGE_CA_FILE`, `BRIDGE_UNIT`,
`BRIDGE_SPEC`, test and operator scripts) are renamed in the documents and scripts only; the daemon does not
learn them.

### 2.3 The compatibility rule (the one place where the rename can weaken a security setting)

A single function, `cirakEnv(os.Getenv)`, is the only reader of the process environment; every existing
`env func(string) string` receives it, and the four direct `os.Getenv` calls go through it. For a canonical name
`CIRAK_X` with legacy twin `BRIDGE_X` (and the `_FILE` forms, which follow):

| `CIRAK_X` | `BRIDGE_X` | Result |
|---|---|---|
| unset or empty | unset or empty | empty (unset), no message |
| set | unset or empty | the new value, no message |
| unset or empty | set | the legacy value, **one warning for this variable** |
| set | set, **equal** | the value, one warning (the legacy twin is redundant) |
| set | set, **different** | **the binary refuses to start**, naming the two variable names, never a value, before any listener, lock or native call |

- **Empty counts as unset on both sides.** This keeps the idiom the H5 runbook relies on (an empty
  `Environment=BRIDGE_TOKEN=` neutralises an inherited value, #450) working: it neutralises the legacy twin.
  The converse is the rule's weak spot: an empty `CIRAK_X` does **not** neutralise a non-empty `BRIDGE_X`
  (open question 3).
- **The scan covers every known name up front**, not only the names a given configuration reads, so a conflict
  in the dashboard variables is found even when the dashboard is off.
- **The existing exclusions run on the merged view**: `CIRAK_TOKEN_FILE` together with `BRIDGE_TOKEN` is still the
  "mutually exclusive" refusal of `readSecret`; the empty-as-unset rule (`cmd/bridge/secret.go:15-23`) is unchanged.
- **The warning** is one line per variable and per process start, naming the legacy and the new name and the
  release in which the legacy name stops working, never a value:
  `legacy environment variable BRIDGE_X is deprecated; set CIRAK_X instead (support ends with v0.2.0)`.
- **The window, and why it ends closed, not open.** v0.1.x (the first public release, which already contains
  the rename) accepts both with the warning, which is the "one minor release" of D26. **Proposed addition
  (open question 1):** v0.2.x **refuses to start** if any known legacy name is set (`BRIDGE_X is no longer
  supported; use CIRAK_X`), and v0.3.0 ignores them. Silently ignoring a legacy name at the end of the window
  would turn `BRIDGE_READ_ONLY=1`, `BRIDGE_ALLOWED_ROOTS` or `BRIDGE_TOKEN_MAX_LIFETIME` into no-ops on an
  upgraded host, so the daemon would start as a writer, with no ACL root, or with a longer lifetime than the
  operator wrote. Refusing for one more minor makes the mistake a startup error.

### 2.4 Order of operations

1. **S1, the code rename PR** (module path, `cmd/cirak`, names of §2.1, the environment shim of §2.3, docs). It
   lands while the repository is still called `harness-bridge`: CI and local builds do not depend on the
   hosting name. Until the GitHub rename, `go install github.com/nevzatcirak/cirak/...` cannot work (the
   repository does not exist yet: measured, 404); nobody installs this way (the project is a binary; `internal/`
   is not importable).
2. **S2, release tooling** (§3), still on the old repository name, with the workflow guarded so that it cannot
   publish before the rename and the secrets exist.
3. **The maintainer renames the repository** (Settings, rename). GitHub redirects the old web and git URLs and
   keeps issues, pull requests, branch protection and the required check name `Go checks` (standard GitHub
   behaviour: **UNVERIFIED here**, no rename was attempted; the maintainer verifies after the rename). Then the
   instruction texts that name `github.com/nevzatcirak/harness-bridge` (the planner and worker prompts, this
   repository's docs) are updated.
4. **The first tag `v0.1.0`**, by the maintainer, on the renamed repository, after the checklist of §5.2.
5. **S3 (tap, npm) and S4 (packages)** publish only after the maintainer publishes the release (§3.2).

**`go get` and the old module path.** After the rename the old path resolves to the new repository, whose
`go.mod` declares the new path, so the go command reports a path mismatch for `harness-bridge` import paths.
That is acceptable because there are no importers: the code lives under `internal/` and the only installable
artifacts are the `cmd/*` binaries (this is the reason the rename can be done in one step; no `replace`, no
dual module path). The Go module proxy caches by path and may lag a rename: **UNVERIFIED**.

### 2.5 Deprecation notice and the migration note for the live H5 host

- The startup warning of §2.3; a README banner and the release notes of v0.1.0 ("Harness Bridge is now Cirak");
  `docs/deploy/*` show both names for the window.
- **The live H5 host must keep working without edits.** It runs a build of a `main` commit with `BRIDGE_*`
  variables in `zzzz-h5.conf` and an `EnvironmentFile`. Nothing changes until the operator chooses to adopt a
  release: (1) install the release binary next to the old one (`/usr/local/libexec/cirak-<version>`); (2) point
  `ExecStart` at it and leave every `BRIDGE_*` line as it is; the journal then shows one warning per variable and
  nothing else changes; (3) migrate at leisure, variable by variable: set `CIRAK_X` to the same value and remove
  `BRIDGE_X` (different values refuse the start, so a half-edited drop-in is caught); (4) the `harness-bridge-
  private.service` unit name, the `/etc/harness-bridge` and `/var/lib/harness-bridge` paths stay valid for ever
  (the binary has no fixed path); (5) the rollback is the previous `ExecStart`. The one connector-visible change
  is the MCP server name and realm (open question 4): the operator checks the connector once at the first
  adoption.

## 3. The release pipeline

### 3.1 Build: GoReleaser in GitHub Actions on a `v*` tag

- **Trigger and permissions.** `on: push: tags: ['v*']`; default permissions `contents: read`; the release job
  adds `contents: write`, `id-token: write` and `attestations: write`. All actions are pinned to commit SHAs with
  a comment, as `.github/workflows/ci.yml` does; the toolchain is `GOTOOLCHAIN: local` with the exact `go1.27.2`
  (recorded in the release notes: the same source with another toolchain gives another binary).
- **Targets.** `linux/amd64`, `linux/arm64` (both verified to build), `darwin/amd64`, `darwin/arm64` (build; Linux
  features fail closed, §1.2; runtime **UNVERIFIED**). **Windows: not in v0.1.0**: it does not compile
  (`syscall.Flock` in `internal/overlay`) and the safety rules (single-writer lock, owner and mode checks,
  `SO_PEERCRED`, `os.OpenRoot` confinement) have no verified Windows equivalent, so a port needs its own ADR
  (open question 5).
- **The recipe is the H4/H5 one**: `CGO_ENABLED=0`, `flags: [-trimpath, -buildvcs=true]`, `ldflags:
  [-buildid=]`, archive timestamps `mod_timestamp: "{{ .CommitTimestamp }}"`. Because GoReleaser's default
  ldflags (`-s -w -X main.version=...`) change the bytes and its docs do not say whether a user list replaces
  them, **the first gate of S2 is an equality test**: the `linux/arm64` binary inside the GoReleaser archive must
  be byte-identical to `go build -trimpath -buildvcs=true -ldflags=-buildid=` of the same commit with the same
  toolchain; the gate fails the release otherwise.
- **A clean tree, as in H5 (finding F-H5-5).** `vcs.modified=true` appeared whenever `out/` or the Go cache were
  untracked inside the clone. GoReleaser writes `./dist` into the clone; the repository adds `dist/` to
  `.gitignore` and the workflow runs a step, **for every artifact**, `go version -m <binary> | grep
  vcs.modified=false` (a failure stops the release). That the dirty-tree check and `dist/` behave this way in
  GoReleaser is **UNVERIFIED** and is exactly what the gate measures.
- **Checksums, SBOM, signatures, provenance** (next section).
- **A snapshot build on every pull request** (`goreleaser build --snapshot --single-target`, no publish) so that
  a broken release configuration is caught before a tag.

### 3.2 Publication is a maintainer gate, in two steps

AGENTS.md: the Coding Agent never tags, publishes or releases. The pipeline keeps that:

1. The maintainer pushes `v*`. The workflow builds, checks and creates a **draft** GitHub release
   (`release.draft: true`; verified documented option). Nothing is announced.
2. A second job rebuilds **independently** (the plain recipe, on a different runner image, no GoReleaser) and
   compares the sha256 of every binary with the archives (the H5 reproducibility check, automated); a mismatch
   leaves the release a draft and fails the workflow.
3. The maintainer reviews the draft (checksums, the verification block, the reproducibility result) and
   **publishes it by hand**. The channel jobs (tap, npm, packages) trigger on `release: published`, run in a
   protected GitHub environment `release` whose required reviewer is the maintainer, and hold the secrets of §6.
   A repository ruleset restricts who may push `v*` tags.

### 3.3 Signing and provenance: comparison and recommendation

| | cosign keyless (`sign-blob --bundle`) | GitHub artifact attestations (`actions/attest`) |
|---|---|---|
| What it proves | a Sigstore certificate bound to the workflow identity signed these bytes | a provenance statement: repository, workflow, commit and the artifact digests, signed |
| Verifier needs | `cosign` (a single static binary) | the GitHub CLI `gh` (`gh attestation verify <file> -R nevzatcirak/cirak`) |
| Works offline / in `install.sh` | yes, with the bundle next to the checksums | needs `gh` and network access to GitHub |
| Cost, secrets | none beyond `id-token: write` | none beyond `attestations: write`; public repositories on all plans (verified) |
| Weak point | an operator must pin the expected identity (workflow path and ref) | tied to GitHub as the verifier |

**Recommendation: both, because they answer different questions and cost no secret.** cosign signs the
`checksums.txt` (one bundle; `install.sh` and the npm and tap jobs verify the archives through it); the
attestation records provenance for every archive and the checksum file (consumers with `gh` verify provenance).
Neither is a substitute for the reproducibility job of §3.2, which is the check that does not trust the
builder. The release notes carry a **Verification** block with the three commands (sha256, `cosign
verify-blob` with the certificate identity of the release workflow, `gh attestation verify`). An SBOM per archive
is produced by GoReleaser's default Syft `sboms` (`spdx-json`).

## 4. The four channels

### 4.1 `install.sh`

- **Where it lives.** A **release asset** (`install.sh`, listed in `checksums.txt`, covered by the attestation)
  and a copy in the repository; the documented command is
  `curl -fsSL https://github.com/nevzatcirak/cirak/releases/latest/download/install.sh | sh`, so the script is
  the one that was released, not whatever `main` holds. The page that offers `curl | sh` also shows the
  download-inspect-run form.
- **Behaviour.** POSIX `sh`, `set -eu`, no `eval`, no bashisms. Detects `uname -s`/`uname -m` (`linux`/`darwin`,
  `x86_64`/`aarch64`/`arm64`) and **refuses anything else** with a message that names the supported platforms.
  Downloads only from `https://github.com/nevzatcirak/cirak/releases/...` (HTTPS only: `curl --proto '=https'
  --tlsv1.2`), the archive, `checksums.txt` and, when present, its cosign bundle. **Verifies sha256 always**
  (`sha256sum` or `shasum -a 256`; none available is an error); **verifies the cosign bundle when `cosign` is on
  the PATH** and treats a failure as an error (`CIRAK_VERIFY=require` makes a missing `cosign` an error too). Nothing
  is executed before verification.
- **Where it installs.** `$HOME/.local/bin` by default (no `sudo`); `--system` installs to `/usr/local/bin` and
  runs `sudo` only for that copy and only when the user asked. It never pipes to a root shell by itself.
- **Idempotent.** The installed version is read with `cirak version`; the same version is a no-op (exit 0), another
  version replaces the binary through a temporary file and an atomic `mv`; `--version vX.Y.Z` pins, `--dry-run`
  prints the plan. It never edits shell profiles, units, drop-ins or configuration, and does not start a service.
- **Offline and air-gapped.** `--from-dir <dir>` installs from a directory holding the archive, `checksums.txt`
  and optionally the bundle (verified the same way); `CIRAK_DOWNLOAD_BASE` points at an internal mirror of the
  release layout. The script makes no request in that mode.

### 4.2 Homebrew tap

- Repository `nevzatcirak/homebrew-tap` (public; the `homebrew-` prefix is the recommended form, verified), so
  `brew install nevzatcirak/tap/cirak` works after an automatic tap.
- **Formula, not cask.** The binary is a command-line tool for Linux and macOS: a Homebrew **formula** serves
  both (Linuxbrew included), while a cask is a macOS artifact with signing and quarantine expectations. GoReleaser's
  `brews:` (formula) is **deprecated since v2.10** in favour of `homebrew_casks` (verified): the options are to use
  `brews:` while it works, or to generate the formula from a template in our own workflow (a few lines: the url,
  the sha256 from `checksums.txt`, `bin.install "cirak"`, `test do assert_match version.to_s, shell_output(
  "#{bin}/cirak version")`). **Recommendation: our own small template step**, because it removes the dependency on
  a deprecated option and the formula is easy to test; open question 7.
- **Token.** The default `GITHUB_TOKEN` cannot push to another repository (verified); the job needs a
  fine-grained token limited to the tap repository (contents: read and write), stored as `HOMEBREW_TAP_TOKEN`
  in the `release` environment (§6).

### 4.3 npm wrapper

- **Rule from D25.** The package only locates, downloads or contains, and verifies a platform Go binary and then
  `exec`s it. Node is a launcher, never a backend: `child_process.spawn` with `stdio: 'inherit'`, the exit code
  and the SIGINT/SIGTERM forwarded unchanged (the daemon handles them: `cmd/bridge/main.go` shutdown).
- **Layouts compared.**
  - **A. Per-platform packages in `optionalDependencies`** (the esbuild pattern, verified): `cirak` depends on
    `cirak-linux-x64`, `cirak-linux-arm64`, `cirak-darwin-x64`, `cirak-darwin-arm64`, each with `os`/`cpu`
    fields and the binary inside. No install script, no network at install time beyond npm itself, works with
    `--ignore-scripts` and with a mirrored registry; npm's integrity hashes and the package provenance cover the
    bytes; the release job checks that the binary inside each platform package has the sha256 of the release
    archive. Drawbacks: five packages to publish and keep in lock step; `--no-optional` leaves no binary.
  - **B. One package with a postinstall download** pinned by an embedded sha256 per platform: one package, but an
    install script (blocked by `--ignore-scripts`, which esbuild documents as a fatal combination with
    `--no-optional`), a runtime download from GitHub, and a hash table that must be regenerated per release.
  - **Recommendation: A**, with a launcher that, when no platform package is present, prints the exact
    `install.sh` command and exits non-zero (it never downloads silently).
- **Names.** `cirak` was **not taken** on 2026-11-21 (404); npm's similarity check and the account's ownership of
  the scope are **UNVERIFIED**. Fallback: the scoped `@nevzatcirak/cirak` (a scope matching the user name, per the
  npm scope page). Platform packages are scoped under the user scope (`@nevzatcirak/cirak-linux-x64`, ...) to avoid
  creating an npm organisation (open question 8).
- **Provenance and the token.** `npm publish --provenance --access public` from the protected job (npm >= 9.5.0,
  `id-token: write`, a public repository, a matching `repository` field: verified). **Trusted publishing** (OIDC,
  npm CLI >= 11.5.1, Node >= 22.14) would remove `NPM_TOKEN` entirely, but it is configured per package on
  npmjs.com and whether it can be set before the first publish is **UNVERIFIED**: the plan is a granular
  publish-only `NPM_TOKEN` for the first publish, then trusted publishing and deletion of the token.

### 4.4 `.deb` and `.rpm` (nfpm through GoReleaser)

- **Contents.** `/usr/bin/cirak`; the unit `/usr/lib/systemd/system/cirak.service`; the example environment file
  `/etc/cirak/cirak.env.example` as `config|noreplace`; `/usr/share/doc/cirak/` (the `LICENSE`, README and the
  deploy runbooks). A system user and group `cirak` (no login, no home), created idempotently by the `preinstall`
  script.
- **The unit (proposal).** A sketch to be proven in S4, not a copy of the live host's unit (which this repository
  does not contain):

  ```ini
  [Unit]
  Description=Cirak (MCP bridge for coding-agent sessions)
  After=network-online.target

  [Service]
  User=cirak
  Group=cirak
  EnvironmentFile=-/etc/cirak/cirak.env
  LoadCredential=token:/etc/cirak/token
  Environment=CIRAK_TOKEN_FILE=%d/token
  RuntimeDirectory=cirak-admin cirak-dashboard
  RuntimeDirectoryMode=0700
  StateDirectory=cirak cirak/overlay cirak/writer-locks
  StateDirectoryMode=0700
  ExecStart=/usr/bin/cirak
  ProtectSystem=strict
  ProtectHome=true
  PrivateTmp=true
  NoNewPrivileges=true
  Restart=on-failure
  ```

  Parts **proven live** in the H5 sitting (and recorded in `H5_EVIDENCE_2026-11-21.md`): a token delivered through
  `LoadCredential` (`mcp-token`), `RuntimeDirectory=` for the admin and dashboard sockets, `StateDirectory=` with
  mode `0700` for the overlay (finding F-H5-3: a hand-made directory failed under `ProtectSystem`). The other
  hardening keys and the `%d` expansion in `Environment=` (documented in `docs/deploy/CREDENTIAL_FILES.md`) are
  **proposed**: the value of `ProtectSystem` on the live host is not recorded in the repository, so S4 proves the
  whole unit on a disposable container or VM, never on a live host.
- **No automatic restart, no automatic start.** The package neither enables nor starts the service (it cannot run
  without a token and an ACL), and **an upgrade never restarts it**: a restart drops the idempotency records and
  needs the ADR-010 fence (an idle session, no pending permission). `postinstall` runs `systemctl daemon-reload`
  and prints the next steps and "restart when idle"; the unit change takes effect at the operator's restart.
- **Remove and purge.** `remove` stops the service (the binary is going away) and keeps configuration, state
  (`overlay.json`, the writer locks) and the user; `purge` removes only `/etc/cirak`. State directories are never
  deleted by a package (no permanent delete, as everywhere in the product). Upgrade replaces the binary and the
  unit file and leaves `config|noreplace` files alone.
- **Compatibility.** No `Provides`/`Conflicts` with anything: the live host uses other unit and path names (§2.1).
  The admin command is `sudo -u cirak cirak admin release-guard ...`.

## 5. Versioning, changelog, support

### 5.1 Versioning
Semantic versioning with `0.y.z` semantics: **the contract is the API**: tool names and schemas, environment
names, audit event and attribute names, and the config file formats; a breaking change of any of them is a minor
bump before 1.0 and is listed in the changelog. The first public tag is **`v0.1.0`**. The version shown by
`cirak version`, the MCP `Implementation.Version` and the SBOM all come from the module version stamped by
`-buildvcs=true` (already visible in the H5 binary), not from a literal (S1 replaces the hard-coded `"0.2.0"`).

### 5.2 Changelog and release notes
GoReleaser `changelog` with `use: git`, groups by conventional-commit type (`feat`, `fix`, `docs`, `chore`,
`perf`, with an explicit `fix` group because the documented example regexp does not match it), `filters` removing
`^test:` and merge commits. Each release carries: the toolchain version, the verification block of §3.3, the
deprecation notice of §2.5, and the **tested-with** line (OpenCode 1.19.4, the version pinned by ADR-017; Claude
Code 2.1.325 where R6 applies).

**Tag gate for v0.1.0 (the maintainer's checklist, `AGENTS.md`: "a feature is not released until its required real
acceptance evidence exists").** S1 to S4 merged; the H5 evidence present (2026-11-21); the Claude Code adapter
stays **off by default and documented experimental** until the R6 acceptance passes (run 5 is pending); the
reproducibility job green on a rehearsal tag in a fork or a pre-release; the secrets and repositories of §6
created; the repository renamed.

### 5.3 Support statement
A single-maintainer project: **no SLA**. Supported: the latest minor of the latest release, on linux/amd64 and
linux/arm64 with the tested-with OpenCode version; darwin builds are **experimental, core features only**
(§1.2); Windows is not supported. Security reports follow the repository's `SECURITY` instructions if any (none
exists today: open question 15). No telemetry is added.

## 6. What the maintainer must create (nothing is published before)

| Item | Purpose | Scope and notes |
|---|---|---|
| Repository rename `nevzatcirak/harness-bridge` to `nevzatcirak/cirak` | D26 | last step of §2.4, before the first tag |
| Repository `nevzatcirak/homebrew-tap` | the tap | public, with a README; the formula is committed by the release job |
| Secret `HOMEBREW_TAP_TOKEN` | tap push | fine-grained token, **only** the tap repository, contents read/write; stored in the environment `release` |
| Secret `NPM_TOKEN` | first npm publish | granular, publish-only, limited to the new packages; delete after trusted publishing works; stored in `release` |
| npm account / scope `nevzatcirak` | scoped names | the account must own the scope (UNVERIFIED) |
| GitHub environment `release` | the human gate | required reviewer = the maintainer; the channel jobs run in it |
| Ruleset for `v*` tags | who may release | only the maintainer pushes `v*` |
| Repository setting | Actions permissions | the release workflow requests `id-token`, `attestations` and `contents: write` itself; no organisation-wide change |

cosign keyless and the attestations need **no secret** (the OIDC token of the run). No package-signing key (GPG) is
planned for `.deb`/`.rpm` in v0.1.0 (the checksum, cosign bundle and attestation cover the files; a repository
signing key is a later ADR if an apt/yum repository is ever hosted: open question 14).

## 7. Slices (one PR each; order S1, S2, then the maintainer's rename, then S3 and S4)

| Slice | Scope | Depends on |
|---|---|---|
| **S1** | module path, `cmd/cirak`, names of §2.1, the `cirakEnv` shim with the rule of §2.3, `cirak version`, build-info version for the MCP name, docs and README | this ADR accepted |
| **S2** | GoReleaser config, the release and snapshot workflows, the byte-equality and `vcs.modified` gates, checksums, SBOM, cosign and attestation, the independent reproducibility job, `install.sh` and its tests, `.gitignore` `dist/` | S1 |
| *(maintainer)* | repository rename, secrets, environment, tag ruleset | S2 merged |
| **S3** | tap formula job, npm wrapper (layout A) and its release job | the rename, a published release |
| **S4** | nfpm packages, the unit, the scripts, container install tests | S2 (can run before S3) |

## 8. Test plan (RED first; no live service, no host, no publication)

**S1.**
- The environment table of §2.3 as a table test: neither / new only / legacy only (one warning) / equal (one
  warning) / different (refusal naming both names and no value) / empty on either side, for **all 31 names** and
  for the `_FILE` forms; the warning appears once per variable and never contains a value; the conflict scan runs
  before any listener is opened (the existing `startup_order` tests extended); `CIRAK_TOKEN_FILE` with
  `BRIDGE_TOKEN` is still refused; the H5 idiom (`BRIDGE_TOKEN=` empty) still neutralises.
- A contract test: the tool list and every input/output schema are **byte-identical** before and after the
  rename (the rename must not touch the contract); AUDIT-01 lines identical except the product name.
- The messages that name a variable (the two exported constants and every `... must be ...` error) show the new name, and the tests that assert them are updated with the rename.
- A grep test: no remaining `nevzatcirak/harness-bridge` in Go files and workflows; `go build ./...`, `go vet`,
  the full `go test ./...` and `-race` pass; the structural pins of `cmd/bridge/main.go` (lifecycle and shutdown)
  are renamed with the package and still reject their mutants.
- Mutations: a missing name in the table; the conflict compared case-insensitively or after trimming; the legacy
  value winning silently; the warning printing the value; the empty value treated as set; the scan skipped when the
  feature is off; a legacy name accepted after the end of the window.

**S2.**
- `goreleaser check` and a snapshot build on every pull request; the byte-equality gate (GoReleaser binary against
  the plain recipe); `go version -m` shows `vcs.modified=false` and the expected revision for every artifact
  (a mutation with an untracked file inside the tree must fail the gate); the checksum file covers every archive;
  the independent rebuild job fails on a one-byte difference.
- `install.sh`: `shellcheck`; the tests use `--from-dir` with a local fixture layout (no network), and one test
  proves that a `CIRAK_DOWNLOAD_BASE` that is not `https://` is refused; they cover: a bad checksum is refused, a tampered archive is refused, an unsupported OS or
  architecture is refused, a missing `sha256sum` and `shasum` is an error, an idempotent second run is a no-op,
  `--dry-run` writes nothing, `--system` is the only path that uses `sudo`, the script never writes outside the
  target and a temporary directory.

**S3.** The formula installs and `cirak version` runs on a macOS and a Linux runner (UNVERIFIED until the
runners exist); the npm layout: `npm pack` of every package, an install into a temporary prefix from the packed
tarballs, the launcher forwards the exit code and signals, a missing platform package prints the `install.sh`
command, the sha256 of each packaged binary equals the release checksum.

**S4.** `nfpm` builds both formats; in a Debian and a Fedora container: install creates the user and the files
with the expected modes, `systemd-analyze verify` accepts the unit, an upgrade does not restart a service and
keeps `config|noreplace` files, remove keeps state and the user, purge removes only `/etc/cirak`.

## 9. Risks

| Risk | Mitigation |
|---|---|
| The rename changes a security setting silently (a legacy name ignored) | the conflict rule, the refusal window of §2.3, tests on all 31 names |
| A non-reproducible or "dirty" release binary | the byte-equality and `vcs.modified` gates, the independent rebuild, the draft release |
| `curl \| sh` abuse | release-pinned script, checksum and signature verification, user-level install by default, inspect-first form |
| Supply chain of the channels (npm, tap) | no install scripts (layout A), provenance, tokens scoped to one repository or package, a protected environment, the maintainer's publish click |
| Users on macOS expect lifecycle features | the support statement; the Linux-only features fail closed and say so |
| An upgrade restarts a service mid-execution | the packages never restart; the ADR-010 fence stays an operator step |
| The repository rename breaks tooling | S1 and S2 do not depend on the hosting name; GitHub redirects; the instruction texts are updated after the rename |

## 10. Open questions for the maintainer

1. **End of the legacy window.** v0.1.x accepts `BRIDGE_*` with a warning (D26). Do you want v0.2.x to **refuse**
   them (recommended: ignoring them would silently drop `BRIDGE_READ_ONLY`, the ACL roots or the token lifetime)
   and v0.3.0 to ignore them?
2. **Both forms set and equal:** warn (recommended, the legacy twin is dead weight) or stay silent?
3. **Empty value across the two families:** keep "empty is unset on both sides" (an empty `CIRAK_X` then does not
   neutralise a non-empty `BRIDGE_X`), or make a set-but-empty `CIRAK_X` win?
4. **MCP server name and realm** (`harness-bridge` to `cirak`): how the claude.ai connector uses `serverInfo.name`
   and the realm is **UNVERIFIED**; accept a one-time connector check at the first live adoption?
5. **Windows:** confirm "not in v0.1.0" (it does not compile and the safety rules have no Windows equivalent).
6. **darwin:** ship `darwin/amd64` and `arm64` as experimental (core features only, runtime unverified), add a
   macOS CI job, or leave darwin out of v0.1.0? (D25's Homebrew channel mostly serves macOS.)
7. **Homebrew:** our own formula template (recommended) or GoReleaser `brews:` (deprecated since v2.10)?
8. **npm layout and names:** layout A (recommended) or B; the unscoped `cirak` (free today) or `@nevzatcirak/cirak`;
   platform packages under the user scope (recommended) or a new `@cirak` organisation?
9. **`install.sh` hosting and default:** release asset (recommended) and `~/.local/bin` default?
10. **Signing:** both cosign and attestations (recommended) or only one?
11. **Names of governance files:** keep `HARNESS_BRIDGE_SPEC.md`, `AGENTS.md` wording and the agent/planner protocol
    markers `harness-bridge-agent` / `harness-bridge-planner` for now (recommended), and decide their rename
    separately?
12. **Dev and host names** (`hb-claude-acceptance`, `hb-claude-wrapper`, user `hbclaude`, `/opt/harness-bridge-test`):
    keep (recommended)?
13. **Paths:** the daemon has no fixed path (measured), so packages use `/etc/cirak` etc. and no dual-path reading is
    added: confirm.
14. **Package repository and signing key:** none in v0.1.0 (files only on GitHub Releases); an apt/yum repository
    needs its own ADR.
15. **Name and legal:** no trademark or name-clash search for "Cirak" was made; a `SECURITY.md`/reporting address
    does not exist yet.
16. **The first-tag gate:** is "adapter off by default and marked experimental until R6 passes" acceptable for
    v0.1.0, or must R6 pass first?
17. **Support statement** of §5.3 (single maintainer, no SLA, latest minor only).

## 11. Limitations

Docs only. No rename, build, signing or publication was attempted. The cross-compilation probe and the name
checks are measurements of 2026-11-21; the upstream tool facts are from public documentation pages read once
(§1.4) and every unverified item is marked. Nothing was run on darwin, nothing on a Windows host, and the unit
of §4.4 was not run anywhere.
