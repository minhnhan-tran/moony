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
