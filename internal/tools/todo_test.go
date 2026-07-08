package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

func TestTodoWriteUpdatesStore(t *testing.T) {
	store := NewTodoStore()
	var got []Todo
	store.SetNotify(func(items []Todo) { got = items })

	tool := &todoWriteTool{store: store}
	in, _ := json.Marshal(map[string]any{
		"todos": []map[string]any{
			{"content": "design schema", "status": "completed", "priority": "high"},
			{"content": "wire UI", "status": "in_progress"},
			{"content": "add tests", "status": "pending"},
		},
	})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("notify expected 3 items, got %d", len(got))
	}
	if got[1].Status != TodoInProgress {
		t.Errorf("second item should be in_progress, got %q", got[1].Status)
	}
	if !strings.Contains(out, "wire UI") {
		t.Errorf("echo missing item: %s", out)
	}
	if items := store.Items(); len(items) != 3 {
		t.Fatalf("store snapshot expected 3, got %d", len(items))
	}
}

func TestTodoWriteRejectsMultipleInProgress(t *testing.T) {
	store := NewTodoStore()
	tool := &todoWriteTool{store: store}
	in, _ := json.Marshal(map[string]any{
		"todos": []map[string]any{
			{"content": "a", "status": "in_progress"},
			{"content": "b", "status": "in_progress"},
		},
	})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for two in_progress items")
	}
}

func TestTodoWriteRejectsBadStatus(t *testing.T) {
	tool := &todoWriteTool{store: NewTodoStore()}
	in, _ := json.Marshal(map[string]any{
		"todos": []map[string]any{{"content": "x", "status": "bogus"}},
	})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestTodoWriteDefaultsMissingStatus(t *testing.T) {
	store := NewTodoStore()
	tool := &todoWriteTool{store: store}
	in, _ := json.Marshal(map[string]any{
		"todos": []map[string]any{{"content": "no status"}},
	})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if items := store.Items(); items[0].Status != TodoPending {
		t.Errorf("missing status should default to pending, got %q", items[0].Status)
	}
}

// todoWriteUse builds an assistant tool_use block for todo_write with the given
// items, and a matching (optionally error) tool_result, mimicking saved history.
func todoWriteUse(id string, isErr bool, items []map[string]any) []llm.Message {
	input, _ := json.Marshal(map[string]any{"todos": items})
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: id, Name: "todo_write", Input: input},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: id, Content: "ok", IsError: isErr},
		}},
	}
}

func TestParseTodoInput(t *testing.T) {
	in, _ := json.Marshal(map[string]any{"todos": []map[string]any{
		{"content": "a", "status": "completed"},
		{"content": "b"},                        // missing status -> pending
		{"content": "   ", "status": "pending"}, // blank content -> dropped
	}})
	got := ParseTodoInput(in)
	if len(got) != 2 {
		t.Fatalf("want 2 items (blank dropped), got %d: %+v", len(got), got)
	}
	if got[0].Status != TodoCompleted {
		t.Errorf("item 0 status = %q", got[0].Status)
	}
	if got[1].Status != TodoPending {
		t.Errorf("item 1 should default to pending, got %q", got[1].Status)
	}

	if ParseTodoInput(json.RawMessage("not json")) != nil {
		t.Error("malformed input should yield nil")
	}
	if ParseTodoInput(json.RawMessage(`{"todos":[]}`)) != nil {
		t.Error("empty list should yield nil")
	}
}

func TestLatestTodosFromMessages(t *testing.T) {
	var msgs []llm.Message
	// First list (superseded), then the final one.
	msgs = append(msgs, todoWriteUse("t1", false, []map[string]any{
		{"content": "old task", "status": "in_progress"},
	})...)
	msgs = append(msgs, todoWriteUse("t2", false, []map[string]any{
		{"content": "final A", "status": "completed"},
		{"content": "final B", "status": "in_progress"},
	})...)

	got := LatestTodosFromMessages(msgs)
	if len(got) != 2 {
		t.Fatalf("want 2 (last call wins), got %d: %+v", len(got), got)
	}
	if got[0].Content != "final A" || got[1].Content != "final B" {
		t.Fatalf("unexpected reconstructed list: %+v", got)
	}
}

func TestLatestTodosFromMessagesIgnoresFailedCall(t *testing.T) {
	var msgs []llm.Message
	msgs = append(msgs, todoWriteUse("t1", false, []map[string]any{
		{"content": "good list", "status": "pending"},
	})...)
	// A later call that errored must not override the good list.
	msgs = append(msgs, todoWriteUse("t2", true, []map[string]any{
		{"content": "bad list", "status": "in_progress"},
	})...)

	got := LatestTodosFromMessages(msgs)
	if len(got) != 1 || got[0].Content != "good list" {
		t.Fatalf("failed call should be ignored, got %+v", got)
	}
}

func TestLatestTodosFromMessagesNone(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "hello"}}},
	}
	if got := LatestTodosFromMessages(msgs); got != nil {
		t.Fatalf("no todo_write should yield nil, got %+v", got)
	}
}
