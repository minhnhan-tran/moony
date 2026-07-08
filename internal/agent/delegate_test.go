package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/worker"
)

func hasTool(reg *tools.Registry, name string) bool {
	for _, n := range reg.Names() {
		if n == name {
			return true
		}
	}
	return false
}

func TestAttachWorkersRegistersDelegate(t *testing.T) {
	reg := tools.NewRegistry(t.TempDir())
	a := New(&config.Config{WorkingDir: t.TempDir()}, nil, reg, nil, nil)

	mgr := worker.New(t.TempDir(), []worker.Config{{Name: "explorer", Backend: worker.BackendOptimus}})
	a.AttachWorkers(mgr)

	if !hasTool(reg, "delegate") {
		t.Fatal("delegate tool should be registered when workers are configured")
	}
	if !hasTool(reg, "worker_reset") {
		t.Fatal("worker_reset tool should be registered")
	}
}

func TestAttachWorkersNoopWhenEmpty(t *testing.T) {
	reg := tools.NewRegistry(t.TempDir())
	a := New(&config.Config{WorkingDir: t.TempDir()}, nil, reg, nil, nil)

	a.AttachWorkers(worker.New(t.TempDir(), nil)) // no workers configured

	if hasTool(reg, "delegate") {
		t.Fatal("delegate tool should NOT be registered with no workers")
	}
}

func TestWorkerActivityContextRoundTrip(t *testing.T) {
	var gotWorker, gotKind, gotText string
	ctx := withWorkerActivity(context.Background(), func(w, k, txt string) {
		gotWorker, gotKind, gotText = w, k, txt
	})
	fn := workerActivityFrom(ctx)
	if fn == nil {
		t.Fatal("expected an emitter from context")
	}
	fn("explorer", "tool", "reading main.go")
	if gotWorker != "explorer" || gotKind != "tool" || gotText != "reading main.go" {
		t.Fatalf("round-trip wrong: %q %q %q", gotWorker, gotKind, gotText)
	}
	if workerActivityFrom(context.Background()) != nil {
		t.Fatal("a bare context should have no emitter")
	}
}

// activityTool emits worker-activity from the context, exercising the streaming
// plumbing that delegate uses.
type activityTool struct{}

func (activityTool) Name() string            { return "fakeact" }
func (activityTool) Description() string     { return "test tool" }
func (activityTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (activityTool) Mutates() bool           { return false }
func (activityTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if fn := workerActivityFrom(ctx); fn != nil {
		fn("w1", "tool", "did a thing")
		fn("w1", "info", "thinking")
	}
	return "done", nil
}

func TestWorkerActivityStreamsThroughExecute(t *testing.T) {
	reg := tools.NewRegistry(t.TempDir())
	reg.Register(activityTool{})
	a := New(&config.Config{WorkingDir: t.TempDir(), MaxToolOutputBytes: 1 << 20}, nil, reg, nil, nil)

	var acts []WorkerActivity
	emit := func(ev Event) {
		if w, ok := ev.(WorkerActivity); ok {
			acts = append(acts, w)
		}
	}
	a.executeApprovedCall(context.Background(), emit, llm.ContentBlock{ID: "call-1", Name: "fakeact", Input: json.RawMessage(`{}`)})

	if len(acts) != 2 {
		t.Fatalf("want 2 worker activities, got %d: %+v", len(acts), acts)
	}
	if acts[0].ID != "call-1" || acts[0].Worker != "w1" || acts[0].Kind != "tool" {
		t.Fatalf("activity tagged wrong: %+v", acts[0])
	}
	if acts[1].Kind != "info" {
		t.Fatalf("second activity kind = %q", acts[1].Kind)
	}
}
