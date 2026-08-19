package web

import (
	"regexp"
	"strings"
	"testing"
)

// The turn pager (.pair-pager) and the quick-actions split button (.quick-actions)
// float side by side over the transcript and must be exactly the same height. That
// is easy to get wrong and invisible to every other test: the pager wraps its
// buttons in its own padding, while quick-actions' halves run edge to edge, so
// sizing those halves to .pager-btn alone leaves the pill short by twice the
// pager's padding (it read 23.6px next to the pager's 30px).
//
// These assertions pin the fix to the shared --pager-* tokens rather than to a pixel
// value, so restyling one pill can't silently desync the other. The EMBEDDED copy is
// read deliberately — that's what the relay actually serves.
func TestPagerPillGeometryShared(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(b)

	for _, want := range []struct{ needle, why string }{
		{"--pager-btn:", "the shared button-box token must be declared"},
		{"--pager-pad:", "the shared pager-padding token must be declared"},
		{"--pager-pill: calc(var(--pager-btn) + 2 * var(--pager-pad))", "the pill height must stay derived from the button box plus the pager's padding"},
		{"padding: var(--pager-pad)", ".pair-pager must consume the shared padding token"},
		{"width: var(--pager-btn); height: var(--pager-btn)", ".pager-btn must consume the shared button token"},
		{"min-height: var(--pager-pill)", "the quick-actions halves must consume the derived pill height"},
	} {
		if !strings.Contains(css, want.needle) {
			t.Errorf("app.css is missing %q: %s", want.needle, want.why)
		}
	}

	// A hardcoded min-height on the quick-actions halves is exactly the regression
	// that made the two pills unequal, so reject one even if the tokens are present.
	halves := ruleBody(css, ".quick-actions-main, .quick-actions-caret")
	if halves == "" {
		t.Fatal("could not find the .quick-actions-main, .quick-actions-caret rule")
	}
	if m := regexp.MustCompile(`min-height:\s*[0-9.]`).FindString(halves); m != "" {
		t.Errorf("quick-actions halves hardcode %q; use var(--pager-pill) so they track the pager", strings.TrimSpace(m))
	}
}

// The three floating pills (turn pager, quick actions, jobs) share one right edge and
// are laid out right-to-left by shifting each past the ones beside it. That only holds
// while every pill that is NOT rightmost consumes --pill-shift, and while the widths
// come from measurement — a hardcoded offset silently overlaps as soon as a pill's
// content changes.
func TestFloatingPillStripWiring(t *testing.T) {
	css := mustReadStatic(t, "app.css")
	js := mustReadStatic(t, "app-core.js")
	html := mustReadStatic(t, "index.html")

	for _, want := range []struct{ in, needle, why string }{
		{css, "--pill-shift", "the shift token must exist"},
		{js, "function syncFloatingPills", "one function must own the strip layout"},
		{js, "getBoundingClientRect().width", "offsets must be measured, not assumed"},
		{html, `id="jobsPill"`, "the jobs pill must be its own element"},
	} {
		if !strings.Contains(want.in, want.needle) {
			t.Errorf("missing %q: %s", want.needle, want.why)
		}
	}

	// Both non-rightmost pills must actually apply the shift, or they stack on the pager.
	for _, sel := range []string{".quick-actions", ".jobs-pill"} {
		body := ruleBody(css, sel)
		if body == "" {
			t.Errorf("no %s rule", sel)
			continue
		}
		if !strings.Contains(body, "var(--pill-shift") {
			t.Errorf("%s does not consume --pill-shift, so it will sit on top of the pager", sel)
		}
	}

	// The pager is rightmost and must stay transform-free: a transform would become the
	// containing block for its "n/total" dropdown and displace it.
	if body := ruleBody(css, ".pair-pager"); strings.Contains(body, "transform:") {
		t.Error(".pair-pager must not set a transform; it is rightmost (shift 0) and a transform reparents its dropdown")
	}

	// The jobs button must no longer be a pager child, or it would be hidden along with
	// the pager and the sibling-divider rules would fight the new pill.
	if i, j := strings.Index(html, `id="pager"`), strings.Index(html, `id="jobsBtn"`); i >= 0 && j > i {
		if k := strings.Index(html[i:], "</div>"); k >= 0 && j < i+k {
			t.Error(`#jobsBtn is still inside #pager; it belongs in #jobsPill`)
		}
	}
}

// The quick-actions pill is icon-only: no visible label anywhere, in markup or CSS.
func TestQuickActionsHasNoVisibleLabel(t *testing.T) {
	for _, f := range []string{"index.html", "app.css", "app-core.js"} {
		if s := mustReadStatic(t, f); strings.Contains(s, "quick-actions-label") {
			t.Errorf("%s still references quick-actions-label; the pill shows only the bolt icon", f)
		}
	}
}

// The pager's children are laid out by DOM order (.pair-pager is a flex row), so the
// pair navigator must precede scroll-to-bottom — and the divider, which only appears
// when both halves show, has to hang off whichever comes SECOND. Mirroring one without
// the other either loses the divider or draws it on the wrong side.
func TestPagerChildOrder(t *testing.T) {
	html := mustReadStatic(t, "index.html")
	nav, scroll := strings.Index(html, `id="pagerNav"`), strings.Index(html, `id="scrollBtn"`)
	if nav < 0 || scroll < 0 {
		t.Fatal("pager is missing #pagerNav or #scrollBtn")
	}
	if scroll < nav {
		t.Error("#scrollBtn precedes #pagerNav; it belongs to the right of the pair navigator")
	}

	css := mustReadStatic(t, "app.css")
	if !strings.Contains(css, "#pagerNav:not([hidden]) + #scrollBtn:not([hidden])") {
		t.Error("the divider must select #scrollBtn as the sibling AFTER #pagerNav to match the DOM order")
	}
	if strings.Contains(css, "#scrollBtn:not([hidden]) + #pagerNav") {
		t.Error("stale divider rule pointing the old direction (#scrollBtn before #pagerNav)")
	}
	// .pager-btn is a fixed-width border-box, so a divider that adds border+padding must
	// restate the width or the chevron gets squeezed off-centre instead of the pill growing.
	body := ruleBody(css, "#pagerNav:not([hidden]) + #scrollBtn:not([hidden])")
	if strings.Contains(body, "padding-left") && !strings.Contains(body, "width:") {
		t.Error("the divider adds padding to a fixed-width .pager-btn without restating width")
	}
}

func mustReadStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// ruleBody returns the declarations of the first CSS rule whose selector matches
// exactly, or "" when absent.
func ruleBody(css, selector string) string {
	i := strings.Index(css, selector+" {")
	if i < 0 {
		return ""
	}
	rest := css[i+len(selector)+2:]
	if j := strings.IndexByte(rest, '}'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// Standing rule: .pair-q always spans 100% of its parent, and so does the question
// inside it. It is easy to lose by accident — .msg-user caps its children to a
// right-hugging chat bubble (max-width: min(86%, 40rem)) for the scrolling transcript,
// and the pinned header inherits that cap unless it is reset, which leaves the question
// misaligned with the answer beneath it.
func TestPairQuestionSpansItsParent(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := strings.ReplaceAll(string(b), "\r\n", "\n")

	block := cssBlock(t, css, ".pair-q {")
	if !regexp.MustCompile(`width:\s*100%`).MatchString(block) {
		t.Error(".pair-q must declare width: 100% — it spans its parent, always")
	}
	if !strings.Contains(block, "box-sizing: border-box") {
		t.Error(".pair-q has padding, so it needs box-sizing: border-box to stay exactly 100% wide")
	}
	if regexp.MustCompile(`max-width:\s*(?:\d|min|calc)`).MatchString(block) {
		t.Error(".pair-q must not cap its own width")
	}

	// The content inside must not re-narrow it.
	inner := cssBlock(t, css, ".pair-q > .msg > * {")
	if !regexp.MustCompile(`width:\s*100%`).MatchString(inner) || !regexp.MustCompile(`max-width:\s*none`).MatchString(inner) {
		t.Error("the question inside .pair-q must reset the .msg-user bubble cap to width:100%/max-width:none")
	}
}

// cssBlock returns the declarations of the first rule whose selector line matches.
func cssBlock(t *testing.T, css, selector string) string {
	t.Helper()
	i := strings.Index(css, selector)
	if i < 0 {
		t.Fatalf("selector %q not found in app.css", selector)
	}
	j := strings.Index(css[i:], "}")
	if j < 0 {
		t.Fatalf("unterminated rule for %q", selector)
	}
	return css[i : i+j]
}
