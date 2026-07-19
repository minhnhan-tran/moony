package tui

import (
	"strings"
	"testing"
)

// brokenRenderer panics on every call so we can prove the cache short-circuits
// the markdown path on subsequent renders.
type brokenRenderer struct{}

// renderOrPanic is the small helper used by transcript.markdown (which uses
// glamour). We can't replace glamour wholesale here, so we test the cache
// behavior by manually setting the cache fields and checking that render uses
// them when the width matches.

func TestRenderCachesAssistantMarkdown(t *testing.T) {
	tr := &transcript{width: 80}
	idx := tr.add(entry{kind: entryAssistant, text: "**hello**"})

	// First render computes & caches.
	first := tr.render(-1)
	if !strings.Contains(first, "hello") {
		t.Fatalf("expected first render to contain body, got:\n%s", first)
	}
	if tr.entries[idx].rendered == "" {
		t.Fatalf("expected the cache to be populated after first render")
	}
	cached := tr.entries[idx].rendered

	// Second render with the same width MUST reuse the cache verbatim. Replace
	// the entry text with garbage to prove the cache (not a fresh render) is
	// what produces the output.
	tr.entries[idx].text = "DIFFERENT — should not appear"
	second := tr.render(-1)
	if !strings.Contains(second, strings.TrimSpace(cached)) {
		t.Fatalf("expected cached body to be reused; got:\n%s", second)
	}
	if strings.Contains(second, "DIFFERENT") {
		t.Fatalf("cache miss: render used new text, not cached output:\n%s", second)
	}
}

func TestRenderInvalidatesCacheOnWidthChange(t *testing.T) {
	tr := &transcript{width: 80}
	idx := tr.add(entry{kind: entryAssistant, text: "hello"})
	tr.render(-1)
	wRendered := tr.entries[idx].renderedW
	if wRendered != 80 {
		t.Fatalf("renderedW = %d, want 80", wRendered)
	}

	// Width change should cause a re-render (which may or may not produce a
	// different string; we only check that renderedW gets updated).
	tr.width = 60
	tr.render(-1)
	if tr.entries[idx].renderedW != 60 {
		t.Errorf("expected renderedW to follow width to 60, got %d", tr.entries[idx].renderedW)
	}
}

func TestStreamingEntryNotCached(t *testing.T) {
	tr := &transcript{width: 80}
	idx := tr.add(entry{kind: entryAssistant, text: "first chunk"})
	tr.render(idx) // streaming
	if tr.entries[idx].rendered != "" {
		t.Fatal("streaming entry should not be cached (its text changes)")
	}
}

func TestMouseWheelDeltaDefault(t *testing.T) {
	t.Setenv("MN_WHEEL_DELTA", "")
	if got := mouseWheelDelta(); got != 2 {
		t.Errorf("default wheel delta = %d, want 2", got)
	}
	t.Setenv("MN_WHEEL_DELTA", "5")
	if got := mouseWheelDelta(); got != 5 {
		t.Errorf("env override = %d, want 5", got)
	}
	t.Setenv("MN_WHEEL_DELTA", "999")
	if got := mouseWheelDelta(); got != 2 {
		t.Errorf("out-of-range should fall back to default, got %d", got)
	}
	t.Setenv("MN_WHEEL_DELTA", "abc")
	if got := mouseWheelDelta(); got != 2 {
		t.Errorf("non-numeric should fall back to default, got %d", got)
	}
}

// silence the unused declaration linter; brokenRenderer was a sketch that ended
// up unused once the test took a different (cleaner) approach.
var _ = brokenRenderer{}

func TestCenterBlock(t *testing.T) {
	// 5-char "hello" inside a 21-col viewport → 8 left-pad spaces.
	got := centerBlock("hello", 21)
	if got != "        hello" {
		t.Errorf("center: got %q", got)
	}

	// Multi-line block: every line gets the same left-pad based on the widest line.
	in := "ab\nabcd\nab"
	got = centerBlock(in, 12)
	if got != "    ab\n    abcd\n    ab" {
		t.Errorf("multi-line center: got %q", got)
	}

	// Wider than the viewport → unchanged (no clipping).
	if got := centerBlock("0123456789", 5); got != "0123456789" {
		t.Errorf("oversized should pass through unchanged, got %q", got)
	}

	// Edge cases.
	if got := centerBlock("", 50); got != "" {
		t.Errorf("empty input should remain empty, got %q", got)
	}
	if got := centerBlock("hi", 0); got != "hi" {
		t.Errorf("zero width should pass through, got %q", got)
	}
}

func TestEntryBannerIsCenteredInTranscript(t *testing.T) {
	// Build a banner that's narrower than the transcript width and verify
	// every line of the rendered transcript is left-padded.
	tr := &transcript{width: 60}
	tr.add(entry{kind: entryBanner, text: "abc\nabcdef\nab"})

	out := tr.render(-1)
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 banner lines, got %d", len(lines))
	}
	// Widest line is 6 chars, viewport is 60 → pad = 27.
	for _, line := range lines {
		if !strings.HasPrefix(line, strings.Repeat(" ", 27)) {
			t.Errorf("banner line not centered: %q", line)
		}
	}
}
