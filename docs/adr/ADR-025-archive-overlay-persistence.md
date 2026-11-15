# ADR-025: Archive overlay persistence (design for ADR-022 slice S8)

Date: 2026-11-15. Status: **PROPOSED** (design only; no code, no deployment). Written for
planner task #296 (ADR-022 section 10, slice S8: "mandatory, **before S6**").

**Acceptance.** This ADR is **not accepted**. Decision D12 (issue #117) delegates acceptance of the
ADRs it names to the Planning Desk; it does not name ADR-025, so acceptance stays with the maintainer
unless D12 is extended. Nothing here changes a D-item, ADR-010, ADR-012, ADR-020, ADR-021, ADR-022,
ADR-023 or ADR-024. Every point that would touch one of them is written as an **open question for the
maintainer** (section 9), each with a recommended answer that is a recommendation only.

**Evidence base.** Every claim about the code is a citation to `main` at `7047255` (checked on
2026-11-15) and is marked as design where it is not implemented. Nothing was run against a live
native server: how OpenCode behaves is not part of this design, because the overlay makes **no native
write** (D16 Q1).

## 1. Problem

D16 Q1 (ADR-022 section 12, line 920) chose **option (B)** for archive: "a reversible Bridge archive
overlay for Chat. Native archive (`PATCH time.archived`) is operator-only and is not an MCP tool.
Consequence: the overlay needs its own persistence design (slice S8, mandatory and before S6)". The
definition of (B) is ADR-022 section 7.2 (line 760): "the Bridge hides the session from its own
catalog and refuses prompts to it, and the native state is untouched. Cost: the overlay is state the
Bridge must keep; if it is memory-only (ADR-010), a restart silently un-archives everything, and if it
is persisted that is new persistent state with its own recovery and no-restoration-from-journal
questions."

What exists today:

- A **read-only** interface, `ArchiveOverlay.Archived(ctx, sessionID) (bool, error)`
  (`internal/mcp/catalog.go:28-35`), with one production implementation, `EmptyArchiveOverlay`
  (`catalog.go:37-41`): nothing is archived. `list_catalog` hides overlay-archived sessions unless
  `include_archived` is true (D16 Q5, `catalog.go:69,95-97`; a read error fails the whole call,
  `catalog.go:28-34`). The dashboard takes the same interface from `Server.Catalog` and hides archived
  rows unless the request carries `archived=1`, and an overlay error fails the list and the detail
  (ADR-023 "As built (slice 5b)"; `internal/mcp/dashsource.go`, `internal/dashboard/source.go`
  `WithArchived`).
- **No writer and no persistence.** `archive_session` and `close_session` do not exist (ADR-022
  section 10 row S6, line 873).
- The invariants the design must keep: ADR-010 "Execution/key/parent-ID mappings remain memory-only"
  (`ADR-010-single-writer.md:61-69`); "No authorization or grant restoration from journal history alone"
  (`AGENTS.md:75`); the lifecycle registry "is not an authority for the lifecycle tools ... cannot be
  forged by editing a file or by a journal replay" (ADR-022 section 3.2, lines 306-318); a dynamic mutable
  ACL file was already rejected (ADR-022 section 9, option 1).

The question of this ADR: **where does the archive set live, how does it survive a restart without
becoming an authority, and how do `include_archived`, the dashboard filter, un-archive and the S6 tools
use it.**

## 2. Definitions and the rule that bounds the design

- The **overlay** is a set of session IDs (`opencode:ses_...`) the Bridge treats as archived. An entry is a
  **label**.
- **The authority rule (the one the whole design serves).** The overlay may only **hide or label** a
  session, and may only **narrow** what a caller can do to it. It never grants, restores or widens
  anything. Authority for every tool stays exactly where it is today: the immutable operator ACL
  (ADR-012), the lifecycle roots and modes (ADR-022 section 3.2) and the per-root `existing_tools`
  flag (which grants nothing until slice S9). Removing the overlay file, replacing it with another, or
  forging an entry must never change the outcome of any authorization check.
- The overlay is not an execution record: nothing in it is an execution ID, a guard or an idempotency
  record (so ADR-010 "memory-only mappings" is untouched), and nothing in it is replayed.

## 3. Options for where it lives

| # | Option | For | Against |
|---|---|---|---|
| 1 | **Memory-only** (like the ADR-010 mappings) | no new persistent state; zero tamper surface | a restart silently un-archives everything: the archive stops being the reversible organization D16 Q1 asked for; the operator's picture of the catalog changes at every restart |
| 2 | **One Bridge-owned snapshot file in a private directory, replaced atomically** | simple; one complete state at any instant; torn writes cannot be observed (rename); bounded size | rewrites the whole file per change (bounded: at most 1000 entries, section 4.2); needs the directory and file rules below |
| 3 | **Append-only event log** (like the Claude history store, ADR-021 section 5) | cheap writes; an audit trail inside the file | needs compaction and a torn-tail rule; replay logic is more code than a set of at most 1000 IDs needs |
| 4 | **Derived from the journal** (replay `session_archive` audit lines) | no new file | **rejected**: it is exactly "restoration from journal history alone" (`AGENTS.md:75`); journal lines are free-text-free by design, rotated, not tamper-evident, and written by `slog` which cannot report failure; the state would depend on journald retention |
| 5 | **A native marker** (metadata or `time.archived` through `PATCH`) | survives anything | **rejected by D16 Q1**: the Chat tools make no native write, and native archive is operator-only |

**Proposed: option 2.** The set is small and bounded, so a whole-file atomic replace is simpler and has
fewer failure modes than a log with compaction. Option 1 remains a valid fallback (section 9, question 5)
for an operator who does not configure the directory: then no archive tool is registered at all and the
interface stays `EmptyArchiveOverlay`.

## 4. The design (proposed)

### 4.1 Location, ownership, configuration

- A new directory, **`BRIDGE_OVERLAY_DIR`** (the variable name is a **proposal**; question 6): absolute,
  owned by the Bridge service user, mode exactly `0700`, no symlink in the final component, validated at
  startup with the same open-by-fd checks as the ADR-010 lock directory
  (`internal/execution/ownership_linux.go:22-33`: `O_DIRECTORY|O_NOFOLLOW`, owner and `mode&0077 == 0`)
  and the history directory (`internal/adapters/claudecode/history_linux.go:17-30`, mode exactly `0700`).
  It is separate from `BRIDGE_WRITER_LOCK_DIR` and from `BRIDGE_CLAUDE_HISTORY_DIR`, and not readable by
  agent users (same operator duty as ADR-021 section 5: the parent chain not writable by others).
- Files in it: `overlay.json` (mode `0600`, regular, single link, opened `O_NOFOLLOW|O_CLOEXEC` like
  `history_linux.go:44-61`) and `overlay.lock` (below).
- **Unset means not configured**: the archive tools are not registered (ADR-022 section 3.1: no
  lifecycle configuration, no lifecycle tools), `Server.Catalog.Overlay` stays `EmptyArchiveOverlay`, and a
  deployed Bridge is unchanged. This is the same default-off rule as every lifecycle slice.

### 4.2 Format and bounds

One strict-JSON object (unknown keys refuse, like the lifecycle config, `internal/lifecycle/config.go`
`maxConfigBytes = 64 << 10` at line 41):

```
{"v":1,"generation":<uint>,"entries":[{"session_id":"opencode:ses_...","archived_at":"<RFC3339>","principal":"<closed shape>"}],"sum":"<hex sha256>"}
```

- `session_id`: the existing global form, validated with `opencode.ValidID` after the prefix; no duplicates.
- `archived_at`: the Bridge's own clock (the same rule as the archive timestamp in ADR-022 7.1).
- `principal`: the closed shape of the audit `principal` attribute (a label for the operator's eyes; it
  authorizes nothing).
- `generation`: incremented by every committed change; `sum`: SHA-256 over the canonical bytes of `v`,
  `generation` and `entries`. The checksum detects **corruption and partial edits**, not a deliberate edit
  by the service user (section 6, T1).
- Bounds: file at most **256 KiB**, at most **1000 entries**. A new archive at the bound is refused with
  `SESSION_LIMIT` (retryable false); **entries are never dropped silently** (the rule of the history store's
  per-session cap, ADR-021 section 5 "Bounds"). Un-archive frees an entry.
- Only sessions that S6 may archive are ever written: a session under a lifecycle root with the `archive`
  mode (D16 Q6). An entry for a session that later leaves the ACL, or whose native session is deleted, is
  **kept and ignored** by every consumer (they only ask about sessions they already list); it is not an error.

### 4.3 Writing: atomic, single-writer, no native write

Only the process that holds the writer role writes (ADR-010; `BRIDGE_READ_ONLY` processes never do). Because
the ADR-010 ownership locks are **per session** (`ownership_linux.go:37-43`, one file per session ID), two
writer processes with disjoint ACLs could both run; the overlay file is global, so it has its **own**
exclusive lock: `overlay.lock` in the overlay directory, `flock(LOCK_EX|LOCK_NB)`, taken at startup, never
unlinked (the ADR-010 rule "never unlink a lock file", `ownership_linux.go:15`). A second process that cannot
take it starts **without** the archive tools (it may read; section 4.4).

A change (add or remove one entry) is:

1. take the session's lifecycle **reservation** in the manager (the `busy[session]` mechanism of ADR-022
   section 5.1, `internal/execution/manager.go:272-278`; **never `x.op`, never an execution ID**), re-check
   the section 7.3 preconditions, then:
2. build the new snapshot in memory (generation + 1, new `sum`);
3. write it to `overlay.json.tmp-<random>` (`O_CREATE|O_EXCL|O_NOFOLLOW`, mode `0600`), `fsync` the file,
   `rename` over `overlay.json`, `fsync` the directory;
4. only after step 3 returned, replace the in-memory snapshot (an atomic pointer) and release the reservation.

Any failure up to the rename leaves the old file and the old memory state: the call is refused with
`UPSTREAM_FAILURE` and the closed reason `overlay_unavailable`, nothing changed. A crash between the rename
and the acknowledgement leaves the **new** state on disk: the caller finds out through `list_catalog`
(`include_archived`) or `get_session`; repeating the call is **idempotent** (archiving an archived session
is a no-op that returns the existing entry). There is no native write to make uncertain, so the project rule
"no automatic replay of uncertain native mutations" is not engaged, but the Bridge still never retries by
itself: the caller decides.

### 4.4 Reading and restart

- **At start** the file is read once into an immutable snapshot. `Archived(ctx, id)` answers **from the
  snapshot, with no file I/O at request time**. The catalog and dashboard code is unchanged (they already
  consume `ArchiveOverlay`).
- **Absent file** (a clean first start, with the directory valid): the empty overlay.
- **Present but invalid** for any reason below: the overlay state is **unknown**. `Archived` returns an error,
  so `list_catalog`, the dashboard list and detail **fail** (catalog `UPSTREAM_TIMEOUT`-class refusal,
  dashboard 503), exactly as ADR-023 slice 5b already specifies; the archive tools refuse. What the writer
  does at start is question 3.
  Invalid means: wrong owner or mode, a symlink, not a regular single-link file, over 256 KiB, not strict
  JSON, an unknown key, a shape violation, a duplicate ID, more than 1000 entries, a checksum mismatch, an
  unsupported `v`.
- **Restart** therefore restores the labels (the archive survives) and **nothing else**: no grant, no
  execution, no guard, no idempotency record, no key, and no replay. The overlay's content is never
  consulted by an authorization decision (section 5).
- **Other processes.** A read-only ingress process that shares the native server (ADR-010 "read-only ingress")
  takes its own snapshot at start and re-reads the file when its `(inode, size, mtime)` changed, at most once
  a second (the file is replaced atomically, so a reader sees a complete old or a complete new file). Whether
  it must do so at all is question 8.

## 5. The overlay is never an authority

The invariants, each of which becomes a test in S6 (section 8):

1. **No import.** The packages that decide authorization (`internal/security`, the lifecycle authorizer
   `internal/lifecycle` `Authorize`/`CheckRoot`, the ACL code in `internal/mcp` `authorized`) do not import the
   overlay package and do not call `ArchiveOverlay`; a dependency test pins it (the pattern of
   `internal/admin/deps_test.go`).
2. **Behavioural.** With any overlay content (including entries for sessions that are not in the ACL, entries
   for every ACL session, an empty file, a missing file, a corrupt file) the set of sessions each tool may
   read or write is the same as with `EmptyArchiveOverlay`, **except** for the narrowings of section 4.5.
3. **Narrowing only.** The only effects of an entry are: the catalog and dashboard hide the session (shown with
   `include_archived` / `archived=1`, section 4.5); `archive_session`/`close_session` treat it as already
   archived; and, **if the maintainer confirms question 1**, `send_prompt` to it is refused. Each is a reduction
   of what a caller can do or see; none adds a reachable session.
4. **No journal source.** The overlay is never rebuilt, repaired or extended from audit lines. The audit lines of
   archive and close (section 4.6) are written **in addition to** the file, never instead of it.
5. **No new grant path.** An entry has no field that any code reads as a permission, and `principal` is a label.

### 4.5 How the filters and un-archive use it

- **`include_archived` (D16 Q5):** unchanged from S3 (`catalog.go:69,95-97`): archived sessions are listed only
  when it is true, with `archived: true`. **Dashboard `archived=1` (D17 Q7):** unchanged from slice 5b. Both read
  the same `ArchiveOverlay`; there is no second reader.
- **Existing discovery tools** (`list_sessions`, `find_sessions`, `get_session`, ...): unchanged by the overlay
  (question 2).
- **`send_prompt` to an archived session:** ADR-022 7.2 (B) says the Bridge "refuses prompts to it". Because the
  archive tools hold the session reservation while they write and `send_prompt` takes the same reservation
  (`manager.go:272-278`), a prompt either was admitted first (and then precondition 2 of section 7.3 refuses
  the archive) or arrives after and sees the committed entry in the snapshot. The refusal is `FORBIDDEN` with the
  closed reason `session_archived` and costs no I/O. It changes an existing tool's behaviour, so it is
  **question 1**, not decided here.
- **Un-archive:** removes the entry through the same 4.3 procedure (generation + 1), so a session returns to the
  default listing; it restores nothing but visibility. Who may call it is question 4.

### 4.6 Audit (ADR-022 4.3 "Tested in S6", 7.3)

One AUDIT-01 line per call on every return path for `archive_session`, `close_session` and (if offered)
`unarchive_session`: `event=session_archive` / `session_close` / `session_unarchive`, `tool`, `outcome`,
`accepted`, `error_code`, `session_id` (well formed only), `root_id`, plus two closed counters
`overlay_generation` and `overlay_entries` after the call, and `archived_in_window`. No title, no path, no
free text, never the file contents. The line is written by the same deferred step as the `session_create` line
(ADR-022 "As built (slice S5)"). The `principal` stored in the file is the same value as in the line.

## 6. Threat model

| # | Threat | Control | Residual |
|---|---|---|---|
| T1 | The file is edited by the service user (or root) | not an authority (section 5): the worst result is a different set of **hidden** sessions or, if question 1 is yes, refused prompts to sessions that were listed; the checksum and strict decoding make an accidental or partial edit an **unknown** state (fail closed) | a deliberate, internally consistent edit by the service user is not detectable (the checksum has no secret). Same-UID/root is outside the boundary, as in ADR-020 and ADR-022 section 8 |
| T2 | Another user or an agent user edits the file | `0700` directory and `0600` file owned by the service user, no symlink, single link, open-by-fd checks at start; agent users cannot read or write it | operator duty: the parent chain is not writable by agent users (as ADR-021 section 5) |
| T3 | **Rollback**: an old snapshot is put back (backup restore, filesystem snapshot) | only visibility changes: sessions archived since are listed again, sessions un-archived since are hidden again; nothing is authorized or lost; `generation` is in the file but there is no external monotonic counter | accepted residual (question 7); the S6 audit lines let an operator see that `overlay_generation` went down |
| T4 | Two Bridge processes write | `overlay.lock` (`flock`, non-blocking, never unlinked) taken at startup; a second process runs without the archive tools; each write also re-reads the generation under the lock and refuses on a mismatch | the lock is advisory like ADR-010's (an unrelated process ignoring flock is the same-UID case) |
| T5 | A crash during a write | tmp file + `fsync` + `rename` + directory `fsync`: the file is the complete old or the complete new snapshot; a leftover `overlay.json.tmp-*` is ignored and removed at the next start by the lock holder | a filesystem that ignores `fsync` loses the **last** change at power loss (visibility only) |
| T6 | Corrupt or oversize file | unknown state: catalog and dashboard lists fail (503), archive tools refuse; the writer start policy is question 3 | an operator must move the file aside to recover (no admin command, question 9) |
| T7 | The native session is deleted, or leaves the ACL or the lifecycle root | the entry is kept and ignored; no error | at most 1000 stale entries; un-archive removes one |
| T8 | A prompt-injected Chat archives many sessions to hide them | the S6 controls of ADR-022 section 8: one session per call, `archive_per_hour` (default 10), `archive` mode per root, preconditions 7.3, an audit line per call, `BRIDGE_READ_ONLY` refuses; the overlay adds the 1000-entry bound | hiding is reversible (un-archive) and is not deletion |
| T9 | Archive races a prompt, a guard release or reconcile | the lifecycle reservation of section 4.3 (never `x.op`, never an execution ID, so ADR-020 release cannot match it) and the pre-write re-check | a native or human writer can still race (ADR-022 5.1 residual) |
| T10 | The overlay is read as proof of "this session may be touched" | forbidden by section 5 and pinned by the dependency test | none if the test holds |

## 7. Consequences

- A configured deployment gains one small piece of persistent state with its own directory, lock and
  recovery rule; an unconfigured one is unchanged (no tools, `EmptyArchiveOverlay`).
- The archive stays **visibility and refusal only**; D16 Q1's "no native write for Chat archive" holds by
  construction, and the S6 spy test is unchanged.
- ADR-010's memory-only rule is kept for everything it names; the overlay is a **label store**, not a record of
  executions, keys or grants.
- S6 can be written against the existing `ArchiveOverlay` read interface plus one new write interface in the
  manager's package; no change to S3, to the dashboard or to the audit middleware.

## 8. What S6 must satisfy (tests and mutations)

Tests (RED before GREEN; each failure mode must be reachable through the real call path):

1. **Atomic write, fault-injected at each step** (open tmp, write, `fsync`, rename, directory `fsync`): after a
   simulated crash at any step the loaded state is exactly the old or exactly the new snapshot, never a
   mixture; a leftover tmp file is ignored.
2. **Corruption table**: each invalid-file reason of section 4.4 gives the unknown state, a failing catalog
   and dashboard list, a refusing archive tool, and **never** the empty overlay.
3. **Directory and file rules**: wrong owner, mode, symlink in the final component, hard link, non-regular file,
   a file replaced between check and open (the fd checks of `history_linux.go`) all refuse.
4. **Never an authority**: the dependency test of section 5.1; a behavioural table of section 5.2 over
   overlays that contain non-ACL ids, all ACL ids, nothing, garbage.
5. **No native write**: the spy native of the S6 row records no request of any method for `archive_session`,
   `close_session` (archive step) and un-archive.
6. **Reservation**: a concurrent `send_prompt` gets `SESSION_BUSY` while the change is in flight and, after it,
   is refused only if question 1 is yes; `get_session` shows `submitting` with `guard_reason: archive`; the
   reservation is released on every exit path including a panic; ADR-020 release and reconcile never act on it.
7. **Idempotence and bounds**: archiving twice is a no-op; at 1000 entries a new archive is `SESSION_LIMIT`
   and nothing is dropped; un-archive frees one.
8. **Two processes**: the second `flock` fails; that process has no archive tools; its read path works; a stale
   generation at write time is refused.
9. **Restart**: write, reopen, the same labels; no grant, execution, key or guard appears; `include_archived`
   and `archived=1` show the same sessions before and after.
10. **Audit**: one line per call on every return path, with the counters and nothing from the file.
11. **`close_session`**: interrupt first, archive only after the interrupt settled, a failed or uncertain
    interrupt leaves the overlay unchanged (the S6 row of ADR-022 section 10).

Mutations (each alone must go RED): write the file in place (no rename); skip the `fsync` calls (a fault-injection
hook must observe their absence); accept a symlinked directory or file; skip the `flock`; ignore the checksum; treat
an invalid file as the empty overlay; update the in-memory snapshot **before** the rename; let an authorization
function call `Archived`; take `x.op` instead of the lifecycle reservation; store the reservation as an execution ID
that ADR-020 release could match; drop the pre-write re-check; issue any native write from either tool; archive after
a failed interrupt in `close_session`; drop an entry silently at the bound; rebuild the overlay from an audit line.

## 9. Open questions for the maintainer

Each has a **recommended** answer. None is decided by this ADR; none is a D-item change unless noted.

1. **Does an overlay-archived session refuse `send_prompt`?** ADR-022 7.2 (B) says the Bridge "refuses prompts to
   it", and D16 Q1 chose (B), but the refusal changes an existing tool's behaviour. *Recommended: yes, `FORBIDDEN`
   with reason `session_archived`, answered from the in-memory snapshot (no I/O), so a corrupt file after startup
   cannot affect prompts.*
2. **Do `list_sessions`, `find_sessions`, `get_session` and `get_repository` show or hide archived sessions?**
   D16 Q5 only names the catalog. *Recommended: no change: the existing tools stay byte-identical; only the catalog
   and the dashboard hide.*
3. **What does the writer do at start if the configured overlay file is invalid?** *Recommended: refuse to start the
   writer role with a clear message (the precedent of an invalid lifecycle configuration, ADR-022 section 3.1);
   read-only processes start and report the unknown state.* The alternative is to start and refuse only the
   archive tools and the lists.
4. **Is un-archive a Chat tool, an operator action, or both?** ADR-022 7.1 says `unarchive_session` is "not offered"
   because the native API has none; with (B) it is possible. *Recommended: offer `unarchive_session` to Chat in S6
   with the same cap, preconditions and audit as archive (it only restores visibility of an ACL-authorized
   session); an operator command on the ADR-020 admin socket would be an ADR-020 amendment and is not proposed.*
5. **If `BRIDGE_OVERLAY_DIR` is unset, is memory-only an acceptable mode?** *Recommended: no: unset means the
   archive tools are not registered (default off), so a restart can never silently un-archive.*
6. **The name and the directory.** *Recommended: `BRIDGE_OVERLAY_DIR` (a proposal), separate from the lock and
   history directories.*
7. **Rollback detection.** *Recommended: accept the residual (T3); an external monotonic counter or a signed
   file would need a secret store this Bridge does not have, and the effect is visibility only.*
8. **Do read-only processes reload the file while running?** *Recommended: yes, stat-poll at most once a second;
   the alternative is "restart to see changes".*
9. **Operator recovery from a corrupt file.** *Recommended: file-level only (move the file aside, restart); no admin
   command (it would be an ADR-020 amendment).*
10. **The bounds** (1000 entries, 256 KiB) and the stale-entry policy (keep and ignore). *Recommended as written.*
11. **Provenance of an entry.** *Recommended: `principal` and `archived_at` only; no operator/chat distinction until
    an operator archive path exists.*
12. **Acceptance.** D12 does not name ADR-025. *Recommended: the maintainer accepts or extends D12; until then S6
    is not started.*

## 10. Limitations

- Design only: no code, no test, no live run. The file rules are copied from existing, tested checks (cited), but
  their reuse for a mutable file (`rename`, `flock`) is a design claim.
- `fsync` semantics depend on the filesystem; the design states what survives a power loss and what does not
  (T5) but nothing here was measured.
- How S6's reservation kind is stored in the manager (ADR-022 5.1) is that ADR's design; this ADR only requires
  that it is neither `x.op` nor an execution ID.
- The variable name, the file name and the numbers are proposals.
