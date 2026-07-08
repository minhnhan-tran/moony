package subagents

// The agents "marketplace": a small curated catalog of generally-useful subagent
// personas the web UI can install. Installing writes the persona's `.md` into the
// agents dir (see Install in edit.go); from there it's discovered like any other
// persona and can be enabled/disabled and invoked via the `task` tool / @mention.

import "strings"

// CatalogEntry is one installable subagent persona.
type CatalogEntry struct {
	Name        string   // dir-less file base, e.g. "explore"
	Title       string   // display label
	Description string   // frontmatter description (the task-tool hook)
	Tools       []string // optional tool allowlist (empty = all tools)
	Body        string   // the system prompt (markdown after the frontmatter)
}

// Catalog returns the built-in marketplace of subagent personas.
func Catalog() []CatalogEntry { return catalog }

// readOnly reports whether the entry is restricted to inspection-only tools
// (mirrors Persona.ReadOnly). Used to decide which catalog entries are safe to
// expose as always-on built-in delegation targets.
func (e CatalogEntry) readOnly() bool {
	if len(e.Tools) == 0 {
		return false
	}
	for _, t := range e.Tools {
		if !readOnlyToolSet[t] {
			return false
		}
	}
	return true
}

// BuiltinPersonas returns the always-available read-only surveyor personas
// (explore/code-reviewer/planner) materialized from the catalog, so the `task`
// tool can delegate exploration/review/planning out of the box — no install
// step. This is what makes the default-on auto-delegate (config.AutoDelegate +
// the explore directive) actually fire: without an `explore` target the `task`
// tool isn't even registered. They are:
//   - Mode "subagent": delegation-only, not selectable as a primary session
//     profile (that's what build/plan are for).
//   - Source "builtin": no on-disk Path, so Remove/SetModel refuse them (install
//     the same-named catalog entry to get an editable copy, which overrides this
//     one in Load); /agents disable still hides them, like any persona.
//
// Only read-only entries are exposed — a writing persona (test-author) stays
// opt-in via the marketplace. Seeded at the LOWEST precedence in Load, so a
// user's own .md (or an Optimus/Claude import) of the same name always wins.
func BuiltinPersonas() []Persona {
	var out []Persona
	for _, e := range catalog {
		if !e.readOnly() {
			continue
		}
		out = append(out, Persona{
			Name:         e.Name,
			Description:  e.Description,
			Mode:         "subagent",
			Tools:        e.Tools,
			SystemPrompt: strings.TrimSpace(e.Body),
			Source:       SourceBuiltin,
		})
	}
	return out
}

// CatalogEntryByName returns the catalog entry with the given name.
func CatalogEntryByName(name string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.Name == name {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// Markdown renders an entry as a full persona `.md` (frontmatter + body).
func (e CatalogEntry) Markdown() string {
	var b strings.Builder
	b.WriteString("---\ndescription: ")
	b.WriteString(e.Description)
	b.WriteString("\n")
	if len(e.Tools) > 0 {
		b.WriteString("tools: ")
		b.WriteString(strings.Join(e.Tools, ", "))
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(e.Body))
	b.WriteString("\n")
	return b.String()
}

// readOnlyTools is the inspection-only allowlist shared by the read-only personas.
var readOnlyTools = []string{"read_file", "list_dir", "glob", "grep", "find_symbol", "git_status", "git_diff", "git_log"}

// readOnlyToolSet is readOnlyTools as a lookup set, for Persona.ReadOnly.
var readOnlyToolSet = func() map[string]bool {
	m := make(map[string]bool, len(readOnlyTools))
	for _, t := range readOnlyTools {
		m[t] = true
	}
	return m
}()

var catalog = []CatalogEntry{
	{
		Name: "explore", Title: "Explorer",
		Description: "Read-only codebase explorer. Delegate to map unfamiliar code, find where something lives, or answer 'how does X work' without making edits.",
		Tools:       readOnlyTools,
		Body: `You are the explore subagent: a fast, read-only code surveyor.

- You have read/search tools only — never propose or make edits.
- Given a question, locate the relevant files and trace the flow, then return a
  concise answer with concrete file:line references.
- Prefer breadth first (where things live, how they connect) over deep detail.
- End with a short summary the parent agent can act on.`,
	},
	{
		Name: "code-reviewer", Title: "Code Reviewer",
		Description: "Read-only reviewer. Delegate to critique a change for correctness, tests, security, and clarity.",
		Tools:       readOnlyTools,
		Body: `You are the code-reviewer subagent. Review the change (or the area you're
pointed at) and report issues, roughly in priority order:

1. Correctness — edge cases, error paths, nil/empty handling.
2. Tests — meaningful coverage of new behavior and failure modes.
3. Security — input validation, injection, secrets, authorization.
4. Clarity & consistency — names, structure, matches surrounding conventions.

Give specific, actionable feedback with file:line references. Separate must-fix
issues from nits. Don't make edits — report back to the parent.`,
	},
	{
		Name: "planner", Title: "Planner",
		Description: "Read-only planner. Delegate to turn a goal into a concrete, ordered implementation plan before any code is written.",
		Tools:       readOnlyTools,
		Body: `You are the planner subagent. Investigate the codebase (read-only) and produce
a concrete, ordered plan to accomplish the given goal:

- List the files/functions to change and what each change does.
- Call out unknowns, risks, and decisions the parent should confirm.
- Note how to verify the result (tests/commands).

Keep it actionable and minimal. Do not make edits — return the plan.`,
	},
	{
		Name: "test-author", Title: "Test Author",
		Description: "Writes focused tests for code. Delegate to add unit/integration tests covering new behavior and edge cases.",
		Tools:       []string{"read_file", "list_dir", "glob", "grep", "find_symbol", "write_file", "edit_file", "multi_edit"},
		Body: `You are the test-author subagent. Add tests for the code you're pointed at:

- Read the target and existing tests to match the project's testing style/framework.
- Cover the happy path, edge cases, and failure modes; keep tests deterministic
  and hermetic (no network/ports/real clocks where avoidable).
- Make the smallest set of edits needed; don't refactor unrelated code.

Report what you added and how to run it.`,
	},
}
