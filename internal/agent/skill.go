package agent

// skillTool lets the model load a Claude-style Agent Skill on demand. Only the
// catalog (name + description) is advertised in the tool's description; calling
// the tool returns the full SKILL.md instructions plus the skill's directory, so
// the model can then follow them and read any bundled scripts/references with
// the normal read/bash tools. This mirrors how skills work in Claude.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/tools"
)

type skillTool struct {
	skills []skills.Skill
	byName map[string]skills.Skill
}

func newSkillTool(found []skills.Skill) *skillTool {
	byName := make(map[string]skills.Skill, len(found))
	for _, s := range found {
		byName[s.Name] = s
	}
	return &skillTool{skills: found, byName: byName}
}

func (t *skillTool) Name() string { return "skill" }

func (t *skillTool) Description() string {
	var b strings.Builder
	b.WriteString("Load a specialized skill that provides domain-specific instructions and workflows. ")
	b.WriteString("When the current task matches one of the skills below, call this tool with its name to load the full instructions, then follow them. ")
	b.WriteString("Available skills:\n")
	for _, s := range t.skills {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *skillTool) Schema() json.RawMessage {
	names := make([]string, 0, len(t.skills))
	for _, s := range t.skills {
		names = append(names, fmt.Sprintf("%q", s.Name))
	}
	enum := ""
	if len(names) > 0 {
		enum = `, "enum": [` + strings.Join(names, ", ") + `]`
	}
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The skill to load."` + enum + `}
		},
		"required": ["name"]
	}`)
}

// Mutates is false: loading a skill only returns instructions. Any actions the
// skill then prescribes go through their own tools' approval gates.
func (t *skillTool) Mutates() bool { return false }

func (t *skillTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid skill input: %v", err)
	}
	s, ok := t.byName[in.Name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q (available: %s)", in.Name, strings.Join(skillNames(t.skills), ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Skill: %s\n\n", s.Name)
	b.WriteString(s.Body)
	fmt.Fprintf(&b, "\n\n---\nSkill directory: %s\n", s.Dir)
	b.WriteString("Bundled files (scripts/, references/, etc.) live there; read or run them with the read and bash tools using that path.")
	return b.String(), nil
}

func skillNames(ss []skills.Skill) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

// contextItems returns the per-skill token contributions to this tool's
// description (the "- name: description" line each skill adds), so the context
// meter can itemize the Skills segment like Claude Code does.
func (t *skillTool) contextItems() []ContextSegment {
	out := make([]ContextSegment, 0, len(t.skills))
	for _, s := range t.skills {
		line := fmt.Sprintf("- %s: %s\n", s.Name, s.Description)
		out = append(out, ContextSegment{Label: s.Name, Tokens: len(line) / 4})
	}
	return out
}

// AttachSkills registers the `skill` tool so the model can load the supplied
// skills on demand. Re-callable: passing the current enabled set replaces the
// tool's catalog live; passing an empty set removes the tool entirely.
func (a *Agent) AttachSkills(found []skills.Skill) {
	if len(found) == 0 {
		a.tools.Unregister("skill")
		return
	}
	a.tools.Register(newSkillTool(found))
}

// Compile-time guarantee that *skillTool satisfies tools.Tool.
var _ tools.Tool = (*skillTool)(nil)
