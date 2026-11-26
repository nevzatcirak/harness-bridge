package dashboard

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The liveness report and the pending-permission row of the execution timeline (ADR-023 A1.4 item 2, A1.8).
// Everything here turns the closed words of ADR-017 (A1 and A2) and ADR-024 into the text the page shows: a chip
// with its word and a plain-language sentence. The page is advisory, it keys on stall_reason (never on
// stall_suspected alone, review #459 P2-1), and a word it does not know is shown as "Unknown" with a fixed
// sentence, never as received and never as if it were known. Nothing here reads anything or changes a state.

// reasonInfo is one row of a label map: the chip word, its tone, and the sentence that explains it.
type reasonInfo struct {
	Label    string
	Tone     string
	Sentence string // may hold one %s: the threshold text
}

// stallReasons are the stall_reason values (ADR-017 A1 and A2). A2 adds HARNESS_UNREACHABLE; TOOL_RUNNING_LONG is
// an explanation, not a stall reason, and has its own map.
var stallReasons = map[string]reasonInfo{
	"NO_PROGRESS":                         {"No progress", "warn", "No native progress for more than %s while the session is busy or a retry is past due."},
	"NO_CORRELATED_ASSISTANT":             {"No assistant reply", "warn", "The session is not busy and OpenCode shows no assistant reply for this prompt."},
	"COMPLETED_WITHOUT_TERMINAL_EVIDENCE": {"No completion evidence", "warn", "The session is not busy but there is no evidence that this execution finished."},
	"HARNESS_UNREACHABLE":                 {"Harness unreachable", "error", "The Bridge could not read the harness for a while, so its state is not known."},
}

// degradationReasons are the degradation_reason values (ADR-017 A2 Variant A). A healthy retry is a degradation,
// never a stall.
var degradationReasons = map[string]reasonInfo{
	"PROVIDER_RETRYING":      {"Provider retrying", "info", "The provider is retrying a failed request. This is a provider problem, not a stall."},
	"PROVIDER_RETRY_OVERDUE": {"Retry overdue", "warn", "The provider retry is past its due time and the attempt has not advanced."},
	"PROVIDER_ERROR":         {"Provider error", "error", "The provider reported an error for this execution."},
}

// explanations are the A2 explanations. TOOL_RUNNING_LONG is the only one.
var explanations = map[string]reasonInfo{
	"TOOL_RUNNING_LONG": {"Tool running long", "info", "The running tool has been running for more than %s. A long tool is not an error."},
}

var nativeStatuses = map[string]chip{
	"busy":        {Label: "Busy", Tone: "info", Raw: "busy"},
	"retry":       {Label: "Retrying", Tone: "warn", Raw: "retry"},
	"absent":      {Label: "Absent", Tone: "neutral", Raw: "absent"},
	"unavailable": {Label: "Unavailable", Tone: "warn", Raw: "unavailable"},
}

var causeHints = map[string]string{
	"network": "Network", "rate_limit": "Rate limit", "auth": "Authentication", "server": "Provider server", "unknown": "Unknown cause",
}

var causeSources = map[string]string{
	"structured": "from structured data", "bounded_text": "from a bounded text match", "none": "no source",
}

// unknownChip is what a word outside a label map becomes. The received word is deliberately not shown: it is
// untrusted text and the page must not look as if it knew it.
var unknownChip = chip{Label: "Unknown", Tone: "warn", Raw: "unknown"}

const unknownSentence = "The Bridge reported a value this page does not know."

// reportNote is the fixed closing note of every report (A1.4 item 2).
const reportNote = "This is a report, not a verdict; the Bridge takes no action."

// part is one piece of a report row or of the permission text: plain text, a mono value, a chip or a time. The
// page and the script render the same parts, so the markup never diverges.
type part struct {
	Kind string `json:"kind"` // text, code, chip or time
	Text string `json:"text,omitempty"`
	Tone string `json:"tone,omitempty"`
	ISO  string `json:"iso,omitempty"`
	Abs  string `json:"abs,omitempty"`
	Rel  string `json:"-"` // set for the HTML only: the JSON must not change every second (its ETag)
}

func textPart(s string) part { return part{Kind: "text", Text: s} }
func codePart(s string) part { return part{Kind: "code", Text: s} }
func chipPart(c chip) part   { return part{Kind: "chip", Text: c.Label, Tone: c.Tone} }
func timePart(t time.Time) part {
	return part{Kind: "time", ISO: isoTime(t), Abs: fmtTime(t)}
}

// reportRow is one key/value row of the report.
type reportRow struct {
	Label string `json:"label"`
	Parts []part `json:"parts"`
}

// reportView is the liveness report of the current execution: the summary chip of its cell, the sentences that
// explain it, the rows, and the fixed note.
type reportView struct {
	Sentences []string    `json:"sentences"`
	Rows      []reportRow `json:"rows"`
	Note      string      `json:"note"`
}

// permView is the "Awaiting a human" row of the timeline (A1.4 item 2).
type permView struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
	Parts []part `json:"parts"`
}

func lookup(m map[string]reasonInfo, word string) (reasonInfo, bool) {
	r, ok := m[word]
	return r, ok
}

// secondsText is a threshold in words: "2 minutes" for the default, otherwise whole seconds.
func secondsText(s int, fallback string) string {
	if s <= 0 || s > 86400 {
		return fallback
	}
	if s%60 == 0 && s >= 120 {
		return strconv.Itoa(s/60) + " minutes"
	}
	return strconv.Itoa(s) + " seconds"
}

func (l *Liveness) noProgressText() string {
	if l.Thresholds != nil {
		return secondsText(l.Thresholds.NoProgressS, "2 minutes")
	}
	return "2 minutes"
}

func (l *Liveness) toolLongText() string {
	if l.Thresholds != nil {
		return secondsText(l.Thresholds.ToolLongS, "10 minutes")
	}
	return "10 minutes"
}

// sentence fills the threshold of a reason sentence.
func (l *Liveness) sentence(word string, info reasonInfo) string {
	if !strings.Contains(info.Sentence, "%s") {
		return info.Sentence
	}
	if word == "TOOL_RUNNING_LONG" {
		return fmt.Sprintf(info.Sentence, l.toolLongText())
	}
	return fmt.Sprintf(info.Sentence, l.noProgressText())
}

// livenessChip is the chip of the Liveness cell of the timeline: the stall reason when there is one, else the
// degradation reason, else none. It keys on the REASON words, not on StallSuspected: a flag without a reason shows
// nothing, and a reason without the flag still shows (review #459 P2-1). A word outside the maps is "Unknown".
func livenessChip(l *Liveness) (c chip, ok bool) {
	if l == nil {
		return chip{}, false
	}
	if l.StallReason != "" {
		if r, known := lookup(stallReasons, l.StallReason); known {
			return chip{Label: r.Label, Tone: r.Tone, Raw: l.StallReason}, true
		}
		return unknownChip, true
	}
	if l.DegradationReason != "" {
		if r, known := lookup(degradationReasons, l.DegradationReason); known {
			return chip{Label: r.Label, Tone: r.Tone, Raw: l.DegradationReason}, true
		}
		return unknownChip, true
	}
	return chip{}, false
}

func nativeChip(status string) chip {
	if c, ok := nativeStatuses[status]; ok {
		return c
	}
	return unknownChip
}

func attemptText(n int64) string {
	if n < 0 || n > 1_000_000 {
		return "?"
	}
	return strconv.FormatInt(n, 10)
}

func timeRow(label string, t *time.Time, none string) reportRow {
	if t == nil || t.IsZero() {
		return reportRow{Label: label, Parts: []part{textPart(none)}}
	}
	return reportRow{Label: label, Parts: []part{timePart(*t)}}
}

// buildReport is the report of one execution's liveness block. Every row is built from closed words, bounded
// numbers and times; the one untrusted string is the tool name, shown in a mono value after truncation.
func buildReport(l *Liveness) *reportView {
	if l == nil {
		return nil
	}
	v := &reportView{Note: reportNote}
	if l.StallReason != "" {
		if r, ok := lookup(stallReasons, l.StallReason); ok {
			v.Sentences = append(v.Sentences, l.sentence(l.StallReason, r))
		} else {
			v.Sentences = append(v.Sentences, unknownSentence)
		}
	}
	if l.DegradationReason != "" {
		if r, ok := lookup(degradationReasons, l.DegradationReason); ok {
			v.Sentences = append(v.Sentences, l.sentence(l.DegradationReason, r))
		} else {
			v.Sentences = append(v.Sentences, unknownSentence)
		}
	}
	if l.Explanation != "" {
		if r, ok := lookup(explanations, l.Explanation); ok {
			v.Sentences = append(v.Sentences, l.sentence(l.Explanation, r))
		} else {
			v.Sentences = append(v.Sentences, unknownSentence)
		}
	}
	if len(v.Sentences) == 0 {
		v.Sentences = []string{"No stall is reported for this execution."}
	}

	v.Rows = append(v.Rows, reportRow{Label: "Native status", Parts: []part{chipPart(nativeChip(l.NativeStatus))}})
	switch {
	case l.RetryAttempt != nil || l.RetryNextAt != nil:
		parts := []part{}
		if l.RetryAttempt != nil {
			parts = append(parts, textPart("Attempt "+attemptText(*l.RetryAttempt)))
		}
		if l.RetryNextAt != nil && !l.RetryNextAt.IsZero() {
			if len(parts) > 0 {
				parts = append(parts, textPart(", next attempt "))
			} else {
				parts = append(parts, textPart("Next attempt "))
			}
			parts = append(parts, timePart(*l.RetryNextAt))
		}
		v.Rows = append(v.Rows, reportRow{Label: "Retry", Parts: parts})
	default:
		v.Rows = append(v.Rows, reportRow{Label: "Retry", Parts: []part{textPart("None")}})
	}
	v.Rows = append(v.Rows, timeRow("Last native progress", l.LastNativeProgressAt, "None"))
	if l.StallReason != "" {
		c, _ := livenessChipStall(l.StallReason)
		v.Rows = append(v.Rows, reportRow{Label: "Stall reason", Parts: []part{chipPart(c)}})
	}
	if l.DegradationReason != "" {
		c := unknownChip
		if r, ok := lookup(degradationReasons, l.DegradationReason); ok {
			c = chip{Label: r.Label, Tone: r.Tone}
		}
		v.Rows = append(v.Rows, reportRow{Label: "Provider", Parts: []part{chipPart(c)}})
	}
	if l.CauseHint != "" {
		label, ok := causeHints[l.CauseHint]
		if !ok {
			label = "Unknown"
		}
		parts := []part{textPart(label)}
		if src, ok := causeSources[l.CauseSource]; ok {
			parts = append(parts, textPart(", "+src))
		}
		v.Rows = append(v.Rows, reportRow{Label: "Cause hint", Parts: parts})
	}
	if l.Tool != nil {
		parts := []part{codePart(truncRunes(l.Tool.Name, 64))}
		if !l.Tool.RunningSince.IsZero() {
			parts = append(parts, textPart(" since "), timePart(l.Tool.RunningSince))
		}
		v.Rows = append(v.Rows, reportRow{Label: "Running tool", Parts: parts})
	}
	if l.Explanation != "" {
		c := unknownChip
		if r, ok := lookup(explanations, l.Explanation); ok {
			c = chip{Label: r.Label, Tone: r.Tone}
		}
		v.Rows = append(v.Rows, reportRow{Label: "Explanation", Parts: []part{chipPart(c)}})
	}
	if l.Thresholds != nil {
		v.Rows = append(v.Rows, reportRow{Label: "Thresholds", Parts: []part{textPart(fmt.Sprintf("No progress after %s, tool long after %s",
			secondsText(l.Thresholds.NoProgressS, "2 minutes"), secondsText(l.Thresholds.ToolLongS, "10 minutes")))}})
	}
	v.Rows = append(v.Rows, timeRow("Observed", &l.ObservedAt, "-"))
	return v
}

// livenessChipStall is the chip of one stall word (known or not).
func livenessChipStall(word string) (chip, bool) {
	if r, ok := lookup(stallReasons, word); ok {
		return chip{Label: r.Label, Tone: r.Tone, Raw: word}, true
	}
	return unknownChip, false
}

// safeDigest accepts only the 64-hex scope digest ADR-024 defines; anything else is not shown.
var safeDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// permissionRow is the "Awaiting a human" row. The count is the Bridge's own count; when the Bridge attributes a
// request to the execution (ADR-024) the tool, kind, pattern count and scope digest follow, and never a pattern.
// With the default incarnation mode (off) nothing is attributed and the row says so and where to answer.
func permissionRow(p *PermissionSummary) *permView {
	if p == nil {
		return nil
	}
	c := stateLabels["waiting_for_permission"]
	v := &permView{Label: "Awaiting a human", Tone: c.Tone}
	n := p.Count
	if n < 0 || n > 1_000_000 {
		n = 0
	}
	noun := "pending native permission requests"
	if n == 1 {
		noun = "pending native permission request"
	}
	if !p.Attributed {
		verb := "It is"
		if n != 1 {
			verb = "They are"
		}
		v.Parts = []part{textPart(fmt.Sprintf("%d %s. %s not attributed to an execution by this Bridge, so no details are shown. Answer it in OpenCode.", n, noun, verb))}
		if n == 0 {
			v.Parts = []part{textPart("A native permission request is pending. It is not attributed to an execution by this Bridge, so no details are shown. Answer it in OpenCode.")}
		}
		return v
	}
	v.Parts = []part{textPart(fmt.Sprintf("%d %s; one is attributed to this execution. Tool ", n, noun)), codePart(truncRunes(p.Tool, 64)),
		textPart(", kind "), codePart(truncRunes(p.Kind, 64)), textPart(fmt.Sprintf(", %d pattern(s), scope digest ", clampCount(p.PatternCount)))}
	if safeDigest.MatchString(p.Digest) {
		v.Parts = append(v.Parts, codePart(p.Digest))
	} else {
		v.Parts = append(v.Parts, textPart("not shown"))
	}
	v.Parts = append(v.Parts, textPart(". The patterns are not shown here. Answer it in OpenCode."))
	return v
}

func clampCount(n int) int {
	if n < 0 || n > 1_000_000 {
		return 0
	}
	return n
}

// durationText is a duration in the timeline: seconds, minutes and seconds, hours and minutes, days and hours.
func durationText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int64(d / time.Second)
	switch {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min %02d s", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%d h %02d min", s/3600, (s%3600)/60)
	}
	return fmt.Sprintf("%d d %d h", s/86400, (s%86400)/3600)
}

// livenessCell is the Liveness column of the timeline: a chip (with its class and the raw closed word as title), or
// a plain word.
type livenessCell struct {
	ChipLabel, ChipClass, ChipTitle string
	Text                            string
}

func terminalState(s string) bool { return s == "completed" || s == "failed" || s == "interrupted" }

func cellFor(e Execution) livenessCell {
	if terminalState(normExecutionState(e.State)) {
		return livenessCell{Text: "Ended"}
	}
	if e.Liveness == nil {
		return livenessCell{Text: "No report yet"}
	}
	if c, ok := livenessChip(e.Liveness); ok {
		title := c.Raw
		if c.Label == unknownChip.Label {
			title = "" // an unknown word is never echoed, not even in a title
		}
		return livenessCell{ChipLabel: c.Label, ChipClass: chipClass(c.Tone), ChipTitle: title}
	}
	return livenessCell{Text: "No stall reported"}
}
