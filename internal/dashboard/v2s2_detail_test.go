package dashboard

// ADR-023 A1.10 items 1, 3, 7, 9, 10 and 11 for slice S2 (the detail page): the liveness report of every reason
// of ADR-017 A1 and A2, the pending-permission row, the diff with its file list and <details>, the JSON twin, the
// types the Source carries, and the hostile-input fixtures. Everything renders with the fixed clock v2Now.

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// textOf is the text content of an HTML fragment as a browser shows it: tags removed, entities decoded.
func textOf(s string) string {
	return html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(s, ""))
}

// between returns the part of body from the first start to the next end after it.
func between(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("no %q in the page", start)
	}
	rest := body[i:]
	j := strings.Index(rest[len(start):], end)
	if j < 0 {
		t.Fatalf("no %q after %q", end, start)
	}
	return rest[:len(start)+j+len(end)]
}

func report(t *testing.T, body string) string {
	t.Helper()
	return between(t, body, `<div id="report">`, `</div></td></tr>`)
}

// ---- the label maps (A1.10 item 7: total over the known values, sentence case, no snake-case chip) -----------

var snakeCase = regexp.MustCompile(`[a-z]+_[a-z]+|[A-Z]{2,}_[A-Z_]+`)

func TestV2S2LabelMapsAreTotalOverTheKnownWords(t *testing.T) {
	want := map[string][]string{
		"stall":       {"NO_PROGRESS", "NO_CORRELATED_ASSISTANT", "COMPLETED_WITHOUT_TERMINAL_EVIDENCE", "HARNESS_UNREACHABLE"},
		"degradation": {"PROVIDER_RETRYING", "PROVIDER_RETRY_OVERDUE", "PROVIDER_ERROR"},
		"explanation": {"TOOL_RUNNING_LONG"},
	}
	maps := map[string]map[string]reasonInfo{"stall": stallReasons, "degradation": degradationReasons, "explanation": explanations}
	for kind, words := range want {
		m := maps[kind]
		if len(m) != len(words) {
			t.Errorf("%s: %d entries, want %d", kind, len(m), len(words))
		}
		for _, w := range words {
			r, ok := m[w]
			if !ok {
				t.Errorf("%s: no label for %s", kind, w)
				continue
			}
			if r.Label == "" || snakeCase.MatchString(r.Label) || r.Label != strings.ToUpper(r.Label[:1])+r.Label[1:] || strings.ToUpper(r.Label) == r.Label {
				t.Errorf("%s: the label %q of %s is not a sentence-case word", kind, r.Label, w)
			}
			if !strings.HasSuffix(r.Sentence, ".") || snakeCase.MatchString(r.Sentence) {
				t.Errorf("%s: the sentence of %s is not plain language: %q", kind, w, r.Sentence)
			}
			if !regexp.MustCompile(`^(ok|info|secondary|error|warn|neutral|outline)$`).MatchString(r.Tone) {
				t.Errorf("%s: tone %q of %s", kind, r.Tone, w)
			}
			if r.Tone == "secondary" {
				t.Errorf("%s: %s uses the colour reserved for pending permissions", kind, w)
			}
		}
	}
	for _, w := range []string{"busy", "retry", "absent", "unavailable"} {
		if c, ok := nativeStatuses[w]; !ok || snakeCase.MatchString(c.Label) || c.Label == w {
			t.Errorf("native status %s has the label %q", w, c.Label)
		}
	}
	for _, w := range []string{"network", "rate_limit", "auth", "server", "unknown"} {
		if l, ok := causeHints[w]; !ok || snakeCase.MatchString(l) {
			t.Errorf("cause hint %s has the label %q", w, l)
		}
	}
	for _, w := range []string{"structured", "bounded_text", "none"} {
		if l, ok := causeSources[w]; !ok || snakeCase.MatchString(l) {
			t.Errorf("cause source %s has the label %q", w, l)
		}
	}
}

// The maps cover what the Bridge can actually emit: every reason word assigned in internal/execution is in a map,
// so a reason added there without a label fails here.
func TestV2S2LabelMapsCoverEveryWordTheManagerEmits(t *testing.T) {
	files, err := filepath.Glob("../execution/*.go") // every non-test source: liveness.go, liveness_failure.go, claude_liveness.go, ...
	if err != nil || len(files) == 0 {
		t.Fatalf("no execution sources found: %v", err)
	}
	re := regexp.MustCompile(`(StallReason|DegradationReason|Explanation)\s*(?:=|:)\s*"([A-Z_]+)"`)
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]+" "+m[2]] = true
			var known bool
			switch m[1] {
			case "StallReason":
				_, known = stallReasons[m[2]]
			case "DegradationReason":
				_, known = degradationReasons[m[2]]
			default:
				_, known = explanations[m[2]]
			}
			if !known {
				t.Errorf("the manager emits %s %s and the dashboard has no label for it", m[1], m[2])
			}
		}
	}
	if len(seen) < 7 {
		t.Errorf("only %d emitted words were found in internal/execution: the scan is broken", len(seen))
	}
}

// ---- the report of every reason --------------------------------------------------------------------------------

func TestV2S2EveryLivenessReasonRendersItsChipSentenceAndRows(t *testing.T) {
	pages := s2Pages(t)
	cases := []struct {
		page      string
		cellChip  string   // the chip of the Liveness cell ("" for none)
		cellTitle string   // its title (the closed word), "" when it carries none
		sentences []string // text in the report
		rows      []string // "Label: value" lines of the report
	}{
		{"healthy", "", "", []string{"No stall is reported for this execution."}, []string{"Native status: Busy", "Retry: None", "Last native progress: 10 s ago"}},
		{"noprogress", "Stalled", "NO_PROGRESS", []string{"No native progress for more than 2 minutes while the session is busy or a retry is past due."}, []string{"Stall reason: No progress", "Native status: Busy", "Thresholds: No progress after 2 minutes, tool long after 10 minutes"}},
		{"no-assistant", "No assistant reply", "NO_CORRELATED_ASSISTANT", []string{"The session is not busy and OpenCode shows no assistant reply for this prompt."}, []string{"Native status: Absent", "Stall reason: No assistant reply"}},
		{"no-evidence", "No completion evidence", "COMPLETED_WITHOUT_TERMINAL_EVIDENCE", []string{"The session is not busy but there is no evidence that this execution finished."}, []string{"Stall reason: No completion evidence"}},
		{"unreachable", "Unreachable", "HARNESS_UNREACHABLE", []string{"The Bridge could not read the harness for a while, so its state is not known."}, []string{"Native status: Unavailable", "Stall reason: Harness unreachable"}},
		{"retrying", "Retrying", "PROVIDER_RETRYING", []string{"The provider is retrying a failed request. This is a provider problem, not a stall."}, []string{"Native status: Retrying", "Retry: Attempt 2 of 5, next attempt in the future", "Provider: Provider retrying", "Cause hint: Network, from a bounded text match"}},
		{"retry-overdue", "Retry overdue", "PROVIDER_RETRY_OVERDUE", []string{"The provider retry is past its due time and the attempt has not advanced."}, []string{"Provider: Retry overdue", "Cause hint: Rate limit, from structured data"}},
		{"provider-error", "Provider error", "PROVIDER_ERROR", []string{"The provider reported an error for this execution."}, []string{"Provider: Provider error", "Cause hint: Authentication, from structured data"}},
		{"tool-long", "Tool running", "TOOL_RUNNING_LONG", []string{"The running tool has been running for more than 10 minutes. A long tool is not an error."}, []string{"Running tool: bash since", "Explanation: Tool running long"}},
		{"reason-only", "Stalled", "NO_PROGRESS", []string{"No native progress for more than 2 minutes"}, []string{"Stall reason: No progress"}},
	}
	for _, c := range cases {
		t.Run(c.page, func(t *testing.T) {
			body := pages["detail-live-"+c.page]
			cell := between(t, body, `<td><code>7a2c91e0</code></td>`, `</tr>`)
			switch {
			case c.cellChip != "":
				if !strings.Contains(cell, `>`+c.cellChip+`</span>`) || !strings.Contains(cell, `title="`+c.cellTitle+`"`) {
					t.Errorf("the Liveness cell does not show the chip %q (title %q):\n%s", c.cellChip, c.cellTitle, cell)
				}
			default:
				if !strings.Contains(cell, "No stall reported") {
					t.Errorf("a block without a reason must say no stall is reported:\n%s", cell)
				}
			}
			r := report(t, body)
			txt := textOf(r)
			for _, s := range c.sentences {
				if !strings.Contains(txt, s) {
					t.Errorf("the report lacks the sentence %q:\n%s", s, txt)
				}
			}
			flat := regexp.MustCompile(`\s+`).ReplaceAllString(txt, " ")
			for _, row := range c.rows {
				label, val, _ := strings.Cut(row, ": ")
				val = strings.TrimSuffix(val, " in the future")
				if !strings.Contains(flat, label+val) {
					t.Errorf("the report lacks the row %q:\n%s", row, flat)
				}
			}
			if !strings.Contains(txt, reportNote) {
				t.Error("the fixed note is missing")
			}
			if snakeCase.MatchString(txt) {
				t.Errorf("a raw snake-case word is shown as text in the report: %v\n%s", snakeCase.FindAllString(txt, -1), txt)
			}
		})
	}
}

// A word the page does not know is "Unknown", with a fixed sentence: never echoed, never shown as if it were known.
func TestV2S2UnknownLivenessWordsAreNeverShownAsReceivedNorAsKnown(t *testing.T) {
	body := s2Pages(t)["detail-live-unknown-words"]
	for _, bad := range []string{"FUTURE_STALL", "FUTURE_DEGRADATION", "FUTURE_EXPLANATION", "future_cause", "future_source", "weird", "<b>x</b>"} {
		if strings.Contains(body, bad) {
			t.Errorf("the page echoes the unknown word %q", bad)
		}
	}
	r := textOf(report(t, body))
	if strings.Count(r, unknownSentence) != 3 {
		t.Errorf("want the unknown sentence once for the stall, the degradation and the explanation:\n%s", r)
	}
	cell := between(t, body, `<td><code>7a2c91e0</code></td>`, `</tr>`)
	if !strings.Contains(cell, `<span class="chip chip--warn">Unknown</span>`) {
		t.Errorf("an unknown stall word must show the Unknown chip without a title:\n%s", cell)
	}
	for _, known := range []string{"No progress", "Provider retrying", "Tool running long", "Busy"} {
		if strings.Contains(r, known) {
			t.Errorf("an unknown word was shown as the known %q", known)
		}
	}
}

// Review #459 P2-1: the page keys on stall_reason. A flag without a reason names nothing; a reason without the flag
// is still shown.
func TestV2S2TheRowIsKeyedOnTheReasonNotOnTheFlag(t *testing.T) {
	flagOnly := between(t, s2Pages(t)["detail-live-flag-only"], `<td><code>7a2c91e0</code></td>`, `</tr>`)
	if !strings.Contains(flagOnly, "No stall reported") || strings.Contains(flagOnly, "chip chip--warn") || strings.Contains(flagOnly, "chip chip--error") {
		t.Errorf("stall_suspected without a reason produced a stall chip:\n%s", flagOnly)
	}
	if strings.Contains(textOf(report(t, s2Pages(t)["detail-live-flag-only"])), "Stall reason") {
		t.Error("a stall reason row appeared for a block with no stall_reason")
	}
	reasonOnly := between(t, s2Pages(t)["detail-live-reason-only"], `<td><code>7a2c91e0</code></td>`, `</tr>`)
	if !strings.Contains(reasonOnly, ">Stalled</span>") {
		t.Errorf("a stall_reason with the flag false was not shown:\n%s", reasonOnly)
	}
}

// The report sits under the CURRENT execution only; an ended execution says "Ended" whatever block it kept.
func TestV2S2TheReportIsUnderTheCurrentExecutionOnly(t *testing.T) {
	body := s2Pages(t)["detail-live-noprogress"]
	if n := strings.Count(body, `<div id="report">`); n != 1 {
		t.Fatalf("%d reports, want 1", n)
	}
	cur := strings.Index(body, `<code>7a2c91e0</code>`)
	rep := strings.Index(body, `<div id="report">`)
	next := strings.Index(body, `<code>5d03aa17</code>`)
	if !(cur < rep && rep < next) {
		t.Errorf("the report is not between the current execution and the next one (%d, %d, %d)", cur, rep, next)
	}
	ended := between(t, body, `<code>5d03aa17</code>`, `</tr>`)
	if !strings.Contains(ended, ">Ended</span>") || strings.Contains(ended, "Stalled") {
		t.Errorf("an ended execution shows a liveness chip:\n%s", ended)
	}
	if !strings.Contains(between(t, s2Pages(t)["detail-no-liveness"], `<td><code>7a2c91e0</code></td>`, `</tr>`), "No report yet") {
		t.Error("a current execution with no block must say so")
	}
	if strings.Contains(s2Pages(t)["detail-no-liveness"], `id="report"`) {
		t.Error("a report was rendered without a liveness block")
	}
}

// ---- the pending-permission row (A1.4 item 2, A1.8) ---------------------------------------------------------

func TestV2S2PendingPermissionRow(t *testing.T) {
	un := between(t, s2Pages(t)["detail-perm-unattributed"], `<tr class="perm-row">`, `</tr>`)
	for _, want := range []string{`chip chip--secondary`, "Awaiting a human", "1 pending native permission request. It is not attributed to an execution by this Bridge, so no details are shown. Answer it in OpenCode."} {
		if !strings.Contains(textOf(un)+un, want) {
			t.Errorf("the unattributed row lacks %q:\n%s", want, un)
		}
	}
	for _, bad := range []string{"digest", "Tool "} {
		if strings.Contains(un, bad) {
			t.Errorf("an unattributed row shows %q", bad)
		}
	}
	at := between(t, s2Pages(t)["detail-perm-attributed"], `<tr class="perm-row">`, `</tr>`)
	for _, want := range []string{"2 pending native permission requests; one is attributed to this execution.", "<code>bash</code>", "2 pattern(s)", digest64, "The patterns are not shown here. Answer it in OpenCode."} {
		if !strings.Contains(at, want) {
			t.Errorf("the attributed row lacks %q:\n%s", want, at)
		}
	}
	// the pending row is the only user of the secondary colour in the page
	for name, body := range s2Pages(t) {
		if name != "detail-perm-unattributed" && name != "detail-perm-attributed" && name != "detail-hostile" && strings.Contains(body, "chip--secondary") && !strings.Contains(body, "Waiting for permission") {
			t.Errorf("%s: the secondary colour is used outside a pending state", name)
		}
	}
	// a digest that is not 64 hex digits is not shown
	p := permissionRow(&PermissionSummary{Count: 1, Attributed: true, Tool: "bash", Kind: "bash", PatternCount: 1, Digest: "<b>not hex</b>"})
	var txt string
	for _, x := range p.Parts {
		txt += x.Text
	}
	if strings.Contains(txt, "not hex") || !strings.Contains(txt, "not shown") {
		t.Errorf("a malformed digest was shown: %q", txt)
	}
	// the row follows the execution that carries it and is a row of the same table
	body := s2Pages(t)["detail-perm-attributed"]
	if !(strings.Index(body, `<code>7a2c91e0</code>`) < strings.Index(body, `<tr class="perm-row">`) && strings.Index(body, `<tr class="perm-row">`) < strings.Index(body, `<code>5d03aa17</code>`)) {
		t.Error("the pending row is not directly under the current execution")
	}
}

// A1.8 and the T11 style type pin: the Source carries display data only, never a pattern or native text.
func TestV2S2TheSourceTypesCarryNoPatternAndNoNativeText(t *testing.T) {
	fields := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			out = append(out, rt.Field(i).Name)
		}
		sort.Strings(out)
		return out
	}
	for _, c := range []struct {
		v    any
		want string
	}{
		{PermissionSummary{}, "Attributed Count Digest Kind PatternCount Tool"},
		{Liveness{}, "CauseHint CauseSource DegradationReason Degraded Explanation LastNativeProgressAt NativeStatus ObservedAt ProgressSource RetryAttempt RetryMax RetryNextAt RetryOverdue StallReason StallSuspected Thresholds Tool"},
		{LivenessTool{}, "Name RunningSince"},
		{LivenessThresholds{}, "NoProgressS ToolLongS"},
		{Execution{}, "Current Ended ID Liveness Permission SessionID Started State UnavailableSince Updated"},
		// S3a: the process facts carry a version, a build, a timestamp and flags: never the token, the Host or the origin
		{Health{}, "Build Features Reach TokenExpires Version"},
		{Feature{}, "Name On"},
		{Reachability{}, "At Harness State"},
	} {
		if got := strings.Join(fields(c.v), " "); got != c.want {
			t.Errorf("%T has the fields %q, want %q", c.v, got, c.want)
		}
	}
}

// ---- the JSON twin ---------------------------------------------------------------------------------------------

func TestV2S2DetailModelCarriesTheReportOfTheCurrentExecutionAndNoClock(t *testing.T) {
	get := func(now time.Time) (string, apiDetail) {
		opts := Options{Now: func() time.Time { return now }}
		h := NewHandlerOptions(s2Source(s2Liveness()["noprogress"], &PermissionSummary{Count: 1}), nil, opts)
		code, body, _ := v2Get(t, h, "/api/state/session/opencode/ses_demo0001")
		if code != 200 {
			t.Fatalf("GET = %d", code)
		}
		var m apiDetail
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		return body, m
	}
	body, m := get(v2Now)
	if len(m.Executions) != 4 {
		t.Fatalf("%d executions", len(m.Executions))
	}
	for i, e := range m.Executions {
		if (e.Report != nil) != (i == 0) || (e.Permission != nil) != (i == 0) {
			t.Errorf("execution %d (%s): report %v, permission %v", i, e.ID, e.Report != nil, e.Permission != nil)
		}
	}
	if m.Executions[0].Report.Note != reportNote || len(m.Executions[0].Report.Rows) < 4 {
		t.Errorf("the report of the model: %+v", m.Executions[0].Report)
	}
	if m.Sig != "7a2c91e0:running:crp,5d03aa17:completed,0c9be452:failed,e41d77b9:interrupted" {
		t.Errorf("sig = %q", m.Sig)
	}
	// no relative time and no clock-dependent text: two clocks, one body (its ETag does not move every second)
	body2, _ := get(v2Now.Add(37 * time.Second))
	if body != body2 {
		t.Error("the detail model changes with the clock")
	}
	for _, bad := range []string{"ago", "Running for"} {
		if strings.Contains(body, bad) {
			t.Errorf("the model carries %q", bad)
		}
	}
	// the page and the model carry the same signature
	_, page := s2Page(t, s2Source(s2Liveness()["noprogress"], &PermissionSummary{Count: 1}), "/session/opencode/ses_demo0001")
	if !strings.Contains(page, `data-sig="`+m.Sig+`"`) {
		t.Errorf("the page does not carry the signature of the model %q", m.Sig)
	}
}

// ---- the diff section (A1.4 item 2) ---------------------------------------------------------------------------

func TestV2S2ChangesFileListDetailsAndLines(t *testing.T) {
	body := s2Pages(t)["detail-live-healthy"]
	sec := between(t, body, `<section class="changes card"`, `</section>`)
	txt := textOf(sec)
	for _, want := range []string{"Changes in the working tree", "3 files, +42 -7", "redaction on: 0 values redacted",
		"Modified services/billing/retry_test.go +11 -2", "Modified services/billing/retry.go +30 -5", "Added docs/NOTE.md +1 -0"} {
		if !strings.Contains(regexp.MustCompile(`\s+`).ReplaceAllString(txt, " "), want) {
			t.Errorf("the changes section lacks %q:\n%s", want, txt)
		}
	}
	if n := strings.Count(sec, `<details class="file"`); n != 3 {
		t.Errorf("%d <details>, want 3", n)
	}
	if n := strings.Count(sec, `<details class="file" open>`); n != 1 || !strings.Contains(sec, `<details class="file" open><summary>services/billing/retry_test.go</summary>`) {
		t.Errorf("exactly the first file must be open (%d are)", n)
	}
	// every added and removed line has its tint AND its character
	pre := between(t, sec, `<pre class="diff">`, `</pre>`)
	for _, ln := range regexp.MustCompile(`<span class="ln add">([^<]*)</span>`).FindAllStringSubmatch(pre, -1) {
		if !strings.HasPrefix(html.UnescapeString(ln[1]), "+") {
			t.Errorf("an added line without its +: %q", ln[1])
		}
	}
	for _, ln := range regexp.MustCompile(`<span class="ln del">([^<]*)</span>`).FindAllStringSubmatch(pre, -1) {
		if !strings.HasPrefix(html.UnescapeString(ln[1]), "-") {
			t.Errorf("a removed line without its -: %q", ln[1])
		}
	}
	if strings.Count(pre, `class="ln add"`) != 4 || strings.Count(pre, `class="ln del"`) != 1 || strings.Count(pre, `class="ln hunk"`) != 3 {
		t.Errorf("line classes: add %d del %d hunk %d\n%s", strings.Count(pre, `class="ln add"`), strings.Count(pre, `class="ln del"`), strings.Count(pre, `class="ln hunk"`), pre)
	}
	// the file headers and the no-newline note are muted, never tinted as changes
	if strings.Contains(pre, `class="ln add">&#43;&#43;&#43;`) || strings.Contains(pre, `class="ln del">---`) {
		t.Error("a file header line is tinted as a change")
	}
}

func TestV2S2DiffStates(t *testing.T) {
	pages := s2Pages(t)
	un := textOf(between(t, pages["detail-diff-unavailable"], `<section class="changes card"`, `</section>`))
	if !strings.Contains(un, "unavailable") || !strings.Contains(un, "(error)") || !strings.Contains(un, "turn list unavailable") {
		t.Errorf("the unavailable state has no word and no reason:\n%s", un)
	}
	em := textOf(between(t, pages["detail-diff-empty"], `<section class="changes card"`, `</section>`))
	if !strings.Contains(em, "computed, no changes") || !strings.Contains(em, "non-git project") {
		t.Errorf("an empty answer is not labelled as one:\n%s", em)
	}
	if strings.Contains(pages["detail-diff-empty"], `<details class="file"`) {
		t.Error("an empty diff rendered a file")
	}
	cl := pages["detail-claude"]
	if !strings.Contains(cl, "diff unavailable (ADR-021 pending)") || strings.Contains(cl, "<details class=\"file\"") || strings.Contains(cl, `id="report"`) {
		t.Error("a Claude Code session must say the diff is unavailable and show no diff")
	}
	turn := textOf(between(t, pages["turn"], `<section class="changes card"`, `</section>`))
	if !strings.Contains(turn, "Changes in this turn") || strings.Contains(pages["turn"], `id="h-tl"`) {
		t.Errorf("the turn page: %s", turn)
	}
}

func TestV2S2SplitDiffClassifiesLines(t *testing.T) {
	cases := []struct{ in, class, text string }{
		{"+added", "ln add", "+added"}, {"-removed", "ln del", "-removed"}, {" context", "ln", " context"},
		{"@@ -1 +1 @@", "ln hunk", "@@ -1 +1 @@"}, {"\\ No newline at end of file", "ln hunk", "\\ No newline at end of file"}, {"", "ln", " "},
		// a lone "+++"/"---" line is not a header pair: it is content (a header needs its position and its partner)
		{"+++ b/x", "ln add", "+++ b/x"}, {"--- a/x", "ln del", "--- a/x"},
	}
	for _, c := range cases {
		got := splitDiff(c.in + "\n")
		if len(got) != 1 || got[0].Class != c.class || got[0].Text != c.text {
			t.Errorf("%q -> %+v, want %s %q", c.in, got, c.class, c.text)
		}
	}
	if splitDiff("") != nil {
		t.Error("an empty patch has lines")
	}
	if got := splitDiff("+a\n-b"); len(got) != 2 {
		t.Errorf("a patch without a final newline: %+v", got)
	}
}

// #568 P3-1 and P3-8: headers are recognised by position and shape. Content lines that merely look like headers
// ("--- foo" for a removed "-- foo", "+++ foo" for an added "++ foo") are tinted as changes; "diff --git" and the
// extended header lines are muted.
func TestV2S2SplitDiffRecognisesHeadersByPositionAndShape(t *testing.T) {
	classes := func(patch string) string {
		var out []string
		for _, l := range splitDiff(patch) {
			switch l.Class {
			case "ln hunk":
				out = append(out, "H")
			case "ln add":
				out = append(out, "+")
			case "ln del":
				out = append(out, "-")
			default:
				out = append(out, ".")
			}
		}
		return strings.Join(out, "")
	}
	for name, c := range map[string]struct{ patch, want string }{
		"a removed SQL comment inside a hunk":      {"@@ -1 +1 @@\n--- foo\n+++ bar\n", "H-+"},
		"the same pair after a context line":       {"@@ -1,2 +1,2 @@\n ctx\n--- foo\n+++ bar\n", "H.-+"},
		"headers before the first hunk":            {"--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", "HHH-+"},
		"a git header block":                       {"diff --git a/x b/x\nindex 1..2 100644\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", "HHHHH-+"},
		"a second file resets the header state":    {"diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/y b/y\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-c\n+d\n", "HHHH-+HHHH-+"},
		"a new file":                               {"diff --git a/n b/n\nnew file mode 100644\nindex 0..1\n--- /dev/null\n+++ b/n\n@@ -0,0 +1 @@\n+x\n", "HHHHHH+"},
		"a deleted file and a rename":              {"diff --git a/o b/n\nsimilarity index 90%\nrename from o\nrename to n\n", "HHHH"},
		"a binary notice":                          {"diff --git a/b b/b\nBinary files a/b and b/b differ\n", "HH"},
		"a lone removed line starting with dashes": {"--- only\n", "-"},
		"added content starting with plus signs":   {"@@ -1 +1 @@\n+++ not a header\n", "H+"},
		"no newline note":                          {"@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n", "H-H+"},
	} {
		if got := classes(c.patch); got != c.want {
			t.Errorf("%s: %q -> %s, want %s", name, c.patch, got, c.want)
		}
	}
}

// The page bound (the 2 MiB rule) accounts for the span markup of every line: a patch of many short lines is
// degraded with a written notice, never refused and never over the bound.
func TestV2S2ManyShortLinesAreDegradedNotAnError(t *testing.T) {
	var b strings.Builder
	for b.Len() < 40<<10 {
		b.WriteString("+a\n")
	}
	files := make([]DiffFile, 0, 12)
	for i := 0; i < 12; i++ {
		files = append(files, DiffFile{Path: "f" + string(rune('a'+i)) + ".txt", HasPath: true, Status: "modified", Additions: 1, HasPatch: true, Patch: "@@ -1 +1 @@\n" + b.String()})
	}
	src := s2Source(s2Liveness()["healthy"], nil)
	src.workDiff = DiffResult{Files: files}
	code, body := s2Page(t, src, "/session/opencode/ses_demo0001")
	if code != 200 || len(body) > maxBodyBytes {
		t.Fatalf("%d, %d bytes", code, len(body))
	}
	if !strings.Contains(body, patchHiddenNote) || strings.Count(body, `<pre class="diff">`) == 0 {
		t.Errorf("the page must keep what fits and say what it hid (%d patches shown)", strings.Count(body, `<pre class="diff">`))
	}
	// the same with patches so large that none fits: every patch is hidden and the page is still served
	var big strings.Builder
	for big.Len() < maxPatchPerFile-8 {
		big.WriteString("+a\n")
	}
	for i := range files {
		files[i].Patch = "@@ -1 +1 @@\n" + big.String()
	}
	src.workDiff = DiffResult{Files: files}
	code, body = s2Page(t, src, "/session/opencode/ses_demo0001")
	if code != 200 || len(body) > maxBodyBytes || !strings.Contains(body, patchHiddenNote) {
		t.Errorf("an oversized patch page: %d, %d bytes", code, len(body))
	}
}

// ---- the header card and the timeline shape --------------------------------------------------------------------

func TestV2S2HeaderCardAndTimelineShape(t *testing.T) {
	body := s2Pages(t)["detail-live-noprogress"]
	head := textOf(between(t, body, `<dl class="kv">`, `</dl>`))
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(head, " ")
	for _, want := range []string{"HarnessOpenCode", "Session IDses_demo0001", "TitleFix flaky retry test", "Path/srv/demo/work/payments-api/services/billing",
		"Lifecycle root/srv/demo/work", "StateRunning", "GuardHeld, running", "Current execution7a2c91e0", "Last activity53 s ago, 2026-11-21 17:47:12Z"} {
		if !strings.Contains(strings.ReplaceAll(flat, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("the header card lacks %q:\n%s", want, flat)
		}
	}
	if !strings.Contains(body, `<button class="btn" type="button" id="copy" data-copy="ses_demo0001" hidden>Copy ID</button>`) || !strings.Contains(body, `<code class="copyable">ses_demo0001</code>`) {
		t.Error("the copy button must be hidden until the script runs, with the ID as selectable text beside it")
	}
	for _, col := range []string{"State", "Execution", "Started", "Duration", "Liveness"} {
		if !strings.Contains(body, `<th scope="col">`+col+`</th>`) {
			t.Errorf("the timeline lacks the column %s", col)
		}
	}
	// newest first, as the Source supplies them
	if !(strings.Index(body, "7a2c91e0") < strings.Index(body, "5d03aa17") && strings.Index(body, "5d03aa17") < strings.Index(body, "0c9be452") && strings.Index(body, "0c9be452") < strings.Index(body, "e41d77b9")) {
		t.Error("the timeline is not newest first")
	}
	for _, d := range []string{"Running for 8 min 50 s", "6 min 02 s", "1 min 15 s", "3 min 30 s"} {
		if !strings.Contains(body, `<td class="num">`+d+`</td>`) {
			t.Errorf("the timeline lacks the duration %q", d)
		}
	}
	// the absolute time sits beside the relative one, and in the title
	if !strings.Contains(body, `<time datetime="2026-11-21T17:39:15Z" title="2026-11-21 17:39:15Z">8 min ago</time>, 2026-11-21 17:39:15Z`) {
		t.Error("the start time lacks the relative form, the absolute form beside it, or the title")
	}
}

func TestV2S2DurationText(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{0, "0 s"}, {-5 * time.Second, "0 s"}, {59 * time.Second, "59 s"}, {60 * time.Second, "1 min 00 s"}, {8*time.Minute + 50*time.Second, "8 min 50 s"},
		{59*time.Minute + 59*time.Second, "59 min 59 s"}, {time.Hour, "1 h 00 min"}, {25*time.Hour + 30*time.Minute, "1 d 1 h"}} {
		if got := durationText(c.d); got != c.want {
			t.Errorf("durationText(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// ---- hostile input (A1.10 item 11) -----------------------------------------------------------------------------

func TestV2S2HostileDetailPageIsEscapedEverywhere(t *testing.T) {
	body := s2Pages(t)["detail-hostile"]
	assertNoActiveContent(t, "detail-hostile", body)
	for _, bad := range []string{"<script>alert", "<img src=x", "onerror=alert(2)>", "</pre><script>", `<a href="javascript`, `href="javascript:`} {
		if strings.Contains(body, bad) {
			t.Errorf("unescaped %q in the hostile page", bad)
		}
	}
	// every hostile string is still shown, as text
	txt := textOf(body)
	for _, shown := range []string{`<script>alert(1)</script>`, `<img src=x onerror=alert(4)>`, `</pre><script>alert(5)</script>`, `style="background:url(https://evil.example/p.png)"`} {
		if !strings.Contains(txt, shown) {
			t.Errorf("the hostile text %q is not shown as text", shown)
		}
	}
	for _, a := range htmlAttrs(body) {
		if strings.HasPrefix(a.name, "on") || a.name == "style" {
			t.Errorf("attribute %s", a.name)
		}
		if (a.name == "href" || a.name == "src") && !strings.HasPrefix(a.value, "/") && !strings.HasPrefix(a.value, "#") {
			t.Errorf("%s=%q leaves the dashboard", a.name, a.value)
		}
	}
	// links are built only from identifiers the routes accept: the hostile turn ID gets none
	if strings.Contains(body, `/turn/msg"`) || strings.Contains(body, "alert(7)</a>") {
		t.Error("a hostile turn ID reached a link")
	}
	if !strings.Contains(body, `href="/session/opencode/ses_evil0001/turn/msg_ok"`) {
		t.Error("the valid turn lost its link")
	}
	// no external URL in any attribute of any S2 page, and none in a page's own markup
	for name, page := range s2Pages(t) {
		for _, a := range htmlAttrs(page) {
			if regexp.MustCompile(`(?i)^(https?:)?//`).MatchString(a.value) {
				t.Errorf("%s: %s=%q is an external URL", name, a.name, a.value)
			}
		}
	}
}

// The liveness tool name is untrusted display text: bounded and shown as text in a mono value.
func TestV2S2ToolNameIsBoundedAndTextOnly(t *testing.T) {
	l := s2Liveness()["tool-long"]
	l.Tool.Name = strings.Repeat("é", 200) + "<script>"
	body := func() string { _, b := s2Page(t, s2Source(l, nil), "/session/opencode/ses_demo0001"); return b }()
	if strings.Contains(body, "<script>") && strings.Count(strings.ToLower(body), "<script") != 1 {
		t.Error("the tool name reached the page as markup")
	}
	if !strings.Contains(body, strings.Repeat("é", 64)+"…") || strings.Contains(body, strings.Repeat("é", 65)) {
		t.Error("the tool name was not bounded to 64 runes")
	}
}

// ---- carried P3s ------------------------------------------------------------------------------------------------

// #512 P3-2: the page has no dead fields.
func TestV2S2PageHasNoDeadFields(t *testing.T) {
	rt := reflect.TypeOf(page{})
	for _, dead := range []string{"Filter", "ShowArchived"} {
		if _, ok := rt.FieldByName(dead); ok {
			t.Errorf("page.%s is dead (nothing renders it)", dead)
		}
	}
}

// #512 P3-3: role="search" wraps the filters only, not the archived link or Refresh.
func TestV2S2SearchLandmarkWrapsOnlyTheFilters(t *testing.T) {
	body := v2Pages(t)["list"]
	i := strings.Index(body, `role="search"`)
	if i < 0 {
		t.Fatal("no search landmark")
	}
	start := strings.LastIndex(body[:i], "<div")
	group := body[start:]
	group = group[:strings.Index(group, "</div>")+len("</div>")]
	if strings.Contains(group, "Refresh") || strings.Contains(group, "archived") || strings.Contains(group, "<a ") {
		t.Errorf("the search landmark wraps the link controls:\n%s", group)
	}
	for _, id := range []string{`id="f-q"`, `id="f-state"`, `id="f-harness"`} {
		if !strings.Contains(group, id) {
			t.Errorf("the search landmark lacks %s", id)
		}
	}
	if !strings.Contains(group, `aria-label="Filter sessions"`) {
		t.Error("the search landmark has no name")
	}
	if strings.Count(body, `role="search"`) != 1 {
		t.Error("more than one search landmark")
	}
}

// A1.6 JS-1 no-JS fallback: the ID is selectable text (user-select: all) beside a button that stays hidden.
func TestV2S2CopyableIDIsSelectableWithoutScript(t *testing.T) {
	var body string
	for _, r := range flatRules(readCSS(t, embeddedCSS)) {
		if r.Sel == ".copyable" {
			body = r.Body
		}
	}
	if !strings.Contains(body, "user-select: all") {
		t.Errorf(".copyable = %q, want user-select: all", body)
	}
	if !strings.Contains(s2Pages(t)["detail-live-healthy"], `<code class="copyable">ses_demo0001</code>`) {
		t.Error("the session ID is not in a selectable element")
	}
	if regexp.MustCompile(`(?i)\.btn\[hidden\]\s*\{[^}]*display:\s*none`).MatchString(readCSS(t, embeddedCSS)) == false {
		t.Error("a hidden button is not removed from the layout")
	}
}

// #568 P3-7: an unattributed row with no count says the count is not known; it never says "pending" with a zero.
func TestV2S2UnattributedPermissionWithoutACountSaysItIsUnknown(t *testing.T) {
	p := permissionRow(&PermissionSummary{Count: 0})
	var txt string
	for _, x := range p.Parts {
		txt += x.Text
	}
	if !strings.Contains(txt, "The number of pending native permission requests is not known") || strings.Contains(txt, "0 pending") || strings.Contains(txt, "is pending") {
		t.Errorf("the zero-count row says: %q", txt)
	}
}

// #569 P3-1: the 64-rune bound on the pending-permission tool and kind, with a long input.
func TestV2S2PermissionToolAndKindAreBoundedTo64Runes(t *testing.T) {
	long := strings.Repeat("é", 200)
	p := permissionRow(&PermissionSummary{Count: 1, Attributed: true, Tool: long, Kind: long, PatternCount: 1, Digest: digest64})
	var codes []string
	for _, x := range p.Parts {
		if x.Kind == "code" && x.Text != digest64 {
			codes = append(codes, x.Text)
		}
	}
	if len(codes) != 2 {
		t.Fatalf("%d mono values besides the digest", len(codes))
	}
	for _, c := range codes {
		if c != strings.Repeat("é", 64)+"…" {
			t.Errorf("the value is %d runes, want the 64-rune cut with an ellipsis", len([]rune(c)))
		}
	}
	// and in the rendered page
	src := s2Source(s2Liveness()["healthy"], &PermissionSummary{Count: 1, Attributed: true, Tool: long, Kind: long, PatternCount: 1, Digest: digest64})
	_, body := s2Page(t, src, "/session/opencode/ses_demo0001")
	if strings.Contains(body, strings.Repeat("é", 65)) || strings.Count(body, strings.Repeat("é", 64)+"…") != 2 {
		t.Error("the page does not show the bounded tool and kind")
	}
}
