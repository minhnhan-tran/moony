package config

import "testing"

func TestBuildAgentUnrestricted(t *testing.T) {
	b := BuildAgent()
	if b.Name != AgentBuild {
		t.Errorf("name = %q, want %q", b.Name, AgentBuild)
	}
	for _, tool := range []string{"write_file", "edit_file", "bash", "task", "read_file"} {
		if got := b.Rule(tool); got != PermAllow {
			t.Errorf("build.Rule(%q) = %q, want allow", tool, got)
		}
	}
	if b.ReadOnly() {
		t.Error("build profile must not be read-only")
	}
}

func TestPlanAgentReadOnly(t *testing.T) {
	p := PlanAgent()
	if p.Name != AgentPlan {
		t.Errorf("name = %q, want %q", p.Name, AgentPlan)
	}
	// Edits and delegation are denied; bash asks; reads are unrestricted.
	for _, tool := range []string{"write_file", "edit_file", "multi_edit", "apply_patch", "task", "delegate", "remember"} {
		if got := p.Rule(tool); got != PermDeny {
			t.Errorf("plan.Rule(%q) = %q, want deny", tool, got)
		}
	}
	if got := p.Rule("bash"); got != PermAsk {
		t.Errorf("plan.Rule(bash) = %q, want ask", got)
	}
	if got := p.Rule("read_file"); got != PermAllow {
		t.Errorf("plan.Rule(read_file) = %q, want allow (unrestricted)", got)
	}
	if !p.ReadOnly() {
		t.Error("plan profile must be read-only (all mutating built-ins denied)")
	}
}

func TestBuiltinAgentLookup(t *testing.T) {
	if BuiltinAgent("build") == nil || BuiltinAgent("PLAN") == nil {
		t.Error("build/plan (any case) must resolve to a built-in")
	}
	if BuiltinAgent("nope") != nil {
		t.Error("unknown agent name must return nil")
	}
}

func TestAgentRuleNilSafe(t *testing.T) {
	var a *Agent
	if a.Rule("anything") != PermAllow {
		t.Error("nil agent should default to allow")
	}
	if a.ReadOnly() {
		t.Error("nil agent is not read-only")
	}
}
