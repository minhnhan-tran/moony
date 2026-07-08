package manage

import (
	"path/filepath"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func TestDefaultEnabled(t *testing.T) {
	isolate(t)
	s := Load(t.TempDir())
	if !s.SkillEnabled("docx") || !s.MCPEnabled("github") {
		t.Error("items should be enabled by default")
	}
}

func TestGlobalDisablePersists(t *testing.T) {
	isolate(t)
	wd := t.TempDir()
	s := Load(wd)
	if err := s.SetSkill("docx", false, Global); err != nil {
		t.Fatal(err)
	}
	if s.SkillEnabled("docx") {
		t.Error("docx should be disabled after global disable")
	}
	// A fresh load sees the persisted state.
	if Load(wd).SkillEnabled("docx") {
		t.Error("global disable did not persist")
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	isolate(t)
	wd := t.TempDir()
	s := Load(wd)
	if err := s.SetMCP("github", false, Global); err != nil {
		t.Fatal(err)
	}
	if s.MCPEnabled("github") {
		t.Fatal("github should be globally disabled")
	}
	// The project re-enables it (override).
	if err := s.SetMCP("github", true, Project); err != nil {
		t.Fatal(err)
	}
	if !s.MCPEnabled("github") {
		t.Error("project enable should override the global disable")
	}
	if !Load(wd).MCPEnabled("github") {
		t.Error("project override did not persist")
	}
}

func TestProjectDisableScopedToProject(t *testing.T) {
	isolate(t)
	wd := t.TempDir()
	other := t.TempDir()
	s := Load(wd)
	if err := s.SetSkill("pdf", false, Project); err != nil {
		t.Fatal(err)
	}
	if s.SkillEnabled("pdf") {
		t.Error("pdf should be disabled in this project")
	}
	// A different project is unaffected.
	if !Load(other).SkillEnabled("pdf") {
		t.Error("project disable leaked to another project")
	}
}

func TestReEnableClearsDisable(t *testing.T) {
	isolate(t)
	wd := t.TempDir()
	s := Load(wd)
	_ = s.SetSkill("x", false, Global)
	_ = s.SetSkill("x", true, Global)
	if !s.SkillEnabled("x") {
		t.Error("global re-enable should clear the disable")
	}
}

func TestAgentEnableDisablePersists(t *testing.T) {
	isolate(t)
	wd := t.TempDir()
	s := Load(wd)
	if !s.AgentEnabled("explore") {
		t.Error("agents should be enabled by default")
	}
	if err := s.SetAgent("explore", false, Project); err != nil {
		t.Fatal(err)
	}
	if s.AgentEnabled("explore") {
		t.Error("explore should be disabled after SetAgent(false)")
	}
	if Load(wd).AgentEnabled("explore") {
		t.Error("agent disable did not persist")
	}
}

func TestLSPEnableDisablePersists(t *testing.T) {
	isolate(t)
	wd := t.TempDir()
	s := Load(wd)
	if !s.LSPEnabled("gopls") {
		t.Error("language servers should be enabled by default")
	}
	if err := s.SetLSP("gopls", false, Project); err != nil {
		t.Fatal(err)
	}
	if s.LSPEnabled("gopls") {
		t.Error("gopls should be disabled after SetLSP(false)")
	}
	if Load(wd).LSPEnabled("gopls") {
		t.Error("LSP disable did not persist")
	}
	// A project enable overrides a global disable (mirrors MCP/skill semantics).
	if err := s.SetLSP("gopls", false, Global); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLSP("gopls", true, Project); err != nil {
		t.Fatal(err)
	}
	if !Load(wd).LSPEnabled("gopls") {
		t.Error("project enable should override global disable")
	}
}
