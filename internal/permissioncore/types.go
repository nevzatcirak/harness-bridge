// Package permissioncore models request-bound permission decisions in RAM.
// It is not connected to MCP, execution, a human approval channel or native IO.
package permissioncore

import (
	"context"
	"time"
)

// Error values contain no request, credential, native output or scope material.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInput      Error = "permission: invalid input"
	ErrAuthority  Error = "permission: authority unavailable or denied"
	ErrHuman      Error = "permission: verified explicit human decision unavailable"
	ErrBinding    Error = "permission: binding mismatch"
	ErrNotFound   Error = "permission: request unavailable"
	ErrConflict   Error = "permission: conflicting decision"
	ErrUsed       Error = "permission: claim or decision already used"
	ErrExpired    Error = "permission: decision expired"
	ErrCanceled   Error = "permission: canceled"
	ErrClosed     Error = "permission: core closed"
	ErrStale      Error = "permission: native identity or scope changed"
	ErrResolved   Error = "permission: native resolution observed"
	ErrUncertain  Error = "permission: outcome uncertain; do not retry"
	ErrDisabled   Error = "permission: native reply disabled"
	ErrUnsafeDeny Error = "permission: reject affects the whole native session; local denial only"
	ErrCapacity   Error = "permission: retained request capacity reached"
)

type Principal string
type Choice string

const (
	AllowOnce Choice = "allow_once"
	Deny      Choice = "deny"
)

type State string

const (
	Pending            State = "pending"
	Decided            State = "decided"
	Reserved           State = "reserved"
	Attempted          State = "attempted"
	ReplyObserved      State = "reply_observed"
	Denied             State = "denied_locally"
	Expired            State = "expired"
	Canceled           State = "canceled"
	ExternallyResolved State = "externally_resolved"
	Stale              State = "stale"
	Uncertain          State = "uncertain"
)

// NativeStatus is an observation, not current activity or execution completion.
type NativeStatus string

const (
	NativeUnknown  NativeStatus = "unknown"
	NativePending  NativeStatus = "pending_observed"
	NativeResolved NativeStatus = "resolution_observed"
)

// Binding is produced by trusted server-side correlation, never tool arguments.
// Opaque identities refer to verified native lineage/workspace/root/policy facts.
// This core checks equality; it cannot independently prove those external facts.
type Binding struct {
	Authority, Lineage, NativeVersion        string
	ProjectRoot, Workspace                   string
	PolicyRevision, Generation               uint64
	RequestID, SessionID, ExecutionID        string
	UserMessageID, ToolMessageID, ToolCallID string
	Principal                                Principal
	Tool, Action                             string
	ResourceCount                            uint32
	ScopeFingerprint                         [32]byte
}

// Request deliberately has no free-text presentation field. ScopeMaterial is
// bounded, exact producer-supplied native scope bytes: hashed then discarded.
// The future adapter must supply a complete, deterministic scope representation.
type Request struct {
	Binding       Binding
	ScopeMaterial []byte
}

// Challenge is correlation, not a bearer capability. Expiry uses process elapsed
// monotonic time. A fresh core generates a fresh epoch and cannot import claims.
type Challenge struct {
	Epoch    string
	ID       uint64
	Binding  Binding
	Deadline time.Duration
}

type Decision struct {
	Challenge Challenge
	Choice    Choice
	ReceiptID string // opaque lookup in the authoritative human channel, not a token
}

type HumanChannel string

const IndependentHumanChannel HumanChannel = "independently_authenticated_human"

// HumanEvidence may ONLY be returned by the trusted existing server authority.
// These fields are not accepted from MCP/model text, peer UID or a socket alone.
// No implementation of that human verification channel is supplied here.
type HumanEvidence struct {
	Decision             Decision
	HumanSubject         string
	Channel              HumanChannel
	ExplicitConfirmation bool
	CompletePresentation bool // human saw the exact scope bound by the fingerprint
}

// Authority is an integration dependency, not a second authorization service.
// Authorize must check authenticated caller, exact session/project/execution and
// current lineage/root/ACL/policy. VerifyHuman must resolve a server-held receipt,
// verify separate human authentication, explicit intent, scope and revocation.
// A Prompt/recovery grant or generic MCP tool consent is never such a receipt.
type Authority interface {
	Authorize(context.Context, Principal, Binding) error
	VerifyHuman(context.Context, string) (HumanEvidence, error)
}

type Config struct {
	DecisionTTL time.Duration // 0 selects 120s; may only shorten that bound
	Capacity    int           // 0 selects 256; max4096 including spent tombstones
}

// View contains safe structured metadata only. It makes no task-result claim.
// NativePending is explicitly the last observation; local denial/expiry does not
// resolve it. There is no raw command/path/metadata, receipt secret or human text.
type View struct {
	Challenge    Challenge
	State        State
	Choice       Choice
	Native       NativeStatus
	Attempted    bool
	Reason       Error
	Presentation string
	RejectScope  string
}

// Claim has no externally constructible authority; copies share one spent bit.
type Claim struct{ data *claim }

// Native IO is an unexported fixture seam. New never installs one. In particular
// there is NO reject method: OpenCode1.19.4 reject fans out to the whole session.
type nativeProvider interface {
	Check(context.Context, Binding) (NativeEvidence, error)
	ReplyOnce(context.Context, Binding) error
}

// NativeEvidence requires exact positive resolution evidence to say Resolved.
// Missing ID, idle, NOT_FOUND or absent list entries must be NativeUnknown.
type NativeEvidence struct {
	Request Request
	Status  NativeStatus
}
