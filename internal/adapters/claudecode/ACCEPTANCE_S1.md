# ADR-021 slice 1: operator acceptance steps

Slice 1 ships the pure core. The executable acceptance is the recorded fixture
replay (`TestSpikeFixtureReplay`, run by `go test ./internal/adapters/claudecode/`).
The two live items below are **operator-run** (ADR-021 §10) and are **NOT_EXERCISED**
by this PR; a skipped or unexecuted live check is never PASS.

Run both as the Claude service user, in a throwaway root, against a throwaway
session UUID the operator created. Never `--bare`; never copy or print a
credential. Evidence files contain verdict lines and event types only, not raw
NDJSON of a run that touched a credential path.

## 1. Prompt on stdin with `--resume` and `stream-json` (2.1.322)

```text
printf '%s' 'Reply with only: ok.' | hb-p6-claude -p --resume <uuid> \
  --output-format stream-json --verbose --model sonnet \
  --permission-mode default --permission-prompts none --max-turns 1 \
  > out.ndjson; echo "exit=$?"
```

Pass: `out.ndjson` has a `system/init` and one `result` with `subtype: success`,
`is_error: false`, and `exit=0`, and the model answered the stdin prompt. Fail: no
`result`, or an answer that ignores the prompt. If it fails, ADR-021 §1 is amended
before slice 2.

## 2. Multi-run capture of the stream after the final `result`

Repeat step 1 at least 10 times (vary `--max-turns 1` and a prompt that uses a
read-only tool). For each run list the raw lines after the last `result` line,
cut to their first 80 bytes. Do NOT use `jq` for this: it cannot print an
undecodable trailing line, which is exactly what row 8 must count.

```text
n=$(grep -n '"type": *"result"' out.ndjson | tail -1 | cut -d: -f1)
tail -n +$((n+1)) out.ndjson | cut -c1-80
```

Record per run: the trailing lines' `type`/`subtype` (or "undecodable"), whether
`tool_progress` appeared before the result and at what spacing, and the
result-to-exit lag. Trailing content may be sensitive: the evidence file holds
types and counts only. Until a capture exists, the row 8 ignorable-trailer list
stays EMPTY (`ignorableTrailers` in `state.go`) and a trailing event of any type
makes a run `unknown`.

## 3. The shape of `system/init` on 2.1.322

Run step 1 and inspect the `system/init` line. Pass: it carries the three keys the
machine verifies, `tools` (a list of strings), `mcp_servers` (a list) and `plugins`
(a list), plus `cwd`, `session_id`, `permissionMode`, `apiKeySource` and
`claude_code_version`. The recorded spike excerpts redacted `tools`, `mcp_servers`
and `plugins`, so their presence is UNVERIFIED. The state machine treats an absent
or non-list value as an identity violation (row 0), so if a key is missing every
run would end `unknown`: then ADR-021 §8.3 item 4 is amended before slice 2.
**NOT_EXERCISED** by this PR.
