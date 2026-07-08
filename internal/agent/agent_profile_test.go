package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
)

// TestPlanProfileVisibility checks the opencode-style plan profile: edits/task
// are denied (hidden), while bash is "ask" (kept visible, gated at call time).
func TestPlanProfileVisibility(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var a, m, s int32
	reg.Register(&recordingTool{name: "edit_file", mutates: true, active: &a, maxSeen: &m, soloFail: &s})
	reg.Register(&recordingTool{name: "bash", mutates: true, active: &a, maxSeen: &m, soloFail: &s})
	reg.Register(&recordingTool{name: "read_thing", mutates: false, active: &a, maxSeen: &m, soloFail: &s})

	ag := &Agent{cfg: &config.Config{}, tools: reg}
	ag.SetPlanMode(true)

	if !ag.PlanMode() || ag.ActiveAgent().Name != config.AgentPlan {
		t.Fatal("expected the plan profile to be active")
	}
	var names []string
	for _, sName := range ag.visibleTools() {
		names = append(names, sName.Name)
	}
	if contains(names, "edit_file") {
		t.Errorf("plan must hide edit_file (deny), got %v", names)
	}
	if !contains(names, "bash") {
		t.Errorf("plan must keep bash visible (ask, not deny), got %v", names)
	}
	if !contains(names, "read_thing") {
		t.Errorf("plan must keep read-only tools visible, got %v", names)
	}

	// Switching back to build exposes everything again.
	ag.SetAgent(config.AgentBuild)
	if ag.PlanMode() {
		t.Error("after SetAgent(build) the agent must not be in plan mode")
	}
	names = names[:0]
	for _, sName := range ag.visibleTools() {
		names = append(names, sName.Name)
	}
	if !contains(names, "edit_file") {
		t.Errorf("build must expose edit_file, got %v", names)
	}
}

// TestOnDiskAgentOverridesBuiltin checks opencode's defaults→user precedence: an
// on-disk persona named "plan" replaces the built-in plan profile, and
// AgentNames lists each profile once (no duplicate build/plan).
func TestOnDiskAgentOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var a, m, s int32
	reg.Register(&recordingTool{name: "edit_file", mutates: true, active: &a, maxSeen: &m, soloFail: &s})

	ag := &Agent{cfg: &config.Config{}, tools: reg}
	// A custom "plan" that, unlike the built-in, ALLOWS edits, plus an extra
	// primary "reviewer" and a delegate-only "explore".
	ag.AttachSubAgents([]subagents.Persona{
		{Name: "plan", Mode: "primary", Permission: map[string]string{"edit_file": config.PermAllow}},
		{Name: "reviewer", Mode: "primary"},
		{Name: "explore", Mode: "subagent"},
	})

	if !ag.SetAgent("plan") {
		t.Fatal("SetAgent(plan) should succeed")
	}
	if ag.activeAgent().Rule("edit_file") != config.PermAllow {
		t.Error("on-disk plan persona must override the read-only built-in")
	}

	names := ag.AgentNames()
	if countOf(names, config.AgentBuild) != 1 || countOf(names, config.AgentPlan) != 1 {
		t.Errorf("build/plan must each appear once, got %v", names)
	}
	if !contains(names, "reviewer") {
		t.Errorf("custom primary persona should be listed, got %v", names)
	}
	if contains(names, "explore") {
		t.Errorf("subagent-only persona must not be a primary profile, got %v", names)
	}
}

func countOf(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}
