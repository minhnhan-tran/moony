package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/manage"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillsCommandOpensSelectableMenu confirms /skills (no args) opens a
// selectable menu (not a printed text list) and that selecting a row toggles
// the skill's enabled state.
func TestSkillsCommandOpensSelectableMenu(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".claude", "skills", "demo", "SKILL.md"),
		"---\nname: demo\ndescription: A demo skill\n---\nbody")

	m := sized(t, newTestModel(t), 100, 30)
	m.cfg.WorkingDir = t.TempDir() // isolate manage.Project writes from the real package dir
	res, _ := m.command("/skills")
	mm := res.(Model)
	if mm.state != stateMenu || mm.menu == nil || mm.menu.kind != menuSkills {
		t.Fatalf("/skills should open a selectable skills menu, got state=%v menu=%v", mm.state, mm.menu)
	}
	idx := -1
	for i, it := range mm.menu.items {
		if it.label == "demo" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("skills menu missing the demo skill: %+v", mm.menu.items)
	}
	if !mm.menu.items[idx].current {
		t.Error("a freshly discovered skill should show as enabled (current)")
	}

	// Selecting it toggles it off.
	mm.menu.cursor = idx
	done, _ := mm.applyMenu()
	fm := done.(Model)
	if fm.state != stateIdle || fm.menu != nil {
		t.Error("menu should close after toggling")
	}
	if manage.Load(fm.cfg.WorkingDir).SkillEnabled("demo") {
		t.Error("selecting the enabled skill should have disabled it")
	}
}

// TestMcpsCommandOpensSelectableMenu confirms /mcps (no args) opens a selectable
// toggle menu, and selecting a server flips its enabled state.
func TestMcpsCommandOpensSelectableMenu(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	base, _ := config.GlobalConfigDir()
	writeFile(t, filepath.Join(base, "mcp.json"),
		`{"servers":{"demo":{"command":"echo"}}}`)

	m := sized(t, newTestModel(t), 100, 30)
	m.cfg.WorkingDir = t.TempDir() // isolate manage.Project writes from the real package dir
	res, _ := m.command("/mcps")
	mm := res.(Model)
	if mm.state != stateMenu || mm.menu == nil || mm.menu.kind != menuMcps {
		t.Fatalf("/mcps should open a selectable MCP menu, got state=%v menu=%v", mm.state, mm.menu)
	}
	idx := -1
	for i, it := range mm.menu.items {
		if it.label == "demo" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("MCP menu missing the demo server: %+v", mm.menu.items)
	}

	mm.menu.cursor = idx
	done, _ := mm.applyMenu()
	fm := done.(Model)
	if fm.state != stateIdle || fm.menu != nil {
		t.Error("menu should close after toggling")
	}
	if manage.Load(fm.cfg.WorkingDir).MCPEnabled("demo") {
		t.Error("selecting the enabled MCP server should have disabled it")
	}
}

// TestAgentsCommandOpensSelectableMenu confirms /agents (no args) opens a
// selectable menu and selecting a persona toggles it; /agents new scaffolds one.
func TestAgentsCommandOpensSelectableMenu(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	base, _ := config.GlobalConfigDir()
	writeFile(t, filepath.Join(base, "agents", "explore.md"),
		"---\ndescription: read-only explorer\ntools: read_file, grep\n---\nYou explore.")

	m := sized(t, newTestModel(t), 100, 30)
	m.cfg.WorkingDir = t.TempDir() // isolate manage.Project writes
	res, _ := m.command("/agents")
	mm := res.(Model)
	if mm.state != stateMenu || mm.menu == nil || mm.menu.kind != menuAgents {
		t.Fatalf("/agents should open a selectable agents menu, got state=%v menu=%v", mm.state, mm.menu)
	}
	idx := -1
	for i, it := range mm.menu.items {
		if it.label == "explore" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("agents menu missing the explore persona: %+v", mm.menu.items)
	}
	if !mm.menu.items[idx].current {
		t.Error("a discovered persona should show as enabled (current)")
	}

	mm.menu.cursor = idx
	done, _ := mm.applyMenu()
	fm := done.(Model)
	if fm.state != stateIdle || fm.menu != nil {
		t.Error("menu should close after toggling")
	}
	if manage.Load(fm.cfg.WorkingDir).AgentEnabled("explore") {
		t.Error("selecting the enabled persona should have disabled it")
	}
}

// TestProviderCommandOpensSelectableMenu confirms /provider (no args) opens a
// selectable provider picker, and selecting a row switches the backend (Ollama
// is the sole provider) via the shared command path.
func TestProviderCommandOpensSelectableMenu(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	m := sized(t, newTestModel(t), 100, 30)
	m.cfg.WorkingDir = t.TempDir()
	res, _ := m.command("/provider")
	mm := res.(Model)
	if mm.state != stateMenu || mm.menu == nil || mm.menu.kind != menuProviders {
		t.Fatalf("/provider should open a selectable provider menu, got state=%v menu=%v", mm.state, mm.menu)
	}
	idx := -1
	for i, it := range mm.menu.items {
		if it.value == "ollama" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("provider menu missing ollama: %+v", mm.menu.items)
	}

	// Selecting ollama re-dispatches "/provider ollama" and sets the backend.
	mm.menu.cursor = idx
	done, _ := mm.applyMenu()
	fm := done.(Model)
	if fm.state != stateIdle || fm.menu != nil {
		t.Error("menu should close after selecting a provider")
	}
	if fm.cfg.LLMProvider != "ollama" {
		t.Errorf("selecting ollama should set LLMProvider=ollama, got %q", fm.cfg.LLMProvider)
	}
}
