// Package subagents loads named agent personas from `.md` files. Each persona
// has a custom system prompt and an optional tool allowlist; they're invoked by
// the parent agent through the `task` tool to delegate focused subtasks (e.g.
// an "explore" persona that only has read tools).
//
// Personas are loaded from:
//   - ~/.config/moony/agents/<name>.md  (global)
//   - ./.moony/agents/<name>.md                    (per-project; overrides)
//
// They're also auto-discovered from neighboring agent tools so personas defined
// for Optimus or Claude Code show up here without copying files (lowest
// precedence — an sqe persona of the same name always wins). Set
// MN_IMPORT_EXTERNAL_AGENTS=0 to disable this:
//   - ~/.config/optimus/agents/<name>.md, ./.optimus/agents/<name>.md  (Optimus)
//   - ~/.claude/agents/<name>.md                                       (Claude Code)
//
// Imported externals inherit the parent's model (their `model:` is dropped,
// since an external model id needn't be valid for sqe's current route) and are
// read-only here (remove/edit them in their own tool; /agents disable still hides
// them).
//
// File format (YAML-ish frontmatter optional, body is the system prompt):
//
//	---
//	description: read-only explorer that surveys a codebase
//	model: anthropic.claude-opus-4-8   (optional; must be valid for the current route)
//	tools: read_file, list_dir, glob, grep
//	---
//
//	You are a code explorer...
package subagents

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// Persona is one named sub-agent.
type Persona struct {
	Name         string            // e.g. "explore"
	Description  string            // for the task tool's enum docs
	Model        string            // optional per-agent model id; empty = inherit the parent's
	Provider     string            // optional per-agent LLM provider override; empty = inherit the parent's
	Reasoning    string            // optional per-agent reasoning-effort override; empty = inherit the parent's
	Mode         string            // "primary" | "subagent" | "all" (default "all")
	SystemPrompt string            // body of the .md, used as the persona's system prompt
	Tools        []string          // optional allowlist; empty = all tools available
	Permission   map[string]string // optional per-tool allow/ask/deny (opencode-style)
	Source       string            // "builtin", "global", "project", or an import ("claude"/"optimus")
	Path         string
	Env          []string // credential env vars the agent declares it needs (frontmatter `env:`/`needs:`)
}

// SourceBuiltin tags the always-on read-only personas materialized in-memory
// from the catalog (BuiltinPersonas) — they have no on-disk Path.
const SourceBuiltin = "builtin"

// Builtin reports whether the persona is one of the always-on in-memory built-ins
// (no on-disk file). Disk/import tooling (plugin install, removal) keys off this
// so a built-in isn't mistaken for something the user installed.
func (p Persona) Builtin() bool { return p.Source == SourceBuiltin }

// PrimaryCapable reports whether the persona may be used as a primary agent
// profile (a build/plan-style profile you can switch the session to). Mode
// "subagent" is task-only; "primary"/"all"/unset (default "all") qualify.
func (p Persona) PrimaryCapable() bool {
	return p.Mode != "subagent"
}

// SubagentCapable reports whether the persona may be delegated to via the `task`
// tool. Mode "primary" is switch-only (a full session profile like build/plan,
// not a delegation target); "subagent"/"all"/unset (default "all") qualify.
func (p Persona) SubagentCapable() bool {
	return p.Mode != "primary"
}

// ToAgent converts a primary-capable persona into a config.Agent profile, or
// returns nil when the persona is subagent-only. The per-tool permission map is
// taken from the persona's `permission:` frontmatter; if none is given but the
// persona has a read-only tool allowlist, the mutating built-ins are denied so
// it behaves like a read-only profile.
func (p Persona) ToAgent() *config.Agent {
	if !p.PrimaryCapable() {
		return nil
	}
	perm := map[string]string{}
	for tool, rule := range p.Permission {
		switch rule {
		case config.PermAllow, config.PermAsk, config.PermDeny:
			perm[tool] = rule
		default:
			// Fail safe: an unrecognized rule (typo like "denied") denies the
			// tool rather than silently leaving it permissive.
			perm[tool] = config.PermDeny
		}
	}
	if len(perm) == 0 && p.ReadOnly() {
		for _, t := range []string{"write_file", "edit_file", "multi_edit", "apply_patch", "bash", "task", "delegate", "remember"} {
			perm[t] = config.PermDeny
		}
	}
	return &config.Agent{Name: p.Name, Permission: perm}
}

// DefaultAgent is one of the canonical built-in profiles, materialized as an
// editable `.md` file so users can inspect and customize "build"/"plan" like any
// other agent. The Body content mirrors config.BuildAgent / config.PlanAgent.
type DefaultAgent struct {
	Name    string // file base name (without .md)
	Content string // full `.md` contents (frontmatter + system prompt body)
}

// DefaultAgentFiles returns the canonical "build" and "plan" agent definitions,
// in stable order. Their `permission:` frontmatter exactly matches the Go
// built-ins (config.BuildAgent / config.PlanAgent), so scaffolding them on disk
// reproduces the shipped behavior while letting users tweak it.
func DefaultAgentFiles() []DefaultAgent {
	return []DefaultAgent{
		{
			Name: "build",
			Content: `---
description: Full-access agent that can read, edit, run commands, and delegate. The default profile.
mode: primary
permission:
  write_file: allow
  edit_file: allow
  multi_edit: allow
  apply_patch: allow
  bash: allow
  task: allow
  delegate: allow
  remember: allow
---

You are in **build** mode — the default, full-access profile. You can read and
search the codebase, create and edit files, run shell commands, delegate to
sub-agents, and update project memory.

Plan briefly, then implement: make the change, run the relevant build/tests to
verify it, and report concisely. Prefer editing existing files over creating new
ones, and keep changes scoped to what the user asked for.
`,
		},
		{
			Name: "plan",
			Content: `---
description: Read-only planning agent that inspects the codebase and proposes a plan without changing anything.
mode: primary
permission:
  write_file: deny
  edit_file: deny
  multi_edit: deny
  apply_patch: deny
  task: deny
  delegate: deny
  remember: deny
  bash: ask
---

You are in **plan** mode — a read-only profile. Investigate the codebase using
read-only tools and produce a clear, step-by-step implementation plan.

You must NOT modify files, run mutating commands, spawn sub-agents, or write
memory. Shell commands require explicit approval and should be limited to
inspection (reading files, listing directories, searching). When the plan is
ready, present it and ask the user to switch to build mode (Tab, or /build) to
execute it.
`,
		},
	}
}

// WriteDefaultAgents materializes the canonical build/plan agent files into dir
// (creating dir if needed). Existing files are left untouched unless force is
// set. It returns the paths actually written, in order.
func WriteDefaultAgents(dir string, force bool) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, d := range DefaultAgentFiles() {
		path := filepath.Join(dir, d.Name+".md")
		if !force {
			if _, err := os.Stat(path); err == nil {
				continue // don't clobber a user's existing definition
			}
		}
		if err := os.WriteFile(path, []byte(d.Content), 0o644); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

// ReadOnly reports whether the persona is restricted to inspection-only tools
// (no write/edit/bash/task) — a "surveyor" whose output feeds the parent agent.
// The agent uses it to route such personas to the cheaper fast model. A persona
// with no tool allowlist (full access) is NOT read-only.
func (p Persona) ReadOnly() bool {
	if len(p.Tools) == 0 {
		return false
	}
	for _, t := range p.Tools {
		if !readOnlyToolSet[t] {
			return false
		}
	}
	return true
}

// Load reads personas from the global config dir and the project `.moony/agents`
// dir, plus (unless MN_IMPORT_EXTERNAL_AGENTS=0) any agents defined for Optimus
// or Claude Code, plus the always-on read-only built-ins (BuiltinPersonas).
// Precedence, low → high: built-ins, external imports, sqe global, sqe project —
// so an sqe persona always overrides an imported or built-in one of the same name.
func Load(workingDir string) []Persona {
	merged := map[string]Persona{}

	// Built-in read-only surveyors (explore/code-reviewer/planner) at the lowest
	// precedence, so delegation — and the default-on auto-delegate — work out of
	// the box. Any same-named persona below (external import, sqe global, sqe
	// project) overrides the built-in.
	for _, p := range BuiltinPersonas() {
		merged[p.Name] = p
	}

	// External agents (Optimus / Claude) come next, just above the built-ins.
	// Their per-agent model is dropped so they inherit the parent's model (an
	// external model id needn't be valid for sqe's route).
	if importExternalAgents() {
		for _, s := range externalAgentSources(workingDir) {
			for _, p := range readDir(s.dir, s.source) {
				p.Model = ""
				merged[p.Name] = p
			}
		}
	}

	if base, err := config.GlobalConfigDir(); err == nil {
		for _, p := range readDir(filepath.Join(base, "agents"), "global") {
			merged[p.Name] = p
		}
	}
	for _, p := range readDir(config.ProjectPath(workingDir, "agents"), "project") {
		merged[p.Name] = p
	}

	out := make([]Persona, 0, len(merged))
	for _, p := range merged {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// agentSource is one directory of agent .md files plus the Source tag to stamp
// on the personas read from it.
type agentSource struct{ dir, source string }

// externalAgentSources lists the neighboring agent-tool directories to import
// from, in increasing precedence. Optimus uses ~/.config (XDG-style) even on
// macOS; both the plural ("agents", current) and singular ("agent") dir names
// are scanned for forward/backward compatibility.
func externalAgentSources(workingDir string) []agentSource {
	var out []agentSource
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, agentSource{filepath.Join(home, ".claude", "agents"), "claude"})
		out = append(out, agentSource{filepath.Join(home, ".config", "optimus", "agents"), "optimus"})
		out = append(out, agentSource{filepath.Join(home, ".config", "optimus", "agent"), "optimus"})
	}
	out = append(out, agentSource{filepath.Join(workingDir, ".optimus", "agents"), "optimus"})
	out = append(out, agentSource{filepath.Join(workingDir, ".optimus", "agent"), "optimus"})
	return out
}

// importExternalAgents reports whether to auto-discover Optimus/Claude agents
// (on by default; set MN_IMPORT_EXTERNAL_AGENTS=0/false/no/off to disable).
func importExternalAgents() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_IMPORT_EXTERNAL_AGENTS"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// readDir lists every *.md persona under dir.
func readDir(dir, source string) []Persona {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Persona
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		desc, model, provider, reasoning, mode, tools, permission, body := parseFile(string(data))
		out = append(out, Persona{
			Name:         strings.TrimSuffix(e.Name(), ".md"),
			Description:  desc,
			Model:        model,
			Provider:     provider,
			Reasoning:    reasoning,
			Mode:         mode,
			Tools:        tools,
			Permission:   permission,
			SystemPrompt: body,
			Source:       source,
			Path:         full,
			Env:          frontmatterEnvVars(string(data)),
		})
	}
	return out
}

// parseFile splits an optional `---` frontmatter block from the body and
// extracts `description`, `model`, `provider`, `reasoning`, `mode`, `tools` (a
// comma-separated allowlist), and a nested `permission:` map (opencode-style:
// `tool: allow|ask|deny` lines indented under the key).
func parseFile(s string) (description, model, provider, reasoning, mode string, tools []string, permission map[string]string, body string) {
	body = s
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", "", "", "", "", nil, nil, strings.TrimSpace(body)
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return "", "", "", "", "", nil, nil, strings.TrimSpace(body)
	}
	front := s[4 : 4+end]
	body = strings.TrimLeft(s[4+end+4:], "\r\n")
	lines := strings.Split(front, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.Trim(strings.TrimSpace(v), `"'`)
		switch key {
		case "description":
			description = val
		case "model":
			model = val
		case "provider":
			provider = val
		case "reasoning":
			reasoning = strings.ToLower(val)
		case "mode":
			mode = strings.ToLower(val)
		case "tools":
			for _, t := range strings.Split(val, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tools = append(tools, t)
				}
			}
		case "permission":
			// A nested block: consume the following indented "tool: rule" lines.
			if val != "" {
				break
			}
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) == "" || (next[0] != ' ' && next[0] != '\t') {
					break // dedent → end of the permission block
				}
				i++
				pk, pv, ok := strings.Cut(strings.TrimSpace(next), ":")
				if !ok {
					continue
				}
				rule := strings.ToLower(strings.Trim(strings.TrimSpace(pv), `"'`))
				if permission == nil {
					permission = map[string]string{}
				}
				permission[strings.TrimSpace(pk)] = rule
			}
		}
	}
	return description, model, provider, reasoning, mode, tools, permission, strings.TrimSpace(body)
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
// sub-agents (sorted, deduped).
func EnvVars(workingDir string) []string {
	set := map[string]bool{}
	for _, p := range Load(workingDir) {
		for _, e := range p.Env {
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
