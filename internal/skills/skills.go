// Package skills discovers Claude-style Agent Skills — directories containing a
// SKILL.md with YAML frontmatter (name, description) plus markdown instructions —
// and exposes them so the agent can load one on demand (progressive disclosure:
// only the catalog of names+descriptions is advertised; the full body loads when
// the model invokes the `skill` tool).
//
// Skills are discovered from (project overrides global by name):
//   - ~/.claude/skills/<name>/SKILL.md      (Claude global)
//   - ~/.agents/skills/<name>/SKILL.md       (shared agents dir)
//   - ./.claude/skills/<name>/SKILL.md       (project)
//
// Set MOONY_SKILLS=0 to disable discovery.
package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is one discovered Agent Skill.
type Skill struct {
	Name        string   // from frontmatter `name`, else the directory name
	Description string   // from frontmatter `description`
	Body        string   // the SKILL.md instructions (everything after frontmatter)
	Dir         string   // the skill's directory, so bundled scripts/ and references/ can be read
	Source      string   // "claude", "agents", or "project"
	Path        string   // path to the SKILL.md file
	Env         []string // credential env vars the skill declares it needs (frontmatter `env:`/`needs:`)
}

// frontmatterEnvVars extracts env-var names declared in a leading --- frontmatter
// block via an `env:` / `needs:` / `secrets:` key. Supports an inline list:
// `env: A, B` or `env: [A, B]`. Returns nil when absent.
func frontmatterEnvVars(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s[4:4+end], "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "env", "needs", "secrets":
			v = strings.TrimSpace(v)
			v = strings.TrimPrefix(v, "[")
			v = strings.TrimSuffix(v, "]")
			for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
				if f = strings.Trim(f, "\"'"); f != "" {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// EnvVars returns the union of credential env vars declared by all installed
// skills (sorted, deduped), so callers can surface them without hardcoding.
func EnvVars(workingDir string) []string {
	set := map[string]bool{}
	for _, s := range Load(workingDir) {
		for _, e := range s.Env {
			if e != "" {
				set[e] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// Load discovers skills across the global and project skill directories. A
// missing directory is not an error. Project skills override global ones with
// the same name.
func Load(workingDir string) []Skill {
	if !enabled() {
		return nil
	}
	type dir struct{ path, source string }
	var dirs []dir
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			dir{filepath.Join(home, ".claude", "skills"), "claude"},
			dir{filepath.Join(home, ".agents", "skills"), "agents"},
		)
	}
	dirs = append(dirs, dir{filepath.Join(workingDir, ".claude", "skills"), "project"})

	merged := map[string]Skill{}
	for _, d := range dirs {
		for _, s := range readSkillDir(d.path, d.source) {
			merged[s.Name] = s // later dirs (project last) override
		}
	}

	out := make([]Skill, 0, len(merged))
	for _, s := range merged {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// enabled reports whether skill discovery is on (default). MOONY_SKILLS=0/off/
// false/no disables it.
func enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MOONY_SKILLS"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// readSkillDir lists every <name>/SKILL.md skill under dir.
func readSkillDir(dir, source string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(dir, e.Name())
		path := filepath.Join(skillDir, "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		name, desc, body := parseFrontmatter(string(data))
		if name == "" {
			name = e.Name()
		}
		if desc == "" {
			continue // a skill with no description can't be advertised usefully
		}
		out = append(out, Skill{
			Name:        name,
			Description: desc,
			Body:        body,
			Dir:         skillDir,
			Source:      source,
			Path:        path,
			Env:         frontmatterEnvVars(string(data)),
		})
	}
	return out
}

// parseFrontmatter splits an optional leading `---` YAML block from the body and
// reads single-line `name` and `description` keys.
func parseFrontmatter(s string) (name, description, body string) {
	body = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", "", body
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return "", "", body
	}
	front := s[4 : 4+end]
	body = strings.TrimLeft(s[4+end+4:], "\r\n")
	body = strings.TrimSpace(body)
	for _, line := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.Trim(strings.TrimSpace(v), `"'`)
		switch key {
		case "name":
			name = val
		case "description":
			description = val
		}
	}
	return name, description, body
}
