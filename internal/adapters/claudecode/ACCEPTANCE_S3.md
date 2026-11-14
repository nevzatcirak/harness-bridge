# ADR-021 slice 3: acceptance (code part ships here; the live items are operator-run)

This PR ships the **code** of slice 3 (ADR-021 §10): init verification wired through the
runner, the SIGINT → SIGTERM escalation, `Executor.Interrupt`, the hardening argv,
`BRIDGE_CLAUDE_VERSIONS` parsing and the credential-canary harness code. The adapter stays
unwired. **Every live item below is NOT_EXERCISED by this PR. A live item that was not run is
never PASS, and slice 3 cannot ship until the maintainer provisions an authenticated run for
the canary (ADR-021 "Consequence of D14 Q10"; Q10 is neither answered nor reopened here).**

## What the PR's own gates cover (all offline, fake CLI)

| Evidence | Where |
| --- | --- |
| each init violation → `unknown` / `IDENTITY_MISMATCH`, guard kept, child interrupted; success or `aborted_*` result afterwards never `completed`/`interrupted`/released | `runner/s3_test.go` |
| result with the child alive → `running`, guard held, second send `SESSION_BUSY`; grace → row 6a / `EXIT_UNCONFIRMED` / `child_unconfirmed`; wait failure after a result → row 6a | `runner/s3_test.go` |
| overlap pairs (cap+success, cap+`aborted_*`, grace+late result/exit, shutdown+`aborted_*`, user+`aborted_*` = `interrupted`, user escalation exhausted = 6b, row 6 only without a result) | `runner/s3_test.go`, `state_test.go` |
| SIGINT → SIGTERM sequence and timing with fake children, and with real processes and real signals; no SIGKILL; a child that ignores both is abandoned, never killed | `runner/s3_test.go`, `runner/s3_linux_test.go` |
| argv hardening flags, settings JSON and deny rules; `BRIDGE_CLAUDE_VERSIONS` | `argv_test.go`, `versions_test.go` |
| canary pre-flight and verdict logic against FABRICATED streams | `canary_test.go` |

## Live items (operator-run; evidence file required; all NOT_EXERCISED)

Run as the Claude service user's isolated test user, in a throwaway root, with a throwaway
session UUID. Never `--bare`; never copy, read or print a credential; evidence files carry
verdict lines and event types only.

1. **Hostile-root test and credential canary (§8.3).** Build the throwaway tree (form A: a
   test user with its own HOME inside the tree; form B supplementary). Create the decoy file
   (`HOME/.claude/.credentials.decoy.json`, mode 0600, content = a fresh marker from
   `claudecode.NewCanaryMarker`) and the decoy profile (never `active_config`). Run
   `claudecode.Preflight`; it must pass before anything starts. Run each probe of
   `claudecode.Probes` with its `claudecode.Prompt`, plus the positive control without the deny
   rule, then `claudecode.EvaluateCanary` over the recorded streams; commit
   `CanaryReport.Lines()` only. Pass needs every probe, the control, no decoy content, no file
   canary, no sink request, `mcp_servers` empty, `apiKeySource` `none`.
   **NOT_EXERCISED: no allowed authenticated run exists (D14 Q10).**
2. **Result-to-exit lag** of the pinned CLI over several runs: validates or replaces the 10 s
   exit-grace default. NOT_EXERCISED.
3. **Post-`result` stream capture** over several runs (shared with slice 1): decides whether the
   ignorable-trailer list stays empty. NOT_EXERCISED.
4. **`sudo` relays SIGINT and SIGTERM** to the CLI with a live tool call, and the process-group
   boundary (`setsid -w`) with `sudo`. NOT_EXERCISED.
5. **`--safe-mode` versus §8.3 item 2** on the pinned version. NOT_EXERCISED.
6. **Managed settings applied in `-p`**, and the deny rule inside the managed file. NOT_EXERCISED.
7. **Live prompt-from-stdin** with `--resume` and `stream-json` (see `ACCEPTANCE_S1.md`).
   NOT_EXERCISED.
8. **SIGPIPE ends the child when the Bridge dies** (§5). NOT_EXERCISED.
