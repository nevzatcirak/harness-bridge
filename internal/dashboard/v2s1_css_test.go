package dashboard

// ADR-023 A1.10 items 2, 4, 6, 7, 8 and 13 (slice S1): the embedded stylesheet is checked as code. A tiny
// CSS reader (comments stripped, rules and at-rules split) feeds the scans and the WCAG computation, so a
// token or a rule cannot drift from what A1.2 documents.

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type cssRule struct {
	Sel  string // the selector, or the at-rule header
	Body string // the declarations, or the nested rules of an at-rule
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// parseCSS splits a stylesheet into its top-level rules; an at-rule keeps its nested text in Body.
func parseCSS(src string) []cssRule {
	src = cssComment.ReplaceAllString(src, "")
	var out []cssRule
	depth, start, bodyStart := 0, 0, 0
	var sel string
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '{':
			if depth == 0 {
				sel = strings.TrimSpace(src[start:i])
				bodyStart = i + 1
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				out = append(out, cssRule{Sel: sel, Body: src[bodyStart:i]})
				start = i + 1
			}
		}
	}
	return out
}

// flatRules returns every rule with its at-rule nesting resolved (a rule inside @media is returned as is).
func flatRules(src string) []cssRule {
	var out []cssRule
	for _, r := range parseCSS(src) {
		if strings.HasPrefix(r.Sel, "@media") || strings.HasPrefix(r.Sel, "@supports") {
			out = append(out, cssRule{Sel: r.Sel, Body: ""})
			for _, n := range parseCSS(r.Body) {
				n.Sel = r.Sel + " >> " + n.Sel
				out = append(out, n)
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func readCSS(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var cssProp = regexp.MustCompile(`(--[a-z0-9-]+)\s*:\s*([^;]+);`)

// rootTokens returns the custom properties of the (unconditional) :root rules of a stylesheet.
func rootTokens(src string) map[string]string {
	tok := map[string]string{}
	for _, r := range parseCSS(src) {
		if r.Sel != ":root" {
			continue
		}
		for _, m := range cssProp.FindAllStringSubmatch(r.Body, -1) {
			tok[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return tok
}

type rgba struct{ r, g, b, a float64 }

func parseColour(t *testing.T, v string) rgba {
	t.Helper()
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "#") && len(v) == 7 {
		n, err := strconv.ParseUint(v[1:], 16, 32)
		if err != nil {
			t.Fatalf("colour %q", v)
		}
		return rgba{float64(n >> 16 & 255), float64(n >> 8 & 255), float64(n & 255), 1}
	}
	m := regexp.MustCompile(`^rgb\((\d+) (\d+) (\d+)(?: / ([0-9.]+))?\)$`).FindStringSubmatch(v)
	if m == nil {
		t.Fatalf("unsupported colour %q", v)
	}
	f := func(s string) float64 { x, _ := strconv.ParseFloat(s, 64); return x }
	a := 1.0
	if m[4] != "" {
		a = f(m[4])
	}
	return rgba{f(m[1]), f(m[2]), f(m[3]), a}
}

// over composites c over an opaque background, exactly (no rounding of the tint colour).
func (c rgba) over(bg rgba) rgba {
	return rgba{c.a*c.r + (1-c.a)*bg.r, c.a*c.g + (1-c.a)*bg.g, c.a*c.b + (1-c.a)*bg.b, 1}
}

func lum(c rgba) float64 {
	f := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.r) + 0.7152*f(c.g) + 0.0722*f(c.b)
}

func ratio(a, b rgba) float64 {
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

const embeddedCSS = "assets/dashboard.css"
const referenceCSS = "../../docs/design/dashboard-v2.css"

// Theme blocks of the stylesheet (A1.1, A1.6 JS-2): the dark tokens are the unconditional :root; the light theme is
// a prefers-color-scheme block (unless the dark choice is made) and the block of the toggle's explicit choice. The two
// light blocks must be identical.
const (
	lightMediaSel  = `@media (prefers-color-scheme: light) >> :root:not([data-theme="dark"])`
	lightToggleSel = `:root[data-theme="light"]`
	darkToggleSel  = `:root[data-theme="dark"]`
)

// isTokenBlock reports whether a flattened selector is one of the blocks that define the colour tokens, where a
// literal colour is the point (Floor 1) and the secondary tokens are defined (the reserved-colour rule).
func isTokenBlock(sel string) bool {
	return sel == ":root" || sel == lightMediaSel || sel == lightToggleSel || sel == darkToggleSel
}

// blockTokens returns the custom properties of the one rule with the given flattened selector.
func blockTokens(src, sel string) map[string]string {
	tok := map[string]string{}
	for _, r := range flatRules(src) {
		if r.Sel != sel {
			continue
		}
		for _, m := range cssProp.FindAllStringSubmatch(r.Body, -1) {
			tok[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return tok
}

// lightTokens is the dark :root with the light block laid over it (the tokens the light theme really draws).
func lightTokens(src, block string) map[string]string {
	tok := rootTokens(src)
	for k, v := range blockTokens(src, block) {
		tok[k] = v
	}
	return tok
}

// contrastReport checks every pair of A1.2 for one theme's tokens, >= 4.5 for text and >= 3 for non-text, computed
// from the embedded stylesheet itself (so a token that changes fails here), including the states a component draws.
// It returns the minima and the number of pairs checked.
func contrastReport(t *testing.T, theme string, tok map[string]string) (minText, minNon float64, checks int) {
	t.Helper()
	col := func(name string) rgba {
		v, ok := tok["--"+name]
		if !ok {
			t.Fatalf("token --%s is missing from the embedded stylesheet", name)
		}
		return parseColour(t, v)
	}
	surfaces := []string{"bg", "surface-low", "surface", "surface-high"}
	minText, minNon = 99.0, 99.0
	var failures []string
	check := func(name string, got, floor float64, text bool) {
		checks++
		if got < floor {
			failures = append(failures, fmt.Sprintf("%s (%s) %.3f < %.1f", name, theme, got, floor))
		}
		if text && got < minText {
			minText = got
		}
		if !text && got < minNon {
			minNon = got
		}
	}
	for _, s := range surfaces {
		bg := col(s)
		check("text on "+s, ratio(col("text"), bg), 4.5, true)
		check("muted on "+s, ratio(col("text-muted"), bg), 4.5, true)
		for _, tone := range []string{"ok", "info", "secondary", "error", "warn", "neutral"} {
			chipText := col("chip-" + tone)
			tint := col("tint-" + tone)
			if math.Abs(tint.a-0.14) > 1e-9 || tint.r != chipText.r || tint.g != chipText.g || tint.b != chipText.b {
				t.Errorf("--tint-%s is not --chip-%s at alpha 0.14: %+v vs %+v", tone, tone, tint, chipText)
			}
			check("chip "+tone+" on "+s, ratio(chipText, tint.over(bg)), 4.5, true)
		}
		check("input border on "+s, ratio(col("input-border"), bg), 3, false)
		check("focus ring on "+s, ratio(col("primary"), bg), 3, false)
	}
	for _, tone := range []string{"ok", "info", "error", "warn", "secondary"} {
		check("semantic "+tone+" on card", ratio(col(tone), col("surface")), 4.5, true)
		check("semantic "+tone+" on low", ratio(col(tone), col("surface-low")), 4.5, true)
	}
	// The pressed primary button: the fill the rule really draws (resolved from the stylesheet, not assumed).
	pressed := ""
	for _, r := range flatRules(readCSS(t, embeddedCSS)) {
		if r.Sel == ".btn--primary:active" {
			if m := regexp.MustCompile(`background:\s*var\(--([a-z-]+)\)`).FindStringSubmatch(r.Body); m != nil {
				pressed = m[1]
			}
		}
	}
	if pressed == "" {
		t.Fatal(".btn--primary:active draws no background token")
	}
	if pressed != "primary-active" {
		t.Errorf(".btn--primary:active draws --%s, A1.2 documents --primary-active", pressed)
	}
	check("text on the fill a pressed primary button draws", ratio(col("on-primary"), col(pressed)), 4.5, true)
	check("text on primary", ratio(col("on-primary"), col("primary")), 4.5, true)
	check("text on the pressed primary fill", ratio(col("on-primary"), col("primary-active")), 4.5, true)
	// The pressed fill is also a non-text boundary against the surface it sits on (the toolbar and the card).
	check("pressed fill on the toolbar", ratio(col("primary-active"), col("surface-low")), 3, false)
	check("pressed fill on the card", ratio(col("primary-active"), col("surface")), 3, false)
	// Fills a component draws.
	hover := col("tint-neutral")
	for _, s := range surfaces {
		check("text on a row or nav hover on "+s, ratio(col("text"), hover.over(col(s))), 4.5, true)
	}
	check("text on the pressed (hairline) fill", ratio(col("text"), col("hairline")), 4.5, true)
	for _, kind := range []string{"ok", "error"} { // the diff add and delete lines, on the card
		fill := col("tint-" + kind).over(col("surface"))
		check("text on a diff "+kind+" line", ratio(col("text"), fill), 4.5, true)
		check("muted text on a diff "+kind+" line", ratio(col("text-muted"), fill), 4.5, true)
	}
	for _, s := range []string{"surface", "surface-low"} { // the pending-permission row (a tint of the secondary colour)
		row := col("tint-secondary").over(col(s))
		check("text on the pending row on "+s, ratio(col("text"), row), 4.5, true)
		check("muted text on the pending row on "+s, ratio(col("text-muted"), row), 4.5, true)
		check("outline chip in the pending row on "+s, ratio(col("chip-secondary"), row), 4.5, true)
	}
	check("archived outline chip border against a card", ratio(col("chip-neutral"), col("surface")), 3, false)
	for _, f := range failures {
		t.Error(f)
	}
	if checks < 70 { // A1.2: 124 token pairs and 26 fill pairs over both themes, so about 75 per theme
		t.Errorf("only %d pairs were checked for the %s theme", checks, theme)
	}
	if minNon < 3 {
		t.Errorf("minimum non-text pair of the %s theme = %.3f", theme, minNon)
	}
	return minText, minNon, checks
}

// A1.10 item 2, dark theme.
func TestV2S1ContrastOverTheDarkTokens(t *testing.T) {
	minText, _, _ := contrastReport(t, "dark", rootTokens(readCSS(t, embeddedCSS)))
	// A1.2 documents this minimum; a token change that moves it must change the ADR with it.
	if math.Abs(minText-4.62) > 0.005 {
		t.Errorf("minimum text pair = %.3f, A1.2 documents 4.62", minText)
	}
}

// A1.10 item 2, light theme (slice S3b): the same pairs over the light tokens, for the media-query block and for the
// block of the toggle's explicit choice, which A1.2 says are identical. The documented minima are the least text pair
// of the light theme (the error chip on the low surface) and the least non-text pair of both themes (the light input
// border on the low surface, 4.06).
func TestS3bContrastOverTheLightTokens(t *testing.T) {
	src := readCSS(t, embeddedCSS)
	for _, block := range []string{lightMediaSel, lightToggleSel} {
		tok := lightTokens(src, block)
		if len(blockTokens(src, block)) < 25 {
			t.Fatalf("the light block %q has %d tokens: it is missing", block, len(blockTokens(src, block)))
		}
		minText, minNon, _ := contrastReport(t, "light "+block, tok)
		if math.Abs(minText-4.65) > 0.005 {
			t.Errorf("%s: minimum text pair = %.3f, A1.2 documents 4.65 (the error chip on the low surface)", block, minText)
		}
		if math.Abs(minNon-4.06) > 0.005 {
			t.Errorf("%s: minimum non-text pair = %.3f, A1.2 documents 4.06 (the input border on the low surface)", block, minNon)
		}
	}
}

// The documented values of A1.2 (dark side), reproduced to two decimals: the table cannot drift from the file.
func TestV2S1ContrastReproducesTheDocumentedFigures(t *testing.T) {
	tok := rootTokens(readCSS(t, embeddedCSS))
	col := func(name string) rgba { return parseColour(t, tok["--"+name]) }
	surf := map[string]rgba{"bg": col("bg"), "low": col("surface-low"), "mid": col("surface"), "high": col("surface-high")}
	chip := func(tone, s string) float64 {
		return ratio(col("chip-"+tone), col("tint-"+tone).over(surf[s]))
	}
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"text on bg", ratio(col("text"), surf["bg"]), 14.19}, {"text on high", ratio(col("text"), surf["high"]), 11.76},
		{"muted on bg", ratio(col("text-muted"), surf["bg"]), 7.70}, {"muted on high", ratio(col("text-muted"), surf["high"]), 6.38},
		{"chip ok on bg", chip("ok", "bg"), 7.25}, {"chip info on mid", chip("info", "mid"), 5.81},
		{"chip secondary on high", chip("secondary", "high"), 4.62}, {"chip error on high", chip("error", "high"), 5.51},
		{"chip warn on low", chip("warn", "low"), 7.47}, {"chip neutral on mid", chip("neutral", "mid"), 8.73},
		{"input border on low", ratio(col("input-border"), surf["low"]), 5.04}, {"focus ring on bg", ratio(col("primary"), surf["bg"]), 8.64},
		{"text on primary", ratio(col("on-primary"), col("primary")), 7.79}, {"text on pressed primary", ratio(col("on-primary"), col("primary-active")), 6.18},
		{"pressed fill on low", ratio(col("primary-active"), surf["low"]), 6.37}, {"pressed fill on card", ratio(col("primary-active"), surf["mid"]), 6.10},
		{"text on the pressed hairline fill", ratio(col("text"), col("hairline")), 9.57},
		{"semantic ok on card", ratio(col("ok"), surf["mid"]), 8.28},
	} {
		if math.Abs(c.got-c.want) > 0.006 {
			t.Errorf("%s = %.3f, A1.2 documents %.2f", c.name, c.got, c.want)
		}
	}
}

// The documented values of A1.2 (light side), reproduced to two decimals: the table cannot drift from the file.
func TestS3bContrastReproducesTheDocumentedLightFigures(t *testing.T) {
	tok := lightTokens(readCSS(t, embeddedCSS), lightToggleSel)
	col := func(name string) rgba {
		v, ok := tok["--"+name]
		if !ok {
			t.Fatalf("token --%s is missing", name)
		}
		return parseColour(t, v)
	}
	surf := map[string]rgba{"bg": col("bg"), "low": col("surface-low"), "mid": col("surface"), "high": col("surface-high")}
	chip := func(tone, s string) float64 {
		return ratio(col("chip-"+tone), col("tint-"+tone).over(surf[s]))
	}
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"text on bg", ratio(col("text"), surf["bg"]), 15.43}, {"text on high", ratio(col("text"), surf["high"]), 16.10},
		{"muted on bg", ratio(col("text-muted"), surf["bg"]), 6.71}, {"muted on high", ratio(col("text-muted"), surf["high"]), 7.00},
		{"chip ok on bg", chip("ok", "bg"), 6.05}, {"chip info on mid", chip("info", "mid"), 5.91},
		{"chip secondary on high", chip("secondary", "high"), 5.92}, {"chip error on high", chip("error", "high"), 5.17},
		{"chip error on low", chip("error", "low"), 4.65}, {"chip warn on low", chip("warn", "low"), 4.67},
		{"chip neutral on mid", chip("neutral", "mid"), 12.19},
		{"input border on low", ratio(col("input-border"), surf["low"]), 4.06}, {"focus ring on bg", ratio(col("primary"), surf["bg"]), 4.69},
		{"text on primary", ratio(col("on-primary"), col("primary")), 4.90}, {"text on pressed primary", ratio(col("on-primary"), col("primary-active")), 6.70},
		{"pressed fill on low", ratio(col("primary-active"), surf["low"]), 5.99}, {"pressed fill on card", ratio(col("primary-active"), surf["mid"]), 6.70},
		{"text on the pressed hairline fill", ratio(col("text"), col("hairline")), 12.20},
		{"semantic ok on card", ratio(col("ok"), surf["mid"]), 7.87},
	} {
		if math.Abs(c.got-c.want) > 0.006 {
			t.Errorf("%s = %.3f, A1.2 documents %.2f", c.name, c.got, c.want)
		}
	}
}

// Slice S3b: the two light blocks are identical to each other and to the reference file's, every colour token of the
// dark theme has a light value (a missing one would leave a dark colour on a light page), and no non-colour token is
// redefined (the light theme changes colour only).
func TestS3bLightBlocksAreIdenticalAndEqualTheReference(t *testing.T) {
	emb, ref := readCSS(t, embeddedCSS), readCSS(t, referenceCSS)
	for _, block := range []string{lightMediaSel, lightToggleSel} {
		e, r := blockTokens(emb, block), blockTokens(ref, block)
		if len(e) < 25 {
			t.Fatalf("the embedded light block %q has %d tokens", block, len(e))
		}
		for k, v := range r {
			if e[k] != v {
				t.Errorf("%s: token %s: embedded %q, reference %q", block, k, e[k], v)
			}
		}
		for k := range e {
			if _, ok := r[k]; !ok {
				t.Errorf("%s: token %s is embedded but not in the reference", block, k)
			}
		}
	}
	media, toggle := blockTokens(emb, lightMediaSel), blockTokens(emb, lightToggleSel)
	for k, v := range media {
		if toggle[k] != v {
			t.Errorf("the media block and the toggle block differ at %s: %q vs %q", k, v, toggle[k])
		}
	}
	for k := range toggle {
		if _, ok := media[k]; !ok {
			t.Errorf("token %s is in the toggle block only", k)
		}
	}
	colour := regexp.MustCompile(`^(#[0-9a-fA-F]{6}|rgb\()`)
	for k, v := range rootTokens(emb) {
		_, overridden := toggle[k]
		switch {
		case colour.MatchString(v) && !overridden:
			t.Errorf("the dark colour token %s has no light value", k)
		case !colour.MatchString(v) && overridden:
			t.Errorf("the light block redefines the non-colour token %s", k)
		}
	}
}

// D31 Q10: the docs file is the reference and the embedded stylesheet is its comment-free copy. The tokens of the
// dark :root block must stay identical.
func TestV2S1EmbeddedTokensEqualTheReference(t *testing.T) {
	emb := rootTokens(readCSS(t, embeddedCSS))
	ref := rootTokens(readCSS(t, referenceCSS))
	if len(emb) < 40 {
		t.Fatalf("the embedded stylesheet has %d tokens", len(emb))
	}
	for k, v := range ref {
		if emb[k] != v {
			t.Errorf("token %s: embedded %q, reference %q", k, emb[k], v)
		}
	}
	for k := range emb {
		if _, ok := ref[k]; !ok {
			t.Errorf("token %s is in the embedded stylesheet but not in the reference", k)
		}
	}
}

// Slice S3b: both themes. The unconditional :root is dark (the default) and declares both colour schemes, the light theme
// is a prefers-color-scheme block unless the dark choice is made, and the toggle's explicit choices set color-scheme too
// (so the native controls follow the toggled theme: review #486 P3-2).
func TestS3bStylesheetCarriesBothThemes(t *testing.T) {
	css := cssComment.ReplaceAllString(readCSS(t, embeddedCSS), "")
	if !regexp.MustCompile(`:root\s*\{[^}]*color-scheme:\s*dark light;`).MatchString(css) {
		t.Error(":root does not declare color-scheme: dark light")
	}
	flat := flatRules(css)
	want := map[string]string{
		lightMediaSel:  "color-scheme: light",
		lightToggleSel: "color-scheme: light",
		darkToggleSel:  "color-scheme: dark",
	}
	for sel, decl := range want {
		found := false
		for _, r := range flat {
			if r.Sel == sel && strings.Contains(r.Body, decl) {
				found = true
			}
		}
		if !found {
			t.Errorf("no rule %q with %q", sel, decl)
		}
	}
	// the toggle blocks come after the media block, so an explicit choice wins over the system preference
	if strings.Index(css, `:root[data-theme="light"]`) < strings.Index(css, "prefers-color-scheme: light") {
		t.Error("the toggle blocks are before the media block")
	}
}

// A1.10 item 4: no external URL, no import, no font face, no url() in the code of the stylesheet.
func TestV2S1StylesheetHasNoExternalReference(t *testing.T) {
	css := cssComment.ReplaceAllString(readCSS(t, embeddedCSS), "")
	for _, bad := range []string{"@import", "@font-face", "url(", "http:", "https:", "//"} {
		if strings.Contains(css, bad) {
			t.Errorf("the stylesheet contains %q", bad)
		}
	}
}

// A1.10 items 7 and 8: Hallmark floors 1, 2, 4 and 9 over the stylesheet.
func TestV2S1StylesheetTypographyAndFloors(t *testing.T) {
	src := cssComment.ReplaceAllString(readCSS(t, embeddedCSS), "")
	for _, re := range []struct{ re, what string }{
		{`text-transform\s*:\s*(uppercase|capitalize)`, "text-transform uppercase/capitalize"},
		{`letter-spacing`, "letter-spacing"},
		{`font-variant(-caps)?\s*:\s*[^;]*small-caps`, "small-caps"},
		{`transition\s*:\s*all`, "transition: all"},
		{`100vw`, "100vw"},
		{`@keyframes`, "a keyframes animation"},
	} {
		if regexp.MustCompile(re.re).MatchString(src) {
			t.Errorf("the stylesheet uses %s", re.what)
		}
	}
	// Transitions are state only: colour and background colour, nothing that moves or resizes.
	for _, m := range regexp.MustCompile(`transition\s*:\s*([^;]+);`).FindAllStringSubmatch(src, -1) {
		for _, prop := range regexp.MustCompile(`(?:^|,)\s*([a-z-]+)`).FindAllStringSubmatch(m[1], -1) {
			switch prop[1] {
			case "color", "background-color", "border-color":
			default:
				if prop[1] != "none" {
					t.Errorf("transition of %q in %q", prop[1], m[1])
				}
			}
		}
	}
	// No literal colour or font-family outside the token blocks.
	lit := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(|hwb\(|oklch\(|oklab\(|color\(`)
	for _, r := range flatRules(src) {
		if isTokenBlock(r.Sel) {
			continue
		}
		if lit.MatchString(r.Body) {
			t.Errorf("a literal colour outside the token block, in %q: %s", r.Sel, lit.FindString(r.Body))
		}
		for _, m := range regexp.MustCompile(`font-family\s*:\s*([^;]+);`).FindAllStringSubmatch(r.Body, -1) {
			if !strings.HasPrefix(strings.TrimSpace(m[1]), "var(") {
				t.Errorf("a literal font-family outside the token block, in %q: %s", r.Sel, m[1])
			}
		}
	}
	// overflow-x: clip on html and body.
	for _, sel := range []string{"html", "body"} {
		found := false
		for _, r := range flatRules(src) {
			if r.Sel == sel && regexp.MustCompile(`overflow-x\s*:\s*clip`).MatchString(r.Body) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no overflow-x: clip", sel)
		}
	}
}

// A1.1 reserved colour and A1.10 item 6: only the pending selectors refer to the secondary tokens.
func TestV2S1SecondaryColourIsReservedForPending(t *testing.T) {
	allowed := map[string]bool{".chip--secondary": true, ".timeline .perm-row td": true, ".perm-row .chip": true}
	src := readCSS(t, embeddedCSS)
	n := 0
	for _, r := range flatRules(src) {
		if isTokenBlock(r.Sel) || !regexp.MustCompile(`--(chip-|tint-)?secondary\b`).MatchString(r.Body) {
			continue
		}
		n++
		if !allowed[r.Sel] {
			t.Errorf("selector %q refers to a secondary token", r.Sel)
		}
	}
	if n == 0 {
		t.Error("no rule uses the secondary tokens: the pending chip would not be tinted")
	}
}

// A1.10 item 13 (static half): min-height only on text-bearing controls; the wrapper has the scroll padding and a floor.
func TestV2S1StylesheetAccessibilityRules(t *testing.T) {
	src := readCSS(t, embeddedCSS)
	height := regexp.MustCompile(`(?:^|[;\s{])height\s*:`)
	for _, r := range flatRules(src) {
		isCtl := regexp.MustCompile(`\.(chip|badge|input|select|btn)\b`).MatchString(r.Sel)
		if isCtl && height.MatchString(r.Body) {
			t.Errorf("a fixed height on %q (use min-height)", r.Sel)
		}
	}
	var wrap string
	for _, r := range flatRules(src) {
		if r.Sel == ".table-wrap" {
			wrap = r.Body
		}
	}
	for _, want := range []string{"scroll-padding-top", "max-height: max(", "overflow: auto"} {
		if !strings.Contains(wrap, want) {
			t.Errorf(".table-wrap lacks %q", want)
		}
	}
	for _, want := range []string{"prefers-reduced-motion: reduce", "forced-colors: active", ":focus-visible", "pointer: coarse", "max-width: 60rem"} {
		if !strings.Contains(src, want) {
			t.Errorf("the stylesheet lacks %q", want)
		}
	}
	if !regexp.MustCompile(`\.btn--primary:active\s*\{[^}]*--primary-active`).MatchString(src) {
		t.Error("a pressed primary button does not use the pressed primary fill (review #480 P2-1)")
	}
	// The pressed primary rule must come after .btn:active (same specificity: the later rule wins).
	if strings.Index(src, ".btn--primary:active") < strings.Index(src, ".btn:active") {
		t.Error(".btn--primary:active is before .btn:active")
	}
}
