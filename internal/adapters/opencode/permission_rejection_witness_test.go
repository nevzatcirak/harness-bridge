package opencode

import "testing"

// ADR-018 (d): VerifiedRejection is the in-memory item-4 witness. It answers
// true only for one verified, rejected, conflict-free entry while the observer
// is available; every degraded case is false.
func TestVerifiedRejection(t *testing.T) {
	const ses, exec, msg, call = "ses_a", "exec_1", "msg_1", "call_1"
	verified := func() permissionObservationEntry {
		id := permissionObservationIdentity{requestID: "per_1", sessionID: ses, toolMessageID: msg, toolCallID: call, executionID: exec}
		return permissionObservationEntry{
			view: ObservedPermission{RequestID: "per_1", SessionID: ses, ToolMessageID: msg, ToolCallID: call, Tool: "bash",
				State: "resolved_externally", Attribution: "verified", ExecutionID: exec, ObservedReply: "reject", Reason: "native_reply_observed"},
			identity: id,
			binding:  &id,
		}
	}
	observer := func(mutate func(o *PermissionObserver, e *permissionObservationEntry)) *PermissionObserver {
		o := &PermissionObserver{scope: PermissionScope{SessionID: ses}, status: "available", entries: map[string]permissionObservationEntry{}}
		e := verified()
		if mutate != nil {
			mutate(o, &e)
		}
		o.entries[e.view.RequestID] = e
		return o
	}
	if !observer(nil).VerifiedRejection(ses, exec, msg, call) {
		t.Fatal("verified rejection not reported")
	}
	for name, mutate := range map[string]func(o *PermissionObserver, e *permissionObservationEntry){
		"stream gap":  func(o *PermissionObserver, e *permissionObservationEntry) { o.status = "observation_uncertain" },
		"unavailable": func(o *PermissionObserver, e *permissionObservationEntry) { o.status = "observation_unavailable" },
		"streaming":   func(o *PermissionObserver, e *permissionObservationEntry) { o.streaming = true },
		"fenced":      func(o *PermissionObserver, e *permissionObservationEntry) { o.fenced = true },
		"reply once":  func(o *PermissionObserver, e *permissionObservationEntry) { e.view.ObservedReply = "once" },
		"still pending": func(o *PermissionObserver, e *permissionObservationEntry) {
			e.view.State = "pending"
			e.view.ObservedReply = ""
		},
		"unattributed": func(o *PermissionObserver, e *permissionObservationEntry) { e.view.Attribution = "unattributed" },
		"no binding":   func(o *PermissionObserver, e *permissionObservationEntry) { e.binding = nil },
		"binding other run": func(o *PermissionObserver, e *permissionObservationEntry) {
			b := *e.binding
			b.executionID = "exec_2"
			e.binding = &b
		},
		"view other run": func(o *PermissionObserver, e *permissionObservationEntry) { e.view.ExecutionID = "exec_2" },
		"conflict":       func(o *PermissionObserver, e *permissionObservationEntry) { e.conflict = true },
		"disappeared":    func(o *PermissionObserver, e *permissionObservationEntry) { e.disappeared = true },
		"conflicting identity": func(o *PermissionObserver, e *permissionObservationEntry) {
			id := e.identity
			e.conflictingIdentity = &id
		},
		"other tool call":        func(o *PermissionObserver, e *permissionObservationEntry) { e.view.ToolCallID = "call_2" },
		"other assistant":        func(o *PermissionObserver, e *permissionObservationEntry) { e.view.ToolMessageID = "msg_2" },
		"other session observer": func(o *PermissionObserver, e *permissionObservationEntry) { o.scope.SessionID = "ses_b" },
		"second matching request": func(o *PermissionObserver, e *permissionObservationEntry) {
			second := verified()
			second.view.RequestID = "per_2"
			o.entries["per_2"] = second
		},
	} {
		if observer(mutate).VerifiedRejection(ses, exec, msg, call) {
			t.Errorf("%s: reported a verified rejection", name)
		}
	}
	o := observer(nil)
	for _, args := range [][4]string{{"", exec, msg, call}, {ses, "", msg, call}, {ses, exec, "", call}, {ses, exec, msg, ""}} {
		if o.VerifiedRejection(args[0], args[1], args[2], args[3]) {
			t.Errorf("empty argument accepted: %q", args)
		}
	}
}
