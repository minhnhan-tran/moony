package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/tools"
)

// The self-review gate must not conclude anything from a change set it knows is incomplete.
//
// snapshotPaths only enumerates targets for the four file tools, so anything bash rewrote
// (`sed -i`, a python heredoc, a generator) never enters the turn's snapshot set. A turn that
// edited JS/CSS/Go via bash while touching CHANGELOG.md with write_file handed the gate
// [CHANGELOG.md] alone, which it correctly classified as docs-only and skipped — hiding every
// code change behind a confidently wrong reason. Reviewing costs one model call; wrongly
// skipping hides bugs.
func TestGateReviewsWhenChangeSetIsIncomplete(t *testing.T) {
	a, dir := newGateAgent(t)
	a.snapshots.startTurn()

	// The only ENUMERABLE change is the CHANGELOG — docs-only on its face.
	writeAndSnapshot(t, a, dir, "CHANGELOG.md",
		"## [1.0.0]\n", "## [1.0.1] - today\n\n### Fixed\n- thing\n\n## [1.0.0]\n")

	// Baseline: without the flag this turn is (correctly, given what it can see) skippable.
	if skip, _ := a.turnIsNonReviewable(); !skip {
		t.Fatal("precondition failed: a docs-only change set should look skippable")
	}

	// Now record that bash also ran and mutated something unknowable.
	a.snapshots.markOpaqueMutation()

	skip, why := a.turnIsNonReviewable()
	if skip {
		t.Errorf("gate skipped review (%q) on a turn whose change set is known-incomplete; code edited via bash would go unreviewed", why)
	}
}

// The flag must be per-turn and self-clearing: a turn that ran bash must not make the NEXT
// turn's honest docs-only change set look incomplete forever.
func TestOpaqueMutationFlagIsPerTurn(t *testing.T) {
	s := &snapshotStore{}
	if s.hadOpaqueMutation() {
		t.Error("a zero-value store must not report an opaque mutation; an idle agent is unaffected")
	}

	s.startTurn()
	s.markOpaqueMutation()
	if !s.hadOpaqueMutation() {
		t.Fatal("flag not visible in the turn it was set")
	}

	s.startTurn()
	if s.hadOpaqueMutation() {
		t.Error("flag leaked into the next turn; every later turn would be forced to review")
	}
}

// A wiring guard. The tests above call snapshotForCall directly, so they would ALL still pass
// if executeApprovedCall reverted to looping over snapshotPaths itself — which is exactly the
// original bug (bash mutations never flagged). Mutation testing caught this gap: un-wiring the
// call escaped the behavioural suite entirely.
func TestExecuteApprovedCallUsesSnapshotForCall(t *testing.T) {
	src, err := os.ReadFile("agent_tools.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "a.snapshotForCall(call.Name, call.Input)") {
		t.Error("executeApprovedCall does not call snapshotForCall, so a bash mutation never flags the turn's change set as incomplete")
	}
	// The old inline loop bypasses the flag entirely; it must not come back.
	if strings.Contains(body, "for _, p := range snapshotPaths(call.Name, call.Input)") {
		t.Error("executeApprovedCall snapshots inline again, bypassing the opaque-mutation flag")
	}
}

// snapshotForCall is the real wiring: it must snapshot enumerable targets and flag only when
// a MUTATING tool has none.
func TestSnapshotForCallFlagsOnlyOpaqueMutations(t *testing.T) {
	// A registry with the real bash + file tools so Mutates() answers truthfully. Note
	// RegisterAllowed takes a STRICT allowlist and Registry.Mutates fails safe (unknown
	// tool → true), so a partially-populated registry makes every tool look mutating —
	// which would make this test pass for the wrong reason. Hence the presence check.
	reg := tools.NewRegistry(t.TempDir())
	tools.RegisterAllowed(reg, 0, map[string]bool{"bash": true, "read_file": true, "write_file": true})
	for _, want := range []string{"bash", "read_file", "write_file"} {
		if reg.Get(want) == nil {
			t.Fatalf("registry is missing %q; Mutates would fail safe and this test would be vacuous", want)
		}
	}
	if reg.Mutates("read_file") {
		t.Fatal("read_file reports as mutating; the registry is not populated as expected and the test would be vacuous")
	}

	cases := []struct {
		name     string
		tool     string
		input    string
		wantFlag bool
	}{
		{"bash mutates opaquely", "bash", `{"command":"sed -i '' s/a/b/ x.go"}`, true},
		{"read_file changes nothing", "read_file", `{"path":"x.go"}`, false},
		{"write_file targets are enumerable", "write_file", `{"path":"x.go","content":"hi"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, dir := newGateAgent(t)
			a.tools = reg
			a.cfg.WorkingDir = dir
			a.snapshots.startTurn()

			a.snapshotForCall(tc.tool, json.RawMessage(tc.input))

			if got := a.snapshots.hadOpaqueMutation(); got != tc.wantFlag {
				t.Errorf("hadOpaqueMutation() = %v, want %v", got, tc.wantFlag)
			}
		})
	}
}
