package opencode

import (
	"context"
	"time"
)

type PermissionObservationError string

func (e PermissionObservationError) Error() string { return string(e) }

const (
	ErrPermissionInput       PermissionObservationError = "permission observation: invalid input"
	ErrPermissionDenied      PermissionObservationError = "permission observation: unauthorized or changed scope"
	ErrPermissionUnavailable PermissionObservationError = "permission observation: unavailable"
	ErrPermissionMalformed   PermissionObservationError = "permission observation: malformed native evidence"
	ErrPermissionLimit       PermissionObservationError = "permission observation: bound exceeded"
	ErrPermissionCanceled    PermissionObservationError = "permission observation: canceled or deadline exceeded"
	ErrPermissionGap         PermissionObservationError = "permission observation: event gap; reconcile snapshot"
	ErrPermissionStale       PermissionObservationError = "permission observation: snapshot raced newer evidence"
	ErrPermissionIncarnation PermissionObservationError = "permission observation: native instance invalidated"
)

// PermissionScope is derived from the existing server authority, never from the
// native payload. EndpointID is Client.InstanceID (not process identity).
// NativeEpoch is EMPTY until independent native incarnation evidence exists.
type PermissionScope struct {
	SessionID, Principal, Directory, EndpointID string
	NativeVersion, NativeEpoch                  string
	RootRef, WorkspaceRef                       string
	PolicyRevision                              uint64
}

// KnownPermissionExecution is trusted Bridge RAM evidence. This baseline cannot
// supply this index: the future owner must expose a read-only submitted-run view.
// Native metadata or a single active-looking execution cannot create these facts.
type KnownPermissionExecution struct {
	ExecutionID, UserMessageID string
	Scope                      PermissionScope
	SubmissionAttempted        bool
}

type PermissionObservationAuthority interface {
	PermissionScope(context.Context, string) (PermissionScope, error)
	PermissionExecutions(context.Context, PermissionScope) ([]KnownPermissionExecution, error)
}

type PermissionObservationConfig struct {
	Timeout, StreamWindow time.Duration // default5s; max5s; whole call incl queue/reads
	MaxRecords            int           // default256,max4096; tombstones retained
	MaxEvents             int           // default256,max4096 per stream call
}

type ObservedPermission struct {
	RequestID, SessionID, ToolMessageID, ToolCallID string
	Kind, Tool, ScopeDigest                         string // safe categories/hash only
	PatternCount                                    int
	State                                           string // pending/resolved_externally/observation_uncertain/observation_unavailable
	Attribution                                     string // verified/unattributed/conflicting
	ExecutionID, UserMessageID                      string
	Reason                                          string // closed diagnostic code, never native text
	ObservedReply                                   string // native once/always/reject; never Bridge human intent
}

type PermissionObservationBatch struct {
	Status       string // available/observation_uncertain/observation_unavailable
	Completeness string // always point_in_time_only; never event history completeness
	Permissions  []ObservedPermission
}
