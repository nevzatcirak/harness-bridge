package dashboard

// ADR-023 A1.10 items 5, 9 and 12 for slice S2: JS-1 (the copy button), the detail refresh that updates the report
// and the permission row in place and reloads when the timeline changed shape, the entity tag stored only after a
// successful render, pagehide and the back/forward cache, and the guard of a hostile href. The shipped script runs
// unmodified in the Node harness; the models it receives are the real JSON of the handler.

import (
	"encoding/json"
	"strings"
	"testing"
)

// detailModel is the real detail JSON of a fixture, as a generic map for the harness.
func detailModel(t *testing.T, src Source) map[string]any {
	t.Helper()
	code, body, _ := v2Get(t, v2Handler(src), "/api/state/session/opencode/ses_demo0001")
	if code != 200 {
		t.Fatalf("GET model = %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// detailDOM is the markup of the detail page that the script looks up (shell.go): the same ids and data attributes.
func detailDOM(sig string) map[string]any {
	dd := func(f string) map[string]any {
		return withID(node("dd", map[string]string{"data-f": f}), "f-"+f)
	}
	return withID(node("main", map[string]string{"data-poll": "detail", "data-harness": "opencode", "data-session": "ses_demo0001", "data-sig": sig},
		hiddenEl(withID(node("p", nil), "stale")),
		hiddenEl(withID(withText(node("button", map[string]string{"data-copy": "ses_demo0001"}), "Copy ID"), "copy")),
		withID(node("td", map[string]string{"data-live": "7a2c91e0"}), "live-cell"),
		dd("title"), dd("root"), dd("lifecycle"), dd("state"), dd("guard"), dd("current"), dd("last"),
		withID(withText(node("div", nil), "SERVER REPORT"), "report"),
		withID(withText(node("td", nil), "SERVER PERM"), "perm"),
	), "content")
}

func okResp(body any, etag string) map[string]any {
	return map[string]any{"status": 200, "body": body, "headers": map[string]string{"ETag": etag}}
}

// ---- JS-1 ------------------------------------------------------------------------------------------------------

func TestV2S2CopyButtonIsHiddenUnlessTheClipboardCanBeWritten(t *testing.T) {
	res := runJS(t, map[string]any{"dom": detailDOM("x"), "steps": []any{map[string]any{"read": []string{"copy"}}}})
	if e := res.el(t, 0, "copy"); !e.Hidden {
		t.Error("the copy button is shown although the clipboard cannot be written (a dead control)")
	}
	res = runJS(t, map[string]any{"dom": detailDOM("x"), "clipboard": "ok", "steps": []any{map[string]any{"read": []string{"copy"}}}})
	if e := res.el(t, 0, "copy"); e.Hidden {
		t.Error("the copy button stays hidden although the clipboard can be written")
	}
}

func TestV2S2CopyButtonWritesTheIDAndReportsTheOutcome(t *testing.T) {
	res := runJS(t, map[string]any{"dom": detailDOM("x"), "clipboard": "ok", "steps": []any{
		map[string]any{"click": "copy", "read": []string{"copy"}},
		map[string]any{"advance": 2000, "read": []string{"copy"}},
	}})
	if len(res.Log.Copied) != 1 || res.Log.Copied[0] != "ses_demo0001" {
		t.Errorf("copied %q, want the session ID only", res.Log.Copied)
	}
	if got := res.el(t, 0, "copy").Text; got != "Copied" {
		t.Errorf("after a successful copy the button says %q", got)
	}
	if got := res.el(t, 1, "copy").Text; got != "Copy ID" {
		t.Errorf("two seconds later the button says %q", got)
	}
	res = runJS(t, map[string]any{"dom": detailDOM("x"), "clipboard": "refuse", "steps": []any{map[string]any{"click": "copy", "read": []string{"copy"}}}})
	if got := res.el(t, 0, "copy").Text; got != "Copy failed" {
		t.Errorf("after a refused copy the button says %q", got)
	}
	for _, f := range res.Log.Fetches {
		t.Errorf("the copy button made a request: %s", f.URL)
	}
}

// ---- the detail refresh ----------------------------------------------------------------------------------------

func TestV2S2DetailRefreshUpdatesTheReportAndThePermissionRowInPlace(t *testing.T) {
	src := s2Source(s2Liveness()["noprogress"], &PermissionSummary{Count: 1})
	m := detailModel(t, src)
	res := runJS(t, map[string]any{"dom": detailDOM(m["sig"].(string)), "responses": []any{okResp(m, `"e1"`)}, "steps": []any{
		map[string]any{"advance": 2000, "read": []string{"report", "perm", "f-title", "f-state", "f-guard", "f-current", "f-root", "f-last"}},
	}})
	if res.Log.Reloads != 0 {
		t.Fatalf("the page was reloaded %d times for an unchanged timeline", res.Log.Reloads)
	}
	rep := res.el(t, 0, "report")
	for _, want := range []string{"No native progress for more than 2 minutes", "<dt>Native status</dt>", `<span class="chip chip--info">Busy</span>`,
		"<dt>Stall reason</dt>", `<span class="chip chip--warn">No progress</span>`, `<time datetime="2026-11-21T17:44:50Z" title="2026-11-21 17:44:50Z">`, reportNote} {
		if !strings.Contains(rep.HTML, want) {
			t.Errorf("the refreshed report lacks %q:\n%s", want, rep.HTML)
		}
	}
	if strings.Contains(rep.HTML, "SERVER REPORT") {
		t.Error("the server-rendered report was not replaced")
	}
	perm := res.el(t, 0, "perm")
	if !strings.Contains(perm.HTML, `<span class="chip chip--secondary">Awaiting a human</span>`) || !strings.Contains(perm.Text, "1 pending native permission request. It is not attributed") {
		t.Errorf("the permission row: %s", perm.HTML)
	}
	if got := res.el(t, 0, "f-title").Text; got != "Fix flaky retry test" {
		t.Errorf("title %q", got)
	}
	if st := res.el(t, 0, "f-state"); !strings.Contains(st.HTML, `<span class="chip chip--info" title="running">Running</span>`) {
		t.Errorf("state: %s", st.HTML)
	}
	if got := res.el(t, 0, "f-guard").Text; got != "Held, running" {
		t.Errorf("guard %q", got)
	}
	if got := res.el(t, 0, "f-current").HTML; !strings.Contains(got, "<code>7a2c91e0</code>") {
		t.Errorf("current %q", got)
	}
}

// A new execution, another state, or a block that appeared or went away changes the shape: the page is reloaded
// (the script never builds the table).
func TestV2S2DetailRefreshReloadsWhenTheTimelineChangedShape(t *testing.T) {
	m := detailModel(t, s2Source(s2Liveness()["noprogress"], nil))
	same := runJS(t, map[string]any{"dom": detailDOM(m["sig"].(string)), "responses": []any{okResp(m, `"e1"`)}, "steps": []any{map[string]any{"advance": 2000}}})
	if same.Log.Reloads != 0 {
		t.Errorf("reloaded %d times with an unchanged signature", same.Log.Reloads)
	}
	changed := runJS(t, map[string]any{"dom": detailDOM("7a2c91e0:running:cr,older"), "responses": []any{okResp(m, `"e1"`)}, "steps": []any{map[string]any{"advance": 2000}}})
	if changed.Log.Reloads != 1 {
		t.Errorf("reloaded %d times for a changed signature, want 1", changed.Log.Reloads)
	}
	// a permission row that appears is a shape change too
	withPerm := detailModel(t, s2Source(s2Liveness()["noprogress"], &PermissionSummary{Count: 1}))
	if withPerm["sig"] == m["sig"] {
		t.Error("a pending-permission row does not change the signature")
	}
}

// Hostile text in a model reaches the page only as text.
func TestV2S2DetailRefreshSetsHostileValuesAsText(t *testing.T) {
	src := hostileSource()
	m := detailModel2(t, src, "ses_evil0001")
	res := runJS(t, map[string]any{"dom": detailDOM(m["sig"].(string)), "responses": []any{okResp(m, `"e1"`)}, "steps": []any{
		map[string]any{"advance": 2000, "read": []string{"report", "perm", "f-title", "f-state"}},
	}})
	for _, id := range []string{"report", "perm", "f-title", "f-state"} {
		e := res.el(t, 0, id)
		if strings.Contains(e.HTML, "<script>") || strings.Contains(e.HTML, "<img") {
			t.Errorf("%s: markup built from data:\n%s", id, e.HTML)
		}
	}
	if !strings.Contains(res.el(t, 0, "perm").HTML, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the hostile permission text is not shown as escaped text")
	}
}

func detailModel2(t *testing.T, src Source, id string) map[string]any {
	t.Helper()
	code, body, _ := v2Get(t, v2Handler(src), "/api/state/session/opencode/"+id)
	if code != 200 {
		t.Fatalf("GET model = %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// ---- carried: the entity tag is stored only after a successful render (#511 P3-2) ------------------------------

func TestV2S2ETagIsStoredOnlyAfterASuccessfulRender(t *testing.T) {
	broken := map[string]any{"kind": "list", "connected": true, "count": 1, "count_text": "1 session", "sessions": []any{nil}} // the row cannot be rendered
	res := runJS(t, map[string]any{"dom": listDOM(), "responses": []any{
		okResp(broken, `"bad"`), okResp(okBody(), `"good"`), okResp(okBody(), `"good"`),
	}, "steps": []any{map[string]any{"advance": 5000}, map[string]any{"advance": 600000}}})
	if len(res.Log.Fetches) < 3 {
		t.Fatalf("%d fetches", len(res.Log.Fetches))
	}
	if got := res.Log.Fetches[1].Headers["If-None-Match"]; got != "" {
		t.Errorf("the request after a model that failed to render sent If-None-Match %q: the broken model would be skipped as unchanged", got)
	}
	if got := res.Log.Fetches[2].Headers["If-None-Match"]; got != `"good"` {
		t.Errorf("the request after a good render sent If-None-Match %q, want the good tag", got)
	}
}

// ---- carried: pagehide aborts the request, pageshow restores (#511 P3-3, #512 P3-5) ----------------------------

func TestV2S2PagehideAbortsAndClearsTimersAndAPersistedPageshowRefreshesOnce(t *testing.T) {
	res := runJS(t, map[string]any{"dom": listDOM(), "responses": []any{map[string]any{"hang": true}, okResp(okBody(), `"e1"`), okResp(okBody(), `"e1"`)}, "steps": []any{
		map[string]any{"advance": 5000},         // 0: the poll is in flight (hangs)
		map[string]any{"pagehide": true},        // 1: the page goes into the back/forward cache: abort, no timers
		map[string]any{"advance": 600000},       // 2: nothing happens while it is cached
		map[string]any{"pageshow": "plain"},     // 3: a non-persisted pageshow (first load) does nothing
		map[string]any{"pageshow": "persisted"}, // 4: restored from the cache: one immediate refresh
		map[string]any{"advance": 0},
	}})
	f0, _, _ := res.step(t, 0)
	f1, p1, ab1 := res.step(t, 1)
	f2, p2, _ := res.step(t, 2)
	f3, _, _ := res.step(t, 3)
	f4, p4, _ := res.step(t, 4)
	if f0 != 1 {
		t.Fatalf("%d fetches before pagehide", f0)
	}
	if ab1 != 1 || f1 != 1 || len(p1) != 0 {
		t.Errorf("pagehide: aborted %d (want 1), fetches %d, pending timers %v (want none)", ab1, f1, p1)
	}
	if f2 != 1 || len(p2) != 0 {
		t.Errorf("a cached page polled: %d fetches, timers %v", f2, p2)
	}
	if f3 != 1 {
		t.Errorf("a non-persisted pageshow made a request (%d fetches)", f3)
	}
	if f4 != 2 || len(p4) == 0 {
		t.Errorf("a restored page: %d fetches (want 2), timers %v (polling must resume)", f4, p4)
	}
}

// ---- carried: the guard of a hostile href (#511 P3-1) ----------------------------------------------------------

func TestV2S2SafeHrefRefusesAnythingButASessionPath(t *testing.T) {
	hrefs := []string{"/session/opencode/ses_ok", "javascript:alert(1)", "//evil.example/session/x", "https://evil.example/session/x", "/sessionx/y", "session/opencode/x", "data:text/html,<b>", "\\session\\x", ""}
	var rows []any
	for i, h := range hrefs {
		rows = append(rows, map[string]any{"harness": "opencode", "harness_name": "OpenCode", "id": "ses_" + string(rune('a'+i)), "title": "T" + string(rune('a'+i)), "state": "idle", "state_label": "Idle",
			"state_tone": "neutral", "guard_text": "None", "path_short": "/x", "root": "/x", "last_activity": "-", "href": h})
	}
	body := map[string]any{"kind": "list", "connected": true, "count": len(rows), "count_text": "9 sessions", "sessions": rows}
	res := runJS(t, map[string]any{"dom": listDOM(), "responses": []any{okResp(body, `"e1"`)}, "steps": []any{map[string]any{"advance": 5000}}})
	anchors := strings.Count(res.Live, "<a ")
	if anchors != 1 || !strings.Contains(res.Live, `<a href="/session/opencode/ses_ok">`) {
		t.Errorf("%d anchors, want exactly the one /session/ link:\n%s", anchors, res.Live)
	}
	for _, bad := range []string{`href="javascript`, `href="//`, `href="https`, `href="/sessionx`, `href="data:`, `href="session/`, `href="\`} {
		if strings.Contains(res.Live, bad) {
			t.Errorf("a hostile href reached the page: %s", bad)
		}
	}
	// the rows whose href was refused still show their title as plain text
	for _, title := range []string{"Tb", "Tc", "Td"} {
		if !strings.Contains(res.Live, title) {
			t.Errorf("the row %s disappeared", title)
		}
	}
}
