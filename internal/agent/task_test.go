package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
)

func TestSubConfigModel(t *testing.T) {
	parent := &Agent{cfg: &config.Config{Model: "parent-model", ApprovalPolicy: config.ApprovalPrompt}}
	tt := newTaskTool(parent, nil)

	// A persona that pins a model overrides the parent's.
	got := tt.subConfig(subagents.Persona{Name: "deep", Model: "anthropic.claude-opus-4-8"})
	if got.Model != "anthropic.claude-opus-4-8" {
		t.Errorf("pinned persona model = %q, want anthropic.claude-opus-4-8", got.Model)
	}
	// Sub-agents always auto-approve (their prompts can't reach the UI).
	if got.ApprovalPolicy != config.ApprovalAuto {
		t.Errorf("sub-agent approval = %q, want auto", got.ApprovalPolicy)
	}
	// The parent's config is untouched.
	if parent.cfg.Model != "parent-model" || parent.cfg.ApprovalPolicy != config.ApprovalPrompt {
		t.Errorf("parent config mutated: %+v", parent.cfg)
	}

	// A persona without a model inherits the parent's.
	if inh := tt.subConfig(subagents.Persona{Name: "explore"}); inh.Model != "parent-model" {
		t.Errorf("persona without model = %q, want inherited parent-model", inh.Model)
	}

	// A read-only persona (no pinned model) runs on the cheaper fast model.
	ro := tt.subConfig(subagents.Persona{Name: "explore", Tools: []string{"read_file", "grep"}})
	if want := config.FastModel("parent-model", parent.cfg.LLMProvider); ro.Model != want {
		t.Errorf("read-only persona model = %q, want fast model %q", ro.Model, want)
	}
}

// TestSubConfigOllamaKeepsModel verifies a read-only sub-agent on the Ollama
// backend keeps the session's Ollama model instead of substituting a hosted
// fast model (which the Ollama server can't serve).
func TestSubConfigOllamaKeepsModel(t *testing.T) {
	parent := &Agent{cfg: &config.Config{Model: "gemma4:31b", LLMProvider: provider.Ollama, ApprovalPolicy: config.ApprovalPrompt}}
	tt := newTaskTool(parent, nil)
	ro := tt.subConfig(subagents.Persona{Name: "explore", Tools: []string{"read_file", "grep"}})
	if ro.Model != "gemma4:31b" {
		t.Errorf("read-only ollama sub-agent model = %q, want the session model gemma4:31b", ro.Model)
	}
}

// TestSubConfigIgnoresCrossProviderPin verifies a pinned persona model that the
// current backend can't serve is ignored, so the sub-agent inherits the
// parent's valid model instead of 404ing.
func TestSubConfigIgnoresCrossProviderPin(t *testing.T) {
	onOllama := &Agent{cfg: &config.Config{Model: "gemma4:31b", LLMProvider: provider.Ollama, ApprovalPolicy: config.ApprovalPrompt}}
	got := newTaskTool(onOllama, nil).subConfig(subagents.Persona{Name: "deep", Model: ""})
	if got.Model != "gemma4:31b" {
		t.Errorf("ollama: empty persona pin should inherit the session model, got %q want gemma4:31b", got.Model)
	}
	// A compatible pin is still honored (Ollama model on Ollama).
	got = newTaskTool(onOllama, nil).subConfig(subagents.Persona{Name: "deep", Model: "qwen3.5:122b"})
	if got.Model != "qwen3.5:122b" {
		t.Errorf("ollama: a valid Ollama pin should be honored, got %q", got.Model)
	}
}

func TestSubConfigProvider(t *testing.T) {
	parent := &Agent{cfg: &config.Config{Model: "anthropic.claude-sonnet-4-5", LLMProvider: provider.Anthropic, ApprovalPolicy: config.ApprovalPrompt}}
	tt := newTaskTool(parent, nil)

	// A persona pinning a different provider (no model) switches LLMProvider
	// and picks that provider's default model.
	got := tt.subConfig(subagents.Persona{Name: "gem", Provider: provider.Gemini})
	if got.LLMProvider != provider.Gemini {
		t.Errorf("LLMProvider = %q, want %q", got.LLMProvider, provider.Gemini)
	}
	if want := provider.DefaultModelFor(provider.Gemini); got.Model != want {
		t.Errorf("Model = %q, want provider default %q", got.Model, want)
	}

	// A pinned provider + a model valid for it are honored together.
	got = tt.subConfig(subagents.Persona{Name: "gem", Provider: provider.Gemini, Model: "gcp:gemini-2.5-pro"})
	if got.LLMProvider != provider.Gemini || got.Model != "gcp:gemini-2.5-pro" {
		t.Errorf("provider+model pin = %q/%q, want %q/%q", got.LLMProvider, got.Model, provider.Gemini, "gcp:gemini-2.5-pro")
	}

	// A pinned provider AND a model id from a different provider is honored as
	// given, same as a plain model pin: ModelServableBy can't validate a
	// cross-provider id, so the pin is trusted and a bad one just errors
	// naturally at call time instead of being second-guessed here.
	got = tt.subConfig(subagents.Persona{Name: "gem", Provider: provider.Gemini, Model: "anthropic.claude-haiku-4-5"})
	if got.LLMProvider != provider.Gemini {
		t.Errorf("LLMProvider = %q, want %q", got.LLMProvider, provider.Gemini)
	}
	if got.Model != "anthropic.claude-haiku-4-5" {
		t.Errorf("Model = %q, want the pin honored as given %q", got.Model, "anthropic.claude-haiku-4-5")
	}

	// A provider equal to the parent's is a no-op for routing — falls through
	// to inherit the parent's model since no model is separately pinned.
	got = tt.subConfig(subagents.Persona{Name: "same", Provider: provider.Anthropic})
	if got.LLMProvider != provider.Anthropic || got.Model != parent.cfg.Model {
		t.Errorf("same-provider pin should inherit the parent's model, got %q/%q", got.LLMProvider, got.Model)
	}

	// A garbled provider (e.g. a hand-edited typo in the persona's .md) is
	// ignored rather than silently normalized to some default provider.
	got = tt.subConfig(subagents.Persona{Name: "typo", Provider: "not-a-real-provider"})
	if got.LLMProvider != provider.Anthropic || got.Model != parent.cfg.Model {
		t.Errorf("invalid provider pin should be ignored, got %q/%q", got.LLMProvider, got.Model)
	}
}

func TestSubConfigReasoning(t *testing.T) {
	parent := &Agent{cfg: &config.Config{Model: "anthropic.claude-sonnet-4-5", LLMProvider: provider.Anthropic, ApprovalPolicy: config.ApprovalPrompt}}
	tt := newTaskTool(parent, nil)

	got := tt.subConfig(subagents.Persona{Name: "deep", Reasoning: "high"})
	if r := got.GetReasoningFor(got.Model); r != "high" {
		t.Errorf("reasoning = %q, want %q", r, "high")
	}

	// Combines with a model pin: reasoning is applied to the resolved (pinned) model.
	got = tt.subConfig(subagents.Persona{Name: "deep", Model: "anthropic.claude-opus-4-8", Reasoning: "medium"})
	if got.Model != "anthropic.claude-opus-4-8" {
		t.Errorf("Model = %q, want the pin", got.Model)
	}
	if r := got.GetReasoningFor(got.Model); r != "medium" {
		t.Errorf("reasoning = %q, want %q", r, "medium")
	}

	// No reasoning pin: inherits the parent's (unset/default).
	inh := tt.subConfig(subagents.Persona{Name: "plain"})
	wantInh := parent.cfg.GetReasoningFor(parent.cfg.Model)
	if r := inh.GetReasoningFor(inh.Model); r != wantInh {
		t.Errorf("reasoning without a pin = %q, want inherited %q", r, wantInh)
	}
}

// A delegated read-only surveyor must not inherit an aggressively-lowered
// main-loop step cap — that starved explore into bailing after 60 steps. Its
// budget is floored to subAgentMinSteps, while a non-read-only persona and a
// parent that's already higher are left as-is.
func TestSubConfigStepFloor(t *testing.T) {
	roTools := []string{"read_file", "grep"}

	// Parent cap lowered for cost (e.g. MN_MAX_STEPS=30): the read-only surveyor
	// is floored so it can actually finish a survey.
	tight := newTaskTool(&Agent{cfg: &config.Config{Model: "m", MaxAgentSteps: 30, AutoContinue: 1}}, nil)
	if got := tight.subConfig(subagents.Persona{Name: "explore", Tools: roTools}); got.MaxAgentSteps != subAgentMinSteps {
		t.Errorf("read-only sub-agent steps = %d, want floor %d", got.MaxAgentSteps, subAgentMinSteps)
	}

	// A parent already above the floor keeps its (higher) budget.
	wide := newTaskTool(&Agent{cfg: &config.Config{Model: "m", MaxAgentSteps: 300}}, nil)
	if got := wide.subConfig(subagents.Persona{Name: "explore", Tools: roTools}); got.MaxAgentSteps != 300 {
		t.Errorf("read-only sub-agent steps = %d, want parent's 300 (no lowering)", got.MaxAgentSteps)
	}

	// A non-read-only persona is not floored — it honors the parent's cap.
	build := newTaskTool(&Agent{cfg: &config.Config{Model: "m", MaxAgentSteps: 30}}, nil)
	if got := build.subConfig(subagents.Persona{Name: "builder", Tools: []string{"read_file", "edit"}}); got.MaxAgentSteps != 30 {
		t.Errorf("non-read-only sub-agent steps = %d, want parent's 30 (unfloored)", got.MaxAgentSteps)
	}
}

// A read-only surveyor sub-agent gets extra auto-continue headroom (it can't be
// resumed with "continue" like an interactive turn), while non-read-only personas
// and parents already higher are left as-is.
func TestSubConfigAutoContinueFloor(t *testing.T) {
	roTools := []string{"read_file", "grep"}

	// Read-only surveyor with the default parent AutoContinue is floored up.
	tt := newTaskTool(&Agent{cfg: &config.Config{Model: "m", MaxAgentSteps: 80, AutoContinue: 1}}, nil)
	if got := tt.subConfig(subagents.Persona{Name: "explore", Tools: roTools}); got.AutoContinue != subAgentMinAutoContinue {
		t.Errorf("read-only sub-agent AutoContinue = %d, want floor %d", got.AutoContinue, subAgentMinAutoContinue)
	}

	// A parent already above the floor keeps its (higher) headroom.
	wide := newTaskTool(&Agent{cfg: &config.Config{Model: "m", MaxAgentSteps: 80, AutoContinue: 5}}, nil)
	if got := wide.subConfig(subagents.Persona{Name: "explore", Tools: roTools}); got.AutoContinue != 5 {
		t.Errorf("read-only sub-agent AutoContinue = %d, want parent's 5 (no lowering)", got.AutoContinue)
	}

	// A non-read-only persona is not floored.
	build := newTaskTool(&Agent{cfg: &config.Config{Model: "m", MaxAgentSteps: 80, AutoContinue: 0}}, nil)
	if got := build.subConfig(subagents.Persona{Name: "builder", Tools: []string{"read_file", "edit"}}); got.AutoContinue != 0 {
		t.Errorf("non-read-only sub-agent AutoContinue = %d, want parent's 0 (unfloored)", got.AutoContinue)
	}
}

// errStepCeiling must be detectable via errors.Is on the returned stop error, so
// the task tool can recover a sub-agent's partial work instead of hard-failing.
func TestStepCeilingErrorIs(t *testing.T) {
	err := stepCeilingError{ceiling: 160}
	if !errors.Is(err, errStepCeiling) {
		t.Error("stepCeilingError should match errStepCeiling via errors.Is")
	}
	if !strings.Contains(err.Error(), "160 tool steps") {
		t.Errorf("message should mention the ceiling, got %q", err.Error())
	}
	if errors.Is(errors.New("other"), errStepCeiling) {
		t.Error("an unrelated error must not match errStepCeiling")
	}
}

// A delegated sub-agent that runs out of tool steps can't be resumed with
// "continue", so the task tool must return its partial work (with a note) rather
// than discarding everything and failing the whole delegation.
func TestTaskReturnsPartialOnStepCeiling(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		// parent delegates (step budget kept tiny so the sub-agent hits the cap fast)
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("p1", "task", `{"agent":"auditor","prompt":"audit it"}`)}},
		// sub-agent keeps calling a tool and never finishes (ceiling = 2)
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("s1", "glob", `{"pattern":"*.go"}`)}},
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{llm.TextBlock("partial findings so far"), toolUse("s2", "glob", `{"pattern":"*.md"}`)}},
		// parent wraps up after the task returns its partial result
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	// ceiling = MaxAgentSteps*(1+AutoContinue) = 2 for both parent and the
	// (non-read-only, so unfloored) sub-agent.
	ag.cfg.MaxAgentSteps = 2
	ag.cfg.AutoContinue = 0
	ag.AttachSubAgents([]subagents.Persona{{Name: "auditor", SystemPrompt: "you audit", Source: "global"}})

	var taskOutput string
	if err := ag.Run(context.Background(), "delegate it", func(ev Event) {
		if e, ok := ev.(ToolEnd); ok && e.Name == "task" && e.Agent == "" {
			taskOutput = e.Output
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if taskOutput == "" {
		t.Fatal("expected a task tool output even though the sub-agent hit the step cap")
	}
	if strings.HasPrefix(taskOutput, "Error:") {
		t.Errorf("hitting the step cap should not fail the delegation, got error output:\n%s", taskOutput)
	}
	if !strings.Contains(taskOutput, "tool-step limit") {
		t.Errorf("output should note the step-limit truncation, got:\n%s", taskOutput)
	}
	if !strings.Contains(taskOutput, "partial findings so far") {
		t.Errorf("output should carry the sub-agent's partial work, got:\n%s", taskOutput)
	}
}

func TestTaskStreamEnabled(t *testing.T) {
	t.Setenv("MN_TASK_STREAM", "")
	if !taskStreamEnabled() {
		t.Error("streaming should be on by default")
	}
	for _, off := range []string{"0", "off", "false", "no", "OFF"} {
		t.Setenv("MN_TASK_STREAM", off)
		if taskStreamEnabled() {
			t.Errorf("MN_TASK_STREAM=%q should disable streaming", off)
		}
	}
}

// A delegated sub-agent's tool calls are forwarded to the parent's UI as real
// ToolStart/ToolEnd events stamped with the parent task call's ID (so the UI can
// nest them) and the persona (Agent), while still running in the sub-agent's
// own isolated context.
func TestTaskForwardsSubAgentProgress(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		// parent delegates to the explore persona
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("p1", "task", `{"agent":"explore","prompt":"find go files"}`)}},
		// sub-agent runs a read tool, then finishes
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("s1", "glob", `{"pattern":"*.go"}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("found them")}},
		// parent wraps up
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.AttachSubAgents([]subagents.Persona{{Name: "explore", SystemPrompt: "you explore", Source: "global"}})

	var nestedStart *ToolStart
	var taskOutput string
	if err := ag.Run(context.Background(), "delegate it", func(ev Event) {
		switch e := ev.(type) {
		case ToolStart:
			if e.Agent != "" && e.Name == "glob" {
				ev := e
				nestedStart = &ev
			}
		case ToolEnd:
			if e.Name == "task" && e.Agent == "" {
				taskOutput = e.Output
			}
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if nestedStart == nil {
		t.Fatal("expected a forwarded sub-agent ToolStart for the glob call")
	}
	// It must be attributed to the persona and parented to the task call so the
	// UI can nest it, with its ID namespaced under the parent (no collision).
	if nestedStart.Agent != "explore" {
		t.Errorf("forwarded ToolStart Agent = %q, want explore", nestedStart.Agent)
	}
	if nestedStart.ParentID != "p1" {
		t.Errorf("forwarded ToolStart ParentID = %q, want the task call id p1", nestedStart.ParentID)
	}
	if nestedStart.ID != "p1/s1" {
		t.Errorf("forwarded ToolStart ID = %q, want the namespaced p1/s1", nestedStart.ID)
	}
	// The final answer is still returned as the (top-level) task tool's output.
	if !strings.Contains(taskOutput, "found them") {
		t.Errorf("task output should carry the sub-agent's final text, got %q", taskOutput)
	}
}

// With MN_TASK_STREAM=0 the sub-agent's steps are silenced; only the final
// answer comes back as the task output.
func TestTaskStreamDisabledSilencesProgress(t *testing.T) {
	t.Setenv("MN_TASK_STREAM", "0")
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("p1", "task", `{"agent":"explore","prompt":"find go files"}`)}},
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("s1", "glob", `{"pattern":"*.go"}`)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("found them")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.AttachSubAgents([]subagents.Persona{{Name: "explore", SystemPrompt: "you explore", Source: "global"}})

	var sawSubAgentTool bool
	if err := ag.Run(context.Background(), "delegate it", func(ev Event) {
		if ts, ok := ev.(ToolStart); ok && ts.Agent != "" {
			sawSubAgentTool = true
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sawSubAgentTool {
		t.Error("MN_TASK_STREAM=0 should suppress forwarded sub-agent tool events")
	}
}

// TestTaskEmitsTaskMeta confirms a finished delegation emits a TaskMeta footer
// stamped with the parent task call id + persona, counting the sub-agent's tool
// calls — and that MN_TASK_STREAM=0 suppresses it like the tool events.
func TestTaskEmitsTaskMeta(t *testing.T) {
	newAgent := func(t *testing.T) *Agent {
		dir := t.TempDir()
		reg := tools.NewRegistry(dir)
		fake := &scriptedStreamer{turns: []llm.Response{
			{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("p1", "task", `{"agent":"explore","prompt":"find go files"}`)}},
			{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("s1", "glob", `{"pattern":"*.go"}`)}},
			{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("found them")}},
			{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
		}}
		ag := runTestAgent(t, reg, dir, fake)
		ag.AttachSubAgents([]subagents.Persona{{Name: "explore", SystemPrompt: "you explore", Source: "global"}})
		return ag
	}

	var meta *TaskMeta
	if err := newAgent(t).Run(context.Background(), "delegate it", func(ev Event) {
		if tm, ok := ev.(TaskMeta); ok {
			m := tm
			meta = &m
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if meta == nil {
		t.Fatal("expected a TaskMeta footer for the finished delegation")
	}
	if meta.ParentID != "p1" {
		t.Errorf("TaskMeta ParentID = %q, want the task call id p1", meta.ParentID)
	}
	if meta.Agent != "explore" {
		t.Errorf("TaskMeta Agent = %q, want explore", meta.Agent)
	}
	if meta.Calls != 1 {
		t.Errorf("TaskMeta Calls = %d, want 1 (the sub-agent's single glob call)", meta.Calls)
	}

	// With streaming off, no TaskMeta should be emitted.
	t.Setenv("MN_TASK_STREAM", "0")
	var sawMeta bool
	if err := newAgent(t).Run(context.Background(), "delegate it", func(ev Event) {
		if _, ok := ev.(TaskMeta); ok {
			sawMeta = true
		}
	}); err != nil {
		t.Fatalf("Run (stream off): %v", err)
	}
	if sawMeta {
		t.Error("MN_TASK_STREAM=0 should suppress the TaskMeta footer too")
	}
}

// TestTaskFoldsSubAgentUsage confirms a delegation's token usage is folded into
// the parent turn's TurnDone, so sub-agent tokens reach the session totals and
// the per-turn footer instead of vanishing into the sub-agent's isolated loop.
func TestTaskFoldsSubAgentUsage(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	// Shared scripted client: turns are consumed in order across the parent and
	// the sub-agent. Parent calls = turns 0 (delegate) and 3 (wrap up); the
	// sub-agent consumes turns 1 (glob) and 2 (finish).
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("p1", "task", `{"agent":"explore","prompt":"find go files"}`)}, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}},
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{toolUse("s1", "glob", `{"pattern":"*.go"}`)}, Usage: llm.Usage{InputTokens: 50, OutputTokens: 5}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("found them")}, Usage: llm.Usage{InputTokens: 40, OutputTokens: 3}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}, Usage: llm.Usage{InputTokens: 20, OutputTokens: 2}},
	}}
	ag := runTestAgent(t, reg, dir, fake)
	ag.AttachSubAgents([]subagents.Persona{{Name: "explore", SystemPrompt: "you explore", Source: "global"}})

	var last *TurnDone
	if err := ag.Run(context.Background(), "delegate it", func(ev Event) {
		if td, ok := ev.(TurnDone); ok {
			d := td
			last = &d
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if last == nil {
		t.Fatal("expected a parent TurnDone")
	}
	// Parent's own calls = 100+20 in / 10+2 out; sub-agent = 50+40 in / 5+3 out.
	// The folded total must include both.
	if got := last.TurnUsage.InputTokens; got != 210 {
		t.Errorf("TurnDone.TurnUsage.InputTokens = %d, want 210 (120 parent + 90 sub)", got)
	}
	if got := last.TurnUsage.OutputTokens; got != 20 {
		t.Errorf("TurnDone.TurnUsage.OutputTokens = %d, want 20 (12 parent + 8 sub)", got)
	}
}
