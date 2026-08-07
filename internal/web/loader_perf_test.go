package web

import (
	"strings"
	"testing"
)

// Reopening a session replays its whole history as a burst of SSE events, and the
// jumping-Claude loader is on screen while that lands. Two things used to make it
// stutter, and both are easy to undo by accident:
//
//   - the loader was an <img> of claude-jumping.svg. An SVG drawn as an image is
//     re-rasterized on the MAIN thread every frame, so its keyframes freeze
//     whenever JS is busy. Inline, the transforms animate on the compositor.
//   - every replayed turn re-measured the pager and the scroll position, forcing
//     ~3 synchronous layouts each on a transcript that grows per turn (O(n²)).
//
// These assert the shape of the fix, since neither is visible to a unit test.
func TestTranscriptLoaderIsInlineSVG(t *testing.T) {
	html := readAsset(t, "static/index.html")
	css := readAsset(t, "static/app.css")
	core := readAsset(t, "static/app-core.js")

	if !strings.Contains(html, `id="jumpArtTpl"`) {
		t.Error("index.html lost the inline loader template; showConnecting would fall back to the <img>")
	}
	if !strings.Contains(core, "jumpArtTpl") || !strings.Contains(core, "cloneNode(true)") {
		t.Error("showConnecting must clone the inline template, not load the SVG as an image")
	}
	// The template's classes are ja-* prefixed on purpose: an inline SVG shares the
	// page stylesheet, so the original's bare .shadow / .left-arm would leak.
	for _, cls := range []string{"ja-body", "ja-shadow", "ja-arm-l", "ja-arm-r", "ja-ear-l", "ja-ear-r"} {
		if !strings.Contains(html, cls) {
			t.Errorf("loader template is missing the %q part", cls)
		}
		if !strings.Contains(css, ".jump-art ."+cls) {
			t.Errorf("app.css does not scope %q under .jump-art", cls)
		}
	}
	for _, kf := range []string{"ja-jump", "ja-shadow", "ja-wave-l", "ja-wave-r", "ja-ear"} {
		if !strings.Contains(css, "@keyframes "+kf) {
			t.Errorf("app.css is missing the %q keyframes", kf)
		}
	}
	if !strings.Contains(css, "will-change: transform") {
		t.Error("the animated parts should be promoted with will-change so they composite")
	}
	// Every other looping animation in this file opts out; this one must too.
	if !hasReducedMotionFor(css, "ja-body") {
		t.Error("the loader animation must be disabled under prefers-reduced-motion")
	}
}

func TestHistoryReplayDefersLayout(t *testing.T) {
	core := readAsset(t, "static/app-core.js")
	render := readAsset(t, "static/app-render.js")

	if strings.Contains(core, `for (let i = 0; i < pairs.length; i++) pairs[i].root.classList.toggle("pair-active"`) {
		t.Error("setActivePair is back to toggling every pair: .pair is display:none/flex, so that rebuilds a box tree per pair")
	}
	if !strings.Contains(core, "pagerFlushPending") || !strings.Contains(render, "scrollFlushPending") {
		t.Error("the replay deferral flags are gone; pager/scroll work would run per replayed turn again")
	}
	if !strings.Contains(core, "function flushReplayLayout") {
		t.Fatal("flushReplayLayout is missing")
	}
	// The flush has to be wired to history_end, which the server always emits
	// (server.go / mirror.go) — otherwise the deferred work never happens.
	end := strings.Index(core, `case "history_end"`)
	if end < 0 {
		t.Fatal(`no "history_end" case in the event switch`)
	}
	if !strings.Contains(core[end:min(end+400, len(core))], "flushReplayLayout()") {
		t.Error("history_end must flush the layout work the replay deferred")
	}
}

// hasReducedMotionFor reports whether some @media (prefers-reduced-motion) block
// mentions sel.
func hasReducedMotionFor(css, sel string) bool {
	for i := 0; ; {
		j := strings.Index(css[i:], "prefers-reduced-motion")
		if j < 0 {
			return false
		}
		i += j
		end := strings.Index(css[i:], "\n}")
		if end < 0 {
			return false
		}
		if strings.Contains(css[i:i+end], sel) {
			return true
		}
		i += end
	}
}
