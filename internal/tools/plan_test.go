package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanWriteStoresDraft(t *testing.T) {
	store := NewPlanStore()
	var got *Plan
	store.SetNotify(func(p *Plan) { got = p })
	tool := &planWriteTool{store: store}

	in, _ := json.Marshal(map[string]any{
		"title": "Ship it",
		"steps": []map[string]any{
			{"title": "write code"},
			{"title": "add tests", "detail": "table-driven"},
		},
	})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Ship it") || !strings.Contains(out, "DRAFT") {
		t.Fatalf("output missing title/draft note:\n%s", out)
	}
	if got == nil || len(got.Steps) != 2 || got.Steps[1].Detail != "table-driven" {
		t.Fatalf("store not updated: %+v", got)
	}
	if tool.Mutates() {
		t.Fatal("plan_write must be non-mutating")
	}
}

func TestPlanWriteRejectsEmpty(t *testing.T) {
	tool := &planWriteTool{store: NewPlanStore()}

	in, _ := json.Marshal(map[string]any{"steps": []map[string]any{}})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("a plan with no steps should error")
	}

	in, _ = json.Marshal(map[string]any{"steps": []map[string]any{{"title": "  "}}})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("a step with a blank title should error")
	}
}

func TestPlanStoreClone(t *testing.T) {
	store := NewPlanStore()
	store.Set(&Plan{Title: "t", Steps: []PlanStep{{Title: "a"}}})
	got := store.Get()
	got.Steps[0].Title = "mutated" // must not affect the stored copy
	if store.Get().Steps[0].Title != "a" {
		t.Fatal("Get must return a defensive copy")
	}
	store.Set(nil)
	if store.Get() != nil {
		t.Fatal("Set(nil) should clear the store")
	}
}
