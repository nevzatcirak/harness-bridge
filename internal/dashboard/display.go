package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Display helpers of the sessions table (ADR-023 A1.1 and A1.4). They turn the closed words and the raw
// values the Source supplies into the text the page shows. None of them reads anything or has a side effect.

// chip is one state chip: a sentence-case label in the body face (the non-colour cue), the tone that picks
// its colours, and the raw closed word that stays in the title attribute and in the JSON (A1.1).
type chip struct {
	Label string
	Tone  string // ok, info, secondary, error, warn, neutral or outline
	Raw   string
}

// stateLabels is the label map of A1.1, extended with the one execution state A1.1 does not list
// ("submitting": ADR-010 admission, shown by the guard of a session that has just been given a prompt).
// Only waiting_for_permission is secondary: that colour is reserved for "awaiting a human".
var stateLabels = map[string]chip{
	"completed":              {"Completed", "ok", "completed"},
	"running":                {"Running", "info", "running"},
	"waiting_for_permission": {"Waiting for permission", "secondary", "waiting_for_permission"},
	"idle":                   {"Idle", "neutral", "idle"},
	"failed":                 {"Failed", "error", "failed"},
	"interrupted":            {"Interrupted", "warn", "interrupted"},
	"unknown":                {"Unknown", "warn", "unknown"},
	"archived":               {"Archived", "outline", "archived"},
	"submitting":             {"Submitting", "info", "submitting"},
}

// stateChip returns the chip of a state word. A word outside the table is shown as received, neutral (the
// template escapes it); callers normalise the closed sets first, so it is only reached by a new word.
func stateChip(raw string) chip {
	if c, ok := stateLabels[raw]; ok {
		return c
	}
	return chip{Label: raw, Tone: "neutral", Raw: raw}
}

// chipClass is the class attribute of a chip of the given tone.
func chipClass(tone string) string {
	switch tone {
	case "ok", "info", "secondary", "error", "warn", "outline":
		return "chip chip--" + tone
	}
	return "chip"
}

// harnessName is the harness badge text (D17 Q8: the harness stays a badge and a column).
func harnessName(h string) string {
	switch h {
	case harnessOpenCode:
		return "OpenCode"
	case harnessClaudeCode:
		return "Claude Code"
	}
	return "Other"
}

// guardText is the guard cell: "None", or "Held, <state label in lower case>".
func guardText(g guardRow) string {
	if !g.Held {
		return "None"
	}
	return "Held, " + strings.ToLower(stateChip(g.State).Label)
}

// relTime is the relative form of a time (A1.4: the absolute time stays in title and datetime). A time in
// the future (clock skew) reads as zero seconds; the zero time reads "-". The script (JS-5) uses the same
// buckets.
func relTime(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch s := int64(d / time.Second); {
	case s < 60:
		return fmt.Sprintf("%d s ago", s)
	case s < 3600:
		return fmt.Sprintf("%d min ago", s/60)
	case s < 86400:
		return fmt.Sprintf("%d h ago", s/3600)
	default:
		return fmt.Sprintf("%d d ago", s/86400)
	}
}

// shortPathRunes is the length from which a path is shortened (A1.4 item 1).
const shortPathRunes = 40

// shortPath shortens a path longer than 40 runes to an ellipsis plus its last two segments, never cutting
// inside a segment. A path with fewer than three segments has nothing to drop and is returned whole.
func shortPath(p string) string {
	if utf8.RuneCountInString(p) <= shortPathRunes {
		return p
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) <= 2 {
		return p
	}
	return "…/" + segs[len(segs)-2] + "/" + segs[len(segs)-1]
}

// pathSuffix is the stable 6-hex suffix that tells two different full paths apart when their shortened forms
// would render identically (A1.4 item 1): the first six hex digits of the sha256 of the full path.
func pathSuffix(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])[:6]
}

// sessionsCountText is "1 session" or "N sessions".
func sessionsCountText(n int) string {
	if n == 1 {
		return "1 session"
	}
	return fmt.Sprintf("%d sessions", n)
}

// isoTime is the datetime attribute value of a time ("" for the zero time).
func isoTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// relTimeISO is relTime for a datetime attribute value; an empty or unparsable value reads "-".
func relTimeISO(now time.Time, iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "-"
	}
	return relTime(now, t)
}
