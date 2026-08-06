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
