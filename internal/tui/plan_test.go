package tui

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// newPlanModel builds a Model whose agent has a session store + plan store wired
// (the plan artifact lives on the session, so a store is required).
func newPlanModel(t *testing.T) Model {
	t.Helper()
	cfg := config.Load()
	client := llm.NewClient(cfg)
	dir := t.TempDir()
	cfg.WorkingDir = dir
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	registry := tools.Default(dir, cfg.MaxToolOutputBytes)
	ps := tools.NewPlanStore()
	registry.RegisterPlan(ps)
	ag := agent.New(cfg, client, registry, store, session.New("m", dir))
	ag.AttachPlanStore(ps)
	return New(cfg, ag, nil)
}

func TestPlanAddAndClearCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newPlanModel(t), 120, 30)

	next, _ := m.command("/plan add draft a migration")
	m = next.(Model)
	if m.plan == nil || len(m.plan.Steps) != 1 || m.plan.Steps[0].Title != "draft a migration" {
		t.Fatalf("/plan add should append a step, got %+v", m.plan)
	}
	if m.plan.Status != session.PlanDraft {
		t.Fatalf("added step should leave plan a draft, got %q", m.plan.Status)
	}

	next, _ = m.command("/plan clear")
	m = next.(Model)
	if m.plan != nil {
		t.Fatalf("/plan clear should drop the plan, got %+v", m.plan)
	}
}

func TestPlanApproveWithoutPlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newPlanModel(t), 120, 30)

	next, _ := m.command("/plan approve")
	m = next.(Model)
	last := m.trans.entries[len(m.trans.entries)-1]
	if last.kind != entryInfo || !strings.Contains(last.text, "No plan to approve") {
		t.Fatalf("approving with no plan should explain, got %q", last.text)
	}
}

func TestSidebarShowsPlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newPlanModel(t), 120, 30)
	m.showSidebar = true
	m.plan = &session.Plan{
		Title:  "Add caching",
		Status: session.PlanDraft,
		Steps:  []session.PlanStep{{Title: "add a cache layer"}, {Title: "invalidate on write"}},
	}

	out := m.sidebarView()
	for _, want := range []string{"Plan · draft", "Add caching", "1. add a cache layer", "/plan approve"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q; got:\n%s", want, out)
		}
	}
}
