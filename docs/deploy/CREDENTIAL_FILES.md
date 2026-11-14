# H3 credential file implementation and local validation

PR branch `feat/h3-secure-claude-remote-connector`. Credential-file input is optional and mutually exclusive with the corresponding inline environment variable. No plaintext credential is ever committed.

## Configuration
- `OPENCODE_SERVER_PASSWORD_FILE=/absolute/path/to/credential`
- `BRIDGE_TOKEN_FILE=/absolute/path/to/credential`
- Alternative legacy environment variables continue to work, but cannot be combined with file variables.
- A normal file must be a direct regular file (no final symlink), private permissions (no group/other permission bits), 1–16384 bytes. Linux additionally accepts verified systemd root-owned read-only credential mounts with exact service-UID ACLs; the apparent 0440 mode is an ACL mask, not general group access. Arbitrary 0640/0440 files remain rejected. One final LF or CRLF is accepted. Embedded line breaks/NUL and empty content fail closed.
- For systemd `LoadCredential`, use the service's actual `CREDENTIALS_DIRECTORY` path in an `ExecStart` wrapper or a supported expansion of `%d` in `Environment=`. Do not copy secrets to command-line arguments. Inspect the resolved environment keys, never their values.
- `BRIDGE_TOKEN` (from either source) must remain at least 32 characters, or at least 43 characters when `BRIDGE_TOKEN_MAX_LIFETIME` is above `24h` (ADR-019).

## Operator's sandboxed test target
```text
OPENCODE_URL=http://127.0.0.1:4097
BRIDGE_ALLOWED_ROOTS=/opt/harness-bridge-test/workspace
BRIDGE_ALLOWED_SESSIONS=ses_e9329c0b4ffebOlm7GRyKxbMQ5
```

Both lists must name only sessions in workspaces whose `.opencode` content
(up to the worktree root) the operator trusts: read-only discovery can make
OpenCode bootstrap those directories and execute their plugins, also with
`BRIDGE_READ_ONLY=1` (CONTRACTS.md, "Discovery bootstraps native instances").

The native OpenCode server on 4097 is nonroot with Basic Auth and verified session survival across restart; current original root server at 4096 is **not** an approved remote target.

## Quality gates
After `git pull --ff-only` on the matching branch and checking that the tree is clean:
```sh
gofmt -w cmd/bridge/main.go cmd/bridge/secret.go cmd/bridge/secret_test.go
git diff --exit-code
go build ./...
go vet ./...
go test ./...
go test -race ./...
```
If formatting fails, commit formatting in the source PR first; do not modify a production working tree silently. Secrets never belong in shell history, tracked files, logs or command-line parameters.

## Verified local result and remaining gates

See [actual systemd/local MCP evidence](SYSTEMD_LOCAL_VALIDATION.md): credential
reader fixed and tested, Bridge service healthy on 127.0.0.1:8080, Basic Auth
against non-root :4097 verified, exact-session policy and real two-turn agent
continuation passed. The original :4096 process was untouched. Workspace files
remain readable where Unix permissions permit; no filesystem confinement claim.
Public TLS/Traefik and ordinary Claude Chat acceptance remain separate pending
gates; no public deployment was performed.

## Claude Code harness (ADR-021, slice 4): files and credentials

Operator reference, written ahead of any deployment. **Nothing in this section has been
performed or verified live (UNVERIFIED): the Claude Code adapter is not wired into the Bridge.**
The Bridge never reads, copies, prints or logs any of the files below except the ones it owns.

| File or directory | Owner and mode | Who reads it | Notes |
| --- | --- | --- | --- |
| `~/.claude/` of the Claude service user (`hbclaude`), including the subscription login | `hbclaude`, 0700/0600 | the CLI only | The Bridge user cannot read it and the Bridge never opens it (ADR-021 §8.4). The read tools of the CLI could, so the credential deny rules of ADR-021 §8.3 item 6 are passed on every run and must also be in the managed file. They are UNVERIFIED until the live canary (slice 3) passes. |
| `~/.config/anthropic/` of the same user | `hbclaude` | the CLI only | Profiles and `active_config`. A profile of `oidc_federation` auth ranks above the subscription login and is not detected by `init.apiKeySource`: the operator must not configure one for this user. |
| `/etc/claude-code/managed-settings.json` | root, 0644 | the CLI | Optional host-level policy (D14 Q5: the runbook may REQUIRE it). Whether managed settings apply in `-p` is UNVERIFIED. |
| `BRIDGE_CLAUDE_COMMAND` (the wrapper) | root, not group/other writable, in root-owned directories | the Bridge user executes it | Validated at startup; never found through `PATH`. |
| `BRIDGE_CLAUDE_SESSIONS` (allowlist JSON) | Bridge user, mode exactly 0600, one link, in directories owned by root or the Bridge user | the Bridge | Strict JSON: a top-level array, keys exactly `session_id`, `root`, `label`, no duplicate key, no `null`. |
| `BRIDGE_CLAUDE_VERSIONS` | environment value | the Bridge | Exact `MAJOR.MINOR.PATCH` strings, comma separated, no default: empty means the adapter refuses to start. |
| history directory | Bridge user, 0700; segment files 0600 | the Bridge | Holds redacted turn records; as sensitive as the CLI's own transcripts (ADR-021 §5). |
| writer lock directory | Bridge user, 0700 | the Bridge | One lock per allowlisted session. |
| the admin socket (`BRIDGE_ADMIN_SOCKET`) | Bridge user, 0700 directory | the Bridge user's operator | Carries the guard release of ADR-020, extended for `claudecode:` sessions. |

Secrets never belong in the wrapper, the sudoers file, argv, the environment of the child, the
allowlist or any committed file. The credential canary (decoy files only) must never name or
copy the real login (D14 Q10).
