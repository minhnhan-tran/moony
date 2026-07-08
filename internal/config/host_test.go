package config

import "testing"

func TestNormalizeHostname(t *testing.T) {
	cases := map[string]string{
		"Johns-MacBook.local": "Johns-MacBook",
		"build-server":        "build-server",
		"  spaced.local  ":    "spaced",
		"":                    "",
	}
	for in, want := range cases {
		if got := normalizeHostname(in); got != want {
			t.Errorf("normalizeHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMachineNameCached verifies MachineName is stable across calls (cached) and
// doesn't panic in this environment.
func TestMachineNameCached(t *testing.T) {
	first := MachineName()
	if second := MachineName(); second != first {
		t.Errorf("MachineName not cached: %q != %q", first, second)
	}
}
