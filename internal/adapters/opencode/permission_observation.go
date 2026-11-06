package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// These private facts never establish caller authority. Scope is supplied by the
// existing trusted owner; native message/part facts only corroborate its run index.
type permissionObservationIdentity struct {
	scope                                                   PermissionScope
	requestID, sessionID, digest, toolMessageID, toolCallID string
	userMessageID, executionID, toolPartID                  string
	toolHash                                                [32]byte
}

func (b permissionObservationIdentity) contradicts(next permissionObservationIdentity) bool {
	return b.scope != next.scope || b.requestID != next.requestID || b.sessionID != next.sessionID || b.digest != next.digest ||
		b.toolMessageID != next.toolMessageID || b.toolCallID != next.toolCallID ||
		next.userMessageID != "" && b.userMessageID != next.userMessageID ||
		next.executionID != "" && b.executionID != next.executionID ||
		next.toolPartID != "" && b.toolPartID != next.toolPartID ||
		next.toolHash != [32]byte{} && b.toolHash != next.toolHash
}

type permissionObservationEntry struct {
	view                  ObservedPermission
	identity              permissionObservationIdentity
	binding               *permissionObservationIdentity // first verified binding; never overwritten
	conflictingIdentity   *permissionObservationIdentity // first contradiction; bounded, never overwritten
	conflict, disappeared bool
}

func (e *permissionObservationEntry) uncertain(reason string) {
	e.view.State, e.view.Attribution, e.view.Reason = "observation_uncertain", "unattributed", reason
	e.view.ExecutionID, e.view.UserMessageID = "", ""
}

func (e permissionObservationEntry) bindingConflict(next permissionObservationEntry) bool {
	return e.binding != nil && (next.view.Attribution == "conflicting" || e.binding.contradicts(next.identity))
}

// PermissionObserver has no reply API and no background worker. The caller owns
// bounded Snapshot/ReadEvents calls. It does not mutate execution or Core state.
type PermissionObserver struct {
	mu         sync.Mutex
	client     *Client
	authority  PermissionObservationAuthority
	scope      PermissionScope
	config     PermissionObservationConfig
	entries    map[string]permissionObservationEntry
	seen       map[string][32]byte
	revision   uint64
	generation uint64
	status     string
	fenced     bool
	streaming  bool
	snapshot   chan struct{}
}

func NewPermissionObserver(ctx context.Context, client *Client, session string, authority PermissionObservationAuthority, cfg PermissionObservationConfig) (*PermissionObserver, error) {
	if client == nil || client.base == nil || client.http == nil || authority == nil || !ValidID(session, "ses") {
		return nil, ErrPermissionInput
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.StreamWindow == 0 {
		cfg.StreamWindow = 5 * time.Second
	}
	if cfg.MaxRecords == 0 {
		cfg.MaxRecords = 256
	}
	if cfg.MaxEvents == 0 {
		cfg.MaxEvents = 256
	}
	if cfg.Timeout < time.Millisecond || cfg.Timeout > 5*time.Second || cfg.StreamWindow < time.Millisecond || cfg.StreamWindow > 5*time.Second || cfg.MaxRecords < 1 || cfg.MaxRecords > 4096 || cfg.MaxEvents < 1 || cfg.MaxEvents > 4096 {
		return nil, ErrPermissionInput
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	s, err := authority.PermissionScope(ctx, session)
	if ctx.Err() != nil || err != nil || s.SessionID != session || s.EndpointID != client.InstanceID() || s.NativeVersion != "1.19.4" || !opaquePermissionID(s.Principal) || len(s.Directory) > 4096 || !filepath.IsAbs(s.Directory) || filepath.Clean(s.Directory) != s.Directory || client.directory != "" && client.directory != s.Directory || s.PolicyRevision == 0 {
		return nil, ErrPermissionDenied
	}
	if s.NativeEpoch != "" && !opaquePermissionID(s.NativeEpoch) || s.RootRef != "" && !opaquePermissionID(s.RootRef) || s.WorkspaceRef != "" && !opaquePermissionID(s.WorkspaceRef) {
		return nil, ErrPermissionDenied
	}
	c := client.WithDirectory(s.Directory)
	h := *client.http
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &h
	return &PermissionObserver{client: c, authority: authority, scope: s, config: cfg, entries: make(map[string]permissionObservationEntry), seen: make(map[string][32]byte), status: "observation_unavailable", snapshot: make(chan struct{}, 1)}, nil
}

func (o *PermissionObserver) authorized(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrPermissionCanceled
	}
	s, err := o.authority.PermissionScope(ctx, o.scope.SessionID)
	if ctx.Err() != nil {
		return ErrPermissionCanceled
	}
	if err != nil || s != o.scope {
		return ErrPermissionDenied
	}
	return nil
}

func (o *PermissionObserver) verifySession(ctx context.Context) error {
	var s Session
	if err := o.readJSON(ctx, "/session/"+o.scope.SessionID, permissionPayloadLimit, &s); err != nil {
		return err
	}
	if s.ID != o.scope.SessionID || s.Directory != o.scope.Directory {
		return ErrPermissionDenied
	}
	return nil
}

// get is a closed GET-only transport: no arbitrary path, method or body input is
// exposed. Redirects are disabled. It preserves the trusted directory route.
func (o *PermissionObserver) get(ctx context.Context, path string, stream bool) (*http.Response, error) {
	base := "/session/" + o.scope.SessionID
	allowed := path == "/permission" || path == base || stream && path == "/event"
	if strings.HasPrefix(path, base+"/message/") {
		allowed = ValidID(strings.TrimPrefix(path, base+"/message/"), "msg")
	}
	if !allowed || stream && path != "/event" {
		return nil, ErrPermissionInput
	}
	u := *o.client.base
	u.Path = path
	q := u.Query()
	q.Set("directory", o.scope.Directory)
	u.RawQuery = q.Encode()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrPermissionInput
	}
	if o.client.password != "" {
		r.SetBasicAuth(o.client.username, o.client.password)
	}
	res, err := o.client.http.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrPermissionCanceled
		}
		return nil, ErrPermissionUnavailable
	}
	content, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	want := "application/json"
	if stream {
		want = "text/event-stream"
	}
	if res.StatusCode != http.StatusOK || content != want {
		res.Body.Close()
		return nil, ErrPermissionUnavailable
	}
	return res, nil
}
func (o *PermissionObserver) readJSON(ctx context.Context, path string, limit int, dst any) error {
	res, err := o.get(ctx, path, false)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	if err != nil {
		return ErrPermissionUnavailable
	}
	if err = strictNativeJSON(raw, limit); err != nil {
		return err
	}
	switch dst.(type) {
	case *Message:
		if err = validatePermissionMessage(raw); err != nil {
			return err
		}
	case *Session:
		if _, err = permissionProjection(raw, []string{"id", "directory"}, []string{"id", "directory"}); err != nil {
			return err
		}
	}
	if json.Unmarshal(raw, dst) != nil {
		return ErrPermissionMalformed
	}
	return nil
}

func (o *PermissionObserver) markLocked(status, reason string) {
	o.status = status
	for id, e := range o.entries {
		if e.view.State != "resolved_externally" {
			e.view.State, e.view.Reason = status, reason
			o.entries[id] = e
		}
	}
	o.revision++
}
func (o *PermissionObserver) fail(err error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	status := "observation_uncertain"
	if err == ErrPermissionUnavailable || err == ErrPermissionDenied {
		status = "observation_unavailable"
	}
	o.markLocked(status, "read_incomplete")
	return err
}
func (o *PermissionObserver) viewLocked() PermissionObservationBatch {
	list := make([]ObservedPermission, 0, len(o.entries))
	for _, e := range o.entries {
		list = append(list, e.view)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].RequestID < list[j].RequestID })
	return PermissionObservationBatch{Status: o.status, Completeness: "point_in_time_only", Permissions: list}
}
func (o *PermissionObserver) View(ctx context.Context) (PermissionObservationBatch, error) {
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	defer cancel()
	if err := o.authorized(ctx); err != nil {
		return PermissionObservationBatch{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.viewLocked(), nil
}

// VerifiedRejection reports, from in-memory state only (no I/O, no authority
// call), whether exactly one retained request for this tool call was asked
// with a verified binding to executionID and then resolved by an observed
// native "reject" reply, while the observer is unfenced and its latest status
// is "available" (a successful snapshot after any stream gap). Every other
// case, including a gap, conflict, disappearance, unattributed reply or a
// second matching request, is false. It is ADR-018 §3(b) item 4 for the
// report-only permission_rejection block and never proof of an outcome.
func (o *PermissionObserver) VerifiedRejection(session, executionID, toolMessageID, toolCallID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fenced || o.streaming || o.status != "available" || session != o.scope.SessionID || executionID == "" || toolMessageID == "" || toolCallID == "" {
		return false
	}
	match := false
	for _, e := range o.entries {
		if e.view.ToolMessageID != toolMessageID || e.view.ToolCallID != toolCallID {
			continue
		}
		if match || e.conflict || e.disappeared || e.conflictingIdentity != nil || e.binding == nil ||
			e.binding.executionID != executionID || e.view.ExecutionID != executionID || e.view.Attribution != "verified" ||
			e.view.State != "resolved_externally" || e.view.ObservedReply != "reject" {
			return false
		}
		match = true
	}
	return match
}

func (o *PermissionObserver) executions(ctx context.Context) ([]KnownPermissionExecution, error) {
	known, err := o.authority.PermissionExecutions(ctx, o.scope)
	if err != nil {
		return nil, ErrPermissionUnavailable
	}
	if len(known) > 256 {
		return nil, ErrPermissionLimit
	}
	return known, nil
}

func (o *PermissionObserver) correlate(ctx context.Context, r nativePermissionRequest, digest string, known []KnownPermissionExecution) (candidate permissionObservationEntry) {
	candidate.view = ObservedPermission{RequestID: r.ID, SessionID: r.SessionID, Kind: safePermissionKind(r.Permission), PatternCount: len(r.Patterns), ScopeDigest: digest, State: "pending", Attribution: "unattributed", Reason: "correlation_missing"}
	candidate.identity = permissionObservationIdentity{scope: o.scope, requestID: r.ID, sessionID: r.SessionID, digest: digest}
	v, identity := &candidate.view, &candidate.identity
	if r.Tool == nil {
		return candidate
	}
	v.ToolMessageID, v.ToolCallID = r.Tool.MessageID, r.Tool.CallID
	identity.toolMessageID, identity.toolCallID = r.Tool.MessageID, r.Tool.CallID
	if o.scope.NativeEpoch == "" || o.scope.RootRef == "" || o.scope.WorkspaceRef == "" {
		v.Reason = "identity_unverified"
		return candidate
	}
	var assistant Message
	if o.readJSON(ctx, "/session/"+r.SessionID+"/message/"+r.Tool.MessageID, permissionPayloadLimit, &assistant) != nil {
		v.Reason = "message_evidence_unavailable"
		return candidate
	}
	conflict := func() permissionObservationEntry {
		v.Attribution, v.Reason = "conflicting", "native_binding_conflict"
		return candidate
	}
	if assistant.Info.ID != r.Tool.MessageID || assistant.Info.SessionID != r.SessionID || assistant.Info.Role != "assistant" || !ValidID(assistant.Info.ParentID, "msg") || assistant.Info.Path.Cwd != o.scope.Directory || len(assistant.Parts) > 256 {
		return conflict()
	}
	// Preserve contradictory parent evidence even if its user GET later fails.
	identity.userMessageID = assistant.Info.ParentID
	matches := 0
	for _, raw := range assistant.Parts {
		var p struct {
			ID, SessionID, MessageID, Type, CallID, Tool string
			State                                        struct{ Status string }
		}
		if json.Unmarshal(raw, &p) != nil {
			return conflict()
		}
		if p.Type != "tool" || p.CallID != r.Tool.CallID {
			continue
		}
		if !ValidID(p.ID, "prt") || p.SessionID != r.SessionID || p.MessageID != r.Tool.MessageID || p.Tool == "" || (p.State.Status != "pending" && p.State.Status != "running") {
			return conflict()
		}
		matches++
		v.Tool = safePermissionKind(p.Tool)
		identity.toolPartID, identity.toolHash = p.ID, sha256.Sum256([]byte(p.Tool))
	}
	if matches != 1 {
		return conflict()
	}
	var user Message
	if o.readJSON(ctx, "/session/"+r.SessionID+"/message/"+assistant.Info.ParentID, permissionPayloadLimit, &user) != nil {
		v.Reason = "message_evidence_unavailable"
		return candidate
	}
	if user.Info.ID != assistant.Info.ParentID || user.Info.SessionID != r.SessionID || user.Info.Role != "user" {
		return conflict()
	}
	var execution string
	for _, e := range known {
		if e.UserMessageID != user.Info.ID {
			continue
		}
		if e.Scope != o.scope || !e.SubmissionAttempted || !opaquePermissionID(e.ExecutionID) || execution != "" {
			return conflict()
		}
		execution = e.ExecutionID
	}
	if execution != "" {
		v.ExecutionID, v.UserMessageID, v.Attribution, v.Reason = execution, user.Info.ID, "verified", "exact_native_parent_and_tool"
		identity.executionID = execution
	}
	return candidate
}

// Snapshot reconciles an entire bounded native pending list. Absence NEVER means
// resolved. A stream event arriving during IO invalidates this snapshot's CAS.
func (o *PermissionObserver) Snapshot(ctx context.Context) (PermissionObservationBatch, error) {
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	defer cancel()
	if err := o.authorized(ctx); err != nil {
		return PermissionObservationBatch{}, o.fail(err)
	}
	select {
	case o.snapshot <- struct{}{}:
		defer func() { <-o.snapshot }()
	case <-ctx.Done():
		return PermissionObservationBatch{}, ErrPermissionCanceled
	}
	o.mu.Lock()
	start, generation, fenced := o.revision, o.generation, o.fenced
	o.mu.Unlock()
	if fenced {
		return PermissionObservationBatch{}, ErrPermissionIncarnation
	}
	var session Session
	if err := o.readJSON(ctx, "/session/"+o.scope.SessionID, permissionPayloadLimit, &session); err != nil {
		return PermissionObservationBatch{}, o.fail(err)
	}
	if session.ID != o.scope.SessionID || session.Directory != o.scope.Directory {
		return PermissionObservationBatch{}, o.fail(ErrPermissionDenied)
	}
	known, err := o.executions(ctx)
	if err != nil {
		return PermissionObservationBatch{}, o.fail(err)
	}
	var raw []json.RawMessage
	if err = o.readJSON(ctx, "/permission", permissionSnapshotLimit, &raw); err != nil || raw == nil {
		if err == nil {
			err = ErrPermissionMalformed
		}
		return PermissionObservationBatch{}, o.fail(err)
	}
	if len(raw) > 4096 {
		return PermissionObservationBatch{}, o.fail(ErrPermissionLimit)
	}
	candidate := make(map[string]permissionObservationEntry)
	for _, item := range raw {
		if _, err = permissionProjection(item, []string{"sessionID"}, []string{"sessionID"}); err != nil {
			return PermissionObservationBatch{}, o.fail(err)
		}
		var route struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(item, &route) != nil || !ValidID(route.SessionID, "ses") {
			return PermissionObservationBatch{}, o.fail(ErrPermissionMalformed)
		}
		if route.SessionID != o.scope.SessionID {
			continue // no other session's metadata/correlation read/retention
		}
		r, digest, e := decodePermissionRequest(item)
		if e != nil {
			return PermissionObservationBatch{}, o.fail(e)
		}
		if _, duplicate := candidate[r.ID]; duplicate {
			return PermissionObservationBatch{}, o.fail(ErrPermissionMalformed)
		}
		candidate[r.ID] = o.correlate(ctx, r, digest, known)
		if len(candidate) > o.config.MaxRecords {
			return PermissionObservationBatch{}, o.fail(ErrPermissionLimit)
		}
	}
	// Native reads are not atomic; detect an observed session/workspace change
	// across the permission/message reads before exposing any association.
	var after Session
	if err = o.readJSON(ctx, "/session/"+o.scope.SessionID, permissionPayloadLimit, &after); err != nil {
		return PermissionObservationBatch{}, o.fail(err)
	}
	if after.ID != session.ID || after.Directory != session.Directory {
		return PermissionObservationBatch{}, o.fail(ErrPermissionDenied)
	}
	if err = o.authorized(ctx); err != nil {
		return PermissionObservationBatch{}, o.fail(err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if start != o.revision || generation != o.generation || o.fenced {
		return PermissionObservationBatch{}, ErrPermissionStale
	}
	newCount := len(o.entries)
	for id := range candidate {
		if _, exists := o.entries[id]; !exists {
			newCount++
		}
	}
	if newCount > o.config.MaxRecords {
		o.markLocked("observation_uncertain", "retained_limit")
		return PermissionObservationBatch{}, ErrPermissionLimit
	}
	for id, e := range o.entries {
		if _, exists := candidate[id]; !exists && e.view.State != "resolved_externally" {
			// Only this fully validated, successful snapshot can record absence.
			// No native request generation exists that could safely undo it.
			e.disappeared = true
			e.uncertain("missing_from_snapshot")
			o.entries[id] = e
		}
	}
	for id, v := range candidate {
		o.mergeLocked(id, v, true)
	}
	o.status = "available"
	for _, e := range o.entries {
		if e.view.State == "observation_uncertain" || e.view.Attribution == "conflicting" || e.view.Reason == "message_evidence_unavailable" {
			o.status = "observation_uncertain"
		}
	}
	o.revision++
	return o.viewLocked(), nil
}

func (o *PermissionObserver) mergeLocked(id string, candidate permissionObservationEntry, snapshot bool) {
	v := candidate.view
	e, exists := o.entries[id]
	if exists && (e.conflict || e.view.State == "resolved_externally" || e.view.ScopeDigest != "" && e.view.ScopeDigest != v.ScopeDigest || e.bindingConflict(candidate)) {
		e.conflict = true
		if e.conflictingIdentity == nil {
			identity := candidate.identity
			e.conflictingIdentity = &identity
		}
		e.uncertain("request_identity_conflict")
		o.status = "observation_uncertain"
		o.entries[id] = e
		return
	}
	if e.disappeared {
		e.uncertain("request_generation_unverified")
		o.status = "observation_uncertain"
		o.entries[id] = e
		return
	}
	if exists && !snapshot && e.view.State != "pending" {
		return // an old/reordered asked event cannot repair a gap/missing snapshot
	}
	if e.binding == nil && v.Attribution == "verified" {
		identity := candidate.identity
		e.binding = &identity
	}
	e.view, e.identity = v, candidate.identity
	o.entries[id] = e
}

func (o *PermissionObserver) processEvent(ctx context.Context, raw []byte, generation uint64) error {
	if err := strictNativeJSON(raw, permissionPayloadLimit); err != nil {
		return o.fail(err)
	}
	var event nativePermissionEvent
	if _, err := permissionProjection(raw, []string{"type", "properties"}, []string{"id", "type", "properties"}); err != nil {
		return o.fail(err)
	}
	if json.Unmarshal(raw, &event) != nil || event.Type == "" || len(event.Type) > 128 || event.ID != "" && !opaquePermissionID(event.ID) {
		return o.fail(ErrPermissionMalformed)
	}
	if err := o.authorized(ctx); err != nil {
		return o.fail(err)
	}
	if event.Type == "server.instance.disposed" {
		o.mu.Lock()
		o.fenced = true
		o.markLocked("observation_unavailable", "instance_disposed")
		o.mu.Unlock()
		return ErrPermissionIncarnation
	}
	if event.Type != "permission.asked" && event.Type != "permission.replied" {
		if strings.HasPrefix(event.Type, "permission.") {
			return o.fail(ErrPermissionMalformed)
		}
		return nil // heartbeat/connected/other bounded events do not prove freshness
	}
	var route struct {
		SessionID string `json:"sessionID"`
	}
	if _, err := permissionProjection(event.Properties, []string{"sessionID"}, []string{"sessionID"}); err != nil {
		return o.fail(err)
	}
	if json.Unmarshal(event.Properties, &route) != nil || !ValidID(route.SessionID, "ses") {
		return o.fail(ErrPermissionMalformed)
	}
	if route.SessionID != o.scope.SessionID {
		return nil
	}
	if err := o.verifySession(ctx); err != nil {
		return o.fail(err)
	}
	var candidate permissionObservationEntry
	if event.Type == "permission.asked" {
		r, digest, err := decodePermissionRequest(event.Properties)
		if err != nil {
			return o.fail(err)
		}
		known, err := o.executions(ctx)
		if err != nil {
			return o.fail(err)
		}
		candidate = o.correlate(ctx, r, digest, known)
	} else {
		var r nativePermissionReply
		d := json.NewDecoder(bytes.NewReader(event.Properties))
		d.DisallowUnknownFields()
		var fields map[string]json.RawMessage
		if json.Unmarshal(event.Properties, &fields) != nil || len(fields) != 3 || fields["sessionID"] == nil || fields["requestID"] == nil || fields["reply"] == nil || d.Decode(&r) != nil || !ValidID(r.RequestID, "per") || r.Reply != "once" && r.Reply != "always" && r.Reply != "reject" {
			return o.fail(ErrPermissionMalformed)
		}
		candidate.view = ObservedPermission{RequestID: r.RequestID, SessionID: r.SessionID, State: "resolved_externally", Attribution: "unattributed", ObservedReply: r.Reply, Reason: "native_reply_observed"}
	}
	if err := o.verifySession(ctx); err != nil {
		return o.fail(err)
	}
	if err := o.authorized(ctx); err != nil {
		return o.fail(err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if generation != o.generation || o.fenced {
		return ErrPermissionStale
	}
	v := candidate.view
	if event.ID != "" {
		digest := sha256.Sum256(raw)
		if previous, exists := o.seen[event.ID]; exists {
			// Duplicate event data cannot hide new contradictory message/run
			// evidence read while processing that event. Otherwise stay idempotent.
			if event.Type == "permission.asked" && o.entries[v.RequestID].bindingConflict(candidate) {
				o.mergeLocked(v.RequestID, candidate, false)
				o.revision++
			}
			if previous != digest {
				o.markLocked("observation_uncertain", "event_identity_conflict")
				return ErrPermissionMalformed
			}
			return nil
		}
		if len(o.seen) >= 4096 {
			return ErrPermissionLimit
		}
		o.seen[event.ID] = digest
	}
	if _, exists := o.entries[v.RequestID]; !exists && len(o.entries) >= o.config.MaxRecords {
		return ErrPermissionLimit
	}
	if event.Type == "permission.asked" {
		o.mergeLocked(v.RequestID, candidate, false)
	} else {
		e := o.entries[v.RequestID]
		if e.view.RequestID == "" {
			e.view = v
		} else if e.conflict || e.view.ObservedReply != "" && e.view.ObservedReply != v.ObservedReply {
			e.conflict = true
			e.uncertain("conflicting_resolution")
			o.status = "observation_uncertain"
		} else if e.disappeared {
			// A replied event identifies no request generation. Retain the native
			// enum as an observation, never as resolution of the vanished identity.
			e.view.ObservedReply = v.ObservedReply
			e.uncertain("request_generation_unverified")
			o.status = "observation_uncertain"
		} else {
			e.view.State, e.view.ObservedReply, e.view.Reason = v.State, v.ObservedReply, v.Reason
		}
		o.entries[v.RequestID] = e
	}
	o.revision++
	return nil
}

// ReadEvents performs ONE bounded GET /event. No replay cursor is fabricated:
// the pinned server sends SSE id:undefined. Every disconnect requires Snapshot;
// no automatic retry loop or independent supervisor is installed.
func (o *PermissionObserver) ReadEvents(ctx context.Context) (result error) {
	ctx, cancel := context.WithTimeout(ctx, o.config.StreamWindow)
	defer cancel()
	if err := o.authorized(ctx); err != nil {
		return o.fail(err)
	}
	o.mu.Lock()
	if o.fenced || o.streaming {
		o.mu.Unlock()
		return ErrPermissionUnavailable
	}
	o.streaming = true
	o.generation++
	generation := o.generation
	o.markLocked("observation_uncertain", "stream_reconnected")
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.streaming = false
		status := "observation_uncertain"
		if o.fenced || o.status == "observation_unavailable" {
			status = "observation_unavailable"
		}
		o.markLocked(status, "stream_gap")
		o.mu.Unlock()
	}()
	res, err := o.get(ctx, "/event", true)
	if err != nil {
		return o.fail(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(io.LimitReader(res.Body, permissionSnapshotLimit+1))
	scanner.Buffer(make([]byte, 4096), permissionPayloadLimit)
	var data []byte
	total, count := 0, 0
	for scanner.Scan() {
		line := scanner.Bytes()
		total += len(line) + 1
		if total > permissionSnapshotLimit {
			return o.fail(ErrPermissionLimit)
		}
		if len(line) == 0 {
			if len(data) != 0 {
				if count >= o.config.MaxEvents {
					return ErrPermissionLimit
				}
				count++
				if err = o.processEvent(ctx, bytes.TrimSuffix(data, []byte{'\n'}), generation); err != nil {
					return err
				}
				data = nil
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			part := bytes.TrimPrefix(line, []byte("data:"))
			if len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			if len(data)+len(part)+1 > permissionPayloadLimit {
				return o.fail(ErrPermissionLimit)
			}
			data = append(append(data, part...), '\n')
		}
	}
	if ctx.Err() != nil {
		return ErrPermissionCanceled
	}
	if scanner.Err() != nil {
		return o.fail(ErrPermissionLimit)
	}
	if len(data) != 0 {
		return o.fail(ErrPermissionMalformed) // partial event cannot be published
	}
	return ErrPermissionGap
}
