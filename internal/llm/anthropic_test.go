package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// TestAnthropicAdaptiveThinking: 4.6-and-newer families use adaptive thinking;
// 4.5-and-older (and the 3.x/2.x families, and unknown ids) use the legacy
// budget mode. Provider prefixes and dated/Bedrock suffixes must not confuse the
// version parse.
func TestAnthropicAdaptiveThinking(t *testing.T) {
	adaptive := []string{
		"claude-opus-4-8", "anthropic.claude-opus-4-8",
		"claude-opus-4-7", "claude-opus-4-6",
		"claude-sonnet-5", "anthropic.claude-sonnet-5",
		"claude-sonnet-4-6", "claude-fable-5", "claude-mythos-5",
	}
	legacy := []string{
		"claude-sonnet-4-5", "anthropic.claude-sonnet-4-5-20250929-v1:0",
		"claude-opus-4-5", "claude-opus-4-1-20250805-v1:0",
		"claude-haiku-4-5", "anthropic.claude-haiku-4-5-20251001-v1:0",
		"claude-opus-4-0", "claude-3-5-sonnet-20241022", "claude-3-opus-20240229",
		"some-unknown-model", // unrecognized → legacy default
	}
	for _, m := range adaptive {
		if !anthropicAdaptiveThinking(m) {
			t.Errorf("anthropicAdaptiveThinking(%q) = false, want true (rank %d)", m, anthropicModelRank(m))
		}
	}
	for _, m := range legacy {
		if anthropicAdaptiveThinking(m) {
			t.Errorf("anthropicAdaptiveThinking(%q) = true, want false (rank %d)", m, anthropicModelRank(m))
		}
	}
}

// TestAnthropicEffort maps reasoning levels to effort strings, and clamps xhigh
// to high on 4.6 (where xhigh doesn't exist yet) but keeps it on 4.7+.
func TestAnthropicEffort(t *testing.T) {
	cases := []struct {
		model, level, want string
	}{
		{"claude-opus-4-8", config.ReasoningMinimal, "low"},
		{"claude-opus-4-8", config.ReasoningLow, "low"},
		{"claude-opus-4-8", config.ReasoningMedium, "medium"},
		{"claude-opus-4-8", config.ReasoningHigh, "high"},
		{"claude-opus-4-8", config.ReasoningXHigh, "xhigh"},
		{"claude-opus-4-8", config.ReasoningMax, "max"},
		{"claude-opus-4-8", config.ReasoningAuto, "medium"},  // concrete fallback
		{"claude-sonnet-4-6", config.ReasoningXHigh, "high"}, // 4.6 clamps xhigh→high
		{"claude-opus-4-7", config.ReasoningXHigh, "xhigh"},  // 4.7 keeps xhigh
	}
	for _, c := range cases {
		if got := anthropicEffort(c.model, c.level); got != c.want {
			t.Errorf("anthropicEffort(%q, %q) = %q, want %q", c.model, c.level, got, c.want)
		}
	}
}

// TestAnthropicThinkingWireShape: the adaptive shape marshals thinking.type
// "adaptive" with NO budget_tokens plus output_config.effort; the legacy shape
// marshals thinking.type "enabled" + budget_tokens and no output_config.
func TestAnthropicThinkingWireShape(t *testing.T) {
	adaptive := mustMarshal(t, &request{
		Model:        "claude-opus-4-8",
		MaxTokens:    16000,
		Thinking:     &thinking{Type: "adaptive", Display: "summarized"},
		OutputConfig: &outputConfig{Effort: "high"},
	})
	if !strings.Contains(adaptive, `"type":"adaptive"`) {
		t.Errorf("adaptive request missing adaptive type: %s", adaptive)
	}
	if strings.Contains(adaptive, "budget_tokens") {
		t.Errorf("adaptive request must not send budget_tokens: %s", adaptive)
	}
	if !strings.Contains(adaptive, `"output_config":{"effort":"high"}`) {
		t.Errorf("adaptive request missing output_config.effort: %s", adaptive)
	}
	if !strings.Contains(adaptive, `"display":"summarized"`) {
		t.Errorf("adaptive request missing display:summarized: %s", adaptive)
	}

	legacy := mustMarshal(t, &request{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 16000,
		Thinking:  &thinking{Type: "enabled", BudgetTokens: 8192},
	})
	if !strings.Contains(legacy, `"type":"enabled"`) || !strings.Contains(legacy, `"budget_tokens":8192`) {
		t.Errorf("legacy request missing enabled/budget_tokens: %s", legacy)
	}
	if strings.Contains(legacy, "output_config") {
		t.Errorf("legacy request must not send output_config: %s", legacy)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestThinkingBlockPreservesEmptySignature: a thinking block must marshal its
// `thinking` field even when empty (adaptive display:"omitted" yields empty text
// with a real signature) so the block round-trips verbatim. Dropping the empty
// field via the shared json:"thinking,omitempty" tag changes the canonical form
// and the API rejects the replay with "Invalid signature in thinking block".
func TestThinkingBlockPreservesEmptySignature(t *testing.T) {
	raw := mustMarshal(t, ContentBlock{Type: BlockThinking, Thinking: "", Signature: "sig-abc"})
	if !strings.Contains(raw, `"thinking":""`) {
		t.Errorf("empty thinking must still emit the field: %s", raw)
	}
	if !strings.Contains(raw, `"signature":"sig-abc"`) {
		t.Errorf("signature missing: %s", raw)
	}
	// Round-trips through Unmarshal unchanged (session-store persistence).
	var back ContentBlock
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != BlockThinking || back.Thinking != "" || back.Signature != "sig-abc" {
		t.Errorf("round-trip mismatch: %+v", back)
	}
	// A thinking block with text keeps both fields.
	raw2 := mustMarshal(t, ContentBlock{Type: BlockThinking, Thinking: "reasoning", Signature: "s2"})
	if !strings.Contains(raw2, `"thinking":"reasoning"`) || !strings.Contains(raw2, `"signature":"s2"`) {
		t.Errorf("non-empty thinking block malformed: %s", raw2)
	}
}
