package provider

import (
	"strings"
	"testing"
)

func TestListDefaultOllamaFirst(t *testing.T) {
	l := List()
	if len(l) != 19 {
		t.Fatalf("expected 19 providers, got %d", len(l))
	}
	if Default != Ollama {
		t.Fatalf("default provider should be ollama, got %q", Default)
	}
	if l[0].ID != Ollama {
		t.Fatalf("expected ollama first, got %q", l[0].ID)
	}
	for _, id := range []string{Ollama, OpenAI, Anthropic, SQEAnthropic, OpenRouter, Gemini, DeepSeek, Groq, XAI, Mistral, Together, Fireworks, Moonshot, ZAI, DashScope, NVIDIA, HuggingFace, Venice, OmniRoute} {
		if _, ok := Lookup(id); !ok {
			t.Errorf("provider %q should be registered", id)
		}
	}
	// Both Anthropic backends must route to the native wire; others must not.
	for _, id := range []string{Anthropic, SQEAnthropic} {
		if !UsesAnthropicWire(id) {
			t.Errorf("%q should use the Anthropic wire", id)
		}
	}
	for _, id := range []string{Ollama, OpenAI, Gemini} {
		if UsesAnthropicWire(id) {
			t.Errorf("%q should NOT use the Anthropic wire", id)
		}
	}
	// Returned slice must be a copy (mutating it must not affect the registry).
	l[0].Label = "mutated"
	if List()[0].Label == "mutated" {
		t.Fatal("List() leaked the underlying registry")
	}
}

func TestLookup(t *testing.T) {
	if _, ok := Lookup(Ollama); !ok {
		t.Fatalf("expected %q to be found", Ollama)
	}
	// Case-insensitive + trims whitespace.
	if p, ok := Lookup("  Ollama "); !ok || p.ID != Ollama {
		t.Fatalf("case-insensitive lookup failed: %+v ok=%v", p, ok)
	}
	for _, id := range []string{"bogus-provider", "gpt-4o", "claude-3"} {
		if _, ok := Lookup(id); ok {
			t.Fatalf("%q should not be a known provider", id)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("OLLAMA"); got != Ollama {
		t.Fatalf("Normalize(OLLAMA) = %q", got)
	}
	if got := Normalize(""); got != Default {
		t.Fatalf("Normalize(empty) should default, got %q", got)
	}
	if got := Normalize("nope"); got != Default {
		t.Fatalf("Normalize(unknown) should default, got %q", got)
	}
}

func TestDefaultModelFor(t *testing.T) {
	if got := DefaultModelFor(Ollama); got != OllamaDefaultModel {
		t.Errorf("DefaultModelFor(ollama) = %q, want %q", got, OllamaDefaultModel)
	}
	for _, p := range List() {
		if got := DefaultModelFor(p.ID); got == "" {
			t.Errorf("DefaultModelFor(%q) is empty", p.ID)
		}
	}
	// Empty / unknown normalize to the default provider's default model.
	if got := DefaultModelFor(""); got != OllamaDefaultModel {
		t.Errorf("DefaultModelFor(empty) = %q, want %q", got, OllamaDefaultModel)
	}
	if got := DefaultModelFor("nope"); got != OllamaDefaultModel {
		t.Errorf("DefaultModelFor(unknown) = %q, want %q", got, OllamaDefaultModel)
	}
}

func TestListTextMarksActive(t *testing.T) {
	txt := ListText(Ollama)
	if !strings.Contains(txt, "* ollama") {
		t.Fatalf("active provider not marked:\n%s", txt)
	}
	// Unknown/empty active falls back to the default being marked.
	if txt := ListText(""); !strings.Contains(txt, "* ollama") {
		t.Fatalf("empty active should mark default:\n%s", txt)
	}
}
