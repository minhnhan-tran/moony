package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Create writes a new skill SKILL.md from caller-supplied content (the Agent
// Builder's "New skill" form), as opposed to Scaffold (a fixed TODO template) or
// Install (a catalog entry). description and the instruction body are required —
// the loader skips skills without a description, so an empty one would make the
// new skill invisible. global selects ~/.claude/skills; otherwise the project's
// ./.claude/skills under workingDir. It errors on an invalid name, an empty
// description/body, or if the skill already exists. Returns the SKILL.md path.
func Create(name, description, body string, global bool, workingDir string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	description = strings.TrimSpace(description)
	body = strings.TrimSpace(body)
	if description == "" {
		return "", fmt.Errorf("a description is required")
	}
	if body == "" {
		return "", fmt.Errorf("skill instructions are required")
	}
	base, err := skillsBase(global, workingDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, name)
	path := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("skill %q already exists at %s", name, path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(renderSkill(name, description, body)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// renderSkill builds a SKILL.md (frontmatter + body) that parseFrontmatter reads
// back: single-line name + description, then the instruction body.
func renderSkill(name, description, body string) string {
	oneLine := func(s string) string { return strings.Join(strings.Fields(strings.TrimSpace(s)), " ") }
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + oneLine(name) + "\n")
	b.WriteString("description: " + oneLine(description) + "\n")
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

// Scaffold creates a new skill directory with a SKILL.md template and returns
// the file path. global selects ~/.claude/skills; otherwise the project's
// ./.claude/skills under workingDir. It errors if the skill already exists.
func Scaffold(name string, global bool, workingDir string) (string, error) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	base, err := skillsBase(global, workingDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, name)
	path := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("skill %q already exists at %s", name, path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(skillTemplate, name, name)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Install writes a catalog skill (see catalog.go) into the skills dir — the
// global ~/.claude/skills, or the project's ./.claude/skills when global is
// false. Returns the SKILL.md path. Errors if the skill already exists.
func Install(name string, global bool, workingDir string) (string, error) {
	e, ok := CatalogEntryByName(name)
	if !ok {
		return "", fmt.Errorf("unknown catalog skill %q", name)
	}
	base, err := skillsBase(global, workingDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, e.Name)
	path := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("skill %q already exists at %s", e.Name, path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(e.SkillMarkdown()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Remove deletes a discovered skill's directory. It only removes skills under a
// .claude/skills location (global ~/.claude/skills or the project's
// ./.claude/skills); skills from ~/.agents/skills are refused — disable those
// with the manage state instead.
func Remove(name, workingDir string) (string, error) {
	for _, s := range Load(workingDir) {
		if s.Name != name {
			continue
		}
		if s.Source == "agents" {
			return "", fmt.Errorf("won't delete %q from %s (shared agents dir); disable it instead", name, s.Dir)
		}
		if err := os.RemoveAll(s.Dir); err != nil {
			return "", err
		}
		return s.Dir, nil
	}
	return "", fmt.Errorf("unknown skill %q", name)
}

func skillsBase(global bool, workingDir string) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", "skills"), nil
	}
	return filepath.Join(workingDir, ".claude", "skills"), nil
}

const skillTemplate = `---
name: %s
description: TODO one line on what this skill does and when to use it
---

# %s

TODO: instructions for the model. Describe the workflow and any bundled files
(scripts/, references/) it should read or run.
`
