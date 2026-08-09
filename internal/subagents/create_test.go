package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// Create writes a persona from user-supplied content that Load/parseFile reads
// back faithfully: description, optional model, optional tools allowlist, body.
func TestCreateRoundTrips(t *testing.T) {
	wd := t.TempDir()
	path, err := Create("researcher", "Digs through docs and reports findings", "anthropic.claude-opus-4-8",
		[]string{"read_file", "grep"}, "You are the researcher subagent. Find and summarize.", false, wd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if want := filepath.Join(wd, config.ProjectDir, "agents", "researcher.md"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	var p *Persona
	for _, c := range Load(wd) {
		if c.Name == "researcher" {
			cc := c
			p = &cc
			break
		}
	}
	if p == nil {
		t.Fatal("created persona not discovered by Load")
	}
	if p.Description != "Digs through docs and reports findings" {
		t.Errorf("description = %q", p.Description)
	}
	if p.Model != "anthropic.claude-opus-4-8" {
		t.Errorf("model = %q", p.Model)
	}
	if strings.Join(p.Tools, ",") != "read_file,grep" {
		t.Errorf("tools = %v", p.Tools)
	}
	if !strings.Contains(p.SystemPrompt, "Find and summarize") {
		t.Errorf("system prompt = %q", p.SystemPrompt)
	}
	if p.Source != "project" {
		t.Errorf("source = %q, want project", p.Source)
	}
}

// A multi-line description must collapse to one frontmatter line so the YAML
// block stays parseable.
func TestCreateFlattensMultilineDescription(t *testing.T) {
	wd := t.TempDir()
	if _, err := Create("x", "line one\nline two", "", nil, "body", false, wd); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(wd, config.ProjectDir, "agents", "x.md"))
	if strings.Contains(string(data), "line one\nline two") {
		t.Error("description was not flattened to a single line")
	}
	for _, p := range Load(wd) {
		if p.Name == "x" && p.Description != "line one line two" {
			t.Errorf("description = %q, want flattened", p.Description)
		}
	}
}

func TestCreateValidates(t *testing.T) {
	wd := t.TempDir()
	if _, err := Create("", "d", "", nil, "b", false, wd); err == nil {
		t.Error("empty name should error")
	}
	if _, err := Create("a/b", "d", "", nil, "b", false, wd); err == nil {
		t.Error("name with a slash should error")
	}
	if _, err := Create("ok", "", "", nil, "b", false, wd); err == nil {
		t.Error("empty description should error")
	}
	if _, err := Create("ok", "d", "", nil, "", false, wd); err == nil {
		t.Error("empty system prompt should error")
	}
	// Duplicate.
	if _, err := Create("dup", "d", "", nil, "b", false, wd); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := Create("dup", "d", "", nil, "b", false, wd); err == nil {
		t.Error("duplicate name should error")
	}
}

// With no tools given, no tools: line is written, so the persona gets all tools.
func TestCreateOmitsEmptyTools(t *testing.T) {
	wd := t.TempDir()
	if _, err := Create("notools", "d", "", nil, "b", false, wd); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(wd, config.ProjectDir, "agents", "notools.md"))
	if strings.Contains(string(data), "tools:") {
		t.Errorf("expected no tools: line, got:\n%s", data)
	}
}
