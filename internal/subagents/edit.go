package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// Scaffold creates a new persona `.md` from a template and returns its path.
// global selects ~/.config/moony/agents; otherwise the project's
// ./.sqe/agents under workingDir. It errors if the persona already exists.
func Scaffold(name string, global bool, workingDir string) (string, error) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid agent name %q", name)
	}
	base, err := agentsBase(global, workingDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, name+".md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("agent %q already exists at %s", name, path)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(agentTemplate, name)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Create writes a new persona `.md` from caller-supplied content (the Agent
// Builder's "New agent" form), as opposed to Scaffold (a fixed TODO template) or
// Install (a catalog entry). description and the system-prompt body are required;
// model and the tools allowlist are optional. global selects
// ~/.config/moony/agents; otherwise the project's ./.sqe/agents under
// workingDir. It errors on an invalid name, an empty description/prompt, or if
// the persona already exists. Returns the `.md` path.
func Create(name, description, model string, tools []string, systemPrompt string, global bool, workingDir string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid agent name %q", name)
	}
	description = strings.TrimSpace(description)
	systemPrompt = strings.TrimSpace(systemPrompt)
	if description == "" {
		return "", fmt.Errorf("a description is required")
	}
	if systemPrompt == "" {
		return "", fmt.Errorf("a system prompt is required")
	}
	base, err := agentsBase(global, workingDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, name+".md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("agent %q already exists at %s", name, path)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(renderPersona(description, model, tools, systemPrompt)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// renderPersona builds a persona `.md` (frontmatter + body) that parseFile reads
// back: a single-line description, an optional model:, an optional comma-joined
// tools: allowlist (omitted entirely → all tools), then the system prompt body.
func renderPersona(description, model string, tools []string, systemPrompt string) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("description: " + oneLine(description) + "\n")
	if m := strings.TrimSpace(model); m != "" {
		b.WriteString("model: " + m + "\n")
	}
	var clean []string
	for _, t := range tools {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) > 0 {
		b.WriteString("tools: " + strings.Join(clean, ", ") + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(systemPrompt))
	b.WriteString("\n")
	return b.String()
}

// oneLine flattens a value to a single frontmatter line (newlines → spaces) so a
// pasted multi-line description can't break the YAML block.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// Install writes a catalog persona (see catalog.go) into the agents dir — the
// global ~/.config/moony/agents, or the project's ./.sqe/agents when
// global is false. Returns the `.md` path. Errors if the persona already exists.
func Install(name string, global bool, workingDir string) (string, error) {
	e, ok := CatalogEntryByName(name)
	if !ok {
		return "", fmt.Errorf("unknown catalog agent %q", name)
	}
	base, err := agentsBase(global, workingDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, e.Name+".md")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("agent %q already exists at %s", e.Name, path)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(e.Markdown()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Remove deletes a discovered persona's `.md` file (global or project) and
// returns the path removed.
func Remove(name, workingDir string) (string, error) {
	for _, p := range Load(workingDir) {
		if p.Name != name {
			continue
		}
		// Imported externals (Optimus/Claude) are read-only here — deleting their
		// file would yank it out from under the other tool. Hide with /agents
		// disable instead.
		if p.Source != "global" && p.Source != "project" {
			return "", fmt.Errorf("agent %q is imported from %s and is read-only here — use /agents disable %s to hide it (or remove %s in that tool)", name, p.Source, name, p.Path)
		}
		if err := os.Remove(p.Path); err != nil {
			return "", err
		}
		return p.Path, nil
	}
	return "", fmt.Errorf("unknown agent %q", name)
}

func agentsBase(global bool, workingDir string) (string, error) {
	if global {
		return config.GlobalConfigPath("agents")
	}
	return filepath.Join(workingDir, ".sqe", "agents"), nil
}

// GlobalAgentsDir returns the global agents directory
// (~/.config/moony/agents).
func GlobalAgentsDir() (string, error) {
	return config.GlobalConfigPath("agents")
}

// SeedGlobalDefaultAgents materializes the canonical build/plan agent files into
// the global config agents dir (~/.config/moony/agents) when they are
// missing, so "build" and "plan" are first-class, editable entries in /agents
// out of the box. Idempotent and best-effort — existing files are never
// clobbered. Returns the names actually seeded.
func SeedGlobalDefaultAgents() ([]string, error) {
	base, err := GlobalAgentsDir()
	if err != nil {
		return nil, err
	}
	written, err := WriteDefaultAgents(base, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(written))
	for _, p := range written {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".md"))
	}
	return names, nil
}

// sqeNames returns the set of persona names backed by an sqe-owned file (the
// global ~/.config/moony/agents dir or the project ./.sqe/agents), i.e.
// the ones that already win over any imported external of the same name.
func sqeNames(workingDir string) map[string]bool {
	out := map[string]bool{}
	if base, err := config.GlobalConfigDir(); err == nil {
		for _, p := range readDir(filepath.Join(base, "agents"), "global") {
			out[p.Name] = true
		}
	}
	for _, p := range readDir(filepath.Join(workingDir, ".sqe", "agents"), "project") {
		out[p.Name] = true
	}
	return out
}

// MaterializeExternals copies each discovered external (Optimus/Claude) persona
// that has no sqe counterpart into the global sqe agents dir, turning it into a
// first-class, editable sqe persona (its model can then be pinned, and it can be
// removed). Idempotent: a name already backed by an sqe file is left untouched
// (sqe always wins). The external `model:` line is dropped on copy — an external
// model id needn't be valid for sqe's route — while description/tools/body are
// preserved, so the user can pick a valid model afterward. Returns the names
// copied. A no-op when external import is disabled (MN_IMPORT_EXTERNAL_AGENTS=0).
func MaterializeExternals(workingDir string) ([]string, error) {
	if !importExternalAgents() {
		return nil, nil
	}
	base, err := config.GlobalConfigPath("agents")
	if err != nil {
		return nil, err
	}
	existing := sqeNames(workingDir)

	// Resolve externals with the same precedence as Load (later sources win), so
	// when two coding agents define the same name we copy the one Load would use.
	ext := map[string]Persona{}
	for _, src := range externalAgentSources(workingDir) {
		for _, p := range readDir(src.dir, src.source) {
			ext[p.Name] = p
		}
	}

	var copied []string
	for name, p := range ext {
		if existing[name] {
			continue
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
			return copied, err
		}
		out := setFrontmatterKey(string(data), "model", "") // drop the external model id
		if err := os.WriteFile(filepath.Join(base, name+".md"), []byte(out), 0o644); err != nil {
			return copied, err
		}
		copied = append(copied, name)
	}
	return copied, nil
}

// SetModel pins (or clears) the per-agent model for a discovered persona by
// rewriting the `model:` key in its `.md` frontmatter. A non-empty model pins it
// (the sub-agent then runs on that model — it must be valid for the current
// route); an empty model clears the pin so the persona inherits the parent's
// model. Only project/global personas are editable — imported externals
// (Optimus/Claude) are read-only here. Returns the path written.
func SetModel(name, model, workingDir string) (string, error) {
	return setPersonaField(name, "model", model, workingDir)
}

// SetProvider sets (or, given "", clears) the persona's frontmatter `provider:`
// override — the LLM provider to run it under, independent of the model id.
// Empty means inherit the parent/session provider. Same file-resolution and
// read-only rules as SetModel.
func SetProvider(name, provider, workingDir string) (string, error) {
	return setPersonaField(name, "provider", provider, workingDir)
}

// SetReasoning sets (or, given "", clears) the persona's frontmatter
// `reasoning:` override — the reasoning-effort level to run it at. Empty
// means inherit the parent/session reasoning. Same file-resolution and
// read-only rules as SetModel.
func SetReasoning(name, reasoning, workingDir string) (string, error) {
	return setPersonaField(name, "reasoning", reasoning, workingDir)
}

// setPersonaField resolves name to its on-disk persona file (must be a
// global/project persona, not one merely discovered from an external tool)
// and rewrites its frontmatter key to value, trimmed. value must be a single
// line: an embedded newline could otherwise splice extra frontmatter keys (or
// close the `---` fence early and inject content into the persona's body) —
// an HTTP handler passing a client-supplied string through unchecked, as
// SetModel/SetProvider/SetReasoning all do, must not allow that.
func setPersonaField(name, key, value, workingDir string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.ContainsAny(value, "\n\r") {
		return "", fmt.Errorf("%s must be a single line", key)
	}
	for _, p := range Load(workingDir) {
		if p.Name != name {
			continue
		}
		if p.Source != "global" && p.Source != "project" {
			return "", fmt.Errorf("agent %q is imported from %s and is read-only here — set its %s in that tool instead", name, p.Source, key)
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return "", err
		}
		out := setFrontmatterKey(string(data), key, value)
		if err := os.WriteFile(p.Path, []byte(out), 0o644); err != nil {
			return "", err
		}
		return p.Path, nil
	}
	return "", fmt.Errorf("unknown agent %q", name)
}

// setFrontmatterKey returns content with its frontmatter `key:` entry set to
// value, replacing any existing active (non-commented) key: line. An empty
// value removes the active line (the persona inherits the parent's setting).
// When content has no frontmatter and a value is given, a minimal frontmatter
// block is prepended; an empty value on such content is a no-op. A new key:
// line is inserted just after description: (or at the top of the block) so the
// file stays tidy. Other lines — including a commented "# key:" template
// hint — are preserved verbatim. Used for model/provider/reasoning overrides.
func setFrontmatterKey(content, key, value string) string {
	lines := strings.Split(content, "\n")
	fence := func(s string) bool { return strings.TrimRight(s, "\r") == "---" }
	if len(lines) == 0 || !fence(lines[0]) {
		if value == "" {
			return content
		}
		return "---\n" + key + ": " + value + "\n---\n\n" + strings.TrimLeft(content, "\r\n")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if fence(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 { // malformed (no closing fence) — don't risk mangling the body
		if value == "" {
			return content
		}
		out := append([]string{lines[0], key + ": " + value}, lines[1:]...)
		return strings.Join(out, "\n")
	}

	var fm []string
	replaced := false
	descIdx := -1
	for _, ln := range lines[1:end] {
		trimmed := strings.TrimSpace(ln)
		fmKey, _, ok := strings.Cut(trimmed, ":")
		k := strings.ToLower(strings.TrimSpace(fmKey))
		if ok && k == key && !strings.HasPrefix(trimmed, "#") {
			if value == "" {
				continue // drop the active line
			}
			fm = append(fm, key+": "+value)
			replaced = true
			continue
		}
		if ok && k == "description" {
			descIdx = len(fm)
		}
		fm = append(fm, ln)
	}
	if value != "" && !replaced {
		insertAt := 0
		if descIdx >= 0 {
			insertAt = descIdx + 1
		}
		next := make([]string, 0, len(fm)+1)
		next = append(next, fm[:insertAt]...)
		next = append(next, key+": "+value)
		next = append(next, fm[insertAt:]...)
		fm = next
	}

	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[0])
	out = append(out, fm...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

const agentTemplate = `---
description: TODO one line on what this subagent does and when to delegate to it
# model: optional — pin a model for this subagent (must be valid for the current /api route); omit to inherit the parent's
tools: read_file, list_dir, glob, grep
---

You are the %s subagent. TODO: describe its role, constraints, and the output
it should return to the parent agent. Keep it focused — the parent delegates a
single subtask to it via the task tool.
`
