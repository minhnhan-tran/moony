package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mkTask(store *TaskStore, persona string) *bgTask {
	bt := &bgTask{id: store.newID(), persona: persona, prompt: "p", started: time.Now(), state: taskRunning}
	store.add(bt)
	return bt
}

func TestBgTaskSnapshotCursor(t *testing.T) {
	bt := &bgTask{state: taskRunning}
	bt.appendOutput("hello ")
	out, st := bt.snapshot()
	if out != "hello " || st != taskRunning {
		t.Fatalf("first snapshot: %q %v", out, st)
	}
	// Second snapshot with no new output yields empty (cursor advanced).
	out, _ = bt.snapshot()
	if out != "" {
		t.Errorf("expected no new output, got %q", out)
	}
	bt.appendOutput("world")
	out, _ = bt.snapshot()
	if out != "world" {
		t.Errorf("incremental snapshot: got %q", out)
	}
}

func TestBgTaskFinishOnce(t *testing.T) {
	bt := &bgTask{state: taskRunning, started: time.Now()}
	bt.finish(taskDone, "result A", "", 0.5, 100, 50)
	bt.finish(taskFailed, "result B", "boom", 9, 9, 9) // must be ignored
	res, errMsg, cost, in, out, _ := bt.finalResult()
	if res != "result A" || errMsg != "" || cost != 0.5 || in != 100 || out != 50 {
		t.Fatalf("finish should set once: res=%q err=%q cost=%v in=%d out=%d", res, errMsg, cost, in, out)
	}
	if bt.status() != taskDone {
		t.Errorf("state should stay done, got %v", bt.status())
	}
}

func TestTaskStoreKillAllCancels(t *testing.T) {
	store := newTaskStore()
	cancelled := false
	bt := &bgTask{id: store.newID(), state: taskRunning, cancel: func() { cancelled = true }}
	store.add(bt)
	store.KillAll()
	if !cancelled {
		t.Error("KillAll should cancel a running task")
	}
}

func TestTaskOutputTool(t *testing.T) {
	store := newTaskStore()
	tool := &taskOutputTool{store: store}

	// Unknown handle.
	if _, err := tool.Execute(nil, json.RawMessage(`{"handle":"task_bg_99"}`)); err == nil {
		t.Error("expected unknown-handle error")
	}

	// Running with new output.
	bt := mkTask(store, "explore")
	bt.appendOutput("progress...")
	got, err := tool.Execute(nil, json.RawMessage(`{"handle":"`+bt.id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "running") || !strings.Contains(got, "progress...") {
		t.Errorf("running output missing: %q", got)
	}

	// Done -> final result + footer.
	bt.finish(taskDone, "final answer", "", 0.1234, 10, 20)
	got, err = tool.Execute(nil, json.RawMessage(`{"handle":"`+bt.id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "done") || !strings.Contains(got, "final answer") || !strings.Contains(got, "$0.1234") {
		t.Errorf("done output missing result/footer: %q", got)
	}
}

func TestTaskStatusTool(t *testing.T) {
	store := newTaskStore()
	tool := &taskStatusTool{store: store}

	// No handle + empty store.
	got, _ := tool.Execute(nil, json.RawMessage(`{}`))
	if !strings.Contains(got, "No background tasks") {
		t.Errorf("empty list: %q", got)
	}

	bt := mkTask(store, "planner")
	// Listing (no handle).
	got, _ = tool.Execute(nil, json.RawMessage(`{}`))
	if !strings.Contains(got, bt.id) || !strings.Contains(got, "planner") {
		t.Errorf("listing missing task: %q", got)
	}
	// status must NOT consume output.
	bt.appendOutput("buffered")
	if _, err := tool.Execute(nil, json.RawMessage(`{"handle":"`+bt.id+`"}`)); err != nil {
		t.Fatal(err)
	}
	if out, _ := bt.snapshot(); out != "buffered" {
		t.Errorf("task_status should not consume output; got %q", out)
	}
}

func TestTaskCancelTool(t *testing.T) {
	store := newTaskStore()
	tool := &taskCancelTool{store: store}

	cancelled := false
	bt := &bgTask{id: store.newID(), persona: "explore", state: taskRunning, cancel: func() { cancelled = true }}
	store.add(bt)

	got, err := tool.Execute(nil, json.RawMessage(`{"handle":"`+bt.id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled || !strings.Contains(got, "cancel requested") {
		t.Errorf("cancel failed: cancelled=%v out=%q", cancelled, got)
	}

	// Cancelling a finished task is a no-op message, not an error.
	bt.finish(taskCancelled, "partial", "cancelled", 0, 0, 0)
	got, err = tool.Execute(nil, json.RawMessage(`{"handle":"`+bt.id+`"}`))
	if err != nil || !strings.Contains(got, "already") {
		t.Errorf("cancel finished: err=%v out=%q", err, got)
	}

	// Missing handle is an error.
	if _, err := tool.Execute(nil, json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing handle")
	}
}
