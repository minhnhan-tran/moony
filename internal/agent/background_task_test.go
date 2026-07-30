package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/subagents"
)

// blockingStreamer stands in for the LLM: StreamMessages blocks until the run's
// context is cancelled (simulating a long-running sub-agent), so we can assert
// task(..., background:true) returns a handle IMMEDIATELY without waiting.
type blockingStreamer struct{ started chan struct{} }

func (b *blockingStreamer) StreamMessages(ctx context.Context, system string, msgs []llm.Message, tools []llm.Tool, h llm.Handler) (*llm.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done() // block until cancelled
	return nil, ctx.Err()
}
func (b *blockingStreamer) ListModels(ctx context.Context) ([]config.ModelInfo, error) {
	return nil, nil
}
func (b *blockingStreamer) Usage(ctx context.Context) (*llm.UsageResponse, error) { return nil, nil }
func (b *blockingStreamer) CheckAuth(ctx context.Context) error                   { return nil }

func TestBackgroundTaskReturnsHandleImmediately(t *testing.T) {
	streamer := &blockingStreamer{started: make(chan struct{}, 1)}
	parent := &Agent{
		cfg:         &config.Config{SubagentMaxDepth: 2, MaxToolOutputBytes: 4096, WorkingDir: t.TempDir()},
		client:      streamer,
		alwaysAllow: map[string]bool{},
		taskStore:   newTaskStore(),
	}
	tt := newTaskTool(parent, []subagents.Persona{{Name: "explore", Description: "d", SystemPrompt: "s"}})

	done := make(chan string, 1)
	go func() {
		out, err := tt.Execute(context.Background(), json.RawMessage(`{"agent":"explore","prompt":"long job","background":true}`))
		if err != nil {
			t.Errorf("background Execute error: %v", err)
		}
		done <- out
	}()

	var handle string
	select {
	case out := <-done:
		if !strings.Contains(out, "background") || !strings.Contains(out, "task_bg_") {
			t.Fatalf("expected an immediate background handle, got %q", out)
		}
		// Extract the handle for polling.
		if i := strings.Index(out, "task_bg_"); i >= 0 {
			handle = out[i:]
			handle = strings.FieldsFunc(handle, func(r rune) bool { return r == ')' || r == ',' || r == ' ' })[0]
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background task did not return a handle promptly (it blocked)")
	}

	// The run should be executing (streamer reached).
	select {
	case <-streamer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("sub-agent run never started")
	}

	// task_status reports running; task_cancel stops it.
	statusTool := &taskStatusTool{store: parent.taskStore}
	st, _ := statusTool.Execute(context.Background(), json.RawMessage(`{"handle":"`+handle+`"}`))
	if !strings.Contains(st, "running") {
		t.Errorf("expected running status, got %q", st)
	}

	cancelTool := &taskCancelTool{store: parent.taskStore}
	if _, err := cancelTool.Execute(context.Background(), json.RawMessage(`{"handle":"`+handle+`"}`)); err != nil {
		t.Fatal(err)
	}

	// After cancel, it should reach a terminal (cancelled) state.
	bt, _ := parent.taskStore.get(handle)
	deadline := time.After(2 * time.Second)
	for bt.status() == taskRunning {
		select {
		case <-deadline:
			t.Fatal("task did not stop after cancel")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if bt.status() != taskCancelled {
		t.Errorf("expected cancelled, got %v", bt.status())
	}
}
