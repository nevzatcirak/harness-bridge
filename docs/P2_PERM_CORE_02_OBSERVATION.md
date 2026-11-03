# P2-PERM-CORE-02: read-only OpenCode permission observation

## Baseline and scope

This increment is isolated at `db9da6b` on `feat/p2-perm-core-01` in
`/tmp/hb-p2-perm-core-01`. The approved five `internal/permissioncore` files are
available as uncommitted baseline files and remain unchanged. The current Core
Runtime integration worktree's later uncommitted Journal, Mutation Gate and
execution APIs are **not** present in this baseline. Roadmap v1.3 was consulted
from that authoritative worktree, not copied or modified.

`PermissionObserver` adds caller-driven bounded `Snapshot`, `ReadEvents` and
`View` operations to the existing OpenCode adapter. It adds no service, goroutine
supervisor, execution authority, persistent database, permission decision API or
native reply implementation. It does not wire the Execution Manager or MCP.
Direct Claude Chat human approval remains NOT_VERIFIED and disabled.

## Verified native contract

The source baseline is OpenCode 1.19.4, commit
`148842ef7cb1bc34192b3fff57d0f9721ab9c801`. Public documentation at
<https://opencode.ai/docs/server/> is supplementary and not version-pinned.
Version-specific definitions take precedence:

- `packages/schema/src/v1/permission.ts`: `Request` has required `id`,
  `sessionID`, `permission`, `patterns`, `metadata`, `always`; optional `tool`
  has required `messageID` and `callID`. Events are `permission.asked` and
  `permission.replied`; reply evidence has `sessionID`, `requestID`, `reply`
  (`once`, `always`, `reject`). This evidence does not identify a trusted human.
- Instance permission routes implement `GET /permission`, an array of current
  pending requests. It is a snapshot, not permission history. Global/native data
  is filtered to the authorized session before metadata is retained or exposed.
- Session routes implement `GET /session/{sessionID}` and
  `GET /session/{sessionID}/message/{messageID}`. The latter returns `info` and
  `parts`; the legacy message shape is in `packages/schema/src/v1/session.ts`.
  Assistant `parentID` links the original user message; a tool part supplies its
  own `sessionID`, `messageID`, `callID`, `id`, `tool` and `state.status`.
- `GET /event` is SSE. The instance event handler registers before emitting
  `server.connected`, and emits JSON event `id`, `type`, `properties`.
  `server.heartbeat` is not evidence of permission completeness.
  SSE uses no replay ID (`id: undefined`); JSON IDs are not replay cursors or
  chronological sequence numbers. No verified Last-Event-ID replay exists here.
  `server.instance.disposed` invalidates this observer.
- The pinned permission service's `reject` also rejects other pending requests
  in the same session. A local denial cannot be represented as an exact native
  rejection. This observer never sends either reply action.

Pinned source bytes, Git blob verification and route/service extracts are
recorded outside the repository in the increment evidence directory. The JSON
permission test fixture is synthetic, source-derived and safely redacted; it is
not a capture of a real user's pending permission.

## Trusted scope and positive execution attribution

`PermissionObservationAuthority` is a **read-only projection of the existing
server authority**, not a second policy implementation. Its scope must come
from authenticated Bridge state: exact session, principal, authorized directory,
endpoint, version, policy revision, and (when established) native incarnation,
project-root and workspace identities. Never populate these values from model
text or native tool metadata. `EndpointID` identifies the client endpoint, not
an OpenCode process incarnation. Empty `NativeEpoch` means unverified.

A pending request can be observed without attribution. A verified attribution
requires all of:

1. Trusted scope contains independently established incarnation, root and
   workspace references. Authorization and native session/directory are checked
   before and after observation I/O, and authorization again before publication.
2. Exact permission `tool.messageID` retrieves an assistant message with the
   same native session, exact message ID and expected directory.
3. Exactly one pending/running tool part matches the exact call ID, session,
   message and valid part identity. The assistant's exact `parentID` retrieves a
   user message with matching session and identity.
4. Exactly one trusted, submitted Bridge execution matches that user message
   and the entire authorized scope. Conflicting or duplicate evidence fails
   attribution. Native claims cannot establish principal or execution identity.

Matching paths, session alone, timing, arguments, prompt text or one active run
are insufficient. The baseline manager lacks the submitted-run index and
native-incarnation source needed to implement this projection. Until supplied,
production attribution must remain `unattributed`; tests use explicit fake
trusted state and do not claim manager integration.

## State and concurrency contract

`State` and `Attribution` are separate. A native request can be `pending` and
`unattributed` or `conflicting`. `resolved_externally` requires an explicit
native replied event; it does not assert human intent, task completion, local
Permission Core decision or a specific actor. `observation_uncertain` records
missing/conflicting evidence; transport failure or invalid incarnation can
produce `observation_unavailable`. All views are `point_in_time_only`.

Snapshots are serialized and committed with a revision/generation check under
the observer mutex. A newer event invalidates a snapshot racing it. Streams are
single-reader, synchronous and bounded; disconnect requires caller-driven
snapshot reconciliation, not automatic retries. Lost events cannot be excluded
and reconnect is not replay. Snapshot absence never means approval/rejection.
Terminal tombstones are retained. Reappearing/rebound IDs and conflicting
replies become uncertain and clear execution attribution; they never rearm a
permission. Out-of-order created-after-replied evidence remains uncertain.

A changed trusted scope denies access to old views. Disposed native instances
require new trusted evidence and a fresh observer; restart restores no authority.
No native incarnation is fabricated from event IDs. No local deny is converted
into a native rejection. No output object is a grant or consumable permission.

## Read-only transport, privacy and resource bounds

The private observation transport has a closed GET-only whitelist:
`/permission`, the fixed authorized `/session/{id}`, that session's exact
`/message/{id}`, and `/event` for streaming. It uses the existing client endpoint
and authentication, fixed directory query and a cloned HTTP client rejecting
redirects. It does not change process cwd or the native session. Existing adapter
mutation APIs are untouched and never called by this component.

Default whole-call deadlines are 5s, maximum 5s. Snapshot body is at most 1 MiB,
individual request/message/event at most 64 KiB, JSON nesting at most 32, native
snapshot at most 4096 entries, message at most 256 parts and trusted execution
projection at most 256 entries. Patterns/always arrays each have at most 64
strings of at most 4096 bytes. Retained records (including tombstones) default to
256, configurable up to 4096; per-stream event count has the same defaults and
maximum. Seen event identities cap at 4096. SSE total input caps at 1 MiB. At
capacity the observer fails closed without evicting identity tombstones.

Duplicate JSON keys (including metadata), invalid UTF-8, malformed IDs, incorrect
case aliases and invalid native shapes fail with typed observation errors.
Metadata stays opaque and untrusted; only a deterministic digest, counts,
whitelisted tool/permission categories and validated opaque IDs are retained.
Raw patterns, commands, prompt fragments, native errors, credentials and tool
arguments are not exposed. No native data establishes a principal. The component
adds no logging. Metadata-only hashes are observational, not authorization or
Journal digest domains.

## Validation and integration limits

HTTP/SSE tests use `httptest`, source-derived payloads, negative independent
mutations, GET-only request assertions, and explicit barriers for stale snapshots,
stream gaps and 2/32 contenders. No real approval is involved. The optional
`TestPermissionObservationNativeReadOnlySmoke` starts and reaps only its own
1.19.4 process with disposable config/data/home/managed directories and no
inherited credentials. It checks an empty pending GET and real SSE connected
frame only. A real pending permission, positive real-execution attribution and
real TUI resolution E2E remain **NOT_VERIFIED**. The ordinary suite skips that
opt-in smoke; a separate opt-in run records its actual result.

Integration prerequisites are an authenticated read-only scope projection from
the existing authority, an exact submitted-user-message/execution index, verified
incarnation handling, and caller-owned bounded observation/reconciliation.
There is no permission audit integration, trusted human decision channel, native
reply dispatch, durable observation history, guaranteed event completeness or
automatic execution recovery. A same-UID process, Unix socket or model-generated
approval text is not human authorization.
