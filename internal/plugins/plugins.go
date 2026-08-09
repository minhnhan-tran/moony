package plugins

// Install/remove fan-out: a plugin bundle expands into the existing per-type
// install primitives (skills.Install, subagents.Install, mcp.AddServer) plus
// command-file writes, and reverses them on remove. Installed-state is derived
// from the live skills/agents/mcp config + the commands dir, so it stays in
// sync with manual edits and with the per-type marketplaces.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
)

// Status reports a plugin's installed state: how many of its components are
// present and whether ALL of them are (so the UI shows "installed" vs Install).
type Status struct {
	CatalogEntry
	Components int  // total parts in the bundle
	Present    int  // how many are currently installed
	Installed  bool // Present == Components (and Components > 0)
}

// StatusAll computes the install state of every catalog plugin against the
// skills/agents/mcp config and commands dir for workingDir.
func StatusAll(workingDir string) []Status {
	haveSkill := map[string]bool{}
	for _, s := range skills.Load(workingDir) {
		haveSkill[s.Name] = true
	}
	haveAgent := map[string]bool{}
	for _, p := range subagents.Load(workingDir) {
		if p.Builtin() {
			continue // always-on built-ins aren't "installed by the user"
		}
		haveAgent[p.Name] = true
	}
	haveMCP := map[string]bool{}
	if cfgs, err := mcp.LoadConfigs(workingDir); err == nil {
		for _, c := range cfgs {
			haveMCP[c.Name] = true
		}
	}

	out := make([]Status, 0, len(catalog))
	for _, e := range catalog {
		present := 0
		for _, n := range e.Skills {
			if haveSkill[n] {
				present++
			}
		}
		for _, n := range e.Agents {
			if haveAgent[n] {
				present++
			}
		}
		for _, n := range e.MCP {
			if haveMCP[n] {
				present++
			}
		}
		for _, c := range e.Commands {
			if commandExists(c.Name, workingDir) {
				present++
			}
		}
		total := e.componentCount()
		out = append(out, Status{CatalogEntry: e, Components: total, Present: present, Installed: total > 0 && present == total})
	}
	return out
}

// Install adds every component of a plugin that isn't already present: catalog
// skills/agents, catalog MCP servers (with ${root} substituted to workingDir),
// and the plugin's slash commands (written as commands/<name>.md). global
// selects the global config/skills dirs; otherwise the project dirs under
// workingDir. Already-present components are skipped, so it's idempotent.
// Returns a short summary of what it added.
func Install(name string, global bool, workingDir string) (string, error) {
	e, ok := CatalogEntryByName(name)
	if !ok {
		return "", fmt.Errorf("unknown plugin %q", name)
	}

	// Snapshot what's already on disk so we skip (rather than error on) parts the
	// user already has from a previous install or the per-type marketplaces.
	haveSkill := map[string]bool{}
	for _, s := range skills.Load(workingDir) {
		haveSkill[s.Name] = true
	}
	haveAgent := map[string]bool{}
	for _, p := range subagents.Load(workingDir) {
		if p.Builtin() {
			continue // a built-in isn't on disk; the plugin still installs its own copy
		}
		haveAgent[p.Name] = true
	}
	haveMCP := map[string]bool{}
	if cfgs, err := mcp.LoadConfigs(workingDir); err == nil {
		for _, c := range cfgs {
			haveMCP[c.Name] = true
		}
	}

	var added []string
	for _, s := range e.Skills {
		if haveSkill[s] {
			continue
		}
		if _, err := skills.Install(s, global, workingDir); err != nil {
			return "", fmt.Errorf("install skill %q: %w", s, err)
		}
		added = append(added, "skill "+s)
	}
	for _, a := range e.Agents {
		if haveAgent[a] {
			continue
		}
		if _, err := subagents.Install(a, global, workingDir); err != nil {
			return "", fmt.Errorf("install agent %q: %w", a, err)
		}
		added = append(added, "agent "+a)
	}
	for _, m := range e.MCP {
		if haveMCP[m] {
			continue
		}
		entry, ok := mcp.CatalogEntryByName(m)
		if !ok {
			continue
		}
		args := make([]string, len(entry.Args))
		for i, arg := range entry.Args {
			args[i] = strings.ReplaceAll(arg, "${root}", workingDir)
		}
		if _, err := mcp.AddServer(entry.Name, entry.Command, args, entry.URL, entry.Headers, global, workingDir); err != nil {
			return "", fmt.Errorf("install mcp %q: %w", m, err)
		}
		added = append(added, "mcp "+m)
	}
	for _, c := range e.Commands {
		wrote, err := writeCommand(c, global, workingDir)
		if err != nil {
			return "", fmt.Errorf("install command /%s: %w", c.Name, err)
		}
		if wrote {
			added = append(added, "command /"+c.Name)
		}
	}
	if len(added) == 0 {
		return "already installed", nil
	}
	return strings.Join(added, ", "), nil
}

// Remove deletes a plugin's components: its skills, agents, slash commands, and
// MCP servers (when sqe-owned). Parts that are shared/owned elsewhere (imported
// agents, Claude-discovered MCP) or already gone are skipped. Best-effort:
// returns a summary of what it removed. Note this removes the bundled skills/
// agents/commands even if you also use them standalone.
func Remove(name, workingDir string) (string, error) {
	e, ok := CatalogEntryByName(name)
	if !ok {
		return "", fmt.Errorf("unknown plugin %q", name)
	}
	var removed []string
	for _, s := range e.Skills {
		if _, err := skills.Remove(s, workingDir); err == nil {
			removed = append(removed, "skill "+s)
		}
	}
	for _, a := range e.Agents {
		if _, err := subagents.Remove(a, workingDir); err == nil {
			removed = append(removed, "agent "+a)
		}
	}
	for _, m := range e.MCP {
		if _, err := mcp.RemoveServer(m, workingDir); err == nil {
			removed = append(removed, "mcp "+m)
		}
	}
	for _, c := range e.Commands {
		if removeCommand(c.Name, workingDir) {
			removed = append(removed, "command /"+c.Name)
		}
	}
	if len(removed) == 0 {
		return "nothing to remove", nil
	}
	return strings.Join(removed, ", "), nil
}

// --- slash-command files -----------------------------------------------------

func commandsBase(global bool, workingDir string) (string, error) {
	if global {
		return config.GlobalConfigPath("commands")
	}
	return config.ProjectPath(workingDir, "commands"), nil
}

// commandExists reports whether a command file is present in either scope.
func commandExists(name, workingDir string) bool {
	if p, err := config.GlobalConfigPath("commands", name+".md"); err == nil {
		if fileExists(p) {
			return true
		}
	}
	return fileExists(config.ProjectPath(workingDir, "commands", name+".md"))
}

// writeCommand writes a plugin command's .md file. It won't clobber an existing
// command of the same name (so a user's own /name is preserved); returns whether
// it wrote a new file.
func writeCommand(c Command, global bool, workingDir string) (bool, error) {
	if c.Name == "" || c.Name != filepath.Base(c.Name) || c.Name == "." || c.Name == ".." {
		return false, fmt.Errorf("invalid command name %q", c.Name)
	}
	if commandExists(c.Name, workingDir) {
		return false, nil
	}
	base, err := commandsBase(global, workingDir)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return false, err
	}
	body := fmt.Sprintf("---\ndescription: %s\n---\n\n%s\n", c.Description, c.Body)
	return true, os.WriteFile(filepath.Join(base, c.Name+".md"), []byte(body), 0o644)
}

// removeCommand deletes a command's .md from both scopes; returns whether any
// file was removed.
func removeCommand(name, workingDir string) bool {
	removed := false
	paths := []string{config.ProjectPath(workingDir, "commands", name+".md")}
	if p, err := config.GlobalConfigPath("commands", name+".md"); err == nil {
		paths = append(paths, p)
	}
	for _, p := range paths {
		if err := os.Remove(p); err == nil {
			removed = true
		}
	}
	return removed
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
