package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func requirePermissionUncertain(t *testing.T, o *PermissionObserver) {
	t.Helper()
	v, err := o.View(t.Context())
	if err != nil || len(v.Permissions) != 1 {
		t.Fatal(v, err)
	}
	p := v.Permissions[0]
	if p.State != "observation_uncertain" || p.Attribution != "unattributed" || p.ExecutionID != "" || p.UserMessageID != "" || v.Status == "available" {
		t.Fatalf("uncertain identity regained attribution: %+v batch=%s", p, v.Status)
	}
}

func permissionFixSnapshot(t *testing.T, o *PermissionObserver) PermissionObservationBatch {
	t.Helper()
	v, err := o.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func permissionFixOtherParent(t *testing.T, a *observationAuthorityFixture, f *observationHTTPFixture) json.RawMessage {
	t.Helper()
	f.mu.Lock()
	other := json.RawMessage(strings.Replace(string(f.messages["/session/ses_A/message/msg_assistant"]), `"parentID":"msg_user"`, `"parentID":"msg_other"`, 1))
	f.messages["/session/ses_A/message/msg_other"] = json.RawMessage(strings.Replace(string(f.messages["/session/ses_A/message/msg_user"]), `"id":"msg_user"`, `"id":"msg_other"`, 1))
	f.mu.Unlock()
	a.mu.Lock()
	a.runs = append(a.runs, KnownPermissionExecution{ExecutionID: "execution-other", UserMessageID: "msg_other", Scope: a.scope, SubmissionAttempted: true})
	a.mu.Unlock()
	return other
}

// F1: retain the first positive binding through conflict, temporary missing
// evidence, duplicate events and attempts to return to the original execution.
func TestPermissionObservationFixImmutableBinding(t *testing.T) {
	for _, via := range []string{"snapshot", "asked", "duplicate-asked"} {
		for _, change := range []string{"parent", "execution", "tool-part", "tool-name", "parent-user-unavailable", "after-unattributed", "scope-digest"} {
			t.Run(via+"/"+change, func(t *testing.T) {
				o, a, f, raw := observationFixture(t)
				originalRaw := raw
				original := append(json.RawMessage(nil), f.messages["/session/ses_A/message/msg_assistant"]...)
				originalRuns := append([]KnownPermissionExecution(nil), a.runs...)
				first := permissionFixSnapshot(t, o)
				if first.Permissions[0].Attribution != "verified" {
					t.Fatal(first)
				}
				if via == "duplicate-asked" {
					if err := o.processEvent(t.Context(), askedEvent("evt_fixed", raw), 0); err != nil {
						t.Fatal(err)
					}
				}
				if change == "after-unattributed" {
					f.mu.Lock()
					delete(f.messages, "/session/ses_A/message/msg_assistant")
					f.mu.Unlock()
					missing := permissionFixSnapshot(t, o)
					if missing.Permissions[0].Attribution != "unattributed" {
						t.Fatal(missing)
					}
					f.mu.Lock()
					f.messages["/session/ses_A/message/msg_assistant"] = original
					f.mu.Unlock()
				}
				other := permissionFixOtherParent(t, a, f)
				f.mu.Lock()
				switch change {
				case "tool-part":
					other = json.RawMessage(strings.Replace(string(original), "prt_tool", "prt_other", 1))
				case "tool-name":
					other = json.RawMessage(strings.Replace(string(original), `"tool":"bash"`, `"tool":"read"`, 1))
				case "parent-user-unavailable":
					delete(f.messages, "/session/ses_A/message/msg_other")
				case "execution":
					other = original
				case "scope-digest":
					other = original
					raw = json.RawMessage(strings.Replace(string(raw), `"metadata":{`, `"metadata":{"changed":"scope",`, 1))
					f.pending = []json.RawMessage{raw}
				}
				f.messages["/session/ses_A/message/msg_assistant"] = other
				f.mu.Unlock()
				if change == "execution" {
					a.mu.Lock()
					a.runs = []KnownPermissionExecution{{ExecutionID: "execution-other", UserMessageID: "msg_user", Scope: a.scope, SubmissionAttempted: true}}
					a.mu.Unlock()
				}
				if via == "snapshot" {
					permissionFixSnapshot(t, o)
				} else {
					var want error
					if via == "duplicate-asked" && change == "scope-digest" {
						want = ErrPermissionMalformed
					}
					observationError(t, o.processEvent(t.Context(), askedEvent("evt_fixed", raw), 0), want)
				}
				requirePermissionUncertain(t, o)
				// Returning the original evidence cannot repair a retained conflict.
				f.mu.Lock()
				f.messages["/session/ses_A/message/msg_assistant"] = original
				f.pending = []json.RawMessage{originalRaw}
				f.mu.Unlock()
				a.mu.Lock()
				a.runs = originalRuns
				a.mu.Unlock()
				permissionFixSnapshot(t, o)
				if err := o.processEvent(t.Context(), askedEvent("evt_original", originalRaw), 0); err != nil {
					t.Fatal(err)
				}
				requirePermissionUncertain(t, o)
			})
		}
	}
}

// F2: only a successful complete pending snapshot proves observed absence.
// Neither a local stream generation nor a native claimed generation repairs it.
func TestPermissionObservationFixDisappearance(t *testing.T) {
	for _, mode := range []string{"same", "changed", "reconnect", "old-asked", "old-replied", "unproven-generation", "missing-incarnation", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			o, a, f, raw := observationFixture(t)
			if mode == "missing-incarnation" {
				o.scope.NativeEpoch, a.scope.NativeEpoch = "", ""
			}
			permissionFixSnapshot(t, o)
			f.mu.Lock()
			f.pending = []json.RawMessage{}
			f.mu.Unlock()
			permissionFixSnapshot(t, o)
			requirePermissionUncertain(t, o)
			newRaw := raw
			if mode == "changed" || mode == "unproven-generation" {
				newRaw = json.RawMessage(strings.Replace(string(raw), `"metadata":{`, `"metadata":{"generation":"native-claimed-new",`, 1))
			}
			if mode == "reconnect" {
				f.sse = func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", askedEvent("evt_reconnect", raw), repliedEvent("evt_old_reply", "per_fixture", "once"))
				}
				observationError(t, o.ReadEvents(t.Context()), ErrPermissionGap)
			} else if mode == "old-asked" {
				observationError(t, o.processEvent(t.Context(), askedEvent("evt_old_asked", raw), 0), nil)
			} else if mode == "old-replied" {
				observationError(t, o.processEvent(t.Context(), repliedEvent("evt_old_reply", "per_fixture", "once"), 0), nil)
			} else if mode == "capacity" {
				o.config.MaxRecords = 1
				f.mu.Lock()
				f.pending = []json.RawMessage{json.RawMessage(strings.Replace(string(raw), "per_fixture", "per_other", 1))}
				f.mu.Unlock()
				_, err := o.Snapshot(t.Context())
				observationError(t, err, ErrPermissionLimit)
			}
			requirePermissionUncertain(t, o)
			f.mu.Lock()
			f.pending = []json.RawMessage{newRaw}
			f.mu.Unlock()
			for i := 0; i < 2; i++ {
				permissionFixSnapshot(t, o)
				requirePermissionUncertain(t, o)
			}
		})
	}
}

func TestPermissionObservationFixIncompleteSnapshot(t *testing.T) {
	for _, mode := range []string{"malformed", "truncated", "unauthorized-after-read"} {
		t.Run(mode, func(t *testing.T) {
			o, a, f, _ := observationFixture(t)
			permissionFixSnapshot(t, o)
			f.listHook = func(w http.ResponseWriter, _ *http.Request) bool {
				switch mode {
				case "malformed":
					fmt.Fprint(w, `[null]`)
				case "truncated":
					fmt.Fprint(w, `[`)
				default:
					a.mu.Lock()
					a.deny = true
					a.mu.Unlock()
					fmt.Fprint(w, `[]`)
				}
				return true
			}
			if _, err := o.Snapshot(t.Context()); err == nil {
				t.Fatal("incomplete snapshot accepted")
			}
			a.mu.Lock()
			a.deny = false
			a.mu.Unlock()
			f.mu.Lock()
			f.listHook = nil
			f.mu.Unlock()
			v := permissionFixSnapshot(t, o)
			if v.Permissions[0].Attribution != "verified" || v.Permissions[0].ExecutionID != "execution-original" {
				t.Fatal("failed snapshot fabricated successful absence", v)
			}
		})
	}
}

// F3: reject all non-string JSON members before any scope digest is returned.
func TestPermissionObservationFixStringArrays(t *testing.T) {
	for _, field := range []string{"patterns", "always"} {
		for _, members := range []string{`[null]`, `["ok",null]`, `[1]`, `[true]`, `[[]]`, `[{}]`, `["ok",{}]`, `[""]`, `[]`} {
			t.Run(field+"/"+members, func(t *testing.T) {
				o, _, f, raw := observationFixture(t)
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				fields[field] = json.RawMessage(members)
				body, _ := json.Marshal(fields)
				_, digest, err := decodePermissionRequest(body)
				valid := members == `[""]` || members == `[]`
				if valid {
					if err != nil || digest == "" {
						t.Fatal("valid string array rejected", err)
					}
				} else {
					observationError(t, err, ErrPermissionMalformed)
					if digest != "" {
						t.Fatal("malformed scope produced digest")
					}
				}
				f.pending = []json.RawMessage{body}
				v, err := o.Snapshot(t.Context())
				if valid {
					if err != nil || v.Permissions[0].Attribution != "verified" {
						t.Fatal(v, err)
					}
				} else {
					observationError(t, err, ErrPermissionMalformed)
					if len(v.Permissions) != 0 {
						t.Fatal("malformed scope published")
					}
				}
			})
		}
	}
}

type permissionFixTransport func(*http.Request) (*http.Response, error)

func (f permissionFixTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPermissionObservationFixConcurrentConflicts(t *testing.T) {
	for _, count := range []int{2, 32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			o, a, f, raw := observationFixture(t)
			permissionFixSnapshot(t, o)
			other := permissionFixOtherParent(t, a, f)
			type alternativeKey struct{}
			transport := o.client.http.Transport
			o.client.http.Transport = permissionFixTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/session/ses_A/message/msg_assistant" && r.Context().Value(alternativeKey{}) == true {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(other)))}, nil
				}
				return transport.RoundTrip(r)
			})
			start := make(chan struct{})
			errs := make(chan error, count)
			var ready sync.WaitGroup
			ready.Add(count)
			for i := 0; i < count; i++ {
				go func(i int) {
					ready.Done()
					<-start
					ctx := context.WithValue(t.Context(), alternativeKey{}, i%2 == 0)
					errs <- o.processEvent(ctx, askedEvent(fmt.Sprintf("evt_contender_%d", i), raw), 0)
				}(i)
			}
			ready.Wait()
			close(start)
			for i := 0; i < count; i++ {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			requirePermissionUncertain(t, o)
			permissionFixSnapshot(t, o)
			requirePermissionUncertain(t, o)
		})
	}
}
