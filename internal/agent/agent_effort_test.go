package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// modelThinkFloor (the #2 model-driven floor) ratchets off → medium → high as the
// per-turn heavy-thinking tally grows, and never above high.
func TestModelThinkFloor(t *testing.T) {
	a := &Agent{}
	if got := a.modelThinkFloor(); got != config.ReasoningOff {
		t.Errorf("no heavy-thinking steps → %q, want off", got)
	}
	a.turnThinkHard.Store(1)
	if got := a.modelThinkFloor(); got != config.ReasoningMedium {
		t.Errorf("one heavy step → %q, want medium", got)
	}
	a.turnThinkHard.Store(2)
	if got := a.modelThinkFloor(); got != config.ReasoningHigh {
		t.Errorf("two heavy steps → %q, want high", got)
	}
	a.turnThinkHard.Store(9)
	if got := a.modelThinkFloor(); got != config.ReasoningHigh {
		t.Errorf("many heavy steps → %q, want high (capped)", got)
	}
}

// responseThought detects an adaptive-thinking block regardless of display mode
// (present even when its text is omitted); text-only / nil → false.
func TestResponseThought(t *testing.T) {
	if responseThought(nil) {
		t.Error("nil response → false")
	}
	if responseThought(&llm.Response{Blocks: []llm.ContentBlock{{Type: llm.BlockText, Text: "hi"}}}) {
		t.Error("text-only response → false")
	}
	if !responseThought(&llm.Response{Blocks: []llm.ContentBlock{{Type: llm.BlockThinking}, {Type: llm.BlockText}}}) {
		t.Error("thinking block present → true")
	}
	if !responseThought(&llm.Response{Blocks: []llm.ContentBlock{{Type: llm.BlockRedactedThinking}}}) {
		t.Error("redacted thinking block → true")
	}
}
