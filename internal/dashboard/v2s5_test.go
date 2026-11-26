package dashboard

// ADR-017 A2.13 S5: the dashboard badges/detail and the poll JSON for the A2 liveness fields. The fixtures are the
// S2/A2 ones plus the wedged-retry, tool-stalled and stale-tool combinations this slice owns.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// cellOf returns the Liveness cell of the current execution of a detail page.
func cellOf(t *testing.T, body string) string {
	t.Helper()
	return between(t, body, `<td><code>7a2c91e0</code></td>`, `</tr>`)
}

func TestV2S5BadgeCombinations(t *testing.T) {
	pages := s2Pages(t)
	t.Run("healthy retry is retrying, not stalled", func(t *testing.T) {
		cell := cellOf(t, pages["detail-live-retrying"])
		for _, want := range []string{`>Retrying</span>`, `title="PROVIDER_RETRYING"`, "attempt 2/5", "next 2026-11-21 17:48:25Z"} {
			if !strings.Contains(cell, want) {
				t.Errorf("the cell lacks %q:\n%s", want, cell)
			}
		}
		if strings.Contains(cell, ">Stalled</span>") {
			t.Errorf("a healthy retry is shown as stalled:\n%s", cell)
		}
	})
	t.Run("wedged retry is both retrying and stalled", func(t *testing.T) {
		cell := cellOf(t, pages["detail-live-wedged"])
		for _, want := range []string{`>Retry overdue</span>`, `title="PROVIDER_RETRY_OVERDUE"`, `>Stalled</span>`, `title="NO_PROGRESS"`} {
			if !strings.Contains(cell, want) {
				t.Errorf("the wedged-retry cell lacks %q:\n%s", want, cell)
			}
		}
	})
	t.Run("busy running tool is stalled plus tool running", func(t *testing.T) {
		cell := cellOf(t, pages["detail-live-tool-stalled"])
		for _, want := range []string{`>Stalled</span>`, `>Tool running</span>`, `title="TOOL_RUNNING_LONG"`, "bash since"} {
			if !strings.Contains(cell, want) {
				t.Errorf("the tool-stalled cell lacks %q:\n%s", want, cell)
			}
		}
		reportText := textOf(report(t, pages["detail-live-tool-stalled"]))
		if !strings.Contains(reportText, "The running tool has been running for more than 10 minutes") {
			t.Errorf("the tool-running explanation is missing:\n%s", reportText)
		}
	})
	t.Run("unreachable carries the since time", func(t *testing.T) {
		cell := cellOf(t, pages["detail-live-unreachable"])
		for _, want := range []string{`>Unreachable</span>`, "since 2026-11-21 17:45:05Z"} {
			if !strings.Contains(cell, want) {
				t.Errorf("the unreachable cell lacks %q:\n%s", want, cell)
			}
		}
		reportText := textOf(report(t, pages["detail-live-unreachable"]))
		if !strings.Contains(reportText, "Unavailable since") {
			t.Errorf("the unavailable-since row is missing:\n%s", reportText)
		}
	})
	t.Run("stale tool is not running", func(t *testing.T) {
		cell := cellOf(t, pages["detail-live-stale-tool"])
		if strings.Contains(cell, "Tool running") || strings.Contains(cell, "Stalled") {
			t.Errorf("an absent status with a stale part is shown as a live tool/stall:\n%s", cell)
		}
		if !strings.Contains(cell, "No stall reported") {
			t.Errorf("the stale-tool cell does not say no stall is reported:\n%s", cell)
		}
		reportText := textOf(report(t, pages["detail-live-stale-tool"]))
		if strings.Contains(reportText, "Running tool") {
			t.Errorf("a stale tool is rendered as running:\n%s", reportText)
		}
	})
}

func TestV2S5PollJSONCarriesTheA2Fields(t *testing.T) {
	// The sessions list twin: the current execution's retrying block.
	_, body, _ := v2Get(t, v2Handler(v2Src{&fakeSource{rows: v2Rows()}}), "/api/state")
	var list apiList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) < 2 || list.Sessions[0].Liveness == nil {
		t.Fatalf("no liveness in the list twin: %+v", list.Sessions)
	}
	l := list.Sessions[0].Liveness
	if l.DegradationReason != "PROVIDER_RETRYING" || l.RetryAttempt == nil || *l.RetryAttempt != 2 || l.RetryMax == nil || *l.RetryMax != 5 ||
		l.ProgressSource != "native" || l.Thresholds == nil || l.Thresholds.NoProgressS != 120 || l.LastNativeProgressAt == "" ||
		len(l.Badges) == 0 || l.Badges[0].Label != "Retrying" {
		t.Errorf("the list twin's liveness fields are wrong: %+v", l)
	}
	if !strings.Contains(list.Sessions[0].Liveness.Text, "attempt 2/5") {
		t.Errorf("the list twin lacks the retry detail: %q", list.Sessions[0].Liveness.Text)
	}
	// The detail twin: a wedged retry, the unavailable-since record and the tool explanation.
	_, body, _ = v2Get(t, v2Handler(s2Source(s2Liveness()["wedged"], nil)), "/api/state/session/opencode/ses_demo0001")
	var det apiDetail
	if err := json.Unmarshal([]byte(body), &det); err != nil {
		t.Fatal(err)
	}
	if len(det.Executions) == 0 || det.Executions[0].Liveness == nil {
		t.Fatalf("no liveness in the detail twin: %+v", det.Executions)
	}
	w := det.Executions[0].Liveness
	labels := map[string]bool{}
	for _, b := range w.Badges {
		labels[b.Label] = true
	}
	if !labels["Retry overdue"] || !labels["Stalled"] || w.StallReason != "NO_PROGRESS" || w.DegradationReason != "PROVIDER_RETRY_OVERDUE" || !w.Degraded {
		t.Errorf("the wedged retry twin lost a state: %+v", w)
	}
	// The detail report and the current execution of the unreachable page carry unavailable_since.
	_, body, _ = v2Get(t, v2Handler(s2Source(s2Liveness()["unreachable"], nil)), "/api/state/session/opencode/ses_demo0001")
	if err := json.Unmarshal([]byte(body), &det); err != nil {
		t.Fatal(err)
	}
	if det.Executions[0].Liveness == nil || det.Executions[0].Liveness.UnavailableSince == "" {
		t.Errorf("the unreachable twin lacks unavailable_since: %+v", det.Executions[0].Liveness)
	}
	if strings.Contains(body, "ago") || strings.Contains(body, "Running for") {
		t.Errorf("the detail twin carries clock text: %s", body)
	}
}

func TestV2S5SessionsListShowsTheCurrentLivenessBadges(t *testing.T) {
	_, body, _ := v2Get(t, v2Handler(v2Src{&fakeSource{rows: v2Rows()}}), "/")
	for _, want := range []string{">Retrying</span>", ">Stalled</span>", ">Tool running</span>", `class="live"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the sessions list lacks %q", want)
		}
	}
}

func TestV2S5HostileToolNameIsTextOnly(t *testing.T) {
	body := s2Pages(t)["detail-hostile"]
	assertNoActiveContent(t, "detail-hostile-a2", body)
	if strings.Contains(body, "<img src=x") || strings.Contains(body, "<script>alert") {
		t.Error("the hostile tool name reached the page as markup")
	}
}

// The detail page's poll updates the liveness cells in place for a changed block without a shape change (the
// signature covers the report/permission shape, not the badge text alone).
func TestV2S5DetailPollUpdatesTheLivenessCellInPlace(t *testing.T) {
	m := detailModel(t, s2Source(s2Liveness()["wedged"], nil))
	res := runJS(t, map[string]any{"dom": detailDOM(m["sig"].(string)), "responses": []any{okResp(m, `"e1"`)}, "steps": []any{
		map[string]any{"advance": 2000, "read": []string{"live-cell"}},
	}})
	if res.Log.Reloads != 0 {
		t.Fatalf("the page was reloaded %d times", res.Log.Reloads)
	}
	cell := res.el(t, 0, "live-cell")
	for _, want := range []string{`>Retry overdue</span>`, `>Stalled</span>`} {
		if !strings.Contains(cell.HTML, want) {
			t.Errorf("the polled liveness cell lacks %q: %s", want, cell.HTML)
		}
	}
}

func TestV2S5ExecutionsPageShowsBothWedgedBadges(t *testing.T) {
	body := s3Pages(t)["executions"]
	cell := between(t, body, `<td><code>7a2c91e0</code></td>`, `</tr>`)
	for _, want := range []string{`>Stalled</span>`, `>Retry overdue</span>`, "attempt 4/5"} {
		if !strings.Contains(cell, want) {
			t.Errorf("the executions row lacks %q:\n%s", want, cell)
		}
	}
}

// No new colour is introduced: every badge tone is one of the existing chip tones (blocked/warn/error/info/neutral).
func TestV2S5BadgesUseOnlyTheExistingChipTones(t *testing.T) {
	allowed := map[string]bool{"ok": true, "info": true, "secondary": true, "error": true, "warn": true, "outline": true, "neutral": true}
	for name, l := range s2Liveness() {
		badges, _ := livenessBadges(l, nil)
		for _, b := range badges {
			if !allowed[b.Tone] {
				t.Errorf("%s: badge %q has the tone %q", name, b.Label, b.Tone)
			}
		}
	}
}

// The poll JSON never echoes an unknown word; it says unknown (the HTML shows the Unknown chip).
func TestV2S5UnknownWordsAreNotEchoedInTheJSON(t *testing.T) {
	_, body, _ := v2Get(t, v2Handler(s2Source(s2Liveness()["unknown-words"], nil)), "/api/state/session/opencode/ses_demo0001")
	for _, bad := range []string{"FUTURE_STALL", "FUTURE_DEGRADATION", "FUTURE_EXPLANATION", "future_cause", "future_source", "weird"} {
		if strings.Contains(body, bad) {
			t.Errorf("the detail JSON echoes %q: %s", bad, body)
		}
	}
}

// #592 P2-1: the poll-JSON liveness.tool.name is bounded on every model that carries it. The bound is 64 runes plus
// the ellipsis truncRunes appends; a multibyte name must be cut by runes, not bytes, and a markup name must stay a
// plain string (the JSON encoder escapes it and the page's renderer uses textContent only).
func TestV2S5PollJSONToolNameIsBounded(t *testing.T) {
	long := strings.Repeat("é", 200) // 200 runes, 400 bytes
	markup := `<img src=x onerror=alert(1)>`
	for _, tc := range []struct {
		name      string
		tool      string
		truncated bool
	}{
		{"200-rune multibyte name", long, true},
		{"markup name", markup, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &Liveness{NativeStatus: "busy", Tool: &LivenessTool{Name: tc.tool, RunningSince: v2Now}, ObservedAt: v2Now}
			s := s2Session()
			s.Liveness = l
			execs := s2Executions(l, nil)
			h := v2Handler(&fakeSource{rows: []Session{s},
				details: map[string]SessionDetail{"opencode/ses_demo0001": {Session: s, Executions: execs}}, execs: execs})
			check := func(model, raw, name string, present bool) {
				t.Helper()
				if !present {
					t.Errorf("%s: the tool name is missing", model)
					return
				}
				if n := utf8.RuneCountInString(name); n > 65 {
					t.Errorf("%s: tool name is %d runes, bound is 64 + ellipsis (65)", model, n)
				}
				if tc.truncated {
					if got := utf8.RuneCountInString(name); got != 65 || !strings.HasSuffix(name, "…") {
						t.Errorf("%s: truncated tool name is %q (%d runes), want 64 runes + ellipsis", model, name, got)
					}
					if strings.HasPrefix(name, "é") && strings.HasSuffix(name, "é…") {
						// a byte-based cut would split a multibyte rune into U+FFFD
						if strings.Contains(name, "\uFFFD") {
							t.Errorf("%s: truncated on a byte boundary: %q", model, name)
						}
					}
				} else if name != tc.tool {
					t.Errorf("%s: tool name = %q, want %q", model, name, tc.tool)
				}
				if strings.Contains(raw, "<img src=x") {
					t.Errorf("%s: the JSON carries the markup name unescaped", model)
				}
			}
			present := func(a *apiLiveness) (string, bool) {
				if a == nil || a.Tool == nil {
					return "", false
				}
				return a.Tool.Name, true
			}

			_, body, _ := v2Get(t, h, "/api/state")
			var list apiList
			if err := json.Unmarshal([]byte(body), &list); err != nil {
				t.Fatalf("/api/state: %v", err)
			}
			var one *apiLiveness
			for i := range list.Sessions {
				if list.Sessions[i].Liveness != nil && list.Sessions[i].Liveness.Tool != nil {
					one = list.Sessions[i].Liveness
					break
				}
			}
			name, ok := present(one)
			check("/api/state", body, name, ok)

			_, body, _ = v2Get(t, h, "/api/state/session/opencode/ses_demo0001")
			var det apiDetail
			if err := json.Unmarshal([]byte(body), &det); err != nil {
				t.Fatalf("/api/state/session: %v", err)
			}
			one = nil
			for i := range det.Executions {
				if det.Executions[i].Liveness != nil && det.Executions[i].Liveness.Tool != nil {
					one = det.Executions[i].Liveness
					break
				}
			}
			name, ok = present(one)
			check("/api/state/session/opencode/ses_demo0001", body, name, ok)

			_, body, _ = v2Get(t, h, "/api/state/executions")
			var ex apiExecList
			if err := json.Unmarshal([]byte(body), &ex); err != nil {
				t.Fatalf("/api/state/executions: %v", err)
			}
			one = nil
			for i := range ex.Executions {
				if ex.Executions[i].Liveness != nil && ex.Executions[i].Liveness.Tool != nil {
					one = ex.Executions[i].Liveness
					break
				}
			}
			name, ok = present(one)
			check("/api/state/executions", body, name, ok)
		})
	}
}

// #592 P3-2: the display side has its own stale-tool gate, mirroring the Manager's rule (Tool is present only for a
// busy execution or a wedged retry). A block that pairs an absent status with a Tool part never shows Tool running.
func TestV2S5ToolBadgeNeedsTheNativeGate(t *testing.T) {
	tool := &LivenessTool{Name: "bash", RunningSince: v2Now.Add(-12 * time.Minute)}
	for _, tc := range []struct {
		name string
		l    *Liveness
		want bool
	}{
		{"busy", &Liveness{NativeStatus: "busy", Tool: tool}, true},
		{"wedged retry", &Liveness{NativeStatus: "retry", StallReason: "NO_PROGRESS", Tool: tool}, true},
		{"absent with a stale part", &Liveness{NativeStatus: "absent", Tool: tool}, false},
		{"healthy retry", &Liveness{NativeStatus: "retry", DegradationReason: "PROVIDER_RETRYING", Tool: tool}, false},
		{"unavailable", &Liveness{NativeStatus: "unavailable", Tool: tool}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			badges, details := livenessBadges(tc.l, nil)
			shown := false
			for _, b := range badges {
				if b.Label == "Tool running" {
					shown = true
				}
			}
			if shown != tc.want {
				t.Errorf("Tool running shown = %v, want %v (%+v)", shown, tc.want, badges)
			}
			if !tc.want && strings.Contains(strings.Join(details, "; "), "bash") {
				t.Errorf("a gated tool is still named in the detail: %q", details)
			}
		})
	}
}
