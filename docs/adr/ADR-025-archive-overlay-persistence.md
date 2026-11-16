# ADR-025: Archive overlay persistence (design for ADR-022 slice S8)

Date: 2026-11-15. Status: **ACCEPTED (D18, 2026-11-16)**. Written for planner task #296 (ADR-022
section 10, slice S8: "mandatory, **before S6**"); implemented by slice S6 (task #303).

**Acceptance.** Accepted by maintainer decision **D18 (2026-11-16)** with every recommendation of section 9
(recorded there as "Decided (D18)"). Nothing here changes a D-item, ADR-010, ADR-012, ADR-020, ADR-021,
ADR-022, ADR-023 or ADR-024; the point that touches an existing tool (Q1) is decided by D18.

**Revision (2026-11-15, task #301).** Revised after the reviews #298 (B, six P3) and #299 (A, one P2 and eight
P3): the overlay now has an overlay-wide write mutex (section 4.3), an explicit uncertain-write procedure,
the exact error codes of the unknown state, the placement of the `send_prompt` check, a fixed checksum
serialization, reconciled bounds and fd-relative file operations; every review finding is either fixed in the
text or recorded as a residual or an open question (table in the task report).

**Revision (2026-11-16, task #303).** Accepted (D18); section 9 is decided; the five P3s of review #302 are
folded in as text (section 4.3 abandon flag and late worker, section 4.3/T4/test 8 generation rule, test 12
delays, T6/T7/T10 wording); section 11 records what S6 built.

**Evidence base.** Every claim about the code is a citation to `main` at `e09ddd6` (checked on
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

- `session_id`: the existing global form `opencode:<id>`, validated with `opencode.ValidID` after the prefix; no
  duplicates. **A `claudecode:` ID is never an entry** (it is a shape violation, so the file is invalid), and
  `Archived` is never asked for one: the dashboard's Claude rows never consult the overlay
  (`internal/mcp/dashsource.go:48-51` keeps `claudecode:` IDs out of the OpenCode path and the pending rows are
  built without it), so ADR-021 sessions cannot be hidden or refused by the overlay.
- `archived_at`: the Bridge's own clock (the same rule as the archive timestamp in ADR-022 7.1).
- `principal`: the closed shape of the audit `principal` attribute (a label for the operator's eyes; it
  authorizes nothing).
- `generation`: incremented by every committed change. `sum`: hex SHA-256 over the **canonical bytes**, defined
  as the compact JSON (no insignificant whitespace, no HTML escaping) of the object `{"v":...,"generation":...,
  "entries":[...]}` encoded by the Bridge's own fixed struct (fields in that order, `entries` sorted by
  `session_id` ascending, `sum` excluded). The verifier decodes the file strictly, **re-encodes** the decoded
  values with the same struct and compares the digest with `sum`; it does not hash the raw bytes of the file, so
  whitespace or key order in an edited file cannot make a good file "corrupt", while any changed value does.
  The checksum detects **corruption and partial edits**, not a deliberate edit by the service user (section 6, T1).
- Bounds: at most **1000 entries** and a file of at most **512 KiB**. The two are reconciled on purpose: one
  entry is at most about 420 bytes in the worst valid shape (`session_id` up to 265 bytes, `principal` 64, the
  timestamp 20 and the JSON framing), so 1000 entries are at most about 420 KB and the **entry count binds first**;
  the file bound is the decoder's defence against a file that is not ours. A new archive that would exceed
  **either** bound is refused with `SESSION_LIMIT` (retryable false) and the test asserts both;
  **entries are never dropped silently** (the rule of the history store's per-session cap, ADR-021 section 5
  "Bounds"). Un-archive frees an entry.
- Only sessions that S6 may archive are ever written: a session under a lifecycle root with the `archive`
  mode (D16 Q6). An entry for a session that later leaves the ACL, or whose native session is deleted, is
  **kept and ignored** by every consumer (they only ask about sessions they already list); it is not an error.
  **Accepted residual:** such a stale entry cannot be un-archived through the Bridge, because un-archive is
  authorized like archive (ACL and mode) and refuses a session that is outside them; only the operator's file
  edit removes it (question 9). At the `archive_per_hour` cap of 10 an injected Chat needs about 100 hours to
  fill 1000 entries with stale ones, and hiding is not deletion; question 10 records the alternative (a
  removal-only prune at start).

### 4.3 Writing: serialized, atomic, single-writer, no native write

Only the process that holds the writer role writes (ADR-010; `BRIDGE_READ_ONLY` processes never do). Because
the ADR-010 ownership locks are **per session** (`ownership_linux.go:37-43`, one file per session ID), two
writer processes with disjoint ACLs could both run; the overlay file is global, so it has its **own**
exclusive lock: `overlay.lock` in the overlay directory, `flock(LOCK_EX|LOCK_NB)`, taken at startup, never
unlinked (the ADR-010 rule "never unlink a lock file", `ownership_linux.go:15`). A second process that cannot
take it starts **without** the archive tools (it may read; section 4.4).

**The `flock` orders processes, not goroutines**: it is taken once at startup and held for the process
lifetime, so it does not order two calls inside the writer process. Two archives of **different** sessions hold
different session reservations and would otherwise both build "current + my entry" from the same snapshot, and
the later rename would silently drop the earlier entry although its call returned success (review #299 P2-1).
Therefore every change goes through **one overlay-wide in-process mutex**, `overlayMu`, held across *read the
current snapshot, build, write, rename, fsync, publish*. The snapshot a change is built from is read **after**
the mutex is taken. Readers never take it (they load the atomic snapshot pointer).

A change (add or remove one entry) is, in this order:

1. take the session's lifecycle **reservation** in the manager (the `busy[session]` mechanism of ADR-022
   section 5.1, `internal/execution/manager.go:272-278`; **never `x.op`, never an execution ID**) and re-check
   the section 7.3 preconditions;
2. take `overlayMu` (the lock order is **reservation, then `overlayMu`**; `overlayMu` is never held while waiting
   for a manager lock, a reservation or a native call, so no cycle exists), read the current snapshot, and build
   the new one (generation + 1, new `sum`; for an idempotent no-op return the existing entry without writing);
3. write it to `overlay.json.tmp-<16 hex>` (`openat` with `O_CREAT|O_EXCL|O_NOFOLLOW`, mode `0600`), `fsync` the
   file, `renameat` over `overlay.json`, `fsync` the directory **fd**. All of these are **fd-relative**
   (`openat`, `renameat`, `unlinkat` on the validated directory descriptor, like the segment operations of
   `history_linux.go`); Go's `os.Rename` is path-based and is not used;
4. only after step 3 returned success, publish the new snapshot (an atomic pointer), release `overlayMu`, then
   release the reservation.

**Failure handling and what is released when.** The whole of steps 2 to 4 runs under **one deadline of 5 s**
(a proposal, the bound of the other native steps; question 13 asks for the error code).

- *Failure up to and including the file `fsync`, or the deadline before the rename was issued*: the old file and
  the old memory state remain; the tmp file is removed with `unlinkat`; `overlayMu` and the reservation are
  released; the call is refused with `UPSTREAM_FAILURE` and the closed reason `overlay_unavailable`.
  **One atomic outcome state per change** (review #302 P3-1): `pending`, `abandoned`, `committing`, `committed`,
  `uncertain-returned`. The caller moves `pending` to `abandoned` at the deadline **only if the worker has not
  already moved `pending` to `committing`**; the worker moves `pending` to `committing` **immediately before the
  rename** and, if that move fails (the caller abandoned the change), removes its tmp file and issues no rename.
  So a call that was answered "nothing changed" can never commit later. At the deadline with the worker in
  `committing` the caller moves it to `uncertain-returned` and answers the uncertain result below.
- *The rename returned but the directory `fsync` failed, or the deadline expired while the rename or the
  directory `fsync` was in flight (the outcome on disk is **uncertain**)*: the call returns the typed
  `UNKNOWN_OUTCOME` (question 13), the in-memory snapshot is **not** replaced, and the overlay is marked
  **uncertain**. The **next** change, which is serialized behind `overlayMu`, first **re-reads the file** under
  the mutex and the `flock`: if its `generation` equals the one that was being written it is **adopted** as the
  snapshot, if it equals the previous one the change simply proceeds, and in any other case (unreadable, invalid,
  another generation) the overlay becomes **unknown** until restart (archive tools refuse, lists fail, section
  4.4). While it is uncertain, readers keep the last good snapshot.
- *A hung write* (an `fsync` or a rename that does not return): the worker that runs steps 3 and 4 keeps
  `overlayMu` until it returns, so **no second write can race the hung one**; later changes wait on their own 5 s
  deadline and are refused with `UPSTREAM_TIMEOUT` (retryable). The calling goroutine does not wait for the
  worker past the deadline: it returns the uncertain result above and **releases the session reservation**,
  which guards the native-state preconditions, not the file. **A worker whose caller already returned the uncertain
  answer does not publish** (review #302 P3-2): the entry it committed stays **unpublished** until the next change
  adopts it (or a restart loads it), as the paragraph above says and as test 13 asserts; readers keep the last good
  snapshot, so `Archived` and the section 4.5 check do not see it before then. **Accepted residual:** a prompt
  admitted in that window reaches a session that is archived a moment later; the effect is a narrowing and one
  prompt the caller was already authorized to send.

A crash between the rename and the acknowledgement leaves the **new** state on disk: the caller finds out
through `list_catalog` (`include_archived`) or the dashboard (`get_session` is unchanged and does not report
overlay state, question 2); repeating the call is **idempotent** (archiving an archived session is a no-op that
returns the existing entry). There is no native write to make uncertain, so the project rule "no automatic
replay of uncertain native mutations" is not engaged, but the Bridge still never retries by itself: the caller
decides. Leftover `overlay.json.tmp-*` files are removed at start by the lock holder with `unlinkat`, by exact
name pattern only.

### 4.4 Reading and restart

- **At start** the file is read once into an immutable snapshot. `Archived(ctx, id)` answers **from the
  snapshot, with no file I/O at request time**. The catalog and dashboard code is unchanged (they already
  consume `ArchiveOverlay`).
- **Absent file** (a clean first start, with the directory valid): the empty overlay.
- **Present but invalid** for any reason below: the overlay state is **unknown**. `Archived` returns an error,
  so `list_catalog` and the dashboard list and detail **fail**, exactly as they already do for any overlay
  error (ADR-023 slice 5b): the catalog returns **`INTERNAL`** ("archive overlay unavailable; the catalog is not
  returned without it", `internal/mcp/catalog.go:225-230`), and `UPSTREAM_TIMEOUT` (`catalogUnavailable()`,
  `catalog.go:180,228`) **only** when the read itself timed out; the dashboard answers 503. The catalog asks the
  overlay only for rows that passed its earlier filters (`catalog.go:206-224`), so a call whose filters leave no
  row does not fail. The archive tools refuse with `UPSTREAM_FAILURE` and the reason `overlay_unavailable`.
  What the writer does at start is question 3.
  Invalid means: wrong owner or mode, a symlink, not a regular single-link file, over 512 KiB, not strict
  JSON, an unknown key, a shape violation, a duplicate ID, more than 1000 entries, a checksum mismatch, an
  unsupported `v`.
- **Restart** therefore restores the labels (the archive survives) and **nothing else**: no grant, no
  execution, no guard, no idempotency record, no key, and no replay. The overlay's content is never
  consulted by an authorization decision (section 5).
- **Other processes.** A read-only ingress process that shares the native server (ADR-010 "read-only ingress")
  takes its own snapshot at start and re-reads the file when its `(inode, size, mtime)` changed, at most once
  a second (the file is replaced atomically, so a reader sees a complete old or a complete new file). Whether
  it must do so at all is question 8. **An unknown state never blocks unrelated prompts:** it keeps the last
  good snapshot for the refusals of section 4.5 (none exists at startup, which is why question 3 recommends the
  writer refuses to start), and a failed reload keeps the last good snapshot while the lists report unknown.

### 4.5 How the filters and un-archive use it

- **`include_archived` (D16 Q5):** unchanged from S3 (`catalog.go:69,95-97`): archived sessions are listed only
  when it is true, with `archived: true`. **Dashboard `archived=1` (D17 Q7):** unchanged from slice 5b. Both read
  the same `ArchiveOverlay`; there is no second reader.
- **Existing discovery tools** (`list_sessions`, `find_sessions`, `get_session`, ...): unchanged by the overlay
  (question 2); `get_session` does not report overlay state.
- **`send_prompt` to an archived session:** ADR-022 7.2 (B) says the Bridge "refuses prompts to it". Because the
  archive tools hold the session reservation while they write and `send_prompt` takes the same reservation
  (`manager.go:272-278`), a prompt either was admitted first (and then precondition 2 of section 7.3 refuses
  the archive) or arrives after and sees the committed entry in the snapshot. The refusal is `FORBIDDEN` with the
  closed reason `session_archived` and costs no I/O. It changes an existing tool's behaviour, so it is
  **question 1**, not decided here. **Where the check lives (if question 1 is yes):** inside
  `Manager.SendWithRunnerOutcome`, in the **same `m.mu` critical section** as the `busy[session]` check and set
  (`manager.go:272-278`), **after** the idempotent-replay branch (`manager.go:238-258`), which returns an
  earlier execution and submits nothing, and after the pending-key branch (`manager.go:259`). Reading the
  snapshot outside that section would let a prompt read "not archived", an archive reserve, write, publish and
  release, and the prompt then take the reservation and be admitted; inside it the two orders are the only two
  that exist. Test 6 pins the interleaving.
- **Un-archive:** removes the entry through the same 4.3 procedure (generation + 1), so a session returns to the
  default listing; it restores nothing but visibility. Who may call it is question 4.

### 4.6 Audit (ADR-022 4.3 "Tested in S6", 7.3)

One AUDIT-01 line per call on every return path for `archive_session`, `close_session` and (if offered)
`unarchive_session`: `event=session_archive` / `session_close` / `session_unarchive`, `tool`, `outcome`,
`accepted`, `error_code`, `session_id` (well formed only), `root_id`, plus two closed counters
`overlay_generation` and `overlay_entries` after the call, and `archived_in_window`. No title, no path, no
free text, never the file contents. **`close_session` writes exactly one line** (`session_close`, with the closed
field `interrupt` = `interrupted`, `none` or `failed`): its interrupt step calls the manager's interrupt function
below the tool layer, **not** the `interrupt_execution` tool path, so no second `interrupt` AUDIT-01 line is
written for the same call; S6's audit test asserts one line. **The new events and counters are not in the
dashboard ring's closed `auditEvents`/`auditAttrs` allowlists today** (`internal/dashboard/audit.go:32` lists
`guard_release`, `prompt_submission`, `interrupt`, `session_create`), so they are journal-only until that
allowlist is amended, as `permission_reply` is. The line is written by the same deferred step as the `session_create` line
(ADR-022 "As built (slice S5)"). The `principal` stored in the file is the same value as in the line.

## 5. The overlay is never an authority

The invariants, each of which becomes a test in S6 (section 8):

1. **No import, no call.** (a) `internal/security` and `internal/lifecycle` (`Authorize`, `CheckRoot`) do not
   import the overlay package; a dependency test pins it (the pattern of `internal/admin/deps_test.go`).
   (b) The ACL function `authorized` (`internal/mcp/server.go:98`) and `claudeSession` live in `internal/mcp`,
   the **same package** that declares `ArchiveOverlay` (`catalog.go:33`), so an import test cannot see them: an
   **AST/call-graph test over `internal/mcp`** asserts that the named authorization functions neither reference
   `ArchiveOverlay` nor call `Archived`, directly or through a helper they call.
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

## 6. Threat model

| # | Threat | Control | Residual |
|---|---|---|---|
| T1 | The file is edited by the service user (or root) | not an authority (section 5): the worst result is a different set of **hidden** sessions or, if question 1 is yes, refused prompts to sessions that were listed; the checksum and strict decoding make an accidental or partial edit an **unknown** state (fail closed) | a deliberate, internally consistent edit by the service user is not detectable (the checksum has no secret). Same-UID/root is outside the boundary, as in ADR-020 and ADR-022 section 8 |
| T2 | Another user or an agent user edits the file | `0700` directory and `0600` file owned by the service user, no symlink, single link, open-by-fd checks at start; agent users cannot read or write it | operator duty: the parent chain is not writable by agent users (as ADR-021 section 5) |
| T3 | **Rollback**: an old snapshot is put back (backup restore, filesystem snapshot) | only visibility changes: sessions archived since are listed again, sessions un-archived since are hidden again; nothing is authorized or lost; `generation` is in the file but there is no external monotonic counter | accepted residual (question 7); the S6 audit lines let an operator see that `overlay_generation` went down |
| T4 | Two Bridge **processes** write | `overlay.lock` (`flock`, non-blocking, never unlinked) taken at startup; a second process runs without the archive tools and so never writes; the lock holder re-reads the file **only after an uncertain write** (section 4.3), where a generation that is neither the pending nor the previous one latches unknown (review #302 P3-4: one rule, not "every write re-reads") | the lock is advisory like ADR-010's (an unrelated process ignoring flock is the same-UID case) |
| T11 | Two archives of **different sessions inside one process** lose an update (the `flock` does not order goroutines) | `overlayMu` held across build, write, rename, fsync and publish (section 4.3); the build reads the snapshot after taking it | none: test 12 and the mutation "drop the overlay mutex" pin it |
| T12 | A write whose outcome is uncertain (directory `fsync` error, a hung rename) leaves memory and disk different | the uncertain procedure of section 4.3: no second write races it, the next change re-reads and adopts or latches unknown | one prompt may reach a session archived a moment later (narrowing only) |
| T5 | A crash during a write | tmp file + `fsync` + `rename` + directory `fsync`: the file is the complete old or the complete new snapshot; a leftover `overlay.json.tmp-*` is ignored and removed at the next start by the lock holder | a filesystem that ignores `fsync` loses the **last** change at power loss (visibility only) |
| T6 | Corrupt or oversize file | unknown state: the catalog fails with `INTERNAL` and the dashboard answers 503, archive tools refuse; the writer start policy is question 3 | an operator must move the file aside to recover (no admin command, question 9) |
| T7 | The native session is deleted, or leaves the ACL or the lifecycle root | the entry is kept and ignored; no error | at most 1000 stale entries; a stale entry is removable only by the operator's file edit (section 4.2, accepted residual) |
| T8 | A prompt-injected Chat archives many sessions to hide them | the S6 controls of ADR-022 section 8: one session per call, `archive_per_hour` (default 10), `archive` mode per root, preconditions 7.3, an audit line per call, `BRIDGE_READ_ONLY` refuses; the overlay adds the 1000-entry bound | hiding is reversible (un-archive) and is not deletion |
| T9 | Archive races a prompt, a guard release or reconcile | the lifecycle reservation of section 4.3 (never `x.op`, never an execution ID, so ADR-020 release cannot match it) and the pre-write re-check | a native or human writer can still race (ADR-022 5.1 residual) |
| T13 | `create_session` runs while an archive or un-archive is in flight | none needed: `create_session` creates a **new** session ID and takes no archive reservation, so there is no shared reservation with the overlay; its caps count **native** sessions (the overlay is not in the count), so archiving or un-archiving frees or takes no create slot; neither path takes `x.op` (review #298 P3-4) | none |
| T10 | The overlay is read as proof of "this session may be touched" | forbidden by section 5 and pinned by the dependency test (security, lifecycle) and the AST test over `internal/mcp` (5.1(b)) | none if the test holds |

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
2. **Corruption table**: each invalid-file reason of section 4.4 (including a `claudecode:` entry and a `sum`
   that no longer matches the re-encoded values) gives the unknown state, a failing catalog and dashboard list, a
   refusing archive tool, and **never** the empty overlay; the test asserts the **exact** answers: catalog
   `INTERNAL` (`UPSTREAM_TIMEOUT` only for a timed-out read), dashboard 503, archive tools `UPSTREAM_FAILURE`
   with reason `overlay_unavailable`, and that unrelated `send_prompt` calls are unaffected.
3. **Directory and file rules**: wrong owner, mode, symlink in the final component, hard link, non-regular file,
   a file replaced between check and open (the fd checks of `history_linux.go`) all refuse; the tmp file, the
   rename and the leftover removal are `openat`/`renameat`/`unlinkat` on the directory fd (a path-based
   `os.Rename` or `os.Remove` in the writer is a mutation), and leftover removal touches only an exact
   `overlay.json.tmp-<16 hex>` name.
4. **Never an authority**: the dependency test of section 5.1; a behavioural table of section 5.2 over
   overlays that contain non-ACL ids, all ACL ids, nothing, garbage.
5. **No native write**: the spy native of the S6 row records **no native write of any kind (no `POST`, `PATCH`
   or `DELETE`)** for `archive_session`, `close_session` (archive step) and un-archive. The section 7.3
   precondition reads (native status, the permission view, the child tree) are expected and are **not** writes;
   the spy's assertion is write-only, so a correct S6 passes and dropping a precondition read does not.
6. **Reservation**: a concurrent `send_prompt` gets `SESSION_BUSY` while the change is in flight and, after it,
   is refused only if question 1 is yes (the interleaving "prompt reads the snapshot, archive commits, prompt takes
   the reservation" must end in the refusal, which is why the check is inside `m.mu`, section 4.5); `get_session` shows `submitting` with `guard_reason: archive`; the
   reservation is released on every exit path including a panic; ADR-020 release and reconcile never act on it.
7. **Idempotence and bounds**: archiving twice is a no-op; at 1000 entries, **and separately** at the 512 KiB file
   bound, a new archive is `SESSION_LIMIT` and nothing is dropped; un-archive frees one.
8. **Two processes**: the second `flock` fails; that process has no archive tools and cannot write; its read path
   works. (The stale-generation refusal of the previous revision is the uncertain-write rule of 4.3, test 13.)
9. **Restart**: write, reopen, the same labels; no grant, execution, key or guard appears; `include_archived`
   and `archived=1` show the same sessions before and after.
10. **Audit**: one line per call on every return path, with the counters and nothing from the file.
11. **`close_session`**: interrupt first, archive only after the interrupt settled, a failed or uncertain
    interrupt leaves the overlay unchanged (the S6 row of ADR-022 section 10); exactly one `session_close` line
    and no `interrupt` line for the call.
12. **Concurrent changes**: N concurrent archives and un-archives of **distinct** sessions (each under its own
    reservation, with a fault-injection delay between build and rename, and again with a delay after the directory `fsync`
    and before the publish, to widen both windows): **all N are in the
    file and in the published snapshot**, `overlay_entries` and `overlay_generation` are consistent with it, and
    every call that returned success is represented. This is what catches the lost update of section 4.3.
13. **Uncertain and hung writes**: a directory `fsync` error after a successful rename and a hung rename each
    return the uncertain result, leave the memory snapshot unchanged, make the next change re-read the file and
    adopt or latch unknown as section 4.3 says, release the reservation at the deadline, and keep `overlayMu`
    until the worker returns (a second write started meanwhile waits and is refused with `UPSTREAM_TIMEOUT`).
14. **Checksum**: a file whose whitespace or key order was edited but whose values are unchanged verifies; any
    changed value, an extra entry or a different `generation` does not.

Mutations (each alone must go RED): **drop `overlayMu`** (caught by test 12: the N-concurrent-distinct-sessions
test loses an entry once the delay between build and rename is injected); **build the new snapshot before taking
`overlayMu`** (test 12); **release `overlayMu` before the publish** (test 12); **replace the in-memory snapshot on
an uncertain write** (test 13); **let the calling goroutine release `overlayMu` at the deadline while the worker
still runs** (test 13); **use a path-based `os.Rename` or `os.Remove`** (test 3); write the file in place (no rename); skip the `fsync` calls (a fault-injection
hook must observe their absence); accept a symlinked directory or file; skip the `flock`; ignore the checksum; treat
an invalid file as the empty overlay; update the in-memory snapshot **before** the rename; let an authorization
function call `Archived` (caught by the AST test of 5.1(b), not by the import test, because `authorized` shares
the package of `ArchiveOverlay`); take `x.op` instead of the lifecycle reservation; store the reservation as an execution ID
that ADR-020 release could match; drop the pre-write re-check; issue any native write from either tool; archive after
a failed interrupt in `close_session`; drop an entry silently at the bound; rebuild the overlay from an audit line.

## 9. Decided (D18, 2026-11-16)

The maintainer accepted every recommendation:

1. **Q1:** an overlay-archived session refuses `send_prompt`: `FORBIDDEN`, reason `session_archived`, from the
   in-memory snapshot, no I/O.
2. **Q2:** `list_sessions`, `find_sessions`, `get_session`, `get_repository` stay byte-identical; only the catalog
   and the dashboard hide.
3. **Q3:** an invalid overlay file: the writer refuses to start; read-only processes start and report unknown.
4. **Q4:** `unarchive_session` is offered to Chat with the same cap, preconditions and audit.
5. **Q5:** `BRIDGE_OVERLAY_DIR` unset: the archive tools are not registered; no memory-only mode.
6. **Q6:** `BRIDGE_OVERLAY_DIR`, separate from the lock and history directories.
7. **Q7:** rollback residual accepted (T3).
8. **Q8:** read-only processes stat-poll at most once a second.
9. **Q9:** file-level operator recovery only (move the file aside, restart).
10. **Q10:** bounds 1000 entries / 512 KiB; stale entries are kept and ignored.
11. **Q11:** provenance is `principal` and `archived_at` only.
12. **Q12:** acceptance: this decision (D18).
13. **Q13:** an uncertain overlay write is `UNKNOWN_OUTCOME` with the closed reason `overlay_uncertain`.

## 10. Limitations

- Written as a design; implemented by slice S6 (section 11). No live run: every test uses a fake native. The file rules are copied from existing, tested checks (cited), but
  their reuse for a mutable file (`rename`, `flock`) is a design claim.
- `fsync` semantics depend on the filesystem; the design states what survives a power loss and what does not
  (T5) but nothing here was measured.
- How S6's reservation kind is stored in the manager (ADR-022 5.1) is that ADR's design; this ADR only requires
  that it is neither `x.op` nor an execution ID.
- The variable name, the file name and the numbers are proposals.

## 11. As built (slice S6, dated 2026-11-16, task #303)

- **Package.** `internal/overlay` holds the store (`Open`, `Archive`, `Unarchive`, `Archived`, `ArchivedKnown`), every file
  operation fd-relative through an `os.Root` on the validated directory (an AST test forbids the path-based `os`
  functions there). The write worker keeps `overlayMu` until it returns; the caller waits only its own 5 s.
- **Not wired in production.** Nothing constructs `ArchiveConfig` or `overlay.Open` outside tests: the startup wiring from
  `BRIDGE_OVERLAY_DIR` (with `BRIDGE_LIFECYCLE_CONFIG`) is a separate task, so a deployed Bridge is unchanged.
- **Where S6 differs from the text above.** The "uncertain" marker of 4.3 is recorded **before** the rename is issued (a
  rename that hangs is already uncertain). The audit line carries `reason` for the closed overlay reasons.
- **Manager.** `ReserveLifecycle`, `LifecycleGuard` and `SetArchiveCheck` in `internal/execution/lifecycle.go`.
