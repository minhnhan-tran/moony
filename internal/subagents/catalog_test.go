package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentsCatalog(t *testing.T) {
	cat := Catalog()
	if len(cat) < 3 {
		t.Fatalf("catalog should list several agents, got %d", len(cat))
	}
	e, ok := CatalogEntryByName("explore")
	if !ok {
		t.Fatal("catalog should include explore")
	}
	if e.Description == "" || e.Body == "" || len(e.Tools) == 0 {
		t.Errorf("explore entry incomplete: %+v", e)
	}
	md := e.Markdown()
	if !strings.HasPrefix(md, "---\n") || !strings.Contains(md, "description:") || !strings.Contains(md, "tools:") {
		t.Errorf("Markdown should be a valid persona .md:\n%s", md)
	}
	if _, ok := CatalogEntryByName("nope"); ok {
		t.Error("CatalogEntryByName should miss unknown names")
	}
}

func TestAgentInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "0") // keep discovery to sqe dirs only
	wd := t.TempDir()

	path, err := Install("explore", false, wd) // project scope
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persona .md not written: %v", err)
	}
	found := Load(wd)
	var got *Persona
	for i := range found {
		if found[i].Name == "explore" {
			got = &found[i]
		}
	}
	if got == nil {
		t.Fatal("installed persona not discovered by Load")
	}
	if got.Source != "project" || got.Description == "" || len(got.Tools) == 0 {
		t.Errorf("discovered persona wrong: %+v", *got)
	}

	if _, err := Install("explore", false, wd); err == nil {
		t.Error("installing an existing agent should error")
	}
	if _, err := Install("nope", false, wd); err == nil {
		t.Error("installing an unknown catalog agent should error")
	}
}
