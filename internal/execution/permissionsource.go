package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-024 slice S6: the production PermissionSource for OpenCode.
//
// It builds, for every call, a FRESH opencode.PermissionObserver for the session and takes one
// bounded Snapshot of it. There is no cache: the view that the reply is checked against (4.2 step 5)
// and the view behind pending_permission (section 3) are each a new native read, inside the observer's
// own 5 s bound, routed through the session's own directory. All attribution is the observer's: this
// file adds only the trusted authority the observer asks for, derived from the manager's own runs.
//
// What the authority supplies, and what it does not:
//   - Scope: the session, the directory the run was accepted in, the endpoint, the native version,
//     the principal and the policy revision come from the manager's run that HOLDS the session guard.
//     No value comes from a native payload or from model text.
//   - Executions: the manager's submitted runs of that session (execution ID, the Bridge-chosen user
//     message ID, submission attempted). Nothing else can create an execution fact.
//   - NativeEpoch: independent native incarnation evidence. The Bridge has no such source yet
//     (docs/P2_PERM_CORE_02_OBSERVATION.md: "No native incarnation is fabricated"). With no source the
//     epoch stays empty, which the observer reads as "unverified": every request stays "unattributed",
//     pending_permission stays count-only and reply_permission refuses with FORBIDDEN not_verified.
//     That is the fail-closed default of a production Bridge today.

// NativeIncarnation is the one place independent native incarnation evidence can enter. A nil
// NativeIncarnation (the production default) means no evidence exists and the epoch stays empty.
// An implementation must return a value that changes when the native process that holds the
// pending requests is replaced; a value derived from anything the native server or the model can
// write (event IDs, message text) is forbidden.
type NativeIncarnation interface {
	NativeEpoch(ctx context.Context) (string, error)
}

// PermissionSourceConfig is the whole configuration of the production source. The zero value is the
// production default: principal "bridge", policy revision 1, no incarnation source, the observer's
// default bounds (5 s, 256 records).
type PermissionSourceConfig struct {
	Principal      string
	PolicyRevision uint64
	Incarnation    NativeIncarnation
	Observation    opencode.PermissionObservationConfig
}

const (
	// permissionNativeVersion is the only native version the observer accepts (ADR-024 section 2).
	permissionNativeVersion    = "1.19.4"
	defaultPermissionPrincipal = "bridge"
)

var principalPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// OpenCodePermissionSource implements PermissionSource against one OpenCode endpoint.
type OpenCodePermissionSource struct {
	client *opencode.Client
	m      *Manager
	cfg    PermissionSourceConfig
}

// NewOpenCodePermissionSource returns the production source. client must be the endpoint's
// client (its own directory routing is replaced by the session's directory per call).
func NewOpenCodePermissionSource(client *opencode.Client, m *Manager, cfg PermissionSourceConfig) (*OpenCodePermissionSource, error) {
	if client == nil || m == nil {
		return nil, errors.New("permission source needs a client and a manager")
	}
	if cfg.Principal == "" {
		cfg.Principal = defaultPermissionPrincipal
	}
	if cfg.PolicyRevision == 0 {
		cfg.PolicyRevision = 1
	}
	if !principalPattern.MatchString(cfg.Principal) {
		return nil, errors.New("permission source principal must be 1-64 characters of A-Za-z0-9_.:-")
	}
	return &OpenCodePermissionSource{client: client, m: m, cfg: cfg}, nil
}

// PermissionView implements PermissionSource: one fresh observer, one bounded Snapshot.
func (s *OpenCodePermissionSource) PermissionView(ctx context.Context, session string) (opencode.PermissionObservationBatch, error) {
	if !opencode.ValidID(session, "ses") {
		return opencode.PermissionObservationBatch{}, opencode.ErrPermissionInput
	}
	o, err := opencode.NewPermissionObserver(ctx, s.client, session, runAuthority{s}, s.cfg.Observation)
	if err != nil {
		return opencode.PermissionObservationBatch{}, err
	}
	return o.Snapshot(ctx)
}

// runAuthority is the observer's PermissionObservationAuthority: a read-only projection of the
// manager's own runs. It never writes manager state and holds no run mutex across a call.
type runAuthority struct{ s *OpenCodePermissionSource }

var errNoGuardRun = errors.New("no execution holds this session's guard")

// PermissionScope returns the scope of the run that holds the session's guard. A session with no
// such run (nothing is waiting, the run is terminal, the guard is a lifecycle reservation) has no
// scope: the observer is denied, so no request is ever observed for a session nobody is running.
func (a runAuthority) PermissionScope(ctx context.Context, session string) (opencode.PermissionScope, error) {
	if err := ctx.Err(); err != nil {
		return opencode.PermissionScope{}, err
	}
	x := a.s.m.guardRun(session)
	if x == nil {
		return opencode.PermissionScope{}, errNoGuardRun
	}
	x.mu.Lock()
	native, directory, canonical := x.native, x.directory, x.canonicalDirectory
	x.mu.Unlock()
	if native != session || directory == "" || directory != canonical {
		return opencode.PermissionScope{}, errNoGuardRun
	}
	epoch := ""
	if inc := a.s.cfg.Incarnation; inc != nil {
		var err error
		if epoch, err = inc.NativeEpoch(ctx); err != nil {
			return opencode.PermissionScope{}, err
		}
	}
	ref := directoryRef(canonical)
	return opencode.PermissionScope{
		SessionID:      session,
		Principal:      a.s.cfg.Principal,
		Directory:      directory,
		EndpointID:     a.s.client.InstanceID(),
		NativeVersion:  permissionNativeVersion,
		NativeEpoch:    epoch,
		RootRef:        "root-" + ref,
		WorkspaceRef:   "workspace-" + ref,
		PolicyRevision: a.s.cfg.PolicyRevision,
	}, nil
}

// PermissionExecutions returns the manager's submitted runs of the scope's session, in the
// scope's own directory. Each carries the Bridge-chosen user message ID, so a native request
// is attributed to an execution only through the message the Bridge itself sent.
func (a runAuthority) PermissionExecutions(ctx context.Context, scope opencode.PermissionScope) ([]opencode.KnownPermissionExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []opencode.KnownPermissionExecution
	for _, x := range a.s.m.sessionRuns(scope.SessionID) {
		x.mu.Lock()
		e := opencode.KnownPermissionExecution{ExecutionID: x.view.ID, UserMessageID: x.view.MessageID, Scope: scope, SubmissionAttempted: x.submissionAttempted}
		same := x.directory == scope.Directory
		x.mu.Unlock()
		if same && e.SubmissionAttempted {
			out = append(out, e)
		}
	}
	return out, nil
}

// directoryRef is an opaque, stable reference to a canonical directory (never the path itself).
func directoryRef(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:16])
}

// guardRun returns the run that holds the session's guard, or nil. It never holds the manager and a
// run mutex together.
func (m *Manager) guardRun(session string) *run {
	m.mu.Lock()
	x := m.runs[m.busy[session]]
	m.mu.Unlock()
	return x
}

// sessionRuns returns the manager's runs of one native session (a snapshot of the pointers).
func (m *Manager) sessionRuns(session string) []*run {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*run
	for _, x := range m.runs {
		if x.native == session {
			out = append(out, x)
		}
	}
	return out
}
