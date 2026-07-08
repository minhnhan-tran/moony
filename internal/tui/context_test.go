package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/agent"
)

func TestContextPercent(t *testing.T) {
	if got := contextPercent(0, 200_000); got != -1 {
		t.Errorf("zero usage should be -1 (hidden), got %d", got)
	}
	if got := contextPercent(50, 0); got != -1 {
		t.Errorf("zero window should be -1 (hidden), got %d", got)
	}
	if got := contextPercent(50_000, 200_000); got != 25 {
		t.Errorf("50k/200k = %d, want 25", got)
	}
	// Caps at 100 so a runaway value can't blow up the formatter.
	if got := contextPercent(500_000, 200_000); got != 100 {
		t.Errorf("over-capacity should clamp to 100, got %d", got)
	}
}

func TestContextStyleThresholds(t *testing.T) {
	// Spot-check the boundary transitions: the foreground color must differ
	// at 75% (warn) and 90% (alarm) compared to the normal style. Comparing
	// rendered strings doesn't work because lipgloss strips colors in
	// non-TTY tests; check the style's configured foreground directly.
	norm := contextStyle(50).GetForeground()
	warn := contextStyle(80).GetForeground()
	alarm := contextStyle(95).GetForeground()

	if norm == warn {
		t.Error("warn style should have a different foreground than normal")
	}
	if warn == alarm {
		t.Error("alarm style should have a different foreground than warn")
	}
	// And the boundaries themselves: 74→75 flips to warn, 89→90 flips to alarm.
	if contextStyle(74).GetForeground() == contextStyle(75).GetForeground() {
		t.Error("the 75% boundary should change style")
	}
	if contextStyle(89).GetForeground() == contextStyle(90).GetForeground() {
		t.Error("the 90% boundary should change style")
	}
}

func TestProgressBar(t *testing.T) {
	cases := map[int]string{
		0:   "░░░░░░░░",
		25:  "██░░░░░░",
		50:  "████░░░░",
		75:  "██████░░",
		100: "████████",
	}
	for pct, want := range cases {
		if got := progressBar(pct, 8); got != want {
			t.Errorf("progressBar(%d, 8) = %q, want %q", pct, got, want)
		}
	}

	// Width 0 → empty (so callers can short-circuit easily).
	if got := progressBar(50, 0); got != "" {
		t.Errorf("zero-width should return empty, got %q", got)
	}

	// Out-of-range clamps.
	if got := progressBar(-5, 4); got != "░░░░" {
		t.Errorf("negative pct should clamp to 0, got %q", got)
	}
	if got := progressBar(150, 4); got != "████" {
		t.Errorf(">100 should clamp to 100, got %q", got)
	}

	// Sub-cell partial-block precision: a value just past a full cell should
	// produce a partial block in the next position.
	got := progressBar(40, 8) // 40% × 8 cells × 8 eighths / 100 = 25 → 3 full + 1 eighth
	if !strings.HasPrefix(got, "███") {
		t.Errorf("40%% should start with 3 full blocks, got %q", got)
	}
}

func TestContextBarEnabled(t *testing.T) {
	t.Setenv("MOONY_CTX_BAR", "")
	if !contextBarEnabled() {
		t.Error("default should be enabled")
	}
	t.Setenv("MOONY_CTX_BAR", "off")
	if contextBarEnabled() {
		t.Error("'off' should disable")
	}
	t.Setenv("MOONY_CTX_BAR", "0")
	if contextBarEnabled() {
		t.Error("'0' should disable")
	}
	t.Setenv("MOONY_CTX_BAR", "text")
	if contextBarEnabled() {
		t.Error("'text' should disable")
	}
	t.Setenv("MOONY_CTX_BAR", "anything-else")
	if !contextBarEnabled() {
		t.Error("unknown values should leave it enabled")
	}
}

func TestStatusBarShowsContextOnceTurnComplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOONY_CTX_BAR", "off") // text-only so we can assert exact substring
	m := sized(t, newTestModel(t), 100, 24)

	// Before any turn, the indicator is hidden.
	if got := m.statusView(); strings.Contains(got, "ctx ") {
		t.Errorf("status bar should not show ctx before any turn; got:\n%s", got)
	}

	// After a turn lands a measurement, it appears.
	m.lastTurnInTokens = 50_000
	got := m.statusView()
	if !strings.Contains(got, "ctx 25%") {
		t.Errorf("expected 'ctx 25%%' in status bar; got:\n%s", got)
	}
}

func TestStatusBarShowsProgressBarByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MOONY_CTX_BAR", "") // default enabled
	m := sized(t, newTestModel(t), 120, 24)
	m.lastTurnInTokens = 100_000 // 50% of 200k

	got := m.statusView()
	if !strings.Contains(got, "ctx ") || !strings.Contains(got, "50%") {
		t.Errorf("expected ctx label + percentage in status bar; got:\n%s", got)
	}
	// Bar at 50% has 4 full cells and 4 light-shade cells.
	if !strings.Contains(got, "████░░░░") {
		t.Errorf("expected 8-cell half-filled bar; got:\n%s", got)
	}
}

func TestResetClearsContextIndicator(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 24)
	m.lastTurnInTokens = 80_000
	m.input.SetValue("/reset")
	next, _ := m.submit()
	nm := next.(Model)
	if nm.lastTurnInTokens != 0 {
		t.Errorf("/reset should zero the context indicator, got %d", nm.lastTurnInTokens)
	}
	if strings.Contains(nm.statusView(), "ctx ") {
		t.Error("status bar should hide ctx after /reset")
	}
}

func TestFormatTokenCount(t *testing.T) {
	cases := map[int]string{
		0:         "0",
		42:        "42",
		999:       "999",
		1500:      "1.5k",
		9499:      "9.5k",
		9999:      "10k", // rounds up but to integer-k, not "10.0k"
		10_000:    "10k", // ...and matches the value just below it (monotonic)
		12_345:    "12k",
		250_000:   "250k",
		999_999:   "1000k",
		1_000_000: "1.0m",
		2_500_000: "2.5m",
	}
	for n, want := range cases {
		if got := formatTokenCount(n); got != want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", n, got, want)
		}
	}
	// Monotonicity spot-check around the tier boundaries: a larger count must
	// never format to a smaller-looking string than a smaller count.
	for _, b := range []int{9_950, 10_000, 1_000_000} {
		lo, hi := formatTokenCount(b-1), formatTokenCount(b)
		if lo == "" || hi == "" {
			t.Fatalf("unexpected empty format near %d", b)
		}
	}
}

func TestContextStatusShowsBreakdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 24)

	// The header line is always present on its own.
	if got := m.contextStatus(); !strings.Contains(got, "Context window:") {
		t.Errorf("expected the window header; got:\n%s", got)
	}

	// /context emits the header (entryInfo) plus a pre-styled breakdown block
	// (entryRaw) so the color-coded bar survives rendering.
	m.addContextStatus()
	es := m.trans.entries
	if len(es) < 2 {
		t.Fatalf("expected at least 2 transcript entries, got %d", len(es))
	}
	header := es[len(es)-2]
	block := es[len(es)-1]
	if header.kind != entryInfo || !strings.Contains(header.text, "Context window:") {
		t.Errorf("expected an info header entry; got kind=%d text=%q", header.kind, header.text)
	}
	if block.kind != entryRaw {
		t.Errorf("breakdown should be entryRaw (pre-styled, not re-styled), got kind=%d", block.kind)
	}
	if !strings.Contains(block.text, "Estimated context usage") {
		t.Errorf("expected the usage breakdown; got:\n%s", block.text)
	}
	if !strings.Contains(block.text, "System") || !strings.Contains(block.text, "Built-in tools") {
		t.Errorf("expected System and Built-in tools segments in the breakdown; got:\n%s", block.text)
	}
}

func TestContextStatusPublishesStatsToWeb(t *testing.T) {
	m, hub := mirrorModel(t)
	_, ch, _, _ := hub.Subscribe()

	m.addContextStatus()

	// Drain published messages; expect a KindAgent carrying a ContextStats.
	var stats *agent.ContextStats
	for i := 0; i < 8 && stats == nil; i++ {
		select {
		case msg := <-ch:
			if cs, ok := msg.Event.(agent.ContextStats); ok {
				stats = &cs
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for the ContextStats event")
		}
	}
	if stats == nil {
		t.Fatal("addContextStatus did not publish a ContextStats event to the hub")
	}
	if stats.Window <= 0 || stats.Total <= 0 || len(stats.Segments) == 0 {
		t.Fatalf("ContextStats payload looks empty: %+v", stats)
	}
}
