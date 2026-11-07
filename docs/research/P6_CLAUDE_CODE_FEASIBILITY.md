# P6 Claude Code adapter feasibility (research only)

Date: 2026-11-07. Task: P6-CLAUDE-CODE-RESEARCH (issue #103). Status:
**RESEARCH, not a decision.** No Go code, no live Bridge, no installs. This
note feeds ROADMAP Phase 6 ("Claude Code Adapter") and the open feasibility
question "Which supported Claude Code permission and model-selection
interfaces are actually installed?"

Method: every claim below cites an official page on `code.claude.com`
(the current home of the Claude Code and Agent SDK docs), accessed
2026-11-07 UTC. Claims not stated in those pages are marked **UNVERIFIED**.
Nothing was executed against Claude Code except a read-only version probe:
`claude --version` on the worker host printed `2.1.319 (Claude Code)`. That is
the CLI this worker itself runs under. It is **not** an audit of the CLI that a
Bridge deployment would use, and no `claude -p` run was made.

Source keys used below:

| Key | URL |
| --- | --- |
| CLI | https://code.claude.com/docs/en/cli-reference |
| HEADLESS | https://code.claude.com/docs/en/headless |
| SESSIONS | https://code.claude.com/docs/en/sessions |
| SDK-SESS | https://code.claude.com/docs/en/agent-sdk/sessions |
| SDK-TS | https://code.claude.com/docs/en/agent-sdk/typescript |
| SDK-PERM | https://code.claude.com/docs/en/agent-sdk/permissions |
| SDK-INPUT | https://code.claude.com/docs/en/agent-sdk/user-input |
| RC | https://code.claude.com/docs/en/remote-control |
| HOOKS | https://code.claude.com/docs/en/hooks |

## Summary

| # | Item | Verdict | One-line evidence |
| --- | --- | --- | --- |
| 1 | Discovery | VERIFIED (SDK); CLI: NOT SUPPORTED for listing | SDK `listSessions()` / `getSessionInfo()`; CLI has only the interactive picker. JSONL files exist but their format is internal |
| 2 | History read | VERIFIED (SDK only); private-file parsing NOT SUPPORTED | SDK `getSessionMessages(id, {limit, offset})`. The docs say the JSONL "entry format is internal … can break on any release" |
| 3 | Resume and prompt | VERIFIED | `claude -p --resume <id>` keeps history and session ID; `--fork-session` makes a new ID; lookup works from any cwd; exit 0 / non-zero |
| 4 | Execution observation | VERIFIED | `stream-json` with `system/init`, `result` (subtype, `is_error`, `terminal_reason`, cost, usage, `permission_denials`), heartbeats, optional state events |
| 5 | Concurrency / single writer | **NOT SUPPORTED** (no lock) | "If you resume the same session in two terminals without forking, messages from both interleave into one transcript." |
| 6 | Permissions | VERIFIED for modes, `--permission-prompt-tool` and SDK `canUseTool`; prompt-tool wire schema UNVERIFIED | `canUseTool` "can stay pending indefinitely"; the MCP prompt-tool request/response JSON is not specified in the official pages read |
| 7 | Interrupt | VERIFIED | SIGINT or SDK `interrupt()` ends the turn; SIGTERM exits 143 and leaves the turn unfinished |
| 8 | Active terminal control | **NOT SUPPORTED** for a third-party backend | Remote Control is a claude.ai / Claude app surface (subscription, no API keys); no documented programmatic API. Background sessions are a separate local feature |
| 9 | Go adapter boundary | Recommendation | Go spawns `claude -p --resume <id> --output-format stream-json` per prompt; Bridge-owned single-writer fence; permissions stay fail-closed (`--permission-prompts none`) until a separate spike |

## 1. Discovery

**VERIFIED (SDK):**
- "Both SDKs expose functions for enumerating sessions on disk and reading
  their messages: `listSessions()` and `getSessionMessages()` in TypeScript,
  `list_sessions()` and `get_session_messages()` in Python" (SDK-SESS).
- `listSessions(options?: ListSessionsOptions): Promise<SDKSessionInfo[]>`,
  "Discovers and lists past sessions with light metadata. Filter by project
  directory or list sessions across all projects." Options: `dir`, `limit`,
  `includeWorktrees` (SDK-TS).
- `getSessionInfo(sessionId)` "Reads metadata for a single session by ID
  without scanning the full project directory", and returns `undefined` if
  the session is not found (SDK-TS).

**VERIFIED (storage, but not a supported read interface):**
- "By default, Claude Code stores transcripts as JSONL at
  `~/.claude/projects/<project>/<session-id>.jsonl`, where `<project>` is your
  working directory path with non-alphanumeric characters replaced by `-`"
  (SESSIONS). Names over 200 characters are truncated and a hash of the full
  path is appended. `CLAUDE_CONFIG_DIR` and `CLAUDE_CODE_PROJECT_DIR_NAME`
  relocate the storage.
- Retention: a 30-day default, configurable through `cleanupPeriodDays`
  (SESSIONS). A Bridge must expect sessions to disappear.
- `-p` and SDK sessions are left out of the interactive picker and of plain
  `--continue`, but `claude --resume <session-id>` still finds them (SESSIONS).

**CLI listing: NOT SUPPORTED.** `claude --resume` without an ID opens an
interactive picker (CLI, SESSIONS). No documented non-interactive CLI command
lists stored sessions. `claude agents --json` prints *active background
sessions* only, and `--all` adds completed background sessions (CLI). That
covers the background-session feature, not every stored transcript.

**Consequence:** a pure-Go adapter cannot discover sessions through a supported
interface without the TS or Python SDK. Directory listing of
`~/.claude/projects` is possible, but it relies on an internal layout. Treat
it as UNSUPPORTED, or at most as unverified candidate hints that must then be
confirmed by an exact-ID resume.

## 2. History read

**VERIFIED (SDK):**
- `getSessionMessages(sessionId, options?)` "Reads user and assistant messages
  from a past session transcript." Pagination is `limit` and `offset` ("Number
  of messages to skip from the start"); there is no cursor. It returns
  `SessionMessage[]` with `type`, `uuid`, `session_id`, raw `message`,
  `parent_tool_use_id` and `parent_agent_id` (SDK-TS).

**NOT SUPPORTED: parsing the JSONL files.** "The entry format is internal to
Claude Code and changes between versions, so scripts that parse these files
directly can break on any release. To build on session data, use `/export` or
the script interfaces instead" (SESSIONS).

**CLI alternatives (supported, but with side effects):**
- `/export` writes a rendered plain-text transcript for a person to read
  (SESSIONS). It is not structured.
- Under "Ask an existing session a question", `claude -p --resume <id>` sends a
  new prompt (SESSIONS). That **appends a turn and spends tokens**, so it is a
  mutation, not a read.

**UNVERIFIED:**
- whether `getSessionMessages` is safe while another process is writing the
  same file;
- how it treats compacted history.

## 3. Resume and prompt

**VERIFIED:**
- **History.** "Use `--continue` to continue the most recent conversation, or
  `--resume` with a session ID to continue a specific conversation" (HEADLESS).
  A resumed session restores the conversation history: messages, tool calls
  and results. After compaction it loads the last summary plus the messages
  since (SESSIONS).
- **Same session ID.** `--fork-session`: "When resuming, create a new session
  ID instead of reusing the original" (CLI). So a plain `--resume` keeps the
  ID. A fork "gets its own session ID; the original's ID and history stay
  unchanged" (SDK-SESS).
- **cwd is not required to match.** "You can run the two commands from
  different directories: Claude Code finds the session by its ID in any
  project on this machine" (HEADLESS).
  - Lookup order: the current project and its worktrees, then every other
    project.
  - A duplicate copy of the transcript gives not-found rather than an
    arbitrary match.
  - An unknown ID gives `No conversation found with session ID: <id>`
    (SESSIONS).
  - Before v2.1.223 the lookup covered only the current project.
- **cwd is not preserved as a guarantee either.** The docs do not say a CLI
  resume switches to the session's original directory. For the SDK session
  store, "resume from a `cwd` matching the original run's" (SDK-SESS).
  - **Adapter rule:** the Bridge must start the subprocess in the session's
    recorded cwd itself and check it against `system/init.cwd`.
  - Which cwd a resumed `-p` run uses: **UNVERIFIED**, needs the spike.
- **Flags that are not restored.** Configuration such as `--mcp-config`,
  `--settings`, `--add-dir` and `--fallback-model` "pass them again when you
  resume" (SESSIONS). With `-p`, the permission mode is that of a new `-p` run,
  except plan mode under conditions (SESSIONS).
- **Output formats.** `text`, `json` or `stream-json`. `stream-json` needs
  `--verbose`, and `--include-partial-messages` adds token deltas (HEADLESS,
  CLI).
- **Exit codes.** "Claude Code exits with code 0 on success and a non-zero code
  when the run fails". Invalid flags are reported on stderr before the run; an
  in-run failure "prints the failure as the result on stdout" (HEADLESS).
  SIGTERM exits with 143 (HEADLESS).
- **Interactive picker failure.** It exits 1 with `Failed to resume the
  conversation` (SESSIONS).
- **Running background session.** `claude -p --resume` on a background session
  that is still running, with output or limit flags, "sends nothing and exits
  with status 1" (SESSIONS).

## 4. Execution observation

**VERIFIED** (HEADLESS, SDK-TS):
- **Init event.** The first event is `system/init`, unless startup hook or
  plugin events come first. It carries `session_id`, `cwd`, `model`, `tools`,
  `permissionMode`, `claude_code_version` and `capabilities` (for example
  `interrupt_receipt_v1`; the field exists from v2.1.205 on).
- **Result event.** "The last line of the stream is a `result` message"
  (HEADLESS).
  - Success: `subtype: "success"`.
  - Errors: `error_max_turns`, `error_during_execution`,
    `error_max_budget_usd` or `error_max_structured_output_retries`.
  - Fields: `is_error`, `stop_reason`, `terminal_reason`, `num_turns`,
    `duration_ms`, `total_cost_usd`, `usage`, `modelUsage`,
    `permission_denials[]`, and `errors[]` on the error subtypes.
  - `terminal_reason` values include `completed`, `max_turns`,
    `aborted_streaming`, `aborted_tools`, `api_error`, `budget_exhausted` and
    `prompt_too_long`.
- **Cost is cumulative and estimated.** On `--resume` the cost "reports the
  conversation's whole total, earlier runs' spend included". Both figures are
  client-side estimates (HEADLESS).
- **Retries.** `system/api_retry` carries `attempt`, `error_status` and an
  `error` category (HEADLESS).
- **Denials.** `system/permission_denied` is best-effort. `permission_denials`
  on the result "is the authoritative record" (SDK-TS).
- **Liveness.** `tool_progress` with `heartbeat: true` every 30 s while a
  main-conversation tool runs, but not inside subagents (SDK-TS).
- **State events, opt-in.** With `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1`,
  `system/session_state_changed` reports `running`, `idle` or
  `requires_action`. "A turn's `idle` message and its `result` message can
  arrive in either order" (SDK-TS).
- **Background work can extend a run.** A `-p` run can stay open for
  background work, with a 10-minute idle cap by default, and later turns emit
  more results (HEADLESS).

**Completion rule for the adapter, matching ADR-011:**
- **Success** requires a `result` event with `subtype == "success"` and
  `is_error == false`, correlated by `session_id`, plus a clean process exit
  with code 0.
- **Idle is never success.** `idle` and process exit without a `result` are not
  success.
- **Process ended without a result:** a missing result, exit 143, a crash or a
  broken stream is `UNKNOWN_OUTCOME`.
- **Background turns:** if the run starts more than one turn through
  background work, the last `result` decides.
  - **UNVERIFIED:** how `result_index` orders those results.

## 5. Concurrency

**NOT SUPPORTED: no lock.**
- **Same session in two terminals.** "If you resume the same session in two
  terminals without forking, messages from both interleave into one
  transcript" (SESSIONS). No official page read here documents a lock, a
  lease or a rejection for a second writer.
- **Running background session: there is a guard.**
  - `claude -p --resume` with output flags exits 1 rather than send (SESSIONS).
  - Without output flags, a terminal `-p --resume` *attaches* and sends the
    prompt as the session's next turn (SESSIONS).
  - The adapter must always pass `--output-format stream-json`, so it hits the
    refusal path.
- **Interactive terminal open on the session.** Docs say only that the
  messages interleave. A non-background interactive session has no documented
  detection. **UNVERIFIED:** whether the resuming process can detect it at all.

**Consequence for ADR-010:**
- **Bridge-owned fence.** The single-writer guarantee must come from the Bridge
  itself: an in-process per-session guard plus a host-level lock file owned by
  the Bridge (not by Claude Code).
- **Human writers stay outside it.** That still cannot exclude a human who
  resumes the session in a terminal. This is an accepted, documented residual
  risk, or the adapter refuses sessions it did not create.
- **Fork when exclusivity matters.** `--fork-session` gives the Bridge a
  private copy with a new ID. That avoids interleaving but breaks "continue
  the same session" semantics, so it is a product decision.

## 6. Permissions

**VERIFIED:**
- **Modes.** `--permission-mode` accepts `default` (alias `manual`),
  `acceptEdits`, `plan`, `auto`, `dontAsk` and `bypassPermissions` (CLI).
  - With v2.1.302 or later, a `-p` run starts in Manual (`default`) when
    nothing sets a mode.
  - A call that needs approval is then "denied unless something approves it,
    such as a `PermissionRequest` hook or a `--permission-prompt-tool`"
    (HEADLESS).
- **Evaluation order** (SDK-PERM):
  1. hooks;
  2. deny rules;
  3. ask rules;
  4. permission mode;
  5. allow rules;
  6. `canUseTool` or the prompt tool.

  Auto-approved calls never reach the callback.
- **`--permission-prompt-tool`.** "Specify an MCP tool to handle permission
  prompts in non-interactive mode. Pass the full name, such as
  `mcp__<server>__<tool>`" (CLI).
  - Claude Code waits for that MCP server to connect, up to `MCP_TIMEOUT`
    (30 s by default).
  - The tool cannot approve MCP tools marked as requiring user interaction.
- **SDK `canUseTool`.**
  - Arguments: `(toolName, input, options)`. `options` carries `signal:
    AbortSignal` and `suggestions` (SDK-INPUT).
  - Reply: `{behavior: "allow", updatedInput}` or `{behavior: "deny",
    message}`.
  - **No timeout:** "The callback can stay pending indefinitely. Execution
    remains paused until your callback returns." For long waits the docs
    recommend a `PreToolUse` hook returning `defer`, so that "the process can
    exit and resume later from the persisted session" (SDK-INPUT).
- **`--permission-prompts none`.** It denies anything that would prompt,
  "Claude is told that nobody can approve the request and not to retry it",
  and denials appear in `permission_denials` (HEADLESS; v2.1.259 or later).
- **Cancellation on shutdown.** If the SDK closes the session, "Claude Code
  cancels the prompt as soon as the input ends". A SIGTERM leaves the prompt
  unanswered (HEADLESS).

**UNVERIFIED:**
- **The MCP prompt-tool wire contract:** the exact request arguments (tool use
  ID, tool name, input) and the response JSON shape. None of the official
  pages read specifies them. Community sources describe a JSON text block with
  `behavior`, `updatedInput` and `message`, but those are not evidence. A
  `tool_use_id` in the request is what Bridge request identity and execution
  correlation would hang on.
- Whether the prompt tool is ever re-invoked for the same call after an MCP
  transport error, and what a missing reply does over MCP. The SDK says the
  callback stays pending; the MCP path is unspecified.
- Allow-once vs persistent rules over the prompt-tool path.
- Race semantics between `PermissionRequest` hooks and the prompt tool.

**Go vs SDK trade-off:**
- **A Go process cannot call `canUseTool`.** It is an in-process TS or Python
  callback.
- **The stream-json control protocol is not a documented public contract.**
  The SDK and CLI exchange `control_request` messages (`initialize` and
  `interrupt` are named in SDK-TS), but no page read specifies the wire format.
  Re-implementing it in Go would rely on an unpublished protocol: UNSUPPORTED.
- **Supported Go-only path:** `claude -p --permission-prompt-tool
  mcp__bridge__approve --mcp-config …`, where the Bridge serves the MCP tool
  itself. Its wire schema is UNVERIFIED (above), so it needs its own spike and
  evidence before Phase 2/4 permission safety can be claimed.
- **SDK path:** a TS or Python sidecar exposing `canUseTool` to Go. The ROADMAP
  forbids a Python or Node backend without explicit approval. That would be a
  separate ADR with its own supply-chain and process-boundary review.
- **Until then:** run with `--permission-prompts none`. Remote approval stays
  disabled, which is the ROADMAP's fail-closed fallback.

## 7. Interrupt

**VERIFIED** (HEADLESS, SDK-TS):
- **SIGINT or SDK `interrupt()`.** "To end the turn instead, send SIGINT, or
  call the Agent SDK's `interrupt()`, before you stop the process."
  `interrupt()` is "Only available in streaming input mode". With the
  `interrupt_receipt_v1` capability it returns the messages that were pending.
- **SIGTERM.** "Claude Code exits with code 143. Claude Code leaves the turn
  that was in progress unfinished and records no result for it."
  - Running Bash process trees are terminated and recorded as killed.
  - `SessionEnd` hooks run.
  - No new tool call or model request starts.
- **Resume after an interrupt.** "Claude Code leaves the interrupted turn as it
  is, and your next prompt drives the conversation", unless
  `CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1` is set. An interrupted tool call "doesn't
  get rerun when you resume" (SESSIONS).

**Adapter mapping:**
- **Cancel:** `Interrupt` sends SIGINT to the child.
- **Bounded wait:** wait for a `result` event and exit within a deadline.
  Escalate to SIGTERM only after that, and report `UNKNOWN_OUTCOME`, since
  SIGTERM records no result.
- **UNVERIFIED:** what `result` subtype and `terminal_reason` a SIGINT
  produces in `-p` mode (expected `aborted_*`). The spike must record it.

## 8. Active terminal control

**Remote Control (RC)** is a Claude Code feature that "connects claude.ai/code
or the Claude app for iOS and Android to a Claude Code session running on your
machine". "The web and mobile interfaces are a window into that local session"
(RC).
- **Starting it:** `claude remote-control` (server mode), `claude
  --remote-control`, or `/remote-control` inside an existing interactive
  session (CLI, RC).
- **Requirements:** it needs Pro, Max, Ultra, Team or Enterprise. "API keys are
  not supported." It is disabled when `ANTHROPIC_BASE_URL` points elsewhere
  (RC).
- **One controller at a time:** "Another connection took over this session" is
  a documented state (RC).
- **No third-party API.** None of the pages read documents a programmatic API
  that lets a third-party process such as the Bridge drive an RC session. RC is
  a claude.ai / Claude app product surface, not a Bridge interface.
  **NOT SUPPORTED** for the adapter.

**Background sessions (agent view)** are a separate, local feature (CLI,
SESSIONS):
- `claude agents --json`, `claude attach <id>`, `claude logs <id>`, `claude
  stop <id>` and `claude respawn <id>` operate on sessions hosted by a
  supervisor.
- `claude --resume <id> "prompt"` on a running background session sends the
  prompt as its next turn and then *attaches the terminal*. That is
  interactive and refused when output is piped or flags such as
  `--output-format` are present (SESSIONS).
- **No structured result stream** to a non-interactive caller is documented
  for this path. **UNVERIFIED as a supported Bridge interface**; not
  recommended.

**Conclusion:**
- **What is supported:** historical resume of a session that is *not*
  currently running, by exact ID. This matches the ROADMAP's distinction.
- **What is not:** no supported mechanism was found for a Go backend to
  control an already-running interactive terminal session. The Bridge must not
  claim it.

## 9. Recommended Go adapter boundary

**Boundary:** a Go `ClaudeCodeAdapter` that spawns one child per prompt:

```text
claude -p --resume <exact-uuid> --output-format stream-json --verbose \
  --permission-prompts none --permission-mode default \
  [--bare] [--max-turns N] [--max-budget-usd X] "<prompt>"
```

It runs with `cmd.Dir` set to the session's recorded cwd, a minimal
environment and no shell.
- **Parsing:** read stdout as NDJSON, bounded per line and in total.
- **Gates:** check `system/init.session_id == <exact-uuid>` and `init.cwd`
  against the recorded cwd. Correlate the final `result`.
- **Size limits:** prompts go through argv or stdin. Stdin is capped at 10 MB
  (HEADLESS).
- **Single writer:** a Bridge-owned per-session guard plus a host lock file
  give single writer *among Bridge writers*. `--fork-session` is the only
  documented way to get a private writer.
- **Discovery and history:** use only Bridge-recorded sessions (IDs the Bridge
  created or the operator registered) and Bridge-recorded results. SDK listing
  needs a sidecar (§6). Never parse the JSONL.
- **Permissions:** `--permission-prompts none` plus deny rules. Remote approval
  stays disabled (§6).
- **Interrupt:** SIGINT, a bounded wait, then SIGTERM with `UNKNOWN_OUTCOME`.

**Risks:**
- **No native lock.** A human terminal on the same session interleaves (§5).
- **Fast CLI churn.** Behaviour is gated on CLI versions in the v2.1.2xx–2.1.30x
  range (lookup scope, default mode, `--permission-prompts`). Pin and record
  `claude_code_version` and `capabilities` from `system/init`.
- **Repository content runs.** Without `--bare`, a `-p` run loads project hooks
  and `.mcp.json` "even in a folder you've never trusted" (HEADLESS). Prefer
  `--bare`, but it needs `ANTHROPIC_API_KEY`, not a subscription login
  (HEADLESS). That is an operator and auth decision.
- **Retention.** Transcripts expire after 30 days by default (SESSIONS).
- **Cost.** It is cumulative across resumes and is an estimate (HEADLESS).
- **Discovery without an SDK sidecar** is limited to Bridge-known IDs.

**Minimal spike plan** (local, non-root, a throwaway project, the exact pinned
CLI):
1. **Version audit:** record `claude --version`, the `system/init`
   `claude_code_version` and `capabilities`, and the auth mode used.
2. **Exact-ID resume:** create session A in dir X with a marker. Run `-p
   --resume A` from dir X and recall the marker without it in prompt 2.
   Expect the same `session_id`, history retained and `init.cwd == X`. Repeat
   from dir Y and record which cwd is used.
3. **Result correlation:** confirm one `result` per turn, `subtype` and
   `is_error`, exit 0. Then an induced failure (`--max-turns 1` on a multi-step
   task): `error_max_turns`, non-zero exit.
4. **Unknown ID:** exit code and message, with zero side effects.
5. **Concurrency:** two concurrent `-p --resume A`. Record the interleaving or
   any refusal, and show that the Bridge fence rejects the second Bridge
   writer.
6. **Interrupt:** SIGINT during a long tool call records the result subtype
   and `terminal_reason`. SIGTERM gives 143, no result, and Bridge
   `UNKNOWN_OUTCOME`. Resume afterwards and record the state.
7. **Permission fail-closed:** a tool needing approval is denied under
   `--permission-prompts none` and appears in `permission_denials`.
8. **Cross-project denial:** a session ID outside the ACL is refused by the
   Bridge before any spawn.

**Acceptance:** every step is recorded with raw NDJSON excerpts (redacted),
exit codes and the CLI version. Anything not observed is NOT_EXERCISED.

**Out of scope for Phase 6:**
- active terminal control and Remote Control;
- a TS/Python SDK sidecar (needs explicit approval and its own ADR);
- remote permission approval through `--permission-prompt-tool` (a separate
  spike after its wire schema is verified);
- JSONL parsing;
- background-session attach;
- model selection beyond what `system/init` reports.

## Limitations

- Docs-only. No `claude -p` run was made. The version probe covers this worker
  host's CLI (`2.1.319`), not a Bridge deployment host.
- Pages are long and were read through a fetch tool in parts. Some SDK-TS
  sections (the `CanUseTool` options type, `persistSession`, the control
  protocol) and the HOOKS `PermissionRequest` and `defer` details were not
  fully retrieved. Claims depending on them are marked UNVERIFIED.
- Docs change frequently, and every cited behaviour carries a version note.
  Re-verify against the pinned CLI before implementing.
