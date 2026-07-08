package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/tools"
)

// TestSidebarHiddenByDefault verifies the panel doesn't draw until toggled and
// that the transcript keeps the full width.
func TestSidebarHiddenByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)

	if m.sidebarVisible() {
		t.Error("sidebar should be hidden by default")
	}
	if got := m.sidebarView(); got != "" {
		t.Errorf("sidebarView should be empty when hidden, got:\n%s", got)
	}
	if m.mainWidth() != 120 {
		t.Errorf("transcript should keep full width when hidden, got %d", m.mainWidth())
	}
}

// TestSidebarToggleReducesMainWidth checks ctrl+b shows the panel and reflows
// the transcript to the narrower remaining width.
func TestSidebarToggleReducesMainWidth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(Model)

	if !m.showSidebar {
		t.Fatal("ctrl+b should set showSidebar")
	}
	if !m.sidebarVisible() {
		t.Fatal("sidebar should be visible after toggle on a wide terminal")
	}
	if want := 120 - sidebarWidth; m.mainWidth() != want {
		t.Errorf("transcript width = %d, want %d", m.mainWidth(), want)
	}

	// Toggling again hides it and restores full width.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(Model)
	if m.sidebarVisible() {
		t.Error("second ctrl+b should hide the sidebar")
	}
	if m.mainWidth() != 120 {
		t.Errorf("transcript should return to full width, got %d", m.mainWidth())
	}
}

// TestSidebarHiddenOnNarrowTerminal verifies the panel suppresses itself when
// the terminal is too narrow even if toggled on.
func TestSidebarHiddenOnNarrowTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 50, 30) // < sidebarWidth+40
	m.showSidebar = true
	if m.sidebarVisible() {
		t.Error("sidebar should stay hidden on a narrow terminal")
	}
	if m.mainWidth() != 50 {
		t.Errorf("narrow terminal should keep full width, got %d", m.mainWidth())
	}
}

// TestSidebarShowsTodosAndMCP checks both sections render their content.
func TestSidebarShowsTodosAndMCP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)
	m.showSidebar = true
	m.todos = []tools.Todo{
		{Content: "wire up the sidebar", Status: tools.TodoInProgress},
		{Content: "write tests", Status: tools.TodoPending},
		{Content: "done thing", Status: tools.TodoCompleted},
	}
	m.mcpServers = []mcp.ServerInfo{
		{Name: "github", Tools: 12},
		{Name: "filesystem", Tools: 4},
	}

	out := m.sidebarView()
	for _, want := range []string{"Todos (3)", "wire up the sidebar", "✓ 1 done", "MCP (2)", "github", "(12)", "filesystem"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q; got:\n%s", want, out)
		}
	}
}

// TestSidebarEmptyState shows a placeholder when there's nothing to display.
func TestSidebarEmptyState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)
	m.showSidebar = true
	out := m.sidebarView()
	if !strings.Contains(out, "No plan, todos, files,") {
		t.Errorf("expected empty-state placeholder, got:\n%s", out)
	}
}

// TestSidebarSuppressesHorizontalTodos ensures the full View() doesn't render
// the todos twice (once in the sidebar, once below the transcript).
func TestSidebarSuppressesHorizontalTodos(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)
	m.showSidebar = true
	m.todos = []tools.Todo{{Content: "uniquetodostring", Status: tools.TodoInProgress}}

	out := m.View()
	if n := strings.Count(out, "uniquetodostring"); n != 1 {
		t.Errorf("todo content should appear exactly once (sidebar only), found %d times", n)
	}
}

// TestSetMCPServers wires the accessor to the field.
func TestSetMCPServers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	m.SetMCPServers([]mcp.ServerInfo{{Name: "x", Tools: 1}})
	if len(m.mcpServers) != 1 || m.mcpServers[0].Name != "x" {
		t.Errorf("SetMCPServers did not record servers: %+v", m.mcpServers)
	}
}

// TestSidebarShowsModifiedFiles verifies the Files section lists files the
// agent has touched, reading the live undo log via the agent.
func TestSidebarShowsModifiedFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)
	m.showSidebar = true

	// A path that doesn't exist on disk records as "created" (missing prior).
	// Use a short name so it isn't left-clipped to fit the panel.
	m.agent.SnapshotPath("newfile.go")

	out := m.sidebarView()
	for _, want := range []string{"Files (1)", "newfile.go", "+ "} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar Files section missing %q; got:\n%s", want, out)
		}
	}
}

// TestSidebarFilesEmptyWhenNoChanges ensures the section is absent with no edits.
func TestSidebarFilesEmptyWhenNoChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)
	m.showSidebar = true
	if got := m.sidebarFiles(24); got != "" {
		t.Errorf("Files section should be empty before any edits, got:\n%s", got)
	}
}

func TestClipPath(t *testing.T) {
	// Short paths are returned unchanged.
	if got := clipPath("a/b.go", 20); got != "a/b.go" {
		t.Errorf("short path changed: %q", got)
	}
	// Long paths truncate from the left, keeping the filename.
	got := clipPath("internal/tui/tui.go", 10)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "tui.go") {
		t.Errorf("clipPath should keep the suffix and lead with an ellipsis, got %q", got)
	}
	if len([]rune(got)) != 10 {
		t.Errorf("clipPath result should be exactly 10 runes, got %d (%q)", len([]rune(got)), got)
	}
}
