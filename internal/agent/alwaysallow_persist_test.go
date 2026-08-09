package agent

import (
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/permissions"
)

// End-to-end for the rule STRING the agent actually persists: alwaysAllowRule builds it,
// permissions.Add writes it, and a reload must match the same call back. This is the join
// between the two packages — the round-trip test in internal/permissions pins the storage
// layer, and this pins that the agent feeds it a string that layer can match.
func TestAlwaysAllowRuleRoundTripsThroughPermissions(t *testing.T) {
	commands := []string{
		"npm run dev",
		"make build && make test",
		"echo hi > out.txt",
		"npm run dev & sleep 1",
		"cat $(which go)",
		"ls -la | head",
		"tail -f log.txt 2>&1",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			dir := t.TempDir()

			input, err := json.Marshal(map[string]any{"command": cmd})
			if err != nil {
				t.Fatal(err)
			}
			rule := alwaysAllowRule("bash", input)
			if rule == "" {
				t.Fatalf("no rule produced for %q", cmd)
			}

			r := permissions.Load(dir)
			if err := r.Add(rule, false, permissions.Project); err != nil {
				t.Fatalf("Add(%q): %v", rule, err)
			}

			// Next turn: rules are reloaded from disk and the same call is re-checked.
			if got := permissions.Load(dir).Check("bash", cmd); got != permissions.Allow {
				t.Errorf("always-allow of %q persisted as %q, but a reload gives %v — the user is prompted again",
					cmd, rule, got)
			}
		})
	}
}

// Non-bash tools persist as a bare tool name; that must round-trip too.
func TestAlwaysAllowRuleRoundTripsForNonBashTools(t *testing.T) {
	for _, tool := range []string{"write_file", "edit_file", "read_file"} {
		t.Run(tool, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			dir := t.TempDir()

			rule := alwaysAllowRule(tool, nil)
			r := permissions.Load(dir)
			if err := r.Add(rule, false, permissions.Project); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if got := permissions.Load(dir).Check(tool, ""); got != permissions.Allow {
				t.Errorf("%s: got %v, want Allow", tool, got)
			}
		})
	}
}
