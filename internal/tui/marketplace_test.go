package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/tools"
)

// modelInEmptyDir builds a Model rooted in a fresh temp dir with an isolated
// HOME, so no skills/MCP servers are present and the prebuilt catalogs show up
// as installable marketplace rows.
func modelInEmptyDir(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir+"/.config")
	cfg := config.Load()
	cfg.WorkingDir = dir
	ag := agent.New(cfg, llm.NewClient(cfg), tools.Default(dir, cfg.MaxToolOutputBytes), nil, nil)
	return New(cfg, ag, nil)
}

func installRows(m Model) map[string]bool {
	out := map[string]bool{}
	if m.menu == nil {
		return out
	}
	for _, it := range m.menu.items {
		if it.install {
			out[it.value] = true
		}
	}
	return out
}

// TestSkillsMenuOffersCatalogInstall checks every prebuilt catalog skill is
// offered as a one-keypress Install row when none is installed yet.
func TestSkillsMenuOffersCatalogInstall(t *testing.T) {
	m := modelInEmptyDir(t)
	next, _ := m.openSkillsMenu()
	rows := installRows(next.(Model))
	cat := skills.Catalog()
	if len(cat) == 0 {
		t.Skip("empty skills catalog")
	}
	for _, e := range cat {
		if !rows[e.Name] {
			t.Errorf("skill %q should be offered as a marketplace install", e.Name)
		}
	}
}

// TestMcpsMenuOffersCatalogInstall checks every prebuilt catalog MCP server is
// offered as an Install row when none is configured yet.
func TestMcpsMenuOffersCatalogInstall(t *testing.T) {
	m := modelInEmptyDir(t)
	next, _ := m.openMcpsMenu()
	rows := installRows(next.(Model))
	cat := mcp.Catalog()
	if len(cat) == 0 {
		t.Skip("empty mcp catalog")
	}
	for _, e := range cat {
		if !rows[e.Name] {
			t.Errorf("MCP server %q should be offered as a marketplace install", e.Name)
		}
	}
}

// TestInstalledSkillIsNotAnInstallRow confirms that once a catalog skill is
// installed, it appears as a normal toggle row (not an Install row).
func TestInstalledSkillIsNotAnInstallRow(t *testing.T) {
	cat := skills.Catalog()
	if len(cat) == 0 {
		t.Skip("empty skills catalog")
	}
	name := cat[0].Name
	m := modelInEmptyDir(t)
	if _, err := skills.Install(name, false, m.cfg.WorkingDir); err != nil {
		t.Fatalf("install %q: %v", name, err)
	}
	next, _ := m.openSkillsMenu()
	if installRows(next.(Model))[name] {
		t.Errorf("installed skill %q should be a toggle row, not an Install row", name)
	}
}

// TestPluginsMenuListsSkillsAndMcps verifies the unified /plugins marketplace
// lists individual catalog skills and MCP servers (by kind), installable in an
// empty workspace.
func TestPluginsMenuListsSkillsAndMcps(t *testing.T) {
	m := modelInEmptyDir(t)
	next, _ := m.openPluginsMenu()
	mm := next.(Model)
	if mm.menu == nil {
		t.Fatal("no marketplace menu opened")
	}
	skillRows, mcpRows := map[string]bool{}, map[string]bool{}
	for _, it := range mm.menu.items {
		switch it.kind {
		case "skill":
			if it.install {
				skillRows[it.value] = true
			}
		case "mcp":
			if it.install {
				mcpRows[it.value] = true
			}
		}
	}
	for _, e := range skills.Catalog() {
		if !skillRows[e.Name] {
			t.Errorf("/plugins should offer skill %q to install", e.Name)
		}
	}
	for _, e := range mcp.Catalog() {
		if !mcpRows[e.Name] {
			t.Errorf("/plugins should offer MCP server %q to install", e.Name)
		}
	}
}

var _ tea.Model = Model{}
