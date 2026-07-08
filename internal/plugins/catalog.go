package plugins

// The plugins "marketplace": a curated catalog of bundles installed in one
// click. A plugin is NOT new content — it references existing catalog items
// (skills, subagents, MCP servers) by name and ships a few slash commands, so
// installing it fans out into skills.Install / subagents.Install /
// mcp.AddServer plus command-file writes (see plugins.go). This mirrors Claude
// Code's "a plugin bundles skills/agents/commands/mcp" idea while keeping one
// source of truth per component.

// Command is an inline slash command a plugin ships. It's written as a
// commands/<Name>.md file on install; the leading slash is implied.
type Command struct {
	Name        string // e.g. "review" -> /review
	Description string // one-line palette description
	Body        string // prompt template; $ARGUMENTS is substituted at run time
}

// CatalogEntry is one installable plugin bundle. The Skills/Agents/MCP fields
// reference names in the respective marketplaces (internal/skills,
// internal/subagents, internal/mcp); unknown names are skipped at install.
type CatalogEntry struct {
	Name     string    // unique id and command/menu key, e.g. "review-suite"
	Title    string    // display label
	Desc     string    // one-line description
	Skills   []string  // skill catalog names to install
	Agents   []string  // subagent catalog names to install
	MCP      []string  // mcp catalog names to install
	Commands []Command // slash commands to write
}

// catalog is the built-in marketplace of plugin bundles. Each references real
// entries from the skills/subagents/mcp catalogs so a single install lights up
// a coherent workflow.
var catalog = []CatalogEntry{
	{
		Name: "review-suite", Title: "Code Review Suite",
		Desc:   "Code-review checklist & PR-writeup skills, a code-reviewer subagent, and a /review command.",
		Skills: []string{"code-review", "pr-description"},
		Agents: []string{"code-reviewer"},
		Commands: []Command{{Name: "review", Description: "Review the current changes",
			Body: "Review the staged git changes for correctness, edge cases, security, and clarity. Group findings by severity and cite file:line. $ARGUMENTS"}},
	},
	{
		Name: "commit-suite", Title: "Commit & Changelog Suite",
		Desc:   "Conventional-commit and changelog skills plus a /commit command for polished commit messages.",
		Skills: []string{"conventional-commits", "changelog-entry"},
		Commands: []Command{{Name: "commit", Description: "Draft a Conventional Commits message",
			Body: "Write a Conventional Commits message for the staged changes: a `type(scope): summary` subject, then a concise body explaining the why. $ARGUMENTS"}},
	},
	{
		Name: "debug-suite", Title: "Debugging Suite",
		Desc:   "Root-cause debugging skill, explore + planner subagents, the sequential-thinking MCP server, and a /debug command.",
		Skills: []string{"root-cause-debugging"},
		Agents: []string{"explore", "planner"},
		MCP:    []string{"sequential-thinking"},
		Commands: []Command{{Name: "debug", Description: "Root-cause a bug step by step",
			Body: "Help me root-cause this issue methodically: form hypotheses, gather evidence, and isolate the cause before proposing a fix. $ARGUMENTS"}},
	},
}

// Catalog returns the built-in marketplace of plugin bundles.
func Catalog() []CatalogEntry { return catalog }

// CatalogEntryByName returns the catalog plugin with the given name.
func CatalogEntryByName(name string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.Name == name {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// componentCount is the number of installable parts in a bundle.
func (e CatalogEntry) componentCount() int {
	return len(e.Skills) + len(e.Agents) + len(e.MCP) + len(e.Commands)
}
