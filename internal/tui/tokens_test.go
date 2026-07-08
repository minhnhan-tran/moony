package tui

import (
	"strings"
	"testing"
)

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		0:          "0",
		42:         "42",
		999:        "999",
		1000:       "1.0k",
		1500:       "1.5k",
		12_345:     "12.3k",
		999_499:    "999.5k",
		1_000_000:  "1.0m",
		2_300_000:  "2.3m",
		15_750_000: "15.8m",
	}
	for n, want := range cases {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestStatusBarFormatsTokens checks the always-visible status bar renders the
// session token counters in the compact k/m form, not raw integers.
func TestStatusBarFormatsTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 24)
	m.totalIn = 12_345
	m.totalOut = 2_300_000

	got := m.statusView()
	if !strings.Contains(got, "↑12.3k") {
		t.Errorf("status bar should show abbreviated input tokens '↑12.3k'; got:\n%s", got)
	}
	if !strings.Contains(got, "↓2.3m") {
		t.Errorf("status bar should show abbreviated output tokens '↓2.3m'; got:\n%s", got)
	}
}

// TestStatusBarShowsCacheHitRate checks the status bar surfaces the prompt-cache
// hit rate (cached reads / input) once caching has engaged, and hides it when no
// cached reads have happened.
func TestStatusBarShowsCacheHitRate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 24)
	m.totalIn = 1000
	m.totalOut = 100

	// No cached reads yet → no cache indicator.
	if got := m.statusView(); strings.Contains(got, "cache ") {
		t.Errorf("cache indicator should be hidden with 0 cached reads; got:\n%s", got)
	}

	// 750 of 1000 input tokens served from cache → "cache 75%".
	m.totalCacheRead = 750
	if got := m.statusView(); !strings.Contains(got, "cache 75%") {
		t.Errorf("status bar should show 'cache 75%%'; got:\n%s", got)
	}
}
