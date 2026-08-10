package web

// The modal shell is a max-height flex column: header / tab strip / body. The
// strip sets overflow-x:auto so many tabs can scroll sideways, and that ALSO
// suppresses its automatic minimum size — so as a flex item with the default
// flex-shrink:1 it collapsed (measured 43.8px -> 16.5px, clipping the tab
// buttons) the moment the body was tall enough to overflow the modal. Any panel
// with a long list did it; the Memory tab just made it easy to hit.
//
// Pinning the flex declarations here because this is pure inline layout with no
// JS to assert against, and the failure is silent: nothing errors, the tabs just
// become unreadable.

import (
	"regexp"
	"testing"
)

func TestModalShellFlexSizing(t *testing.T) {
	html := staticAsset(t, "index.html")

	for _, tc := range []struct {
		name, elem, want string
	}{
		// The strip must not shrink, or the tabs get squeezed to a sliver.
		{"tab strip", `<div id="manageTabs"[^>]*>`, `flex:0\s*0\s*auto`},
		// The body is the only part that should absorb the leftover space, and it
		// needs min-height:0 so it can actually shrink instead of pushing its
		// siblings (min-height:auto on an overflow container is the classic trap).
		{"body", `<div id="manageBody"[^>]*>`, `flex:1\s*1\s*auto`},
		{"body min-height", `<div id="manageBody"[^>]*>`, `min-height:\s*0`},
	} {
		tag := regexp.MustCompile(tc.elem).FindString(html)
		if tag == "" {
			t.Fatalf("%s: element not found in index.html", tc.name)
		}
		if !regexp.MustCompile(tc.want).MatchString(tag) {
			t.Errorf("%s should declare %s; got:\n%s", tc.name, tc.want, tag)
		}
	}

	// The tab strip keeps its overflow scroller (that's why it needs flex:0 0 auto
	// in the first place) — if this ever goes away the sizing comment is stale.
	strip := regexp.MustCompile(`<div id="manageTabs"[^>]*>`).FindString(html)
	if !regexp.MustCompile(`overflow-x-auto`).MatchString(strip) {
		t.Error("tab strip is expected to scroll horizontally (overflow-x-auto)")
	}
	// It starts hidden: only tabbed modals show it.
	if !regexp.MustCompile(`display:\s*none`).MatchString(strip) {
		t.Error("tab strip should start hidden")
	}
}

// The header row is a sibling in the same column and had the same latent bug.
func TestModalHeaderDoesNotShrink(t *testing.T) {
	html := staticAsset(t, "index.html")
	hdr := regexp.MustCompile(`<div class="px-4 py-3 border-b border-edge flex items-center gap-2"[^>]*>`).FindString(html)
	if hdr == "" {
		t.Fatal("modal header row not found")
	}
	if !regexp.MustCompile(`flex:0\s*0\s*auto`).MatchString(hdr) {
		t.Errorf("the modal header should not shrink; got:\n%s", hdr)
	}
}

// renderTabStrip toggles display on the strip; it must not also rewrite the flex
// shorthand, or it would clobber the inline sizing above.
func TestTabStripJSOnlyTogglesDisplay(t *testing.T) {
	core := staticAsset(t, "app-core.js")
	if regexp.MustCompile(`manageTabsEl\.style\.flex\b`).MatchString(core) {
		t.Error("renderTabStrip should leave flex sizing to the markup, not set style.flex")
	}
	if !regexp.MustCompile(`manageTabsEl\.style\.display\s*=\s*"flex"`).MatchString(core) {
		t.Error("renderTabStrip is expected to reveal the strip with display:flex")
	}
}
