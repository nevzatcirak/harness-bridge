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

## The live driver (task #314; the live run is a separate task)

`cmd/hb-claude-acceptance` (build tag `live`; not in the default build, tests or CI) with its logic in
`internal/acceptance` runs items 1-8 through the configured wrapper (default
`/usr/local/libexec/hb-claude-wrapper`) with the full pinned argv, at most 30 CLI runs and `--max-turns`
at most 5, stopping at the first budget breach. It writes verdict lines, event types, timings and exit
codes only. Item 1 now uses a **control path** (ADR-021 §8.3 amendment, 2026-11-16): the deny rule is
never lifted; each probe has a control run against an identical decoy outside every denied path, and the
verdict fails unless `init.mcp_servers` is empty with the pinned argv (finding in ADR-021 §8.3). Item 3 is
recorded by item 2's runs. **Every item below stays NOT_EXERCISED until the live run task reports.**

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
   the lines of `CanaryReport.LinesWith(observations)` only, normally through the acceptance driver
   (`cmd/hb-claude-acceptance`, build tag `live`), which writes them. The three checks that are not
   in the NDJSON stream (the history comparison, the file-canary scan, the sink observation) must
   each be **explicitly observed** and passed in `CanaryObservations`; a check that was not made
   prints `unknown` and the verdict is `incomplete`, never `pass`. There is no public entry that
   assumes them. Pass needs every probe, the control, no decoy content, no file canary, no sink
   request, all three of those checks observed, `mcp_servers` empty, `apiKeySource` `none`.
   **R6 run 1 (2026-11-18, #409):** item 1 was FAIL `control_reads_decoy=false` with every security check clean;
   the run could not decide the item (every run resumed one session, so the model declined after earlier refusals,
   and the controls sat outside the run's working directory). The driver now runs every probe and every control in
   its own fresh `--session-id` session (item 1 costs 12 runs, no bootstrap), gives both the same readable scope
   (`--add-dir <run home>`), prints a closed-set `denial_class=rule|scope|declined|none|other` per run, and ends
   item 1 as `INCOMPLETE` (`why=control_refused_<class>`, exit 3) when a control was refused for any reason, never
   as a FAIL of the product; a probe that was refused for any reason but the deny rule ends it as `INCOMPLETE`
   (`why=probe_not_rule_denied_<class>`), the production runner validates item 1 runs with the probe validator
   and refuses the glued flag forms, and the pre-flight checks `-run-home` (a directory owned by the run user,
   not group- or world-writable).
   **R6 run 2 (2026-11-19, #422):** INCOMPLETE `probe_not_rule_denied_other`; all six controls read their decoy and
   every security check was clean, but the four Grep/Glob probes were refused with `Permission to read <dir> has been
   denied.` (the Read probes with the known "denied by your permission settings" text), a text of the same managed
   rule that the classifier did not know. It now counts as `rule` only for the probe's own, clean target inside a
   denied directory (`DenialClass(.., DeniedDirs(plan))`); every other text of that shape stays `other`.
   **R6 run 3 (2026-11-20, relayed from the Planning Desk's log #117, not reproduced here): item 1 PASS**
   (`why=all_probes_denied_controls_read`, 6/6 probes `denial_class=rule`, 6/6 controls `none`, no leak, no file
   canary, no sink request, `mcp_servers=0` and `apiKeySource` `none` on 12/12 runs). The only live PASS of item 1 so far; a
   single run, never repeated (`docs/deploy/CLAUDE_CODE_RUNBOOK.md` §7a).
   The production `Observer` (task #372, `internal/acceptance/observer.go`) supplies the three observations
   behind the flags `-history-dir`, `-canary-dir` and `-sink-addr` of the driver command (operator steps,
   expected evidence lines and the UNVERIFIED list: `docs/deploy/CLAUDE_CODE_RUNBOOK.md` §7a); a flag that is
   left out leaves its check `unknown` and item 1 `INCOMPLETE`.
   **NOT_EXERCISED: no allowed authenticated run exists (D14 Q10).**
2. **Result-to-exit lag** of the pinned CLI over several runs: validates or replaces the 10 s
   exit-grace default. NOT_EXERCISED.
3. **Post-`result` stream capture** over several runs (shared with slice 1): decides whether the
   ignorable-trailer list stays empty. NOT_EXERCISED.
4. **`sudo` relays SIGINT and SIGTERM** to the CLI with a live tool call, and the process-group
   boundary (`setsid -w`) with `sudo`. **R6 run 4 (2026-11-20, #426): FAIL `signal_not_relayed_or_child_alive`,
   but the relay was not tested:** the SIGINT run was relayed (tool_use, signal sent, child gone), the SIGTERM run
   had `tool_uses=0` and `sent=none` (the model never called the tool). The two runs resumed one bootstrap session
   and ran outside the working directory without a scope. Each run is now a fresh `--session-id` session with the
   item 1 scope (`--add-dir <run home>`); a run without a tool_use is `INCOMPLETE`
   `signal_not_sent_no_tool_use` (or `signal_not_sent_after_tool_use` when a tool_use happened but the wrapper exited before the signal
   fired), FAIL stays for "sent, but the wrapper did not exit or the child lives". Run 5
   (`-items 4,6`) is UNVERIFIED. NOT_EXERCISED as a PASS.
5. **`--safe-mode` versus §8.3 item 2** on the pinned version. NOT_EXERCISED.
6. **Managed settings applied in `-p`**, and the deny rule inside the managed file. **R6 run 4 (2026-11-20,
   #426): FAIL `inconclusive_no_tool_use_or_denial`** (`tool_use=false`, `denial=false`, marker not shown: the model
   never called Read; a resumed bootstrap session and no scope). The run is now a fresh session with the item 1
   scope. No tool_use is `INCOMPLETE` `no_tool_use`; a tool_use without a denial and without the marker is
   `INCOMPLETE` `no_denial_no_marker`; a denial that is not the rule's is `INCOMPLETE` `denial_not_rule_<class>`;
   the marker shown is FAIL; **PASS needs `denial_class=rule`** (printed on the `managed` line). Run 5
   (`-items 4,6`) is UNVERIFIED. NOT_EXERCISED as a PASS.
7. **Live prompt-from-stdin** with `--resume` and `stream-json` (see `ACCEPTANCE_S1.md`).
   NOT_EXERCISED.
8. **SIGPIPE ends the child when the Bridge dies** (§5). NOT_EXERCISED.
