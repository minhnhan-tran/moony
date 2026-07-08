package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// scriptedStreamer is a fake llm.Streamer that replays a fixed list of model
// turns, letting the agent's Run loop be tested deterministically with no
// network. It records the messages it was sent on each call for assertions.
type scriptedStreamer struct {
	turns        []llm.Response
	calls        int
	noThinkCalls int
	sent         [][]llm.Message
	sentSystem   []string
	sentEfforts  []string
	sentModels   []string
}

func (s *scriptedStreamer) StreamMessages(ctx context.Context, system string, messages []llm.Message, _ []llm.Tool, h llm.Handler) (*llm.Response, error) {
	snap := make([]llm.Message, len(messages))
	copy(snap, messages)
	s.sent = append(s.sent, snap)
	s.sentSystem = append(s.sentSystem, system)
	s.sentEfforts = append(s.sentEfforts, llm.EffortFrom(ctx))
	s.sentModels = append(s.sentModels, llm.ModelFrom(ctx))

	if s.calls >= len(s.turns) {
		// Should never happen in a well-formed test; surface it loudly.
		return &llm.Response{StopReason: "end_turn"}, nil
	}
	r := s.turns[s.calls]
	s.calls++
	// Replay text through the handler the way the real stream would.
	for _, b := range r.Blocks {
		if b.Type == llm.BlockText && b.Text != "" && h.OnTextDelta != nil {
			h.OnTextDelta(b.Text)
		}
	}
	return &r, nil
}

func (s *scriptedStreamer) ListModels(context.Context) ([]config.ModelInfo, error) { return nil, nil }
func (s *scriptedStreamer) Usage(context.Context) (*llm.UsageResponse, error)      { return nil, nil }
func (s *scriptedStreamer) CheckAuth(context.Context) error                        { return nil }

// StreamMessagesNoThink lets the fake satisfy the agent's no-think auxiliary
// path; it records the call and replays the same scripted turns so ordering is
// unchanged, while letting tests assert that judge/summarize/title used it.
func (s *scriptedStreamer) StreamMessagesNoThink(ctx context.Context, system string, messages []llm.Message, tools []llm.Tool, h llm.Handler) (*llm.Response, error) {
	s.noThinkCalls++
	return s.StreamMessages(ctx, system, messages, tools, h)
}

func toolUse(id, name, input string) llm.ContentBlock {
	return llm.ContentBlock{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func toolResults(msgs []llm.Message) []llm.ContentBlock {
	var out []llm.ContentBlock
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				out = append(out, b)
			}
		}
	}
	return out
}

// messagesText concatenates the text blocks across a message slice, for
// asserting on injected prefaces (checkpoint / plan-mode steering / plan).
func messagesText(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == llm.BlockText {
				b.WriteString(c.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// runTestAgent builds an agent wired to a scripted streamer, sandboxed config,
// and a registry the caller pre-populated.
func runTestAgent(t *testing.T, reg *tools.Registry, dir string, fake *scriptedStreamer) *Agent {
	t.Helper()
	isolateConfig(t)
	cfg := &config.Config{
		Model:              "test-model",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	return New(cfg, fake, reg, nil, nil)
}

// noticeStreamer is a scriptedStreamer that also implements preTurnNoticer, so a
// turn can exercise the "model is loading" pre-turn notice path.
type noticeStreamer struct {
	*scriptedStreamer
	notice string
}

func (n *noticeStreamer) PreTurnNotice(context.Context) string { return n.notice }

// TestPreTurnNoticeEmittedTopLevelOnly verifies the pre-turn notice is emitted as
// an Info on a top-level user turn, but NOT on a sub-agent turn (whose ctx carries
// a stamped persona identity).
func TestPreTurnNoticeEmittedTopLevelOnly(t *testing.T) {
	const notice = "Loading the Ollama model \"gemma4:31b\" on the server — first response may be slow."
	dir := t.TempDir()
	isolateConfig(t)
	cfg := &config.Config{Model: "gemma4:31b", MaxTokens: 256, MaxToolOutputBytes: 1 << 20, MaxAgentSteps: 10, ApprovalPolicy: config.ApprovalAuto, WorkingDir: dir}

	newAg := func() *Agent {
		fake := &scriptedStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("ok")}}}}
		c := *cfg
		return New(&c, &noticeStreamer{scriptedStreamer: fake, notice: notice}, tools.NewRegistry(dir), nil, nil)
	}
	infos := func(ag *Agent, ctx context.Context) []string {
		var got []string
		_ = ag.RunWith(ctx, "hi", nil, func(ev Event) {
			if e, ok := ev.(Info); ok {
				got = append(got, e.Text)
			}
		})
		return got
	}

	// Top-level turn (plain ctx) → notice present.
	if !containsStr(infos(newAg(), context.Background()), notice) {
		t.Error("top-level turn should emit the pre-turn loading notice")
	}
	// Sub-agent turn (identity stamped on ctx, as the task tool does) → no notice.
	subCtx := withAgentIdentity(context.Background(), "explore")
	if containsStr(infos(newAg(), subCtx), notice) {
		t.Error("a sub-agent turn must NOT emit the pre-turn notice")
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestRunDispatchesToolThenCompletes(t *testing.T) {
	var active, maxSeen, soloFail int32
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(&recordingTool{name: "echo", active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t1", "echo", `7`)}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("all done")}, Usage: llm.Usage{InputTokens: 120, OutputTokens: 5}},
	}}
	ag := runTestAgent(t, reg, dir, fake)

	var toolStarts, toolEnds, turnDones int
	var text strings.Builder
	err := ag.Run(context.Background(), "do the thing", func(ev Event) {
		switch e := ev.(type) {
		case ToolStart:
			toolStarts++
		case ToolEnd:
			toolEnds++
		case TextDelta:
			text.WriteString(e.Text)
		case TurnDone:
			turnDones++
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 2 {
		t.Errorf("model turns = %d, want 2", fake.calls)
	}
	if toolStarts != 1 || toolEnds != 1 {
		t.Errorf("tool start/end = %d/%d, want 1/1", toolStarts, toolEnds)
	}
	if turnDones != 1 {
		t.Errorf("TurnDone events = %d, want 1", turnDones)
	}
	if !strings.Contains(text.String(), "all done") {
		t.Errorf("final assistant text = %q, want it to contain 'all done'", text.String())
	}
	if maxSeen < 1 {
		t.Error("the echo tool never executed")
	}

	// History should be: user, assistant(tool_use), user(tool_result), assistant(text).
	if got := len(ag.Messages()); got != 4 {
		t.Fatalf("history length = %d, want 4", got)
	}
	// The 2nd model call must have been fed the tool_result.
	if len(fake.sent) != 2 || len(toolResults(fake.sent[1])) != 1 {
		t.Errorf("the second model call should include the tool_result; sends=%d", len(fake.sent))
	}
}

func TestRunCompletesWithoutTools(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("hello")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)

	var toolStarts, turnDones int
	err := ag.Run(context.Background(), "hi", func(ev Event) {
		switch ev.(type) {
		case ToolStart:
			toolStarts++
		case TurnDone:
			turnDones++
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("model turns = %d, want 1", fake.calls)
	}
	if toolStarts != 0 {
		t.Errorf("tool starts = %d, want 0", toolStarts)
	}
	if turnDones != 1 {
		t.Errorf("TurnDone events = %d, want 1", turnDones)
	}
}

func TestRunStopsAtStepLimit(t *testing.T) {
	var active, maxSeen, soloFail int32
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(&recordingTool{name: "echo", active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	// Every turn asks for another tool, so the loop only ends at the step cap.
	loop := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "echo", `1`)}}
	fake := &scriptedStreamer{turns: []llm.Response{loop, loop, loop, loop, loop}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.MaxAgentSteps = 3

	err := ag.Run(context.Background(), "loop forever", func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "stopped after 3 tool steps") {
		t.Fatalf("expected a step-limit error, got %v", err)
	}
	if fake.calls != 3 {
		t.Errorf("model turns = %d, want 3 (MaxAgentSteps)", fake.calls)
	}
}

func TestRunAutoContinue(t *testing.T) {
	var active, maxSeen, soloFail int32
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(&recordingTool{name: "echo", active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	loop := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "echo", `1`)}}
	turns := make([]llm.Response, 10)
	for i := range turns {
		turns[i] = loop
	}
	fake := &scriptedStreamer{turns: turns}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.MaxAgentSteps = 2
	ag.cfg.AutoContinue = 2 // hard ceiling = 2 * (1+2) = 6

	var steps []StepUpdate
	autoNotices := 0
	err := ag.Run(context.Background(), "loop", func(ev Event) {
		switch e := ev.(type) {
		case StepUpdate:
			steps = append(steps, e)
		case Info:
			if strings.Contains(e.Text, "Auto-continuing") {
				autoNotices++
			}
		}
	})

	if err == nil || !strings.Contains(err.Error(), "stopped after 6 tool steps") {
		t.Fatalf("expected the ceiling error, got %v", err)
	}
	if fake.calls != 6 {
		t.Errorf("model turns = %d, want 6 (ceiling)", fake.calls)
	}
	if len(steps) != 6 || steps[0].Step != 1 || steps[5].Step != 6 || steps[5].Max != 6 {
		t.Errorf("step updates = %+v, want 6 with Max=6", steps)
	}
	if autoNotices != 2 { // soft boundaries at steps 2 and 4
		t.Errorf("auto-continue notices = %d, want 2", autoNotices)
	}
}

// TestRunTurnGuardWarnsOnCalls checks the runaway-cost guard's call-count
// warning: once a single turn crosses SQE_TURN_WARN_CALLS model calls it emits a
// one-time advisory Info (and never stops the turn — the ceiling does that).
func TestRunTurnGuardWarnsOnCalls(t *testing.T) {
	t.Setenv("SQE_TURN_WARN_CALLS", "2")
	t.Setenv("SQE_TURN_WARN_COST", "0") // isolate the call guard

	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	reg.Register(&recordingTool{name: "echo", active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	loop := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "echo", `1`)}}
	turns := []llm.Response{loop, loop, loop,
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}}}
	fake := &scriptedStreamer{turns: turns}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.MaxAgentSteps = 10 // ceiling well above the 4 scripted calls

	warnings := 0
	err := ag.Run(context.Background(), "loop a few times", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "model calls") {
			warnings++
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if warnings != 1 {
		t.Errorf("call-count warnings = %d, want exactly 1 (fired once at the threshold)", warnings)
	}
}

// TestRunTurnGuardWarnsOnCost checks the cost warning: once a turn's accumulated
// cost crosses SQE_TURN_WARN_COST it emits a one-time advisory Info.
func TestRunTurnGuardWarnsOnCost(t *testing.T) {
	t.Setenv("SQE_TURN_WARN_CALLS", "0") // isolate the cost guard
	t.Setenv("SQE_TURN_WARN_COST", "0.50")

	// Give the test model a known price so accumulated cost is deterministic.
	const model = "guard-cost-test-model"
	config.OverlayRuntimePricing(model, 0.00001, 0.00003, 0, 0) // $/token
	t.Cleanup(config.ClearRuntimePricing)

	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	reg.Register(&recordingTool{name: "echo", active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	// Each tool round bills 20k in + 5k out = $0.20 + $0.15 = $0.35; two rounds
	// cross the $0.50 threshold.
	loop := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "echo", `1`)}, Usage: llm.Usage{InputTokens: 20000, OutputTokens: 5000}}
	turns := []llm.Response{loop, loop,
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}, Usage: llm.Usage{InputTokens: 20000, OutputTokens: 5000}}}
	fake := &scriptedStreamer{turns: turns}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.Model = model
	ag.cfg.MaxAgentSteps = 10

	warnings := 0
	err := ag.Run(context.Background(), "expensive", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "cost ~$") {
			warnings++
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if warnings != 1 {
		t.Errorf("cost warnings = %d, want exactly 1", warnings)
	}
}

// TestRunTurnHardStopOnCalls checks the opt-in hard per-turn stop: once a turn
// reaches SQE_TURN_MAX_CALLS model calls the agent ends the turn cleanly
// (emitting a stop Info and returning nil) instead of running on to the ceiling.
// The check runs after the model call but before that step's tools execute, so
// with a limit of 2 the model is called exactly twice and the second step's
// tools never run.
func TestRunTurnHardStopOnCalls(t *testing.T) {
	t.Setenv("SQE_TURN_MAX_CALLS", "2")
	t.Setenv("SQE_TURN_MAX_COST", "0") // isolate the call limit

	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	reg.Register(&recordingTool{name: "echo", active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	// Every turn keeps asking for another tool; only the hard stop ends it.
	loop := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "echo", `1`)}}
	turns := []llm.Response{loop, loop, loop, loop, loop}
	fake := &scriptedStreamer{turns: turns}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.MaxAgentSteps = 10 // ceiling well above the hard limit

	stops, turnDones := 0, 0
	err := ag.Run(context.Background(), "loop forever", func(ev Event) {
		switch e := ev.(type) {
		case Info:
			if strings.Contains(e.Text, "hard limit of") {
				stops++
			}
		case TurnDone:
			turnDones++
		}
	})
	if err != nil {
		t.Fatalf("hard stop should end the turn cleanly, got err %v", err)
	}
	if fake.calls != 2 {
		t.Errorf("model turns = %d, want 2 (SQE_TURN_MAX_CALLS)", fake.calls)
	}
	if stops != 1 {
		t.Errorf("hard-stop notices = %d, want exactly 1", stops)
	}
	if turnDones != 1 {
		t.Errorf("TurnDone events = %d, want 1 (the turn ends cleanly)", turnDones)
	}
	// The stop fires before the 2nd step's tools run, so the tool executed once.
	if maxSeen != 1 {
		t.Errorf("tool executions (maxSeen) = %d, want 1 (step 2 stopped before its tools)", maxSeen)
	}
}

// TestRunTurnHardStopOnCost checks the cost-based hard stop: once a turn's
// accumulated cost crosses SQE_TURN_MAX_COST the agent ends the turn cleanly.
func TestRunTurnHardStopOnCost(t *testing.T) {
	t.Setenv("SQE_TURN_MAX_CALLS", "0") // isolate the cost limit
	t.Setenv("SQE_TURN_MAX_COST", "0.50")

	const model = "hardstop-cost-test-model"
	config.OverlayRuntimePricing(model, 0.00001, 0.00003, 0, 0) // $/token
	t.Cleanup(config.ClearRuntimePricing)

	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	var active, maxSeen, soloFail int32
	reg.Register(&recordingTool{name: "echo", active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	// Each round bills 20k in + 5k out = $0.20 + $0.15 = $0.35; the second round
	// crosses the $0.50 hard limit, ending the turn after 2 calls.
	loop := llm.Response{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("t", "echo", `1`)}, Usage: llm.Usage{InputTokens: 20000, OutputTokens: 5000}}
	turns := []llm.Response{loop, loop, loop, loop}
	fake := &scriptedStreamer{turns: turns}
	ag := runTestAgent(t, reg, dir, fake)
	ag.cfg.Model = model
	ag.cfg.MaxAgentSteps = 10

	stops := 0
	err := ag.Run(context.Background(), "expensive loop", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "hard cost limit of") {
			stops++
		}
	})
	if err != nil {
		t.Fatalf("hard cost stop should end the turn cleanly, got err %v", err)
	}
	if fake.calls != 2 {
		t.Errorf("model turns = %d, want 2 (stopped when cost crossed the limit)", fake.calls)
	}
	if stops != 1 {
		t.Errorf("hard cost-stop notices = %d, want exactly 1", stops)
	}
}

func TestRunDispatchesToolsInParallel(t *testing.T) {
	t.Setenv("SQE_PARALLEL_TOOLS", "") // enabled
	var active, maxSeen, soloFail int32
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(&recordingTool{name: "echo", delay: 15 * time.Millisecond, active: &active, maxSeen: &maxSeen, soloFail: &soloFail})

	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("a", "echo", `1`), toolUse("b", "echo", `2`), toolUse("c", "echo", `3`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)

	// emit is called concurrently from the parallel tool goroutines, so the
	// counter must be synchronized (the real TUI emit is a channel send).
	var toolEnds int32
	if err := ag.Run(context.Background(), "go", func(ev Event) {
		if _, ok := ev.(ToolEnd); ok {
			atomic.AddInt32(&toolEnds, 1)
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if toolEnds != 3 {
		t.Errorf("tool ends = %d, want 3", toolEnds)
	}
	if maxSeen < 2 {
		t.Errorf("expected concurrent execution (maxSeen >= 2), got %d", maxSeen)
	}
	// tool_result order must match the request order (a, b, c).
	results := toolResults(fake.sent[1])
	if len(results) != 3 || results[0].ToolUseID != "a" || results[1].ToolUseID != "b" || results[2].ToolUseID != "c" {
		t.Errorf("tool_result order not preserved: %+v", results)
	}
}
