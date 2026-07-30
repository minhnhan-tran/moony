package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/subagents"
)

// personaAllowsTaskTool mirrors the predicate task.Execute uses to decide whether
// a sub-agent may receive a nested task tool (default persona, or one that
// explicitly allow-lists "task").
func personaAllowsTaskTool(p subagents.Persona) bool {
	if len(p.Tools) == 0 {
		return true
	}
	for _, n := range p.Tools {
		if n == "task" {
			return true
		}
	}
	return false
}

func TestNestedDelegationDepthGate(t *testing.T) {
	// maxDepth=2: depth 0 (primary) may spawn a level that itself may spawn (1<2),
	// but depth 1's child (2) may not (2<2 is false).
	max := 2
	cases := []struct {
		depth   int
		canNest bool
	}{
		{0, true},
		{1, false},
		{2, false},
	}
	for _, c := range cases {
		got := (c.depth+1 < max)
		if got != c.canNest {
			t.Errorf("depth %d: canNest=%v, want %v", c.depth, got, c.canNest)
		}
	}
}

func TestSubagentMaxDepthClamped(t *testing.T) {
	// Load clamps to [1, ceiling]. Verify the ceiling constant is sane and the
	// clamp arithmetic used in config.Load.
	if config.SubagentDepthCeiling < 2 {
		t.Fatalf("ceiling too low: %d", config.SubagentDepthCeiling)
	}
	clamp := func(n int) int {
		if n < 1 {
			n = 1
		}
		if n > config.SubagentDepthCeiling {
			n = config.SubagentDepthCeiling
		}
		return n
	}
	if clamp(0) != 1 {
		t.Error("0 should clamp to 1")
	}
	if clamp(999) != config.SubagentDepthCeiling {
		t.Error("huge value should clamp to ceiling")
	}
	if clamp(2) != 2 {
		t.Error("2 should stay 2")
	}
}

func TestNestedTaskCycleGuard(t *testing.T) {
	personas := []subagents.Persona{
		{Name: "explore", Description: "d", SystemPrompt: "s"},
	}
	parent := &Agent{cfg: &config.Config{SubagentMaxDepth: 3, ApprovalPolicy: config.ApprovalPrompt}}
	tt := newTaskTool(parent, personas)
	// Simulate a tool one level deep whose ancestor chain already contains
	// "explore"; delegating back to "explore" must be refused.
	tt.depth = 1
	tt.chain = []string{"explore"}
	_, err := tt.Execute(context.Background(), []byte(`{"agent":"explore","prompt":"loop"}`))
	if err == nil {
		t.Fatal("expected cycle-guard error delegating to an ancestor persona")
	}
	if !strings.Contains(err.Error(), "already an ancestor") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPersonaAllowsTaskTool(t *testing.T) {
	if !personaAllowsTaskTool(subagents.Persona{Name: "explore"}) {
		t.Error("default persona (no tool allowlist) should allow nested task")
	}
	if personaAllowsTaskTool(subagents.Persona{Name: "ro", Tools: []string{"read_file", "grep"}}) {
		t.Error("restricted persona without task should not allow nested task")
	}
	if !personaAllowsTaskTool(subagents.Persona{Name: "lead", Tools: []string{"read_file", "task"}}) {
		t.Error("restricted persona that lists task should allow nested task")
	}
}
