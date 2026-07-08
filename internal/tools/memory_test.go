package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRememberTool(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	sink := NewMemorySink()
	reg.RegisterRemember(sink)

	// remember is non-mutating, so it never triggers an approval prompt.
	if reg.Mutates("remember") {
		t.Error("remember should be non-mutating")
	}

	// With no save wired, the tool reports memory is unavailable.
	if _, err := reg.Execute(context.Background(), "remember", json.RawMessage(`{"text":"x"}`)); err == nil {
		t.Error("expected an error when the sink has no save callback")
	}

	var saved, savedCat string
	sink.SetSave(func(text, category string) (string, error) {
		saved = text
		savedCat = category
		return "ok: " + text, nil
	})
	out, err := reg.Execute(context.Background(), "remember", json.RawMessage(`{"text":"run make vet","category":"rule"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if saved != "run make vet" {
		t.Errorf("sink received %q", saved)
	}
	if savedCat != "rule" {
		t.Errorf("sink received category %q, want %q", savedCat, "rule")
	}
	if out != "ok: run make vet" {
		t.Errorf("tool output = %q", out)
	}
}
