# H3 credential file implementation and local validation

PR branch `feat/h3-secure-claude-remote-connector`. Credential-file input is optional and mutually exclusive with the corresponding inline environment variable. No plaintext credential is ever committed.

## Configuration
- `OPENCODE_SERVER_PASSWORD_FILE=/absolute/path/to/credential`
- `BRIDGE_TOKEN_FILE=/absolute/path/to/credential`
- Alternative legacy environment variables continue to work, but cannot be combined with file variables.
- A normal file must be a direct regular file (no final symlink), private permissions (no group/other permission bits), 1–16384 bytes. Linux additionally accepts verified systemd root-owned read-only credential mounts with exact service-UID ACLs; the apparent 0440 mode is an ACL mask, not general group access. Arbitrary 0640/0440 files remain rejected. One final LF or CRLF is accepted. Embedded line breaks/NUL and empty content fail closed.
- For systemd `LoadCredential`, use the service's actual `CREDENTIALS_DIRECTORY` path in an `ExecStart` wrapper or a supported expansion of `%d` in `Environment=`. Do not copy secrets to command-line arguments. Inspect the resolved environment keys, never their values.
- `BRIDGE_TOKEN` (from either source) must remain at least 32 characters.

## Operator's sandboxed test target
```text
OPENCODE_URL=http://127.0.0.1:4097
BRIDGE_ALLOWED_ROOTS=/opt/harness-bridge-test/workspace
BRIDGE_ALLOWED_SESSIONS=ses_e9329c0b4ffebOlm7GRyKxbMQ5
```

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
