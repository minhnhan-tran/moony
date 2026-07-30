package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// mutatedAgent returns an agent whose todo list is `items` and which is treated
// as having edited a file this turn (the gate the guard requires).
func mutatedAgent(items []tools.Todo) *Agent {
	a := &Agent{todos: tools.NewTodoStore(), snapshots: &snapshotStore{}}
	a.todos.Set(items)
	a.snapshots.startTurn()
	a.snapshots.entries = append(a.snapshots.entries, fileSnapshot{path: "x.go", turn: a.snapshots.turn})
	return a
}

func collectNotices(a *Agent) []string {
	var out []string
	a.maybeWarnStaleTodos(func(e Event) {
		if info, ok := e.(Info); ok {
			out = append(out, info.Text)
		}
	})
	return out
}

// TestStaleTodoWarnsWhenLeftInProgress is the regression this guard exists for: a
// turn that edits files and ends with a task still in_progress must say so, since
// todo_write never completes a task on its own.
func TestStaleTodoWarnsWhenLeftInProgress(t *testing.T) {
	a := mutatedAgent([]tools.Todo{
		{Content: "Bump config.Version", Status: tools.TodoInProgress},
		{Content: "Write the code", Status: tools.TodoCompleted},
	})
	got := collectNotices(a)
	if len(got) != 1 {
		t.Fatalf("expected one notice, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "Bump config.Version") {
		t.Errorf("notice should name the open task, got %q", got[0])
	}
	if !strings.Contains(got[0], "todo_write") {
		t.Errorf("notice should say how to fix it, got %q", got[0])
	}
}

// TestStaleTodoSilentWhenAllClosed: no nagging on a properly-closed list.
func TestStaleTodoSilentWhenAllClosed(t *testing.T) {
	a := mutatedAgent([]tools.Todo{
		{Content: "a", Status: tools.TodoCompleted},
		{Content: "b", Status: tools.TodoCompleted},
		{Content: "c", Status: tools.TodoPending}, // pending is fine — not yet started
	})
	if got := collectNotices(a); len(got) != 0 {
		t.Fatalf("expected silence for a list with nothing in progress, got %v", got)
	}
}

// TestStaleTodoSilentWithoutEdits: a read-only turn may legitimately be mid-task,
// so the guard stays quiet unless files actually changed.
func TestStaleTodoSilentWithoutEdits(t *testing.T) {
	a := &Agent{todos: tools.NewTodoStore(), snapshots: &snapshotStore{}}
	a.todos.Set([]tools.Todo{{Content: "exploring", Status: tools.TodoInProgress}})
	a.snapshots.startTurn() // turn started, but nothing recorded
	if got := collectNotices(a); len(got) != 0 {
		t.Fatalf("expected silence on a read-only turn, got %v", got)
	}
}

// TestStaleTodoSilentWithEmptyList: no list, no notice.
func TestStaleTodoSilentWithEmptyList(t *testing.T) {
	if got := collectNotices(mutatedAgent(nil)); len(got) != 0 {
		t.Fatalf("expected silence with no todos, got %v", got)
	}
}

// TestStaleTodoTitlesCapped keeps the notice short when many tasks are open, and
// still reports the TRUE total rather than the truncated length.
func TestStaleTodoTitlesCapped(t *testing.T) {
	var items []tools.Todo
	for i := 0; i < staleTodoLimit+3; i++ {
		items = append(items, tools.Todo{Content: fmt.Sprintf("task %d", i), Status: tools.TodoInProgress})
	}
	got, total := staleTodoTitles(items)
	if len(got) != staleTodoLimit+1 {
		t.Fatalf("expected %d entries (cap + marker), got %d: %v", staleTodoLimit+1, len(got), got)
	}
	if last := got[len(got)-1]; last != "+3 more" {
		t.Errorf("expected a '+3 more' marker, got %q", last)
	}
	if total != staleTodoLimit+3 {
		t.Errorf("total should count every open task, got %d want %d", total, staleTodoLimit+3)
	}
}

// TestStaleTodoCountIsNotTruncated pins the arithmetic in the emitted message: the
// count must be the real number of open tasks, not the length of the truncated
// title list (which would both undercount and count the "+N more" marker).
func TestStaleTodoCountIsNotTruncated(t *testing.T) {
	var items []tools.Todo
	for i := 0; i < 10; i++ {
		items = append(items, tools.Todo{Content: fmt.Sprintf("task %d", i), Status: tools.TodoInProgress})
	}
	got := collectNotices(mutatedAgent(items))
	if len(got) != 1 {
		t.Fatalf("expected one notice, got %v", got)
	}
	if !strings.Contains(got[0], "10 tasks") {
		t.Errorf("notice should report all 10 open tasks, got %q", got[0])
	}
	if strings.Contains(got[0], "4 tasks") {
		t.Errorf("notice reported the truncated title count instead of the total: %q", got[0])
	}
}

// TestStaleTodoSkipsBlankTitles: a blank title shouldn't produce an empty mention.
func TestStaleTodoSkipsBlankTitles(t *testing.T) {
	got, total := staleTodoTitles([]tools.Todo{
		{Content: "   ", Status: tools.TodoInProgress},
		{Content: "real", Status: tools.TodoInProgress},
	})
	if len(got) != 1 || got[0] != "**real**" {
		t.Fatalf("expected only the titled task, got %v", got)
	}
	if total != 1 {
		t.Errorf("blank-titled tasks should not count toward the total, got %d", total)
	}
}

// TestStaleTodoNeverMutatesList: the guard is advisory — it must not silently
// mark anything complete, since only the agent knows if the work finished.
func TestStaleTodoNeverMutatesList(t *testing.T) {
	a := mutatedAgent([]tools.Todo{{Content: "open", Status: tools.TodoInProgress}})
	collectNotices(a)
	after := a.Todos()
	if len(after) != 1 || after[0].Status != tools.TodoInProgress {
		t.Fatalf("guard must not change the list, got %+v", after)
	}
}

// TestStaleTodoNilSafe: the hook runs on every turn, including for agents with no
// todo store or snapshot store wired up.
func TestStaleTodoNilSafe(t *testing.T) {
	(&Agent{}).maybeWarnStaleTodos(func(Event) {})
	mutatedAgent(nil).maybeWarnStaleTodos(nil) // nil emit must not panic
	var a *Agent
	a.maybeWarnStaleTodos(func(Event) {})
}

func TestPluralTasks(t *testing.T) {
	if got := pluralTasks(1); got != "a task" {
		t.Errorf("pluralTasks(1) = %q", got)
	}
	if got := pluralTasks(3); got != "3 tasks" {
		t.Errorf("pluralTasks(3) = %q", got)
	}
}

// TestRunWarnsAboutStaleTodo drives a real turn end-to-end: the model marks a task
// in_progress, writes a file, then ends the turn without closing the task out.
// That is exactly the sequence that used to leave the user's progress view stuck,
// so the turn must surface a notice.
func TestRunWarnsAboutStaleTodo(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	store := tools.NewTodoStore()
	reg.RegisterTodo(store)
	reg.RegisterDefaults(1 << 20)

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{
			toolUse("t1", "todo_write", `{"todos":[{"content":"Ship the fix","status":"in_progress"}]}`),
		}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}},
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{
			toolUse("t2", "write_file", `{"path":"out.txt","content":"done"}`),
		}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("shipped it")}, Usage: llm.Usage{InputTokens: 120, OutputTokens: 5}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.AttachTodoStore(store)

	var events []Event
	if err := ag.Run(context.Background(), "ship the fix", func(ev Event) {
		events = append(events, ev)
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var found string
	for _, msg := range collectInfo(events) {
		if strings.Contains(msg, "Ship the fix") {
			found = msg
		}
	}
	if found == "" {
		t.Fatalf("expected a stale-todo notice naming the open task, got %v", collectInfo(events))
	}
	// Advisory only: the list itself must be untouched.
	if items := ag.Todos(); len(items) != 1 || items[0].Status != tools.TodoInProgress {
		t.Errorf("guard must not mutate the list, got %+v", items)
	}
}
