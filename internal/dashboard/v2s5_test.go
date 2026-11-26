package dashboard

// ADR-017 A2.13 S5: the dashboard badges/detail and the poll JSON for the A2 liveness fields. The fixtures are the
// S2/A2 ones plus the wedged-retry, tool-stalled and stale-tool combinations this slice owns.

import (
	"encoding/json"
	"strings"
	"testing"
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
