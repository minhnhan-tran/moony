package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Create writes a SKILL.md from user-supplied content that Load/parseFrontmatter
// reads back: name, description, and the instruction body.
func TestCreateRoundTrips(t *testing.T) {
	wd := t.TempDir()
	path, err := Create("pdf-tools", "Use when working with PDF files", "# PDF tools\n\nRun the helper in scripts/.", false, wd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if want := filepath.Join(wd, ".claude", "skills", "pdf-tools", "SKILL.md"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	var sk *Skill
	for _, c := range Load(wd) {
		if c.Name == "pdf-tools" {
			cc := c
			sk = &cc
			break
		}
	}
	if sk == nil {
		t.Fatal("created skill not discovered by Load")
	}
	if sk.Description != "Use when working with PDF files" {
		t.Errorf("description = %q", sk.Description)
	}
	if !strings.Contains(sk.Body, "Run the helper") {
		t.Errorf("body = %q", sk.Body)
	}
	if sk.Source != "project" {
		t.Errorf("source = %q, want project", sk.Source)
	}
}

func TestCreateValidates(t *testing.T) {
	wd := t.TempDir()
	if _, err := Create("", "d", "b", false, wd); err == nil {
		t.Error("empty name should error")
	}
	if _, err := Create("a/b", "d", "b", false, wd); err == nil {
		t.Error("name with a slash should error")
	}
	if _, err := Create("ok", "", "b", false, wd); err == nil {
		t.Error("empty description should error (loader skips no-description skills)")
	}
	if _, err := Create("ok", "d", "", false, wd); err == nil {
		t.Error("empty body should error")
	}
	if _, err := Create("dup", "d", "b", false, wd); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := Create("dup", "d", "b", false, wd); err == nil {
		t.Error("duplicate name should error")
	}
}

// A created skill must always carry a description line — the loader drops skills
// without one, which would make a newly created skill silently invisible.
func TestCreateAlwaysWritesDescription(t *testing.T) {
	wd := t.TempDir()
	if _, err := Create("withdesc", "does a thing", "# body", false, wd); err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(wd, ".claude", "skills", "withdesc", "SKILL.md"))
	if !strings.Contains(string(data), "description: does a thing") {
		t.Errorf("expected description line, got:\n%s", data)
	}
}
