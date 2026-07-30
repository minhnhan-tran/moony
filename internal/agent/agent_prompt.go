// agent_prompt.go — system-prompt assembly and AGENTS.md loading.

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func buildSystemPrompt(cfg *config.Config, registry *tools.Registry) string {
	var b strings.Builder
	b.WriteString("You are moony, an autonomous software engineering assistant ")
	b.WriteString("running in a terminal. You help with coding tasks by reading, writing, ")
	b.WriteString("and editing files and running shell commands.\n\n")

	b.WriteString("Environment:\n")
	fmt.Fprintf(&b, "- Working directory: %s\n", cfg.WorkingDir)
	// Multi-root: when extra workspace roots are configured, tell the model so it
	// reaches them by absolute path (relative paths still resolve against the
	// working dir above).
	if roots := registry.Roots(); len(roots) > 1 {
		fmt.Fprintf(&b, "- Additional workspace roots (use absolute paths to read/write here): %s\n", strings.Join(roots[1:], ", "))
	}
	fmt.Fprintf(&b, "- Model (your identity): %s (served via Ollama)\n", cfg.Model)
	fmt.Fprintf(&b, "- Available tools: %s\n\n", strings.Join(displayToolNames(registry.Names()), ", "))

	// A static, ignore-aware structural map of the repo so the model orients
	// (where packages/entrypoints live) without globbing first. Recomputed on each
	// prompt rebuild; no model call. Disable with MN_REPO_MAP=0.
	if cfg.RepoMap {
		if m := registry.RepoMap(); m != "" {
			b.WriteString(m)
			b.WriteString("\n\n")
		}
	}

	// A model-distilled, cross-session profile of the codebase (learned from prior
	// sessions), framed as fallible orientation. Opt-in via MN_PROJECT_PROFILE;
	// see profile.go. Complements the mechanical RepoMap above and user-authored
	// AGENTS.md below.
	if cfg.ProjectProfile {
		if s := projectProfileSection(loadProjectProfile(cfg.WorkingDir)); s != "" {
			b.WriteString(s)
		}
	}

	// The maintained per-project codebase map (CODEBASE_MAP.md): a structural
	// section auto-refreshed on change plus an on-demand feature→files narrative.
	// Injecting it lets the model consult the map instead of re-exploring every
	// turn. Bounded so a huge map can't dominate the prompt.
	if codebaseMapEnabled(cfg) {
		digest := ""
		if registry != nil {
			digest = registry.CodebaseMapDigest()
		}
		if s := codebaseMapPromptSection(cfg.WorkingDir, digest, cfg.CodebaseMapPromptBytes); s != "" {
			b.WriteString(s)
		}
	}

	b.WriteString("Guidelines:\n")
	b.WriteString("- Be concise and direct. Prefer action over lengthy explanation.\n")
	if cfg.Terse {
		// Verbosity steering: output tokens are billed several times higher than
		// input, and much of a turn's output is ceremony (preambles, sign-offs,
		// restated context, re-printed unchanged code). Steer it down — but scope
		// it strictly to ceremony so brevity never cuts into the actual work.
		b.WriteString("- OUTPUT BUDGET: write tersely — output tokens cost several times more than input. Skip preambles, sign-offs, and conversational filler; don't restate the request or re-print unchanged code (show only the changed lines/hunks, never whole unchanged files); keep final summaries to a few lines. In code, comment the WHY rather than narrating each line, and don't add explanations the user didn't ask for. Brevity applies to ceremony ONLY: never skip a required tool call, edit, verification, or correctness detail to be short.\n")
	}
	b.WriteString("- Use tools to gather context before answering questions about the code.\n")
	b.WriteString("- Your own identity (which model/provider you are) is the \"Model (your identity)\" line in the Environment block above — answer questions about which model you are from THAT line directly; do not search the repo. Model names that appear in source, docs, config, or AGENTS.md are the project's data (defaults, examples, catalog entries), not necessarily you.\n")
	b.WriteString("- Gather context in as few rounds as possible: issue independent read_file/grep/glob/find_symbol calls TOGETHER in one message (they run in parallel) rather than one per round, and don't re-read files you've already seen.\n")
	b.WriteString("- To locate where a symbol is defined, use find_symbol (it returns declaration sites, not every usage); use glob/grep for plain-text matches and usages, before reading whole files.\n")
	if registry.Has("search_code") {
		// Steer conceptual "where/how is X done?" retrieval to search_code. Without
		// this the model defaults to the grep/find_symbol guidance above and never
		// picks search_code, leaving its ranked-chunk retrieval (and semantic
		// rerank) unused. Scoped to concept/keyword lookups so exact-string and
		// known-symbol cases still go to grep/find_symbol.
		b.WriteString("- For conceptual \"where/how is X implemented?\" lookups by keyword or intent (not an exact string or a known symbol name), prefer search_code: it returns the most relevant ranked code chunks in one call, so you read only the pertinent snippets instead of running a grep-then-read_file loop.\n")
	}
	if hasToolPrefix(registry, "remember") {
		b.WriteString("- When the user states a durable rule, preference, or convention — e.g. \"always …\", \"never …\", \"prefer …\", \"we use …\", or a correction to follow going forward — proactively call the remember tool to persist it, choosing category rule/preference/convention/note. Don't remember transient task state.\n")
	}
	if hasToolPrefix(registry, "mcp.codebase-memory.") {
		fmt.Fprintf(&b, "- OPTIONAL — codebase-memory knowledge graph: a codebase-memory MCP server is also available. Reach for it when a relationship/impact question spans MANY files and the built-in tools above would be slower — e.g. search_graph (definitions/classes/routes and their relationships), trace_path (callers/callees and data flow), get_architecture (high-level structure), detect_changes (impact of edits). Call each by the EXACT name shown in your tool list (don't reformat or guess). These tools take a \"project\" argument set to this repo's INDEXED NAME, not its path — here that is %q (the working-dir path with the leading slash dropped and every \"/\" and space turned into \"-\"). If a call returns \"project not found or not indexed\", use the server's list_projects tool and pass the exact name it lists. It's optional: prefer find_symbol/glob/grep for ordinary look-ups.\n", cbmProjectName(cfg.WorkingDir))
	}
	if registry.Has("task") {
		// Delegate-exploration steer: route broad, read-heavy investigation to the
		// read-only `explore` sub-agent so its file reads stay in ITS context window
		// instead of being re-sent on every round of this turn (the dominant cost of
		// long tool loops). Scoped to multi-file investigation so small look-ups
		// aren't needlessly delegated.
		b.WriteString("- For BROAD, read-heavy investigation — understanding how/where/why something works across SEVERAL files — prefer delegating to the read-only `explore` sub-agent via the task tool (run several in parallel for independent facets) and work from its summary, rather than reading many files into THIS conversation. The sub-agent reads in its own context and returns a focused answer with file:line refs, keeping this turn's context (and cost) small. Reserve direct read_file/grep for targeted look-ups and the files you're about to edit.\n")
	}
	if cfg.BatchContext {
		// Opt-in (MN_BATCH_CONTEXT): the user wants context gathered in very few
		// steps. Push hard on front-loading + parallelism so the turn doesn't
		// dribble out one read per round.
		b.WriteString("- CONTEXT BUDGET: gather everything you need within your FIRST 1–2 tool rounds. In your first response, fire off ALL the independent look-ups at once (multiple read_file/grep/glob/find_symbol calls in a single message — they run in parallel)")
		if registry.Has("task") {
			b.WriteString(", and/or delegate broad investigation to the `explore` sub-agent")
		}
		b.WriteString(". Only after that context is in hand should you start editing or answering. Do NOT read one file per round — batch aggressively and avoid redundant reads.\n")
	}
	if registry.Has("todo_write") {
		// Cadence steer: the task list is the user's LIVE progress view, so keep
		// it current step by step. Without this the model tends to batch its work
		// and only write the list at the start/end, so the panel jumps straight to
		// "all done" instead of reflecting progress.
		b.WriteString("- Keep the todo_write task list current in REAL TIME for multi-step work: enumerate the steps up front, then mark a task in_progress BEFORE you start it and completed the MOMENT it finishes — each as its own small todo_write call, NOT batched with other work or deferred to the end. This is the user's live progress view; updating it step by step (one task in_progress at a time) matters more than minimizing calls.\n")
	}
	if registry.Has("plan_write") {
		// Steer plans to the plan_write tool (rendered inline for the user to
		// review/approve), NOT to a written file — the model otherwise sometimes
		// dumps a plan into PLAN.md, which isn't reviewable/approvable and clutters
		// the repo.
		b.WriteString("- When you propose a plan — because the user asked, or before a non-trivial multi-step change — record it with the plan_write tool. That is how a plan is presented: it's shown to the user inline for review and approval. Do NOT write the plan to a file (PLAN.md or similar) unless the user explicitly asks for a file.\n")
	}
	b.WriteString("- Make focused edits with edit_file; use write_file for new files.\n")
	b.WriteString("- Mutating actions (write_file, edit_file, bash) may require user approval; if one is denied, stop and ask.\n")
	b.WriteString("- After changing code, build or test it when a build/test command exists.\n")
	b.WriteString("- Never invent file contents; read files before editing them.\n")
	b.WriteString("- Reference code locations as path:line when helpful.\n")
	b.WriteString("- Paths you pass to tools are relative to the working directory.\n")
	b.WriteString("- When the task is complete, give a short summary of what you did.\n")

	// Append global + project instruction files (AGENTS.md / CLAUDE.md). They
	// arrive least- to most-specific, so the closest file lands last and its
	// guidance reinforces or overrides broader guidance.
	b.WriteString(agentsMDPrompt(cfg.WorkingDir))
	return b.String()
}

// agentsMDPrompt renders the global + project instruction files (AGENTS.md /
// CLAUDE.md) as the trailing section of the system prompt, or "" when there are
// none. It is split out of buildSystemPrompt so the context breakdown can size
// the AGENTS.md contribution separately from the base prompt.
func agentsMDPrompt(workingDir string) string {
	extras := loadAgentsMD(workingDir)
	if len(extras) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, e := range extras {
		label := "Project instructions"
		if e.global {
			label = "Global instructions"
		}
		fmt.Fprintf(&b, "%s (from %s):\n%s\n\n", label, e.path, strings.TrimSpace(e.body))
	}
	return b.String()
}

// displayToolNames renders registry tool names as the model can actually call
// them: a name carrying characters the gateway forbids (notably the dots in
// "mcp.<server>.<tool>") is shown in its sanitized, dispatch-safe form
// (llm.CleanToolName), so the "Available tools" listing matches the tool schema
// instead of teaching the model the dotted internal name it can't invoke (which
// it would otherwise underscore by hand into a name that misses the schema). The
// suffix-less form shown here is exactly what addSuffixlessAliases accepts, so a
// call copied verbatim from the listing still dispatches. Wire-safe names (the
// built-ins) pass through unchanged.
func displayToolNames(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		if c := llm.CleanToolName(n); c != "" {
			out[i] = c
		} else {
			out[i] = n
		}
	}
	return out
}

// hasToolPrefix reports whether any tool registered in registry has a name
// beginning with prefix — e.g. "mcp.codebase-memory." to detect the
// codebase-memory MCP server's tools and tailor the system prompt to them.
func hasToolPrefix(registry *tools.Registry, prefix string) bool {
	for _, n := range registry.Names() {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// cbmProjectName derives the identifier the codebase-memory MCP server uses to
// key a project: the absolute working-dir path with the leading slash dropped
// and every "/" and space turned into "-" (e.g. "/Users/x/My Code/repo" ->
// "Users-x-My-Code-repo"). It is embedded in the system prompt so the model
// passes the right "project" argument to the mcp.codebase-memory.* tools rather
// than the raw path, which the server rejects as "project not found".
func cbmProjectName(workingDir string) string {
	p := strings.Trim(strings.TrimSpace(workingDir), "/")
	return strings.NewReplacer("/", "-", " ", "-").Replace(p)
}

// agentsMDFile is one ingested instructions file (AGENTS.md or CLAUDE.md).
type agentsMDFile struct {
	path   string
	body   string
	global bool // true for the user-level file under the config dir
}

// agentsMDMaxBytes caps how much of one instructions file we ingest, to keep
// the system prompt under any sensible context budget.
const agentsMDMaxBytes = 16 * 1024

// agentsFilenames are the per-directory instruction files we look for, in
// precedence order: AGENTS.md is canonical; CLAUDE.md is accepted as a fallback
// so repos using that convention work without renaming. Only the first found in
// a given directory is used.
var agentsFilenames = []string{"AGENTS.md", "CLAUDE.md"}

// readAgentsDoc returns the first instructions file present in dir (per
// agentsFilenames), its contents (capped), and true. ok is false when none
// exists or it can't be read.
func readAgentsDoc(dir string) (path, body string, ok bool) {
	for _, name := range agentsFilenames {
		p := filepath.Join(dir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if len(data) > agentsMDMaxBytes {
			data = append(data[:agentsMDMaxBytes], []byte("\n…(truncated)\n")...)
		}
		return p, string(data), true
	}
	return "", "", false
}

// loadAgentsMD gathers instruction files for the system prompt, least- to
// most-specific so the closest guidance lands last (and thus reinforces or
// overrides broader guidance):
//
//  1. a global, user-level file at <config>/moony/AGENTS.md
//     (CLAUDE.md fallback), shared across every project;
//  2. each AGENTS.md (or CLAUDE.md) from the repo root down to the working dir.
//
// The repo walk stops at a repo boundary (a directory containing .git) so we
// don't pull in unrelated files from a parent monorepo, and never crosses $HOME.
func loadAgentsMD(start string) []agentsMDFile {
	if start == "" {
		return nil
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil
	}
	home, _ := os.UserHomeDir()

	var out []agentsMDFile

	// 1. Global, user-level instructions first, so per-project files (appended
	// below, most specific last) take precedence.
	if base, err := config.GlobalConfigDir(); err == nil {
		if p, body, ok := readAgentsDoc(base); ok {
			out = append(out, agentsMDFile{path: tildePath(p, home), body: body, global: true})
		}
	}

	var dirs []string
	cur := abs
	for {
		dirs = append(dirs, cur)
		// Stop after the repo root.
		if fi, err := os.Stat(filepath.Join(cur, ".git")); err == nil && fi.IsDir() {
			break
		}
		// Stop at $HOME or filesystem root.
		if cur == home || cur == "/" || cur == filepath.Dir(cur) {
			break
		}
		cur = filepath.Dir(cur)
	}

	// 2. Repo files, root-first (so the most specific file is appended last).
	for i := len(dirs) - 1; i >= 0; i-- {
		if p, body, ok := readAgentsDoc(dirs[i]); ok {
			out = append(out, agentsMDFile{path: relTo(p, abs), body: body})
		}
	}
	return out
}

// tildePath abbreviates a path under the user's home directory to ~/… for
// readable display; other paths are returned unchanged.
func tildePath(p, home string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + p[len(home):]
	}
	return p
}

// relTo returns p relative to base when possible, else p unchanged.
func relTo(p, base string) string {
	if rel, err := filepath.Rel(base, p); err == nil {
		return rel
	}
	return p
}
