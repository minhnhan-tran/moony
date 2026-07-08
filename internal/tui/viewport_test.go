package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// ansiSGR matches the color/style escape sequences glamour emits. The markdown
// renderer wraps individual words (and splits on underscores) in their own
// sequences, so a marker must be reassembled by stripping these before a
// substring check.
var ansiSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiSGR.ReplaceAllString(s, "") }

// TestViewportHeightAccountsForChrome guards the invariant behind the tail-clip
// bug: the state-aware viewport height used for both rendering (View) and scroll
// math (refresh) must reserve MORE rows than resize()'s coarse estimate, which
// under-counted the blank-line-padded status row and the bordered input box. If
// these ever match again, GotoBottom() would scroll against a too-tall viewport
// and clip the last couple of lines of a long answer.
func TestViewportHeightAccountsForChrome(t *testing.T) {
	const w, h = 100, 30
	m := sized(t, newTestModel(t), w, h)

	// resize()'s coarse reservation: headerH(2) + statusH(1) + inputH(3).
	const coarse = h - 2 - 1 - 3
	got := m.viewportHeight()
	if got >= coarse {
		t.Fatalf("viewportHeight()=%d should be < resize estimate %d; the chrome height is under-counted and GotoBottom will clip the tail", got, coarse)
	}
}

// TestLongAnswerTailIsVisible reproduces the reported bug: a long assistant
// answer (taller than the viewport) had its last 2-3 lines clipped because the
// auto-follow scrolled using a viewport height that didn't match what View()
// actually drew. After refresh()+View() the final line must be on screen.
func TestLongAnswerTailIsVisible(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)

	// 40 short paragraphs (well past the ~22-row viewport) then a unique final
	// line. Separate paragraphs (blank-line delimited) survive markdown as
	// distinct lines, so the marker is genuinely the last visual line.
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "filler paragraph %02d\n\n", i)
	}
	const marker = "TAIL_MARKER_LAST_LINE"
	b.WriteString(marker)

	m.trans.add(entry{kind: entryAssistant, text: b.String()})
	m.streamingIdx = -1 // not streaming: rendered through the final markdown path
	m.refresh()

	if out := stripANSI(m.View()); !strings.Contains(out, marker) {
		t.Fatalf("last line of a long answer was clipped from the view (tail-clip regression):\n%s", out)
	}
}

// TestTailVisibleAfterBusyToIdleTransition covers the web-driven case: a turn
// ends (state flips busy->idle, the input box re-focuses) which can change the
// bottom-panel height. The final refresh must still land the answer's tail on
// screen rather than leaving it scrolled a few lines short.
func TestTailVisibleAfterBusyToIdleTransition(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)

	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "streamed line %02d\n\n", i)
	}
	const marker = "FINAL_STREAMED_LINE"
	b.WriteString(marker)

	// Simulate an in-progress streamed answer pinned to the bottom...
	idx := m.trans.add(entry{kind: entryAssistant, text: b.String()})
	m.streamingIdx = idx
	m.state = stateBusy
	m.refresh()

	// ...then the turn completing (as handleStream does on `done`).
	m.streamingIdx = -1
	m.state = stateIdle
	m.input.Focus()
	m.refresh()

	if out := stripANSI(m.View()); !strings.Contains(out, marker) {
		t.Fatalf("answer tail clipped after the busy->idle transition:\n%s", out)
	}
}
