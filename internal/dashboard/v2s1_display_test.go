package dashboard

// ADR-023 A1.4 and A1.1 (slice S1): the display helpers of the sessions table. RED-first: these tests were
// written before the helpers existed.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestV2S1RelativeTime(t *testing.T) {
	now := time.Date(2026, 11, 21, 17, 48, 5, 0, time.UTC)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{0, "0 s ago"}, {3 * time.Second, "3 s ago"}, {59 * time.Second, "59 s ago"}, {60 * time.Second, "1 min ago"},
		{6 * time.Minute, "6 min ago"}, {59*time.Minute + 59*time.Second, "59 min ago"}, {time.Hour, "1 h ago"},
		{23*time.Hour + 59*time.Minute, "23 h ago"}, {24 * time.Hour, "1 d ago"}, {30 * time.Hour, "1 d ago"}, {9 * 24 * time.Hour, "9 d ago"},
		{-5 * time.Second, "0 s ago"}, // a timestamp in the future (clock skew) never reads as a negative age
	}
	for _, c := range cases {
		if got := relTime(now, now.Add(-c.ago)); got != c.want {
			t.Errorf("relTime(%v ago) = %q, want %q", c.ago, got, c.want)
		}
	}
	if got := relTime(now, time.Time{}); got != "-" {
		t.Errorf("relTime(zero) = %q, want -", got)
	}
}

func TestV2S1ShortPath(t *testing.T) {
	cases := map[string]string{
		"/srv/demo/work":                                           "/srv/demo/work",
		"/srv/demo/work/tools/config-loader":                       "/srv/demo/work/tools/config-loader", // 34 runes: kept whole
		"/srv/demo/work/payments-api/services/billing":             "…/services/billing",
		"/srv/demo/work/prototypes/old/very/long/segment/names/ok": "…/names/ok",
		"/" + strings.Repeat("a", 60):                              "/" + strings.Repeat("a", 60),   // one segment: never cut inside it
		"/x/" + strings.Repeat("b", 60):                            "/x/" + strings.Repeat("b", 60), // two segments: nothing to drop
		"/örnek/projeler/çalışma/büyük/dizin/adı/ve/başka":         "…/ve/başka",
		"": "",
	}
	for in, want := range cases {
		got := shortPath(in)
		if got != want {
			t.Errorf("shortPath(%q) = %q, want %q", in, got, want)
		}
		if got != in && !strings.HasPrefix(got, "…/") {
			t.Errorf("shortPath(%q) = %q: a shortened path starts with an ellipsis", in, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("shortPath(%q) is not valid UTF-8", in)
		}
	}
	// A path of exactly 40 runes is kept, 41 is shortened.
	p40 := "/" + strings.Repeat("a", 18) + "/" + strings.Repeat("b", 10) + "/" + strings.Repeat("c", 9)
	if utf8.RuneCountInString(p40) != 40 {
		t.Fatalf("fixture is %d runes", utf8.RuneCountInString(p40))
	}
	if shortPath(p40) != p40 {
		t.Errorf("a 40-rune path was shortened: %q", shortPath(p40))
	}
	if shortPath(p40+"d") == p40+"d" {
		t.Error("a 41-rune path was not shortened")
	}
}

func TestV2S1ShortPathCollisionGetsStableSuffix(t *testing.T) {
	a := "/srv/one/project-alpha/services/billing"
	b := "/srv/two/project-beta/services/billing"
	c := "/srv/one/project-alpha/services/billing" // the same full path as a: it is not ambiguous with a
	long := func(p string) string { return p + "/extra" }
	rows := []Session{
		{Harness: "opencode", ID: "ses_a", Root: long(a)}, {Harness: "opencode", ID: "ses_b", Root: long(b)},
		{Harness: "opencode", ID: "ses_c", Root: long(c)}, {Harness: "opencode", ID: "ses_d", Root: "/srv/other/project/services/shipping/extra-extra-extra"},
	}
	out, _ := listRows(rows)
	sum := func(p string) string { s := sha256.Sum256([]byte(p)); return hex.EncodeToString(s[:])[:6] }
	want := map[string]string{
		"ses_a": "…/billing/extra (" + sum(long(a)) + ")", "ses_b": "…/billing/extra (" + sum(long(b)) + ")",
		"ses_c": "…/billing/extra (" + sum(long(c)) + ")", "ses_d": "…/shipping/extra-extra-extra",
	}
	for _, r := range out {
		if r.PathShort != want[r.ID] {
			t.Errorf("%s: PathShort = %q, want %q", r.ID, r.PathShort, want[r.ID])
		}
	}
	// Stable: the same input gives the same suffix on every call, and the suffix is 6 lowercase hex digits.
	again, _ := listRows(rows)
	for i := range out {
		if out[i].PathShort != again[i].PathShort {
			t.Errorf("%s: the suffix is not stable", out[i].ID)
		}
	}
	// Two rows with the very same full path are the same place: no suffix on either.
	same, _ := listRows([]Session{{Harness: "opencode", ID: "ses_1", Root: long(a)}, {Harness: "opencode", ID: "ses_2", Root: long(a)}})
	for _, r := range same {
		if strings.Contains(r.PathShort, "(") {
			t.Errorf("%s: a suffix on an unambiguous path: %q", r.ID, r.PathShort)
		}
	}
}

func TestV2S1StateLabelMapIsTotalAndSentenceCase(t *testing.T) {
	want := map[string][2]string{ // raw -> label, tone
		"completed": {"Completed", "ok"}, "running": {"Running", "info"}, "waiting_for_permission": {"Waiting for permission", "secondary"},
		"idle": {"Idle", "neutral"}, "failed": {"Failed", "error"}, "interrupted": {"Interrupted", "warn"}, "unknown": {"Unknown", "warn"},
		"archived": {"Archived", "outline"}, "submitting": {"Submitting", "info"},
	}
	for raw, w := range want {
		c := stateChip(raw)
		if c.Label != w[0] || c.Tone != w[1] || c.Raw != raw {
			t.Errorf("stateChip(%q) = %+v, want label %q tone %q", raw, c, w[0], w[1])
		}
	}
	for raw := range sessionStates {
		if _, ok := want[raw]; !ok {
			t.Errorf("session state %q has no entry in the label map", raw)
		}
	}
	for raw := range executionStates {
		if _, ok := want[raw]; !ok {
			t.Errorf("execution state %q has no entry in the label map", raw)
		}
	}
	// Labels are sentence case: the first letter is the only capital, no underscore, no glyph.
	for raw := range want {
		l := stateChip(raw).Label
		if strings.ContainsAny(l, "_") || l != strings.ToUpper(l[:1])+strings.ToLower(l[1:]) {
			t.Errorf("label %q of %q is not sentence case", l, raw)
		}
	}
	// Reserved colour: only the pending state carries the secondary tone.
	for raw := range want {
		if (stateChip(raw).Tone == "secondary") != (raw == "waiting_for_permission") {
			t.Errorf("tone secondary for %q", raw)
		}
	}
	// A value outside the table is shown as received, neutral (the template escapes it).
	if c := stateChip("weird<b>"); c.Label != "weird<b>" || c.Tone != "neutral" {
		t.Errorf("unknown value = %+v", c)
	}
}

func TestV2S1HarnessNamesAndGuardText(t *testing.T) {
	for h, w := range map[string]string{"opencode": "OpenCode", "claudecode": "Claude Code", "weird": "Other"} {
		if got := harnessName(h); got != w {
			t.Errorf("harnessName(%q) = %q, want %q", h, got, w)
		}
	}
	for _, c := range []struct {
		g    guardRow
		want string
	}{
		{guardRow{}, "None"}, {guardRow{Held: true, State: "running"}, "Held, running"},
		{guardRow{Held: true, State: "waiting_for_permission"}, "Held, waiting for permission"},
		{guardRow{Held: true, State: "unknown"}, "Held, unknown"},
	} {
		if got := guardText(c.g); got != c.want {
			t.Errorf("guardText(%+v) = %q, want %q", c.g, got, c.want)
		}
	}
}
