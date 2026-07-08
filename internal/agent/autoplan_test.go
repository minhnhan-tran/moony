package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/subagents"
)

// TestLooksLikeLargeTask checks the conservative big-task heuristic: clear
// multi-step/large change phrasing trips it, small asks and questions do not.
func TestLooksLikeLargeTask(t *testing.T) {
	large := []string{
		"Refactor the authentication module",
		"please rewrite this package from scratch",
		"migrate the database layer to v2",
		"redesign the settings page",
		"restructure the repo layout",
		"Do these:\n- add a model\n- add an endpoint\n- wire the UI\n- write tests",
	}
	for _, s := range large {
		if !looksLikeLargeTask(s) {
			t.Errorf("expected large task: %q", s)
		}
	}
	small := []string{
		"fix the typo on line 12",
		"what does this function do?",
		"add a log line to the handler",
		"rename foo to bar",
		"bump the version",
		"",
	}
	for _, s := range small {
		if looksLikeLargeTask(s) {
			t.Errorf("expected small task: %q", s)
		}
	}
}

// TestMaybeAutoPlanGating verifies the guards around auto-entering plan mode.
func TestMaybeAutoPlanGating(t *testing.T) {
	// Off by default: never switches, even for a clearly large task.
	off := &Agent{cfg: &config.Config{AutoPlan: false}}
	if off.MaybeAutoPlan("refactor everything") || off.PlanMode() {
		t.Error("AutoPlan off must not switch or enter plan mode")
	}

	// On + large task + build mode: switches into plan mode.
	on := &Agent{cfg: &config.Config{AutoPlan: true}}
	if !on.MaybeAutoPlan("refactor the auth module") {
		t.Fatal("AutoPlan on + large task should switch")
	}
	if !on.PlanMode() {
		t.Error("MaybeAutoPlan should have entered plan mode")
	}

	// On + small task: no switch.
	small := &Agent{cfg: &config.Config{AutoPlan: true}}
	if small.MaybeAutoPlan("fix a typo") {
		t.Error("small task must not auto-switch")
	}

	// On but already planning: no-op.
	already := &Agent{cfg: &config.Config{AutoPlan: true}}
	already.SetPlanMode(true)
	if already.MaybeAutoPlan("refactor the world") {
		t.Error("already in plan mode must not re-switch")
	}

	// On but an approved plan is mid-execution: don't re-plan.
	approved := &Agent{
		cfg:  &config.Config{AutoPlan: true},
		sess: &session.Session{Plan: &session.Plan{Steps: []session.PlanStep{{Title: "x"}}, Status: session.PlanApproved}},
	}
	if approved.MaybeAutoPlan("refactor more") {
		t.Error("approved-plan execution must not auto-plan")
	}

	// On a custom (non-build) agent: auto-plan must NOT clobber it.
	custom := &Agent{cfg: &config.Config{AutoPlan: true}, personas: []subagents.Persona{{Name: "reviewer", Mode: "primary"}}}
	custom.SetAgent("reviewer")
	if custom.MaybeAutoPlan("refactor the world") {
		t.Error("auto-plan must not switch away from a user-selected custom agent")
	}
	if custom.ActiveAgent().Name != "reviewer" {
		t.Errorf("custom agent must be preserved, got %q", custom.ActiveAgent().Name)
	}
}
