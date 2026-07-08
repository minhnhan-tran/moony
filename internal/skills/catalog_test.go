package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsCatalog(t *testing.T) {
	cat := Catalog()
	if len(cat) < 3 {
		t.Fatalf("catalog should list several skills, got %d", len(cat))
	}
	e, ok := CatalogEntryByName("conventional-commits")
	if !ok {
		t.Fatal("catalog should include conventional-commits")
	}
	if e.Description == "" || e.Body == "" {
		t.Errorf("entry incomplete: %+v", e)
	}
	md := e.SkillMarkdown()
	if !strings.Contains(md, "name: conventional-commits") || !strings.HasPrefix(md, "---\n") {
		t.Errorf("SkillMarkdown should be a valid SKILL.md:\n%s", md)
	}
	if _, ok := CatalogEntryByName("nope"); ok {
		t.Error("CatalogEntryByName should miss unknown names")
	}
}

// TestBundledSkillsLoaded verifies the embedded SKILL.md starter skills (gh,
// pr-review) are parsed from catalog/*.md into the catalog with their
// frontmatter intact and round-trip through SkillMarkdown.
func TestBundledSkillsLoaded(t *testing.T) {
	for _, name := range []string{"gh", "pr-review"} {
		e, ok := CatalogEntryByName(name)
		if !ok {
			t.Errorf("bundled skill %q should be in the catalog", name)
			continue
		}
		if e.Title == "" || e.Description == "" || e.Body == "" {
			t.Errorf("bundled skill %q incomplete: %+v", name, e)
		}
		md := e.SkillMarkdown()
		if !strings.HasPrefix(md, "---\n") || !strings.Contains(md, "name: "+name) {
			t.Errorf("bundled skill %q SkillMarkdown invalid:\n%s", name, md)
		}
	}
}

func TestSkillInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	wd := t.TempDir()

	path, err := Install("conventional-commits", false, wd) // project scope
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}
	found := Load(wd)
	var got *Skill
	for i := range found {
		if found[i].Name == "conventional-commits" {
			got = &found[i]
		}
	}
	if got == nil {
		t.Fatal("installed skill not discovered by Load")
	}
	if got.Source != "project" || got.Description == "" {
		t.Errorf("discovered skill wrong: %+v", *got)
	}

	if _, err := Install("conventional-commits", false, wd); err == nil {
		t.Error("installing an existing skill should error")
	}
	if _, err := Install("nope", false, wd); err == nil {
		t.Error("installing an unknown catalog skill should error")
	}
}
