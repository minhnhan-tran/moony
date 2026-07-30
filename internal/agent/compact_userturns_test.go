package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

// TestCountUserTurns verifies the compaction turn count matches what the web
// client renders as "user" turns: RoleUser messages with a real prompt (text,
// image, or document), but NOT pure tool_result carriers (also RoleUser) or
// assistant messages. It's the number of leading turns the UI dims after a
// compaction, so an off-by-one here mis-marks the compaction horizon.
func TestCountUserTurns(t *testing.T) {
	toolResult := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.ToolResultBlock("tu_1", "output", false)},
	}
	image := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.ImageBlock("image/png", "AAAA")},
	}
	blankUser := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "   "}},
	}

	cases := []struct {
		name string
		msgs []llm.Message
		want int
	}{
		{"empty", nil, 0},
		{"only assistant", []llm.Message{llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("hi")}}}, 0},
		{"only tool results", []llm.Message{toolResult, toolResult}, 0},
		{"blank user text ignored", []llm.Message{blankUser}, 0},
		{"single prompt", []llm.Message{llm.UserText("first")}, 1},
		{"image counts as a turn", []llm.Message{image}, 1},
		{
			"realistic conversation",
			[]llm.Message{
				llm.UserText("q1"),
				llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("a1")}},
				toolResult,
				llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("a1 cont")}},
				llm.UserText("q2"),
				llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("a2")}},
			},
			2, // two genuine user prompts -> two client turns
		},
	}
	for _, tc := range cases {
		if got := countUserTurns(tc.msgs); got != tc.want {
			t.Errorf("%s: countUserTurns = %d, want %d", tc.name, got, tc.want)
		}
	}
}
