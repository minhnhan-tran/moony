package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/pysty1995/moony/internal/llm"
)

// TodoStatus is the lifecycle state of a todo item.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
	TodoCancelled  TodoStatus = "cancelled"
)

// Todo is a single agent task list item.
type Todo struct {
	Content  string     `json:"content"`
	Status   TodoStatus `json:"status"`
	Priority string     `json:"priority,omitempty"`
}

// ParseTodoInput extracts the task list from a todo_write tool-call input
// (`{"todos":[...]}`), applying the same missing-status default the tool uses
// and dropping items with empty content. It returns nil on malformed input.
// Used to reconstruct the task-list panel when a saved session is reopened.
func ParseTodoInput(input json.RawMessage) []Todo {
	var in struct {
		Todos []Todo `json:"todos"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil
	}
	out := make([]Todo, 0, len(in.Todos))
	for _, td := range in.Todos {
		if strings.TrimSpace(td.Content) == "" {
			continue
		}
		if td.Status == "" {
			td.Status = TodoPending
		}
		out = append(out, td)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LatestTodosFromMessages reconstructs the final task list from a conversation
// history by replaying todo_write tool calls (each call replaces the whole
// list, so the last successful one wins). A call whose recorded tool result is
// an error is ignored, since it never updated the live list. Returns nil when
// the session never produced a task list. This lets a reopened/persisted
// session restore its todo panel without re-running the model.
func LatestTodosFromMessages(messages []llm.Message) []Todo {
	// Map each tool_use id to whether its result errored.
	errored := map[string]bool{}
	for _, m := range messages {
		for _, c := range m.Content {
			if c.Type == llm.BlockToolResult {
				errored[c.ToolUseID] = c.IsError
			}
		}
	}

	var latest json.RawMessage
	for _, m := range messages {
		if m.Role != llm.RoleAssistant {
			continue
		}
		for _, c := range m.Content {
			if c.Type == llm.BlockToolUse && c.Name == "todo_write" && !errored[c.ID] {
				latest = c.Input
			}
		}
	}
	if latest == nil {
		return nil
	}
	return ParseTodoInput(latest)
}

// TodoStore is a thread-safe holder for the agent's current task list. The TUI
// reads it (TodoList) and the todo_write tool overwrites it (Set), notifying
// any subscribed listener.
type TodoStore struct {
	mu     sync.RWMutex
	items  []Todo
	notify func([]Todo)
}

// NewTodoStore creates an empty store.
func NewTodoStore() *TodoStore { return &TodoStore{} }

// SetNotify registers a callback fired (with a copy of the items) after every
// Set. Pass nil to clear it.
func (s *TodoStore) SetNotify(fn func([]Todo)) {
	s.mu.Lock()
	s.notify = fn
	s.mu.Unlock()
}

// Set replaces the task list and fires the notification (outside the lock).
func (s *TodoStore) Set(items []Todo) {
	s.mu.Lock()
	s.items = append([]Todo(nil), items...)
	cb := s.notify
	snapshot := append([]Todo(nil), s.items...)
	s.mu.Unlock()
	if cb != nil {
		cb(snapshot)
	}
}

// Items returns a copy of the current task list.
func (s *TodoStore) Items() []Todo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Todo(nil), s.items...)
}

// todoWriteTool is the agent-facing wrapper. It is non-mutating from the
// approval perspective (no filesystem/shell side effects).
type todoWriteTool struct {
	store *TodoStore
}

func (t *todoWriteTool) Name() string { return "todo_write" }

func (t *todoWriteTool) Description() string {
	return "Maintain a structured task list for the current session. Pass the " +
		"COMPLETE list every call (the previous list is replaced). Use this " +
		"proactively for multi-step work: enumerate the steps up front, mark " +
		"exactly one task 'in_progress' while you work it, mark it 'completed' " +
		"as soon as it's done, and append new tasks as they emerge. " +
		"Each item: {content, status: pending|in_progress|completed|cancelled, " +
		"priority?: low|medium|high}."
}

func (t *todoWriteTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"todos": {
				"type": "array",
				"description": "The full updated task list, replacing the previous one.",
				"items": {
					"type": "object",
					"properties": {
						"content":  {"type": "string", "description": "Brief description of the task."},
						"status":   {"type": "string", "enum": ["pending", "in_progress", "completed", "cancelled"]},
						"priority": {"type": "string", "enum": ["low", "medium", "high"]}
					},
					"required": ["content", "status"]
				}
			}
		},
		"required": ["todos"]
	}`)
}

func (t *todoWriteTool) Mutates() bool { return false }

func (t *todoWriteTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Todos []Todo `json:"todos"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}

	// Validate each item's status; default missing priority to "medium".
	in_progress := 0
	for i := range in.Todos {
		switch in.Todos[i].Status {
		case TodoPending, TodoInProgress, TodoCompleted, TodoCancelled:
		case "":
			in.Todos[i].Status = TodoPending
		default:
			return "", fmt.Errorf("todo %d: invalid status %q", i, in.Todos[i].Status)
		}
		if in.Todos[i].Status == TodoInProgress {
			in_progress++
		}
		if strings.TrimSpace(in.Todos[i].Content) == "" {
			return "", fmt.Errorf("todo %d: content is required", i)
		}
	}
	if in_progress > 1 {
		return "", fmt.Errorf("only one todo may be in_progress at a time (found %d)", in_progress)
	}

	t.store.Set(in.Todos)

	// Return a compact echo so the model can confirm what was recorded.
	var b strings.Builder
	fmt.Fprintf(&b, "Updated task list (%d items):\n", len(in.Todos))
	for i, td := range in.Todos {
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, td.Status, td.Content)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
