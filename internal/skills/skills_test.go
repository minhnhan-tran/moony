package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir, frontmatterBody string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(frontmatterBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byName(ss []Skill) map[string]Skill {
	m := map[string]Skill{}
	for _, s := range ss {
		m[s.Name] = s
	}
	return m
}

func TestLoadDiscoversSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()

	writeSkill(t, filepath.Join(home, ".claude", "skills", "docx"),
		"---\nname: docx\ndescription: Work with Word documents\n---\n# Docx\nInstructions here.")
	// no `name` in frontmatter -> falls back to the directory name
	writeSkill(t, filepath.Join(home, ".agents", "skills", "pdf"),
		"---\ndescription: Work with PDFs\n---\nPDF body.")
	writeSkill(t, filepath.Join(wd, ".claude", "skills", "proj"),
		"---\nname: proj\ndescription: Project skill\n---\nProject body.")
	// a skill with no description is skipped (can't advertise it)
	writeSkill(t, filepath.Join(home, ".claude", "skills", "nodesc"),
		"---\nname: nodesc\n---\nNo description.")

	got := byName(Load(wd))
	if _, ok := got["nodesc"]; ok {
		t.Error("skill without a description should be skipped")
	}
	for _, n := range []string{"docx", "pdf", "proj"} {
		if _, ok := got[n]; !ok {
			t.Errorf("missing discovered skill %q", n)
		}
	}
	if got["docx"].Description != "Work with Word documents" || got["docx"].Body != "# Docx\nInstructions here." {
		t.Errorf("docx parsed wrong: %+v", got["docx"])
	}
	if got["pdf"].Name != "pdf" {
		t.Errorf("name should fall back to dir name, got %q", got["pdf"].Name)
	}
	if got["docx"].Dir != filepath.Join(home, ".claude", "skills", "docx") {
		t.Errorf("docx dir = %q", got["docx"].Dir)
	}
}

func TestProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	writeSkill(t, filepath.Join(home, ".claude", "skills", "dup"),
		"---\nname: dup\ndescription: global\n---\nglobal body")
	writeSkill(t, filepath.Join(wd, ".claude", "skills", "dup"),
		"---\nname: dup\ndescription: project\n---\nproject body")

	got := byName(Load(wd))["dup"]
	if got.Source != "project" || got.Description != "project" {
		t.Errorf("project skill should override global; got %+v", got)
	}
}

func TestSkillsOptOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MOONY_SKILLS", "0")
	writeSkill(t, filepath.Join(home, ".claude", "skills", "docx"),
		"---\nname: docx\ndescription: x\n---\nbody")
	if got := Load(t.TempDir()); got != nil {
		t.Errorf("MOONY_SKILLS=0 should discover nothing; got %d", len(got))
	}
}

func TestSkillEnvVars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()

	writeSkill(t, filepath.Join(home, ".claude", "skills", "gh"),
		"---\nname: gh\ndescription: GitHub helper\nenv: GITHUB_PERSONAL_ACCESS_TOKEN, SOME_SECRET\n---\nbody")
	writeSkill(t, filepath.Join(home, ".claude", "skills", "brace"),
		"---\nname: brace\ndescription: bracketed\nneeds: [FOO_TOKEN]\n---\nbody")
	writeSkill(t, filepath.Join(wd, ".claude", "skills", "none"),
		"---\nname: none\ndescription: no env\n---\nbody")

	got := map[string]bool{}
	for _, e := range EnvVars(wd) {
		got[e] = true
	}
	for _, want := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "SOME_SECRET", "FOO_TOKEN"} {
		if !got[want] {
			t.Errorf("EnvVars missing %q; got %v", want, got)
		}
	}
}
