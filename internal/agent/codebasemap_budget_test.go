package agent

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// writeMap seeds a working dir with a CODEBASE_MAP.md whose three sections have the
// requested sizes, so budget arithmetic can be asserted against real inputs.
func writeMap(t *testing.T, dir string, pinnedN, structN, narrN int) {
	t.Helper()
	line := func(n int, tag string) string {
		if n <= 0 {
			return ""
		}
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(tag)
			b.WriteString(" filler text for the budget test\n")
		}
		return b.String()[:n]
	}
	if err := config.WriteCodebaseMapPinned(dir, line(pinnedN, "pin")); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteCodebaseMapStructure(dir, line(structN, "str")); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteCodebaseMapNarrative(dir, line(narrN, "nar")); err != nil {
		t.Fatal(err)
	}
}

// The section must fit the byte budget INCLUDING its headings and preamble. Those were
// appended after trimming, so the section always overshot MOONY_CODEBASE_MAP_BYTES — by
// ~797B, which is 2.5% at the 32000 default but 17x at a small budget.
func TestCodebaseMapSectionRespectsBudget(t *testing.T) {
	dir := t.TempDir()
	writeMap(t, dir, 20000, 20000, 20000)

	for _, budget := range []int{1000, 2000, 8000, 32000} {
		got := codebaseMapPromptSection(dir, "", budget)
		if len(got) > budget {
			t.Errorf("budget %d: section is %d bytes, %d over — the headings/preamble must be reserved inside the budget",
				budget, len(got), len(got)-budget)
		}
		if len(got) == 0 {
			t.Errorf("budget %d: no section at all, but the budget comfortably fits the framing", budget)
		}
	}
}

// A budget too small for even the framing must yield NO map, not a headless fragment: the
// preamble is what tells the model this section IS the maintained map, so content without it
// is worse than nothing.
func TestCodebaseMapSectionEmptyWhenChromeCannotFit(t *testing.T) {
	dir := t.TempDir()
	writeMap(t, dir, 5000, 5000, 5000)

	for _, budget := range []int{1, 50, 200, 700} {
		got := codebaseMapPromptSection(dir, "", budget)
		if got != "" {
			t.Errorf("budget %d: returned %d bytes; a budget below the framing cost must return no map, got %.80q",
				budget, len(got), got)
		}
	}
}

// The generated digest keeps a floor so a large NARRATIVE can't starve it to nothing —
// bounded on BOTH sides, since an upper-bound-only assertion would miss a floor that had been
// removed entirely. (A huge PINNED section legitimately claims the budget: pinned is
// hand-authored and is trimmed last, so it is not the case being tested here.)
func TestCodebaseMapStructureKeepsItsFloor(t *testing.T) {
	dir := t.TempDir()
	writeMap(t, dir, 500, 20000, 20000)

	const budget = 12000
	got := codebaseMapPromptSection(dir, "", budget)
	if len(got) > budget {
		t.Fatalf("section %d bytes over budget %d", len(got), budget)
	}
	i := strings.Index(got, "## Directory index")
	if i < 0 {
		t.Fatal("no structure section: the digest was starved out entirely")
	}
	end := len(got)
	if j := strings.Index(got[i:], "## Features → files"); j > 0 {
		end = i + j
	}
	// Measured heading-to-heading, so allow a small slack for the heading/marker bytes
	// themselves; the point is that the digest keeps its floor rather than being starved.
	if n := end - i; n < codebaseMapMinStructureBytes-100 {
		t.Errorf("structure section is only %d bytes; its %d-byte floor is not being honoured", n, codebaseMapMinStructureBytes)
	}
}

// clipBytes must fit its truncation marker WITHIN n. Appending the 18-byte marker on top of n
// made all three call sites overshoot their share of the budget.
func TestClipBytesIncludesMarkerInLimit(t *testing.T) {
	body := strings.Repeat("some line of content here\n", 200)

	for _, n := range []int{5, 14, 18, 19, 40, 100, 1000} {
		got := clipBytes(body, n)
		if len(got) > n {
			t.Errorf("clipBytes(_, %d) returned %d bytes, %d over the limit", n, len(got), len(got)-n)
		}
	}
	// Room for the marker: it must actually be present, so truncation stays visible.
	if got := clipBytes(body, 100); !strings.Contains(got, "truncated") {
		t.Errorf("clipBytes(_, 100) dropped the truncation marker: %q", got)
	}
	// Too small for marker+content: a bare prefix, still within n.
	if got := clipBytes(body, 10); len(got) != 10 || strings.Contains(got, "truncated") {
		t.Errorf("clipBytes(_, 10) = %q (len %d); want a 10-byte bare prefix", got, len(got))
	}
	// Non-truncating cases are untouched.
	if got := clipBytes("short", 100); got != "short" {
		t.Errorf("clipBytes padded or altered a string that already fits: %q", got)
	}
	if got := clipBytes("anything", 0); got != "" {
		t.Errorf("clipBytes(_, 0) = %q, want empty", got)
	}
}

// The measured chrome must match what the renderer actually emits: the reservation is derived
// by rendering with placeholders, so this pins the two together and fails if the renderer
// grows a part the measurement doesn't see.
func TestCodebaseMapChromeMeasurementMatchesRenderer(t *testing.T) {
	measured := len(renderCodebaseMapSection("x", "x", "x")) - 3
	full := renderCodebaseMapSection(strings.Repeat("p", 100), strings.Repeat("s", 100), strings.Repeat("n", 100))
	if got := len(full) - 300; got != measured {
		t.Errorf("chrome is %d bytes with real content but measured as %d", got, measured)
	}
	// A section with fewer parts must not cost MORE than the measured maximum, or the
	// reservation would under-count and the budget could still be exceeded.
	for _, r := range []string{
		renderCodebaseMapSection("x", "", ""),
		renderCodebaseMapSection("", "x", ""),
		renderCodebaseMapSection("", "", "x"),
		renderCodebaseMapSection("x", "x", ""),
	} {
		if len(r)-1 > measured {
			t.Errorf("a partial section costs %d bytes of chrome, more than the reserved %d", len(r)-1, measured)
		}
	}
}
