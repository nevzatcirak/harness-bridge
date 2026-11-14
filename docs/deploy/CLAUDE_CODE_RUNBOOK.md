# Claude Code adapter: deployment runbook (ADR-021)

**Status: reference only. Nothing in this runbook has been performed. Every statement about
live behaviour is UNVERIFIED until the operator-run acceptance of ADR-021 slices 3 and 5 has
passed.** The adapter is not wired into the Bridge (`claudecode:` session IDs are refused
"not enabled"), the live credential canary has not run (no allowed authenticated run exists,
D14 Q10), and slice 3 cannot ship until it can. The steps below are for the operator; the
Bridge, its tests and its authors perform none of them.

## 0. Preconditions
- ADR-021 is ACCEPTED (D12, D14). Read §1, §3, §4 and §8 first.
- Two OS users, neither root: the Bridge service user (`harness-bridge`) and the Claude service
  user (`hbclaude`). The Claude user cannot read the Bridge's token files, lock directory, history
  directory, admin socket or TLS key.
- Do not resume a session that the Bridge also drives from a terminal at the same time: the CLI has
  no writer lock (D14 Q1, an accepted and documented residual risk).

## 1. Wrapper and sudoers (ADR-021 §1, §8.1)
1. Install a short root-owned wrapper (the `hb-p6-claude` pattern), for example
   `exec sudo -n -u hbclaude -H -- <absolute path of claude> "$@"` with `setsid -w` so the wrapper
   stays the parent until the CLI exits. File: regular, root-owned, not group/other writable, in
   root-owned directories that are not group/other writable. Never `--bare`.
2. A sudoers entry lets only `harness-bridge` run only that wrapper's target as `hbclaude`: no
   password, `env_reset`, no `SETENV`, no wildcard arguments beyond the pinned flags. The prompt is
   never in argv, so it never reaches `sudo`'s log.
3. UNVERIFIED: that `sudo` relays SIGINT and SIGTERM to the CLI and that `setsid -w` works with it
   (live acceptance, slice 3, item 4).

## 2. Managed settings (D14 Q5: this runbook may require them)
- Install `/etc/claude-code/managed-settings.json` as root. It should set `allowManagedHooksOnly`,
  `allowManagedMcpServersOnly` and `allowManagedPermissionRulesOnly`, and **it must itself contain
  the credential deny rules** for `Read`, `Glob` and `Grep` on `~/.claude/**` and
  `~/.config/anthropic/**` (the same rules the Bridge passes inline, ADR-021 §8.3 item 6): with
  `allowManagedPermissionRulesOnly` the inline rules may be ignored.
- UNVERIFIED: that managed settings apply in `-p`, the rule syntax and `~` expansion, and that
  `Grep`/`Glob` honour a `Read` deny. The live canary decides; a failing canary blocks shipping
  (D14 Q9).
- Alternatively record the `--safe-mode` decision (ADR-021 §8.3 item 3, UNVERIFIED).

## 3. Transcript retention
- Raise `cleanupPeriodDays` in the Claude user's user settings so the CLI's transcript outlives the
  Bridge's history retention (`BRIDGE_CLAUDE_HISTORY_MAX_AGE`, default 30 days; the CLI default is
  documented as 30 days). If the CLI transcript expires first, resume fails (ADR-021 §3 item 2) while
  the Bridge record stays readable. The Bridge does not edit this setting.

## 4. Version pin (D14 Q7)
- `BRIDGE_CLAUDE_VERSIONS` is an exact-version list, for example `2.1.322`, comma separated, no
  wildcard, range or prefix, at most 16 entries. There is no default: until a version has passed the
  live acceptance the list is empty and the adapter refuses to start.
- Before enabling, `claude --version` run as `hbclaude` must equal one entry. A run whose `init`
  reports another version ends `unknown` / `IDENTITY_MISMATCH` with the guard kept and the child
  interrupted.
- Only 2.1.322 has been exercised (the spike), and not through this adapter.

## 5. Allowlist and roots
- `BRIDGE_CLAUDE_SESSIONS`: a JSON array of `{session_id: "claudecode:<uuid>", root, label}` with exact
  lowercase UUIDs and roots inside `BRIDGE_ALLOWED_ROOTS`. Every ID must also be in
  `BRIDGE_ALLOWED_SESSIONS`. Treat each root as trusted content: a `CLAUDE.md` or project file can
  carry a prompt injection (ADR-021 §8.3 item 5).
- The sessions must already exist: the Bridge resumes by exact ID and never creates or forks.

## 6. Operating it (once wired; UNVERIFIED)
- Interrupt: SIGINT, then SIGTERM after 10 s, then the Bridge gives up after 5 s more
  (`child_unconfirmed`) and **never** sends SIGKILL. A child that ignores both must be removed by
  the operator (for example by killing its process group as `hbclaude`).
- Guard release of an `unknown` Claude execution, operator only, as the Bridge service user:
  `bridge admin release-guard --session claudecode:<uuid> --execution <execution_id> --ticket <ticket>`.
  The Bridge checks that the guard is held by exactly that execution, that it is `unknown`, that
  its child was reaped with a known status and its process group is empty, and again after 5 s. If
  the child is still alive, remove it first, then retry. A refused release changes nothing. A `failed`
  row-1 execution whose wait was unknown cannot be released: restart the Bridge (the guard is memory
  only).
- Restart gap: killing the Bridge mid-turn leaves a turn the next start marks `restart_gap`.

## 7. Live acceptance still open (operator-run, NOT_EXERCISED)
See `internal/adapters/claudecode/ACCEPTANCE_S3.md` and ADR-021 §10 slice 5 (R6).
