package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// newTestModel builds a Model wired to real (but unused) dependencies so we can
// exercise rendering and input logic without any network calls.
func newTestModel(t *testing.T) Model {
	t.Helper()
	cfg := config.Load()
	client := llm.NewClient(cfg)
	registry := tools.Default(cfg.WorkingDir, cfg.MaxToolOutputBytes)
	ag := agent.New(cfg, client, registry, nil, nil)
	return New(cfg, ag, nil)
}

// sized drives a WindowSizeMsg through Update and returns the resized model.
func sized(t *testing.T, m Model, w, h int) Model {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

// TestNoteWarningSurfacesInTranscript verifies the startup auth warning
// shows up in the rendered view (used when the session is expired).
func TestNoteWarningSurfacesInTranscript(t *testing.T) {
	m := newTestModel(t)
	m.NoteWarning("Auth session unavailable — re-authenticate.")
	m.NoteWarning("") // empty is ignored
	m = sized(t, m, 100, 30)

	out := m.View()
	if !strings.Contains(out, "Auth session unavailable") {
		t.Fatalf("startup warning not visible in the transcript; got:\n%s", out)
	}
}

func TestViewRendersWithoutPanic(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)

	out := m.View()
	if !strings.Contains(out, "moony") {
		t.Fatalf("expected header badge in view, got:\n%s", out)
	}

	// Exercise the transcript with one of every entry kind.
	m.trans.add(entry{kind: entryUser, text: "add a --json flag"})
	m.trans.add(entry{kind: entryAssistant, text: "Sure, here's the **plan**."})
	m.trans.add(entry{kind: entryTool, toolName: "edit_file", toolInput: `{"path":"cmd/export.go"}`, toolDiff: "+ added\n- removed", toolDone: true})
	m.trans.add(entry{kind: entryError, text: "boom"})
	m.refresh()
	if got := m.View(); got == "" {
		t.Fatal("view should not be empty after adding entries")
	}
}

func TestBusyAndApprovalViewsRender(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)

	// Busy state with an active tool should render the spinner/activity line.
	m.state = stateBusy
	m.activeTool = "bash"
	if got := m.statusView(); !strings.Contains(got, "running bash") {
		t.Fatalf("expected busy status to mention the active tool, got %q", got)
	}

	// Awaiting-approval state must render the modal without panicking.
	m.state = stateAwaitingApproval
	m.pending = &agent.ApprovalRequest{
		Tool:    "edit_file",
		Summary: "edit cmd/export.go — 1 match (+6 -1)",
		Diff:    "- old\n+ new",
		Reply:   make(chan agent.Decision, 1),
	}
	out := m.View()
	if !strings.Contains(out, "approve edit_file") {
		t.Fatalf("expected approval modal in view, got:\n%s", out)
	}
}

func TestCommandHintsAndCompletion(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)

	// No hints unless the input begins with a slash.
	m.input.SetValue("hello")
	if h := m.commandHints(); h != "" {
		t.Fatalf("expected no hints for plain text, got %q", h)
	}

	// Typing a slash prefix surfaces matching commands.
	m.input.SetValue("/s")
	hints := m.commandHints()
	if !strings.Contains(hints, "/sessions") {
		t.Fatalf("expected /sessions hint, got %q", hints)
	}

	// A trailing space (entering args) hides the menu.
	m.input.SetValue("/model ")
	if h := m.commandHints(); h != "" {
		t.Fatalf("expected hints hidden once args begin, got %q", h)
	}

	// Tab completes a unique prefix to the full command plus a space.
	m.input.SetValue("/h")
	m.completeCommand()
	if got := m.input.Value(); got != "/help " {
		t.Fatalf("expected completion to /help, got %q", got)
	}

	// An ambiguous prefix completes to the longest common prefix.
	m.input.SetValue("/mo")
	m.completeCommand()
	if got := m.input.Value(); got != "/model" {
		t.Fatalf("expected ambiguous completion to /model, got %q", got)
	}
}

func TestModelCommandSwitchesModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)
	m.input.SetValue("/model anthropic.claude-test")
	next, _ := m.submit()
	nm := next.(Model)
	if nm.cfg.Model != "anthropic.claude-test" {
		t.Fatalf("expected model switch, got %q", nm.cfg.Model)
	}
	if !strings.Contains(nm.View(), "claude-test") {
		t.Fatal("expected header to reflect the new model")
	}
}

func TestModelsMenuSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	// /models kicks off an async fetch; it does not open the menu synchronously.
	m.input.SetValue("/models")
	next, cmd := m.submit()
	nm := next.(Model)
	if cmd == nil {
		t.Fatal("/models should return a fetch command")
	}
	if nm.state == stateMenu {
		t.Fatal("/models should not open the menu before the fetch completes")
	}

	// Simulate the fetch completing with a live model list.
	loaded := modelsLoadedMsg{
		models: []config.ModelInfo{
			{ID: "anthropic.model-a:0", Name: "Model A"},
			{ID: "anthropic.model-b:0", Name: "Model B"},
		},
	}
	res, _ := nm.handleModelsLoaded(loaded)
	mm := res.(Model)
	if mm.state != stateMenu || mm.menu == nil || mm.menu.kind != menuModels {
		t.Fatal("expected the model menu to open after models load")
	}
	if len(mm.menu.items) < 3 { // 2 fetched + current model prepended
		t.Fatalf("expected fetched models plus current, got %d items", len(mm.menu.items))
	}
	if !strings.Contains(mm.menuView(), "Select a model") {
		t.Fatal("menu view should show the title")
	}

	// Selecting a fetched model sets it.
	mm.menu.cursor = len(mm.menu.items) - 1
	want := mm.menu.items[mm.menu.cursor].value
	done, _ := mm.applyMenu()
	fm := done.(Model)
	if fm.cfg.Model != want {
		t.Fatalf("expected model %q after selection, got %q", want, fm.cfg.Model)
	}
	if fm.state != stateIdle || fm.menu != nil {
		t.Fatal("menu should close after applying a selection")
	}
}

func TestModelsLoadErrorFallsBackToCurated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)
	m.state = stateIdle
	res, _ := m.handleModelsLoaded(modelsLoadedMsg{err: errors.New("boom")})
	mm := res.(Model)
	if mm.menu == nil || mm.menu.kind != menuModels || len(mm.menu.items) == 0 {
		t.Fatal("on fetch error the curated model list should still open")
	}
}

func TestReasoningMenuAndCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	// Reasoning applies on the anthropic route, so switch there first.
	m.input.SetValue("/api anthropic")
	a, _ := m.submit()
	m = a.(Model)

	// Direct form sets the level immediately and the header shows it.
	m.input.SetValue("/reasoning high")
	next, _ := m.submit()
	nm := next.(Model)
	if nm.cfg.ReasoningLevel != "high" {
		t.Fatalf("expected reasoning high, got %q", nm.cfg.ReasoningLevel)
	}
	if !strings.Contains(nm.View(), "reasoning:high") {
		t.Fatal("header should show the reasoning level on the anthropic route")
	}

	// Menu form opens a picker.
	nm.input.SetValue("/reasoning")
	next2, _ := nm.submit()
	mm := next2.(Model)
	if mm.state != stateMenu || mm.menu == nil || mm.menu.kind != menuReasoning {
		t.Fatal("expected /reasoning to open the reasoning menu")
	}
}

// TestReasoningPerModelMemory verifies that /reasoning records a level under
// the active model id and that switching models with /model restores the
// remembered level for the new model (so e.g. an Opus session can stay on
// "high" while a Sonnet session keeps "medium").
func TestReasoningPerModelMemory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	// Anthropic route + an Opus id; pin Opus to "high".
	m.input.SetValue("/api anthropic")
	a, _ := m.submit()
	m = a.(Model)
	m.input.SetValue("/model anthropic.claude-opus-4-8")
	a, _ = m.submit()
	m = a.(Model)
	m.input.SetValue("/reasoning high")
	a, _ = m.submit()
	m = a.(Model)
	if m.cfg.ReasoningLevel != "high" {
		t.Fatalf("opus level = %q, want high", m.cfg.ReasoningLevel)
	}
	if got := m.cfg.ReasoningByModel["anthropic.claude-opus-4-8"]; got != "high" {
		t.Fatalf("opus map entry = %q, want high", got)
	}

	// Switch to a Sonnet id and pin it to "medium".
	m.input.SetValue("/model anthropic.claude-sonnet-4-6")
	a, _ = m.submit()
	m = a.(Model)
	// Sonnet has no remembered level yet, so the active level should fall
	// back to the legacy value (still "high" from above) — that's fine; the
	// important behavior is that Sonnet's memory is independent.
	m.input.SetValue("/reasoning medium")
	a, _ = m.submit()
	m = a.(Model)
	if m.cfg.ReasoningLevel != "medium" {
		t.Fatalf("sonnet level = %q, want medium", m.cfg.ReasoningLevel)
	}
	if got := m.cfg.ReasoningByModel["anthropic.claude-sonnet-4-6"]; got != "medium" {
		t.Fatalf("sonnet map entry = %q, want medium", got)
	}
	if got := m.cfg.ReasoningByModel["anthropic.claude-opus-4-8"]; got != "high" {
		t.Fatalf("opus entry should be unchanged, got %q", got)
	}

	// Switching back to Opus must restore "high".
	m.input.SetValue("/model anthropic.claude-opus-4-8")
	a, _ = m.submit()
	m = a.(Model)
	if m.cfg.ReasoningLevel != "high" {
		t.Fatalf("after switching back to opus, level = %q, want high", m.cfg.ReasoningLevel)
	}

	// And forward to Sonnet again — should read "medium" from memory.
	m.input.SetValue("/model anthropic.claude-sonnet-4-6")
	a, _ = m.submit()
	m = a.(Model)
	if m.cfg.ReasoningLevel != "medium" {
		t.Fatalf("after switching to sonnet, level = %q, want medium", m.cfg.ReasoningLevel)
	}
}

func TestHistoryRecall(t *testing.T) {
	m := sized(t, newTestModel(t), 100, 30)
	m.history = []string{"first", "second"}
	m.histIdx = len(m.history)

	m.historyPrev()
	if got := m.input.Value(); got != "second" {
		t.Fatalf("expected most recent history, got %q", got)
	}
	m.historyPrev()
	if got := m.input.Value(); got != "first" {
		t.Fatalf("expected oldest history, got %q", got)
	}
	m.historyNext()
	if got := m.input.Value(); got != "second" {
		t.Fatalf("expected to move forward in history, got %q", got)
	}
}

func TestToolSummary(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"read_file", `{"path":"main.go"}`, "main.go"},
		{"bash", `{"command":"go build ./..."}`, "go build ./..."},
		{"grep", `{"pattern":"foo","path":"internal"}`, `"foo" in internal`},
		{"glob", `{"pattern":"**/*.go"}`, "**/*.go"},
		{"task", `{"agent":"explore","prompt":"find the bug"}`, "explore"},
	}
	for _, c := range cases {
		if got := toolSummary(c.name, c.input); got != c.want {
			t.Errorf("toolSummary(%s, %s) = %q, want %q", c.name, c.input, got, c.want)
		}
	}
}

func TestMarkdownStyleNeverProbes(t *testing.T) {
	// "auto" probes the terminal background at runtime and leaks escape
	// sequences into the input; markdownStyle must never select it.
	t.Setenv("GLAMOUR_STYLE", "auto")
	if got := markdownStyle(); got == "auto" {
		t.Fatal("markdownStyle must not return \"auto\"")
	}
	t.Setenv("GLAMOUR_STYLE", "")
	if got := markdownStyle(); got != "dark" {
		t.Fatalf("markdownStyle default = %q, want dark", got)
	}
	t.Setenv("GLAMOUR_STYLE", "light")
	if got := markdownStyle(); got != "light" {
		t.Fatalf("markdownStyle should honor GLAMOUR_STYLE, got %q", got)
	}
}

func TestPrettyDirAbbreviatesLongPaths(t *testing.T) {
	t.Setenv("HOME", "/Users/tester")

	// Home is mapped to ~.
	if got := prettyDir("/Users/tester"); got != "~" {
		t.Errorf("prettyDir(home) = %q, want ~", got)
	}
	// Short ~-relative paths are left untouched.
	if got := prettyDir("/Users/tester/proj"); got != "~/proj" {
		t.Errorf("prettyDir short = %q, want ~/proj", got)
	}

	// Long paths abbreviate parents but keep the final folder in full.
	long := "/Users/tester/Documents/Source Code/some-very-long-project-name/moony"
	got := prettyDir(long)
	if got != "~/D/S/s/moony" {
		t.Errorf("prettyDir long = %q, want ~/D/S/s/moony", got)
	}

	// Hidden parent dirs keep their leading dot.
	if got := abbrevSegment(".config"); got != ".c" {
		t.Errorf("abbrevSegment(.config) = %q, want .c", got)
	}
}

func TestHelpers(t *testing.T) {
	if got := shortModel("anthropic.claude-sonnet-4-5-20250929-v1:0"); got != "claude-sonnet-4-5-20250929-v1" {
		t.Errorf("shortModel = %q", got)
	}
	if got := appendHistory([]string{"a"}, "a"); len(got) != 1 {
		t.Errorf("appendHistory should dedupe consecutive entries, got %v", got)
	}
	if got := commonPrefix([]slashCommand{{name: "/sessions"}, {name: "/set"}}); got != "/se" {
		t.Errorf("commonPrefix = %q, want /se", got)
	}
	if got := clampPct(1.5); got != 100 {
		t.Errorf("clampPct(1.5) = %d, want 100", got)
	}
}

func TestModelSelectionDerivesRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	// Default provider is Ollama, which speaks the openai-compatible wire.
	if m.cfg.Provider != "openai" {
		t.Fatalf("expected openai wire route for the default (ollama) provider, got %q", m.cfg.Provider)
	}

	// Switching to the Anthropic backend derives the anthropic wire route.
	m.input.SetValue("/provider anthropic")
	next, _ := m.submit()
	nm := next.(Model)
	if nm.cfg.LLMProvider != "anthropic" || nm.cfg.Provider != "anthropic" {
		t.Fatalf("anthropic backend should derive the anthropic route, got provider=%q wire=%q", nm.cfg.LLMProvider, nm.cfg.Provider)
	}
}

func TestShortModelStripsPrefixes(t *testing.T) {
	cases := map[string]string{
		"anthropic.claude-sonnet-4-5-20250929-v1:0": "claude-sonnet-4-5-20250929-v1",
		"aws:anthropic.claude-opus-4-8":             "claude-opus-4-8",
		"gcp:gemini-2.5-pro":                        "gemini-2.5-pro",
	}
	for in, want := range cases {
		if got := shortModel(in); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", in, got, want)
		}
	}
}
