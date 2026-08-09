package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

func TestScaffoldAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	wd := t.TempDir()

	// Scaffold a project persona.
	path, err := Scaffold("reviewer", false, wd)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("scaffolded file missing: %v", err)
	}
	if want := filepath.Join(wd, config.ProjectDir, "agents", "reviewer.md"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	// It's now discovered with a (template) description (alongside the built-ins).
	rev := findPersona(Load(wd), "reviewer")
	if rev == nil || rev.Description == "" {
		t.Fatalf("expected the scaffolded reviewer persona, got %+v", Load(wd))
	}

	// Scaffolding the same name again errors.
	if _, err := Scaffold("reviewer", false, wd); err == nil {
		t.Error("scaffolding an existing agent should error")
	}

	// Remove deletes it.
	if _, err := Remove("reviewer", wd); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if findPersona(Load(wd), "reviewer") != nil {
		t.Error("agent should be gone after Remove")
	}
	if _, err := Remove("nope", wd); err == nil {
		t.Error("removing an unknown agent should error")
	}
}

func TestScaffoldRejectsBadName(t *testing.T) {
	if _, err := Scaffold("../escape", false, t.TempDir()); err == nil {
		t.Error("expected an error for a path-like agent name")
	}
}

func TestSetModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "0")
	wd := t.TempDir()

	if _, err := Scaffold("reviewer", false, wd); err != nil {
		t.Fatalf("Scaffold: %v", err)
	}
	// Pin a model — it's parsed back and the description/tools survive.
	if _, err := SetModel("reviewer", "anthropic.claude-haiku-4-5", wd); err != nil {
		t.Fatalf("SetModel pin: %v", err)
	}
	p := loadOne(t, wd, "reviewer")
	if p.Model != "anthropic.claude-haiku-4-5" {
		t.Errorf("Model = %q, want the pinned id", p.Model)
	}
	if p.Description == "" || len(p.Tools) == 0 {
		t.Errorf("description/tools should survive a model pin: %+v", p)
	}

	// Re-pinning replaces (no duplicate model: line). A Gemini id with a colon
	// in it must round-trip intact.
	if _, err := SetModel("reviewer", "gcp:gemini-2.5-pro", wd); err != nil {
		t.Fatalf("SetModel re-pin: %v", err)
	}
	p = loadOne(t, wd, "reviewer")
	if p.Model != "gcp:gemini-2.5-pro" {
		t.Errorf("Model = %q, want the re-pinned colon id", p.Model)
	}
	data, _ := os.ReadFile(p.Path)
	if n := strings.Count(string(data), "\nmodel:"); n != 1 {
		t.Errorf("expected exactly one active model: line, got %d:\n%s", n, data)
	}

	// Clearing the pin removes it; the persona then inherits the parent.
	if _, err := SetModel("reviewer", "", wd); err != nil {
		t.Fatalf("SetModel clear: %v", err)
	}
	if p = loadOne(t, wd, "reviewer"); p.Model != "" {
		t.Errorf("Model = %q after clear, want empty", p.Model)
	}

	// Imported externals are read-only here.
	ext := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "outside.md"), []byte("---\ndescription: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "1")
	if _, err := SetModel("outside", "anthropic.claude-opus-4-8", wd); err == nil {
		t.Error("setting a model on an imported external persona should error")
	}
}

func TestSetProviderAndReasoning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "0")
	wd := t.TempDir()

	if _, err := Scaffold("reviewer", false, wd); err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	// Pin a provider — parsed back, coexists with description/tools, and
	// doesn't disturb an independently-pinned model.
	if _, err := SetProvider("reviewer", "anthropic", wd); err != nil {
		t.Fatalf("SetProvider pin: %v", err)
	}
	if _, err := SetModel("reviewer", "anthropic.claude-haiku-4-5", wd); err != nil {
		t.Fatalf("SetModel pin: %v", err)
	}
	if _, err := SetReasoning("reviewer", "HIGH", wd); err != nil {
		t.Fatalf("SetReasoning pin: %v", err)
	}
	p := loadOne(t, wd, "reviewer")
	if p.Provider != "anthropic" {
		t.Errorf("Provider = %q, want the pinned id", p.Provider)
	}
	if p.Model != "anthropic.claude-haiku-4-5" {
		t.Errorf("Model = %q, want it to survive the provider/reasoning pins", p.Model)
	}
	if p.Reasoning != "high" {
		t.Errorf("Reasoning = %q, want lower-cased %q", p.Reasoning, "high")
	}
	if p.Description == "" || len(p.Tools) == 0 {
		t.Errorf("description/tools should survive provider/reasoning pins: %+v", p)
	}

	// Re-pinning replaces (no duplicate lines).
	if _, err := SetProvider("reviewer", "gemini", wd); err != nil {
		t.Fatalf("SetProvider re-pin: %v", err)
	}
	p = loadOne(t, wd, "reviewer")
	if p.Provider != "gemini" {
		t.Errorf("Provider = %q, want the re-pinned id", p.Provider)
	}
	data, _ := os.ReadFile(p.Path)
	if n := strings.Count(string(data), "\nprovider:"); n != 1 {
		t.Errorf("expected exactly one active provider: line, got %d:\n%s", n, data)
	}

	// Clearing either pin removes it independently of the other.
	if _, err := SetProvider("reviewer", "", wd); err != nil {
		t.Fatalf("SetProvider clear: %v", err)
	}
	p = loadOne(t, wd, "reviewer")
	if p.Provider != "" {
		t.Errorf("Provider = %q after clear, want empty", p.Provider)
	}
	if p.Reasoning != "high" {
		t.Errorf("Reasoning = %q, clearing provider should not touch it", p.Reasoning)
	}
	if _, err := SetReasoning("reviewer", "", wd); err != nil {
		t.Fatalf("SetReasoning clear: %v", err)
	}
	if p = loadOne(t, wd, "reviewer"); p.Reasoning != "" {
		t.Errorf("Reasoning = %q after clear, want empty", p.Reasoning)
	}

	// A value with an embedded newline is rejected rather than splicing extra
	// frontmatter lines or escaping into the body.
	if _, err := SetProvider("reviewer", "anthropic\nmodel: injected", wd); err == nil {
		t.Error("SetProvider should reject a multi-line value")
	}
	if _, err := SetReasoning("reviewer", "high\r\nmodel: injected", wd); err == nil {
		t.Error("SetReasoning should reject a multi-line value")
	}
	if p = loadOne(t, wd, "reviewer"); p.Model != "anthropic.claude-haiku-4-5" {
		t.Errorf("a rejected injection attempt should leave the file untouched: %+v", p)
	}

	// Imported externals are read-only here.
	ext := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "outside.md"), []byte("---\ndescription: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "1")
	if _, err := SetProvider("outside", "anthropic", wd); err == nil {
		t.Error("setting a provider on an imported external persona should error")
	}
	if _, err := SetReasoning("outside", "high", wd); err == nil {
		t.Error("setting a reasoning level on an imported external persona should error")
	}
}

func loadOne(t *testing.T, wd, name string) Persona {
	t.Helper()
	for _, p := range Load(wd) {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("persona %q not found", name)
	return Persona{}
}

func TestMaterializeExternals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "1")
	wd := t.TempDir()

	// A Claude Code agent with a (possibly route-invalid) pinned model.
	claude := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(claude, "surveyor.md"),
		"---\ndescription: external surveyor\nmodel: some-claude-only-model\ntools: read_file, grep\n---\nYou survey.")

	// Before materializing it's an imported, read-only persona: model dropped,
	// and SetModel refuses it.
	if p := loadOne(t, wd, "surveyor"); p.Source != "claude" || p.Model != "" {
		t.Fatalf("precondition: expected imported claude persona with no model, got %+v", p)
	}
	if _, err := SetModel("surveyor", "anthropic.claude-opus-4-8", wd); err == nil {
		t.Fatal("SetModel should refuse an imported external persona before it's materialized")
	}

	// Copy it into the sqe config.
	copied, err := MaterializeExternals(wd)
	if err != nil {
		t.Fatalf("MaterializeExternals: %v", err)
	}
	if len(copied) != 1 || copied[0] != "surveyor" {
		t.Fatalf("copied = %v, want [surveyor]", copied)
	}

	// It's now an editable global sqe persona: description/tools survive, the
	// external model is stripped (inherits), and SetModel works.
	p := loadOne(t, wd, "surveyor")
	if p.Source != "global" {
		t.Errorf("source = %q after materialize, want global", p.Source)
	}
	if p.Model != "" {
		t.Errorf("model = %q after materialize, want empty (external id dropped)", p.Model)
	}
	if p.Description != "external surveyor" || len(p.Tools) != 2 {
		t.Errorf("description/tools should survive the copy: %+v", p)
	}
	if _, err := SetModel("surveyor", "anthropic.claude-opus-4-8", wd); err != nil {
		t.Fatalf("SetModel should work on the materialized persona: %v", err)
	}

	// Idempotent: re-running copies nothing (the name is now sqe-backed), and the
	// pinned model set above is not clobbered.
	again, err := MaterializeExternals(wd)
	if err != nil {
		t.Fatalf("MaterializeExternals (2nd): %v", err)
	}
	if len(again) != 0 {
		t.Errorf("2nd materialize copied %v, want nothing", again)
	}
	if p := loadOne(t, wd, "surveyor"); p.Model != "anthropic.claude-opus-4-8" {
		t.Errorf("model = %q after re-materialize, want the pin to survive", p.Model)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
