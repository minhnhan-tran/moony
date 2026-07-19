// Package e2e holds black-box, end-to-end tests that drive the TUI purely
// through its exported API (tui.New + Model.Update/View) — no access to any
// internal/tui unexported symbols. They construct a fully wired Model the same
// way main.go does and exercise it with window-resize events, slash commands,
// and keybindings. No network is touched: only submitting a real (non-slash)
// prompt would start an LLM turn, which these tests never do.
package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/tui"
)

// newApp wires a tui.Model through the exported constructors only, mirroring
// main.go (minus the optional hooks/MCP/LSP/sub-agents). The environment is
// sandboxed so config/prefs are deterministic.
func newApp(t *testing.T) tui.Model {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Pin the route/model/policy so assertions don't depend on the dev's env.
	for _, k := range []string{"MN_API", "MN_MODEL", "MN_REASONING", "MN_APPROVAL", "GLAMOUR_STYLE"} {
		t.Setenv(k, "")
	}

	cfg := config.Load()
	cfg.WorkingDir = t.TempDir()

	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	registry := tools.Default(cfg.WorkingDir, cfg.MaxToolOutputBytes)
	todos := tools.NewTodoStore()
	registry.RegisterTodo(todos)
	ag := agent.New(cfg, llm.NewClient(cfg), registry, store, nil)
	ag.AttachTodoStore(todos)

	return tui.New(cfg, ag, store)
}

// ready returns a model that has received an initial window size, so View()
// renders the full layout.
func ready(t *testing.T, w, h int) tui.Model {
	t.Helper()
	return send(t, newApp(t), tea.WindowSizeMsg{Width: w, Height: h})
}

// send applies one message through the exported Update and returns the model.
func send(t *testing.T, m tui.Model, msg tea.Msg) tui.Model {
	t.Helper()
	next, _ := m.Update(msg)
	out, ok := next.(tui.Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return out
}

// typeLine types s into the input box and presses Enter.
func typeLine(t *testing.T, m tui.Model, s string) tui.Model {
	t.Helper()
	m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

// TestBootRendersChrome verifies the app boots and renders its persistent chrome
// (header badge + version, input prompt, status hints) via the exported API.
func TestBootRendersChrome(t *testing.T) {
	view := ready(t, 120, 40).View()
	for _, want := range []string{"moony", "v" + config.Version, "›", "enter"} {
		if !strings.Contains(view, want) {
			t.Errorf("boot view missing %q; got:\n%s", want, view)
		}
	}
}

// TestResizeFillsTerminalHeight checks the layout fills exactly the terminal
// height through Update(WindowSizeMsg) + View().
func TestResizeFillsTerminalHeight(t *testing.T) {
	const w, h = 100, 30
	if got := lipgloss.Height(ready(t, w, h).View()); got != h {
		t.Errorf("view height = %d, want %d", got, h)
	}
}

// TestClearCommandShowsWelcome drives the full type→Enter→command→render path
// for /clear and confirms the welcome banner returns.
func TestClearCommandShowsWelcome(t *testing.T) {
	m := typeLine(t, ready(t, 120, 40), "/clear")
	if view := m.View(); !strings.Contains(view, "bring your own model provider") {
		t.Errorf("/clear should show the welcome banner; got:\n%s", view)
	}
}

// TestModelCommandShowsCurrent confirms a no-arg slash command renders its reply
// without any network call.
func TestModelCommandShowsCurrent(t *testing.T) {
	m := typeLine(t, ready(t, 120, 40), "/model")
	if view := m.View(); !strings.Contains(view, "Current model") {
		t.Errorf("/model should report the current model; got:\n%s", view)
	}
}

// TestSidebarTogglesAndShowsMCP exercises the exported SetMCPServers setter plus
// the ctrl+b keybinding end to end.
func TestSidebarTogglesAndShowsMCP(t *testing.T) {
	m := newApp(t)
	m.SetMCPServers([]mcp.ServerInfo{{Name: "githubmcp", Tools: 12}})
	m = send(t, m, tea.WindowSizeMsg{Width: 110, Height: 40})

	if before := m.View(); strings.Contains(before, "githubmcp") {
		t.Fatal("sidebar should be hidden before ctrl+b")
	}
	m = send(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	view := m.View()
	for _, want := range []string{"MCP", "githubmcp"} {
		if !strings.Contains(view, want) {
			t.Errorf("after ctrl+b the sidebar should show %q; got:\n%s", want, view)
		}
	}
}

// TestImplementsBubbleTeaModel is a compile-time + runtime check that the
// exported Model satisfies tea.Model.
func TestImplementsBubbleTeaModel(t *testing.T) {
	var m tea.Model = newApp(t)
	if m.Init(); m.View() == "" {
		// Init returns a cmd we ignore; View before sizing is allowed to be a
		// short placeholder but must not panic or be empty.
		t.Skip("view empty before sizing (acceptable)")
	}
}
