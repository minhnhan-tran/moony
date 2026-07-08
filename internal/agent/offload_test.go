package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

func TestReadToolCallIDs(t *testing.T) {
	calls := []llm.ContentBlock{
		{Type: llm.BlockToolUse, ID: "1", Name: "read_file"},
		{Type: llm.BlockToolUse, ID: "2", Name: "grep"},
		{Type: llm.BlockToolUse, ID: "3", Name: "search_code"},
		{Type: llm.BlockToolUse, ID: "4", Name: "bash"},      // not a read tool
		{Type: llm.BlockToolUse, ID: "5", Name: "edit_file"}, // mutating
		{Type: llm.BlockText, Text: "thinking"},              // not a tool call
	}
	got := readToolCallIDs(calls)
	want := map[string]bool{"1": true, "2": true, "3": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("expected read tool id %q to be included, got %v", id, got)
		}
	}
	if got["4"] || got["5"] {
		t.Errorf("non-read tool ids must be excluded, got %v", got)
	}
}
