package agent

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

func TestLastUserText(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("first request about parser"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "ok"}}},
		// user-role message carrying only a tool_result must be skipped
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock("id", "tool output", false)}},
		llm.UserText("now fix the tokenizer"),
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultBlock("id2", "more output", false)}},
	}
	if got := lastUserText(msgs); got != "now fix the tokenizer" {
		t.Errorf("lastUserText = %q, want %q", got, "now fix the tokenizer")
	}
}

func TestGoalTermsAndRelevance(t *testing.T) {
	terms := goalTerms("Please fix the tokenizer and the embedding cache")
	// stopwords/short words dropped; content terms kept.
	for _, w := range []string{"fix", "tokenizer", "embedding", "cache"} {
		if !terms[w] {
			t.Errorf("expected term %q", w)
		}
	}
	if terms["the"] || terms["and"] {
		t.Error("stopwords should be dropped")
	}

	rel := "the embedding cache stores tokenizer vectors on disk"
	if s := relevanceScore(rel, terms); s < 0.5 {
		t.Errorf("relevant text score %v, want >= 0.5", s)
	}
	if s := relevanceScore("unrelated text about colors", terms); s != 0 {
		t.Errorf("irrelevant text score %v, want 0", s)
	}
}

func TestRelevantExcerpt(t *testing.T) {
	body := strings.Repeat("A", 700) + strings.Repeat("B", 5000) + strings.Repeat("C", 300)
	ex := relevantExcerpt(body, 700, 300)
	if len(ex) >= len(body) {
		t.Fatalf("excerpt not smaller: %d vs %d", len(ex), len(body))
	}
	if !strings.HasPrefix(ex, strings.Repeat("A", 700)) || !strings.HasSuffix(ex, strings.Repeat("C", 300)) {
		t.Error("excerpt should keep head and tail")
	}
	if !strings.Contains(ex, "middle elided") {
		t.Error("excerpt should note the elided middle")
	}
	// Short content is returned unchanged.
	if got := relevantExcerpt("short", 700, 300); got != "short" {
		t.Errorf("short content should be unchanged, got %q", got)
	}
}
