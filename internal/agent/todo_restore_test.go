package agent

import (
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// todoHistory builds a saved-session message pair: a todo_write tool_use plus a
// successful tool_result.
func todoHistory(id string, items []map[string]any) []llm.Message {
	input, _ := json.Marshal(map[string]any{"todos": items})
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, ID: id, Name: "todo_write", Input: input},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.BlockToolResult, ToolUseID: id, Content: "ok"},
		}},
	}
}

func TestAttachTodoStoreSeedsFromHistory(t *testing.T) {
	sess := &session.Session{Messages: todoHistory("t1", []map[string]any{
		{"content": "resumed task", "status": "in_progress"},
		{"content": "later task", "status": "pending"},
	})}
	a := New(&config.Config{}, nil, tools.NewRegistry(t.TempDir()), nil, sess)

	a.AttachTodoStore(tools.NewTodoStore())

	got := a.Todos()
	if len(got) != 2 || got[0].Content != "resumed task" {
		t.Fatalf("AttachTodoStore should seed todos from history, got %+v", got)
	}
}

func TestAttachTodoStoreEmptyHistory(t *testing.T) {
	a := New(&config.Config{}, nil, tools.NewRegistry(t.TempDir()), nil, nil)
	a.AttachTodoStore(tools.NewTodoStore())
	if got := a.Todos(); len(got) != 0 {
		t.Fatalf("fresh session should have no todos, got %+v", got)
	}
}

func TestResumeReplacesTodos(t *testing.T) {
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess1 := session.New("m", t.TempDir())
	sess1.Messages = todoHistory("a", []map[string]any{{"content": "first session task", "status": "in_progress"}})

	a := New(&config.Config{}, nil, tools.NewRegistry(t.TempDir()), store, sess1)
	a.AttachTodoStore(tools.NewTodoStore())
	if got := a.Todos(); len(got) != 1 || got[0].Content != "first session task" {
		t.Fatalf("initial seed wrong: %+v", got)
	}

	// Switching to another session replaces the panel with that session's list.
	sess2 := session.New("m", t.TempDir())
	sess2.Messages = todoHistory("b", []map[string]any{
		{"content": "second session task", "status": "completed"},
	})
	a.Resume(sess2)
	got := a.Todos()
	if len(got) != 1 || got[0].Content != "second session task" {
		t.Fatalf("Resume should replace todos with the new session's list, got %+v", got)
	}
}

func TestResetClearsTodos(t *testing.T) {
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("m", t.TempDir())
	sess.Messages = todoHistory("a", []map[string]any{{"content": "task", "status": "in_progress"}})

	a := New(&config.Config{}, nil, tools.NewRegistry(t.TempDir()), store, sess)
	a.AttachTodoStore(tools.NewTodoStore())
	if got := a.Todos(); len(got) != 1 {
		t.Fatalf("expected seeded todo, got %+v", got)
	}

	a.Reset()
	if got := a.Todos(); len(got) != 0 {
		t.Fatalf("Reset should clear the task list, got %+v", got)
	}
}
