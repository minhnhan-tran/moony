package agent

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// The delegate-exploration steer should appear only when the task tool (and thus
// the read-only `explore` sub-agent) is available — so it never points at a tool
// the session doesn't have.
func TestBuildSystemPromptDelegateExploreSteer(t *testing.T) {
	// Isolate the global config dir so we don't ingest the developer's real
	// global AGENTS.md.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &config.Config{WorkingDir: t.TempDir(), Model: "test-model"}

	const marker = "delegating to the read-only `explore` sub-agent"

	// Absent when there's no task tool to delegate to.
	base := tools.Default(cfg.WorkingDir, 30000)
	if strings.Contains(buildSystemPrompt(cfg, base), marker) {
		t.Error("delegate-exploration steer should be absent without the task tool")
	}

	// Present once the task tool is registered.
	withTask := tools.Default(cfg.WorkingDir, 30000)
	withTask.Register(&recordingTool{name: "task"})
	if got := buildSystemPrompt(cfg, withTask); !strings.Contains(got, marker) {
		t.Fatalf("delegate-exploration steer should appear when the task tool is registered; prompt:\n%s", got)
	}
}

// The always-on batching guideline should always be present; the stronger
// "CONTEXT BUDGET" directive only when BatchContext (MN_BATCH_CONTEXT) is set.
// The search_code steer should appear only when the search_code tool is
// available, so the prompt never advertises a tool the session lacks.
func TestBuildSystemPromptSearchCodeSteer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const marker = "prefer search_code"

	reg := tools.Default(t.TempDir(), 30000)
	cfg := &config.Config{WorkingDir: reg.Root(), Model: "test-model"}
	if got := buildSystemPrompt(cfg, reg); !strings.Contains(got, marker) {
		t.Fatalf("search_code steer should appear when the tool is registered; prompt:\n%s", got)
	}

	// A registry without search_code must omit the steer.
	bare := tools.NewRegistry(cfg.WorkingDir)
	if got := buildSystemPrompt(cfg, bare); strings.Contains(got, marker) {
		t.Error("search_code steer should be absent when the tool is not registered")
	}
}

func TestBuildSystemPromptBatchContext(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := tools.Default(t.TempDir(), 30000)

	cfg := &config.Config{WorkingDir: reg.Root(), Model: "test-model"}
	base := buildSystemPrompt(cfg, reg)
	if !strings.Contains(base, "issue independent read_file/grep/glob/find_symbol calls TOGETHER in one message") {
		t.Error("the always-on batching guideline should always be present")
	}
	if strings.Contains(base, "CONTEXT BUDGET") {
		t.Error("the CONTEXT BUDGET directive should be absent unless BatchContext is set")
	}

	cfg.BatchContext = true
	if got := buildSystemPrompt(cfg, reg); !strings.Contains(got, "CONTEXT BUDGET") {
		t.Fatalf("the CONTEXT BUDGET directive should appear when BatchContext is set; prompt:\n%s", got)
	}
}

// The verbosity-steering "OUTPUT BUDGET" directive should appear only when Terse
// (MN_TERSE) is set, so it can be turned off without code changes.
func TestBuildSystemPromptTerse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := tools.Default(t.TempDir(), 30000)

	cfg := &config.Config{WorkingDir: reg.Root(), Model: "test-model"}
	if strings.Contains(buildSystemPrompt(cfg, reg), "OUTPUT BUDGET") {
		t.Error("the OUTPUT BUDGET directive should be absent unless Terse is set")
	}

	cfg.Terse = true
	if got := buildSystemPrompt(cfg, reg); !strings.Contains(got, "OUTPUT BUDGET") {
		t.Fatalf("the OUTPUT BUDGET directive should appear when Terse is set; prompt:\n%s", got)
	}
}

// The real-time task-list cadence steer should appear only when the todo_write
// tool is available, so it never references a tool the session lacks.
func TestBuildSystemPromptTodoCadence(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &config.Config{WorkingDir: t.TempDir(), Model: "test-model"}

	const marker = "Keep the todo_write task list current in REAL TIME"

	base := tools.Default(cfg.WorkingDir, 30000)
	if strings.Contains(buildSystemPrompt(cfg, base), marker) {
		t.Error("todo cadence steer should be absent without the todo_write tool")
	}

	withTodo := tools.Default(cfg.WorkingDir, 30000)
	withTodo.Register(&recordingTool{name: "todo_write"})
	if got := buildSystemPrompt(cfg, withTodo); !strings.Contains(got, marker) {
		t.Fatalf("todo cadence steer should appear when todo_write is registered; prompt:\n%s", got)
	}
}

// The "- Model: … (served via …)" identity line must follow the selected
// provider so the model self-identifies correctly: the hosted provider by default,
// and Ollama when running locally — never claiming one while running on the other.
// Regression test for the provider mis-identification bug.
func TestBuildSystemPromptProviderIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := tools.Default(t.TempDir(), 30000)

	const ollamaText = "served via Ollama"

	cfg := &config.Config{WorkingDir: reg.Root(), Model: "llama3.1:8b"}
	got := buildSystemPrompt(cfg, reg)
	if !strings.Contains(got, ollamaText) || !strings.Contains(got, "llama3.1:8b") {
		t.Fatalf("identity line should name Ollama and the session model; prompt:\n%s", got)
	}
}
