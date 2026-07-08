package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// recordingTool is a fake tool that tracks how many invocations run at once, so
// tests can prove concurrency. When solo is set it asserts it never overlaps
// with another invocation (used to verify mutating tools act as barriers).
type recordingTool struct {
	name     string
	mutates  bool
	delay    time.Duration
	solo     bool
	active   *int32
	maxSeen  *int32
	soloFail *int32
}

func (r *recordingTool) Name() string            { return r.name }
func (r *recordingTool) Description() string     { return r.name }
func (r *recordingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r *recordingTool) Mutates() bool           { return r.mutates }
func (r *recordingTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	n := atomic.AddInt32(r.active, 1)
	for { // record the high-water mark of concurrent invocations
		old := atomic.LoadInt32(r.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(r.maxSeen, old, n) {
			break
		}
	}
	if r.solo && n != 1 {
		atomic.StoreInt32(r.soloFail, 1)
	}
	time.Sleep(r.delay)
	atomic.AddInt32(r.active, -1)
	return "out:" + string(input), nil
}

func toolCalls(name string, n int) []llm.ContentBlock {
	calls := make([]llm.ContentBlock, n)
	for i := range calls {
		calls[i] = llm.ContentBlock{
			Type:  llm.BlockToolUse,
			ID:    name + strconv.Itoa(i),
			Name:  name,
			Input: json.RawMessage(strconv.Itoa(i)),
		}
	}
	return calls
}

func newParallelTestAgent(reg *tools.Registry) *Agent {
	return &Agent{
		cfg:         &config.Config{MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: config.ApprovalAuto},
		tools:       reg,
		alwaysAllow: map[string]bool{},
		snapshots:   &snapshotStore{},
	}
}

func TestParallelToolConfig(t *testing.T) {
	cases := []struct {
		val         string
		wantEnabled bool
		wantLimit   int
	}{
		{"", true, defaultParallelToolLimit},
		{"on", true, defaultParallelToolLimit},
		{"true", true, defaultParallelToolLimit},
		{"0", false, 0},
		{"off", false, 0},
		{"false", false, 0},
		{"4", true, 4},
		{"999", true, 64},                      // capped
		{"-3", true, defaultParallelToolLimit}, // invalid → default
		{"abc", true, defaultParallelToolLimit},
	}
	for _, tc := range cases {
		t.Setenv("MOONY_PARALLEL_TOOLS", tc.val)
		gotEnabled, gotLimit := parallelToolConfig()
		if gotEnabled != tc.wantEnabled || gotLimit != tc.wantLimit {
			t.Errorf("MOONY_PARALLEL_TOOLS=%q → (%v, %d), want (%v, %d)", tc.val, gotEnabled, gotLimit, tc.wantEnabled, tc.wantLimit)
		}
	}
}

func TestParallelSafe(t *testing.T) {
	reg := tools.NewRegistry(t.TempDir())
	var z int32
	reg.Register(&recordingTool{name: "reader", mutates: false, active: &z, maxSeen: &z, soloFail: &z})
	reg.Register(&recordingTool{name: "writer", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	a := newParallelTestAgent(reg)

	cases := map[string]bool{
		"reader":     true,  // read-only
		"writer":     false, // mutating
		"task":       true,  // sub-agents run in parallel by design
		"delegate":   true,  // workers run in parallel (Manager serializes same-worker)
		"todo_write": false, // non-mutating but writes shared state
	}
	for name, want := range cases {
		if got := a.parallelSafe(name); got != want {
			t.Errorf("parallelSafe(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRunToolCallsParallelOrderAndConcurrency(t *testing.T) {
	t.Setenv("MOONY_PARALLEL_TOOLS", "")
	var active, maxSeen, soloFail int32
	reg := tools.NewRegistry(t.TempDir())
	reg.Register(&recordingTool{name: "reader", delay: 20 * time.Millisecond, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})
	a := newParallelTestAgent(reg)

	calls := toolCalls("reader", 5)
	results := a.runToolCalls(context.Background(), func(Event) {}, calls)

	if len(results) != 5 {
		t.Fatalf("got %d results, want 5", len(results))
	}
	for i, r := range results {
		if r.ToolUseID != calls[i].ID {
			t.Errorf("result %d out of order: ToolUseID=%q want %q", i, r.ToolUseID, calls[i].ID)
		}
		want := "out:" + string(calls[i].Input)
		if r.Content != want {
			t.Errorf("result %d content = %q, want %q", i, r.Content, want)
		}
	}
	if maxSeen < 2 {
		t.Errorf("expected concurrent execution (maxSeen >= 2), got %d", maxSeen)
	}
}

func TestRunToolCallsSequentialWhenDisabled(t *testing.T) {
	t.Setenv("MOONY_PARALLEL_TOOLS", "0")
	var active, maxSeen, soloFail int32
	reg := tools.NewRegistry(t.TempDir())
	reg.Register(&recordingTool{name: "reader", delay: 5 * time.Millisecond, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})
	a := newParallelTestAgent(reg)

	calls := toolCalls("reader", 4)
	results := a.runToolCalls(context.Background(), func(Event) {}, calls)

	if maxSeen != 1 {
		t.Errorf("disabled parallelism should run one at a time, maxSeen=%d", maxSeen)
	}
	for i, r := range results { // order still preserved
		if r.ToolUseID != calls[i].ID {
			t.Errorf("result %d out of order", i)
		}
	}
}

func TestRunToolCallsBarrierIsolatesMutating(t *testing.T) {
	t.Setenv("MOONY_PARALLEL_TOOLS", "")
	var active, maxSeen, soloFail int32
	reg := tools.NewRegistry(t.TempDir())
	reg.Register(&recordingTool{name: "reader", delay: 15 * time.Millisecond, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})
	// The writer shares the counters and asserts it runs solo (no reader overlap).
	reg.Register(&recordingTool{name: "writer", mutates: true, delay: 15 * time.Millisecond, solo: true, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})
	a := newParallelTestAgent(reg) // auto policy → writer runs without a prompt

	// reader,reader  |  writer (barrier)  |  reader,reader
	calls := []llm.ContentBlock{
		{Type: llm.BlockToolUse, ID: "a", Name: "reader", Input: json.RawMessage(`0`)},
		{Type: llm.BlockToolUse, ID: "b", Name: "reader", Input: json.RawMessage(`1`)},
		{Type: llm.BlockToolUse, ID: "c", Name: "writer", Input: json.RawMessage(`2`)},
		{Type: llm.BlockToolUse, ID: "d", Name: "reader", Input: json.RawMessage(`3`)},
		{Type: llm.BlockToolUse, ID: "e", Name: "reader", Input: json.RawMessage(`4`)},
	}
	results := a.runToolCalls(context.Background(), func(Event) {}, calls)

	for i, r := range results {
		if r.ToolUseID != calls[i].ID {
			t.Errorf("result %d out of order: %q want %q", i, r.ToolUseID, calls[i].ID)
		}
	}
	if soloFail != 0 {
		t.Error("mutating writer overlapped with a read-only call (barrier failed)")
	}
	if maxSeen < 2 {
		t.Errorf("read groups should run concurrently, maxSeen=%d", maxSeen)
	}
}

// TestParallelTaskApproval drives the approval gate for a batch of parallel
// sub-agent (task) calls: approval happens serially up front, then approved
// calls run together.
func TestParallelTaskApproval(t *testing.T) {
	t.Setenv("MOONY_PARALLEL_TOOLS", "")
	run := func(decision Decision) []llm.ContentBlock {
		var active, maxSeen, soloFail int32
		reg := tools.NewRegistry(t.TempDir())
		reg.Register(&recordingTool{name: "task", mutates: true, delay: 10 * time.Millisecond, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})
		a := newParallelTestAgent(reg)
		a.cfg.ApprovalPolicy = config.ApprovalPrompt

		emit := func(ev Event) {
			if ar, ok := ev.(ApprovalRequest); ok {
				ar.Reply <- decision // auto-answer the prompt
			}
		}
		return a.runToolCalls(context.Background(), emit, toolCalls("task", 2))
	}

	approved := run(Approve)
	for i, r := range approved {
		if r.IsError {
			t.Errorf("approved task %d should have run, got error: %q", i, r.Content)
		}
	}

	denied := run(Deny)
	for i, r := range denied {
		if !r.IsError {
			t.Errorf("denied task %d should be an error result", i)
		}
	}
}
