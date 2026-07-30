package agent

import (
	"os"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// codebaseMapMaxPromptBytes is the DEFAULT cap on how much of the codebase map
// is injected into the system prompt. Override with MN_CODEBASE_MAP_BYTES.
//
// The prompt now carries the compact digest (a directory index) rather than the
// full per-file listing, so this budget comfortably fits pinned + narrative +
// digest; the full detail lives on disk and is fetched with the codebase_map
// tool. Raising this trades prompt tokens for resident detail.
const codebaseMapMaxPromptBytes = 32000

// codebaseMapMinStructureBytes is the share of the budget the generated
// structure/digest is guaranteed, so a very long narrative can't squeeze the
// directory index out of the prompt entirely.
const codebaseMapMinStructureBytes = 4000

// codebaseMapEnabled reports whether the CODEBASE_MAP.md machinery (auto-refresh
// of the structural section + prompt injection) is active for this agent. It is
// on when cfg.CodebaseMap is set, or when a CODEBASE_MAP.md already exists at the
// project root — unless MN_CODEBASE_MAP is explicitly set to a falsey value.
func codebaseMapEnabled(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_CODEBASE_MAP"))) {
	case "0", "off", "false", "no":
		return false
	case "1", "on", "true", "yes":
		return true
	}
	if cfg.CodebaseMap {
		return true
	}
	if cfg.WorkingDir != "" {
		if _, ok := config.ReadCodebaseMap(cfg.WorkingDir); ok {
			return true
		}
	}
	return false
}

// codebaseMapPromptSection renders the codebase map for injection into the system
// prompt, with a heading that tells the model this is the maintained map and to
// consult it before exploring.
//
// Tiering: the prompt gets the PINNED notes, the hand-maintained NARRATIVE, and a
// COMPACT directory digest — not the full per-file symbol listing, which stays in
// CODEBASE_MAP.md and is fetched on demand via the codebase_map tool. That keeps
// prompt cost flat while making arbitrarily detailed structure reachable.
//
// Budgeting priority when it still doesn't fit: pinned (human-authored, never
// trimmed) > narrative > digest (mechanically reproducible, so it absorbs
// trimming down to a floor). Returns "" when there's no map.
func codebaseMapPromptSection(workingDir string, digest string, budget int) string {
	if workingDir == "" {
		return ""
	}
	if budget <= 0 {
		budget = codebaseMapMaxPromptBytes
	}
	pinned := strings.TrimSpace(config.CodebaseMapPinned(workingDir))
	narrative := strings.TrimSpace(config.CodebaseMapNarrative(workingDir))
	structure := strings.TrimSpace(digest)
	if structure == "" {
		// No digest available (symbol map not built yet) — fall back to the
		// on-disk structure section so the prompt still has an index.
		structure = strings.TrimSpace(config.CodebaseMapStructure(workingDir))
	}
	if pinned == "" && narrative == "" && structure == "" {
		return ""
	}

	if len(pinned)+len(narrative)+len(structure) > budget {
		pinned = clipBytes(pinned, budget)
		rest := budget - len(pinned)

		structBudget := rest - len(narrative)
		if structBudget < codebaseMapMinStructureBytes {
			structBudget = codebaseMapMinStructureBytes
		}
		// Never let the structure floor push the total past the budget:
		// pinned already has first claim on it.
		if structBudget > rest {
			structBudget = rest
		}
		structure = clipBytes(structure, structBudget)
		narrative = clipBytes(narrative, rest-len(structure))
	}

	var c strings.Builder
	if pinned != "" {
		c.WriteString("## Pinned notes (hand-authored; authoritative)\n\n")
		c.WriteString(pinned)
		c.WriteString("\n\n")
	}
	if structure != "" {
		c.WriteString("## Directory index\n\n")
		c.WriteString(structure)
		c.WriteString("\n\n")
	}
	if narrative != "" {
		c.WriteString("## Features → files\n\n")
		c.WriteString(narrative)
	}

	var b strings.Builder
	b.WriteString("Codebase map (from CODEBASE_MAP.md, the project's maintained map):\n")
	b.WriteString("Consult this map FIRST to locate features and the files that implement them, and to answer questions about tool names, HTTP routes, SSE events, env vars, on-disk paths and project invariants without opening files. ")
	b.WriteString("Prefer it over launching the explore sub-agent or broad grepping; only fall back to exploration for areas the map doesn't cover or when it looks stale. ")
	b.WriteString("For a directory's full per-file symbol listing (kinds, declaration lines, reference counts), call the `codebase_map` tool with that directory instead of reading files. ")
	b.WriteString("The directory index and per-file detail are auto-generated; the narrative is maintained via @map.\n\n")
	b.WriteString(strings.TrimSpace(c.String()))
	b.WriteString("\n\n")
	return b.String()
}

// clipBytes truncates s to at most n bytes on a line boundary where possible,
// appending a marker so the model knows content was elided.
func clipBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, '\n'); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, "\n") + "\n…(truncated)…"
}
