package web

import (
	"strings"
	"testing"
)

// The header pill pickers (provider/model/reasoning) and the transcript pager's
// "n/total" dropdown share one .hdr-menu element, so the pager's arbitrarily long
// question text is what stresses the shared styling. Without a width cap the menu
// sizes to its longest label's max-content width: measured in a real browser, an
// ~85-character question stretched it to 508px against the 196px the same menu has
// when opened from a header pill.
func TestHdrMenuWidthIsCapped(t *testing.T) {
	css := readAsset(t, "static/app.css")

	menu := ruleBlock(t, css, ".hdr-menu {")
	if !strings.Contains(menu, "max-width:") {
		t.Error(".hdr-menu needs a max-width outside the phone media query, or the pager's question list stretches it")
	}
	// The cap (and the phone rule's calc(100vw - 1.5rem)) only bounds the visible
	// width if padding and borders are inside it.
	if !strings.Contains(menu, "box-sizing: border-box") {
		t.Error(".hdr-menu must be border-box so its max-width bounds the outer width")
	}
}

func TestPagerMenuRowsClampQuestions(t *testing.T) {
	css := readAsset(t, "static/app.css")
	core := readAsset(t, "static/app-core.js")

	if strings.Contains(core, "text.slice(0, 80)") {
		t.Error("openPagerMenu is hard-slicing the question again: it stops mid-word with no ellipsis, and the menu still sizes to the longest one")
	}
	if !strings.Contains(core, `"hdr-menu-item is-pager"`) {
		t.Error("pager rows must carry .is-pager so they get the row layout instead of the label/desc stack")
	}
	if !strings.Contains(core, "hdr-menu-num") {
		t.Error("the pager row's index should be its own token, not baked into the bold label")
	}
	if !strings.Contains(core, "item.title = text") {
		t.Error("the full question must stay on the row title, since the visible text is clamped")
	}

	label := ruleBlock(t, css, ".hdr-menu-item.is-pager .hdr-menu-label {")
	if !strings.Contains(label, "-webkit-line-clamp") {
		t.Error("pager question text must be line-clamped")
	}
	// A long unbroken token (a path, a URL) would otherwise force the menu wide.
	if !strings.Contains(label, "overflow-wrap: anywhere") {
		t.Error("pager question text must be able to break inside an unbroken token")
	}
}

// ruleBlock returns the declarations of the first TOP-LEVEL CSS rule whose opening
// line is sel, so a test can assert on one rule instead of the whole sheet. The
// leading newline anchor matters: rules nested in a media query are indented, and
// matching one of those instead would assert against a block that source order may
// well have made dead.
func ruleBlock(t *testing.T, css, sel string) string {
	t.Helper()
	i := strings.Index(css, "\n"+sel)
	if i < 0 {
		t.Fatalf("app.css has no top-level %q rule", sel)
	}
	rest := css[i+len(sel)+1:]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("unterminated %q rule", sel)
	}
	return rest[:end]
}
