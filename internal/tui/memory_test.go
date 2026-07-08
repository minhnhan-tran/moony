package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// memoryModel builds a model whose agent works in a sandboxed temp dir, so
// memory writes go to a throwaway AGENTS.md rather than the repo.
func memoryModel(t *testing.T) (Model, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()

	cfg := config.Load()
	cfg.WorkingDir = dir
	reg := tools.Default(dir, cfg.MaxToolOutputBytes)
	ag := agent.New(cfg, llm.NewClient(cfg), reg, nil, nil)
	return New(cfg, ag, nil), dir
}

// TestMemoryHashShortcutSaves verifies a leading "#" saves to memory instead of
// messaging the model.
func TestMemoryHashShortcutSaves(t *testing.T) {
	m, dir := memoryModel(t)
	m = sized(t, m, 100, 30)

	m.input.SetValue("# always run make vet")
	next, _ := m.submit()
	m = next.(Model)

	// Stays idle (no model turn started) and reports the save.
	if m.state != stateIdle {
		t.Errorf("state = %v, want idle (the # shortcut must not start a turn)", m.state)
	}
	last := m.trans.entries[len(m.trans.entries)-1]
	if last.kind != entryInfo || !strings.Contains(last.text, "Remembered") {
		t.Errorf("expected a 'Remembered' info entry, got kind=%v text=%q", last.kind, last.text)
	}
	// Persisted to the project AGENTS.md.
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(data), "- always run make vet") {
		t.Errorf("memory not written:\n%s", data)
	}
}

// TestMemorySlashCommand covers /memory <note> (add) and /memory (view).
func TestMemorySlashCommand(t *testing.T) {
	m, _ := memoryModel(t)
	m = sized(t, m, 100, 30)

	m.input.SetValue("/memory tests live next to code")
	next, _ := m.submit()
	m = next.(Model)
	if last := m.trans.entries[len(m.trans.entries)-1]; !strings.Contains(last.text, "Remembered") {
		t.Errorf("/memory <note> should confirm a save, got %q", last.text)
	}

	m.input.SetValue("/memory")
	next, _ = m.submit()
	m = next.(Model)
	view := m.trans.entries[len(m.trans.entries)-1].text
	if !strings.Contains(view, "Project memory") || !strings.Contains(view, "tests live next to code") {
		t.Errorf("/memory view should list the saved note, got:\n%s", view)
	}
}
