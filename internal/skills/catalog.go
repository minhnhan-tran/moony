package skills

// The skills "marketplace": a small curated catalog of generally-useful Agent
// Skills the web UI can install. Installing writes the skill's SKILL.md into the
// skills dir (see Install in edit.go); from there it's discovered like any other
// skill and can be enabled/disabled. The bodies are intentionally concise
// starting points — edit them after installing to taste.

import (
	"embed"
	"sort"
	"strings"
)

// bundledFS holds the larger, content-rich starter skills as real SKILL.md
// files (code-fence- and backtick-heavy bodies are impractical as Go string
// literals). They're loaded into the catalog at init alongside the inline
// entries below.
//
//go:embed catalog/*.md
var bundledFS embed.FS

// bundledTitles maps a bundled skill's frontmatter name to its marketplace
// display title (the SKILL.md frontmatter carries only name + description).
var bundledTitles = map[string]string{
	"gh":        "GitHub Enterprise (gh)",
	"pr-review": "PR Review (GHE)",
}

// init appends the embedded SKILL.md starter skills to the inline catalog. The
// files are compiled into the binary, so we skip a malformed file rather than
// panic — one bad entry can't take down the whole marketplace.
func init() {
	entries, err := bundledFS.ReadDir("catalog")
	if err != nil {
		return
	}
	loaded := make([]CatalogEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := bundledFS.ReadFile("catalog/" + e.Name())
		if err != nil {
			continue
		}
		name, desc, body := parseFrontmatter(string(data))
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".md")
		}
		if desc == "" || body == "" {
			continue
		}
		title := bundledTitles[name]
		if title == "" {
			title = name
		}
		loaded = append(loaded, CatalogEntry{Name: name, Title: title, Description: desc, Body: body})
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Name < loaded[j].Name })
	catalog = append(catalog, loaded...)
}

// CatalogEntry is one installable skill in the marketplace.
type CatalogEntry struct {
	Name        string // dir + frontmatter name, e.g. "conventional-commits"
	Title       string // display label
	Description string // frontmatter description (the progressive-disclosure hook)
	Body        string // SKILL.md instructions (markdown after the frontmatter)
}

// Catalog returns the built-in marketplace of skills.
func Catalog() []CatalogEntry { return catalog }

// CatalogEntryByName returns the catalog entry with the given name.
func CatalogEntryByName(name string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.Name == name {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// SkillMarkdown renders an entry as a full SKILL.md (frontmatter + body).
func (e CatalogEntry) SkillMarkdown() string {
	return "---\nname: " + e.Name + "\ndescription: " + e.Description + "\n---\n\n" + strings.TrimSpace(e.Body) + "\n"
}

var catalog = []CatalogEntry{
	{
		Name: "conventional-commits", Title: "Conventional Commits",
		Description: "Write a clear, conventional git commit message. Use when committing changes.",
		Body: `# Conventional Commits

When writing a commit message:

- Subject: ` + "`<type>(<scope>): <summary>`" + ` — imperative mood, <=72 chars, no trailing period.
- Types: feat, fix, docs, style, refactor, perf, test, build, ci, chore.
- Leave a blank line, then a body that explains the **why** (motivation, context),
  not just the what — the diff already shows the what.
- Reference issues in a footer (e.g. ` + "`Fixes: #123`" + `).
- One logical change per commit; don't bundle unrelated edits.`,
	},
	{
		Name: "pr-description", Title: "Pull Request Description",
		Description: "Draft a clear pull-request description. Use when opening or summarizing a PR.",
		Body: `# Pull Request Description

Produce a concise PR description with these sections:

- **Summary** — 1-3 bullets on what changed and why.
- **Changes** — the notable edits, grouped by area.
- **Testing** — how it was verified (commands run, cases covered).
- **Risk / rollout** — anything reviewers should watch; migration or config notes.

Keep it skimmable. Lead with the why. Omit empty sections.`,
	},
	{
		Name: "code-review", Title: "Code Review Checklist",
		Description: "Review a code change systematically. Use when asked to review or critique code.",
		Body: `# Code Review

Review for, roughly in priority order:

1. **Correctness** — does it do what it claims? Edge cases, error paths, nil/empty.
2. **Tests** — meaningful coverage of the new behavior and failure modes.
3. **Security** — input validation, injection, secrets, authz; no secrets in logs.
4. **Clarity** — names, structure, comments that explain *why*; dead code removed.
5. **Consistency** — matches surrounding conventions and patterns.
6. **Performance** — only where it matters; avoid premature optimization.

Give specific, actionable feedback with file:line references. Separate
must-fix issues from nits.`,
	},
	{
		Name: "root-cause-debugging", Title: "Root-Cause Debugging",
		Description: "Systematically find the root cause of a bug. Use when investigating a failure.",
		Body: `# Root-Cause Debugging

1. **Reproduce** — find the smallest reliable repro; note exact inputs/env.
2. **Observe** — read the actual error/stack; add logging or use a debugger to
   confirm the failing state, don't guess.
3. **Localize** — bisect (commits, inputs, or code paths) to narrow where the
   invariant first breaks.
4. **Explain** — state the root cause in one sentence before fixing.
5. **Fix + guard** — make the minimal fix and add a test that fails without it.
6. **Verify** — re-run the repro and the broader suite.

Resist fixing symptoms; fix the cause.`,
	},
	{
		Name: "changelog-entry", Title: "Changelog Entry",
		Description: "Write a Keep a Changelog entry for a change. Use when updating CHANGELOG.md.",
		Body: `# Changelog Entry

Follow Keep a Changelog:

- Group under the right heading: **Added**, **Changed**, **Deprecated**,
  **Removed**, **Fixed**, **Security**.
- Write for users, not implementers — describe the observable change and why it
  matters, in present tense.
- One entry per change; link issues/PRs where useful.
- Keep the newest version at the top with its release date.`,
	},
}
