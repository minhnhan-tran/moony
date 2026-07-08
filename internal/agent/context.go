package agent

import (
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// ContextSegment is one labeled slice of the per-turn context window, with an
// estimated token cost. A category segment (e.g. "MCP tools") may carry Items —
// the individual contributors (each MCP server, agent, skill, …) — so the meter
// can itemize the breakdown the way Claude Code does.
type ContextSegment struct {
	Label  string           // human label, e.g. "System", "Memory files", "MCP tools"
	Tokens int              // estimated tokens (bytes/4, matching estimateTokens)
	Items  []ContextSegment `json:",omitempty"` // optional per-item detail
}

// ContextBreakdown is an estimate of how the current turn's input context is
// apportioned across its components, in the order they are sent to the model.
// All counts use the same cheap bytes/4 heuristic as estimateTokens, so they
// are approximate and meant for proportional visualization, not billing.
type ContextBreakdown struct {
	// Segments are, in send order: System, Memory files, Built-in tools, MCP
	// tools, Subagents, Skills, Checkpoint, History. Zero-token segments are
	// omitted so the meter only shows what's actually loaded. Category segments
	// carry per-item detail in Items.
	Segments []ContextSegment
	Total    int // sum of all segment tokens
}

// ContextBreakdown estimates the token cost of each part of the context the
// agent would send on the next turn, mirroring what appendTurnContext/visibleTools
// actually send so the proportions reflect reality:
//
//   - System         — the base built-in system prompt (instructions + guidance)
//   - Memory files    — appended AGENTS.md / CLAUDE.md files (itemized per file)
//   - Built-in tools  — native tool schemas (itemized per tool)
//   - MCP tools       — schemas from connected MCP servers (itemized per server)
//   - Subagents       — the `task` tool (itemized per persona)
//   - Skills          — the `skill` tool (itemized per skill)
//   - Checkpoint      — the per-turn preface (checkpoint + plan context)
//   - History         — the conversation messages
//
// Plugins are not a context segment of their own: a plugin installs skills,
// subagents, and/or MCP servers, whose token cost is already counted under
// those segments.
func (a *Agent) ContextBreakdown() ContextBreakdown {
	// Split the system prompt into its base and its trailing memory-files
	// section so they can be sized independently. agentsMDPrompt reproduces
	// exactly the suffix buildSystemPrompt appends, so subtracting it yields the
	// base.
	memoryFiles := agentsMDPrompt(a.cfg.WorkingDir)
	base := a.system
	if memoryFiles != "" && len(a.system) >= len(memoryFiles) &&
		a.system[len(a.system)-len(memoryFiles):] == memoryFiles {
		base = a.system[:len(a.system)-len(memoryFiles)]
	}

	segs := []ContextSegment{
		{Label: "System", Tokens: tokensOf(base)},
	}
	add := func(s ContextSegment) {
		if s.Tokens > 0 {
			segs = append(segs, s)
		}
	}

	add(a.memoryFilesSegment())
	builtin, mcp, subagents, skills := a.toolSegments()
	add(builtin)
	add(mcp)
	add(subagents)
	add(skills)
	add(ContextSegment{Label: "Checkpoint", Tokens: prefaceTokens(a)})
	add(ContextSegment{Label: "History", Tokens: estimateTokens(a.messages)})

	total := 0
	for _, s := range segs {
		total += s.Tokens
	}
	return ContextBreakdown{Segments: segs, Total: total}
}

// memoryFilesSegment sizes the appended AGENTS.md/CLAUDE.md files, itemized per
// file (path → tokens), mirroring agentsMDPrompt's per-file rendering so the
// item sizes sum to the segment total.
func (a *Agent) memoryFilesSegment() ContextSegment {
	files := loadAgentsMD(a.cfg.WorkingDir)
	seg := ContextSegment{Label: "Memory files"}
	for _, f := range files {
		// Match agentsMDPrompt: "<label> (from <path>):\n<trimmed body>\n\n".
		label := "Project instructions"
		if f.global {
			label = "Global instructions"
		}
		rendered := label + " (from " + f.path + "):\n" + strings.TrimSpace(f.body) + "\n\n"
		tok := len(rendered) / 4
		seg.Tokens += tok
		seg.Items = append(seg.Items, ContextSegment{Label: f.path, Tokens: tok})
	}
	return seg
}

// toolSegments categorizes the visible tool schemas into Built-in tools, MCP
// tools (grouped per server), Subagents (the task tool's personas), and Skills
// (the skill tool's skills), each itemized for the meter.
func (a *Agent) toolSegments() (builtin, mcp, subagents, skills ContextSegment) {
	builtin = ContextSegment{Label: "Built-in tools"}
	mcp = ContextSegment{Label: "MCP tools"}
	subagents = ContextSegment{Label: "Subagents"}
	skills = ContextSegment{Label: "Skills"}

	mcpByServer := map[string]int{}
	var mcpOrder []string
	for _, t := range a.visibleTools() {
		tok := toolSchemaTokens([]llm.Tool{t})
		switch {
		case strings.HasPrefix(t.Name, "mcp."):
			server := mcpServerName(t.Name)
			if _, seen := mcpByServer[server]; !seen {
				mcpOrder = append(mcpOrder, server)
			}
			mcpByServer[server] += tok
			mcp.Tokens += tok
		case t.Name == "task":
			subagents.Tokens += tok
			if tt, ok := a.tools.Get("task").(*taskTool); ok {
				subagents.Items = tt.contextItems()
			}
		case t.Name == "skill":
			skills.Tokens += tok
			if st, ok := a.tools.Get("skill").(*skillTool); ok {
				skills.Items = st.contextItems()
			}
		default:
			builtin.Tokens += tok
			builtin.Items = append(builtin.Items, ContextSegment{Label: t.Name, Tokens: tok})
		}
	}
	for _, server := range mcpOrder {
		mcp.Items = append(mcp.Items, ContextSegment{Label: server, Tokens: mcpByServer[server]})
	}
	// Show the heaviest contributors first within each category.
	sortItemsDesc(builtin.Items)
	sortItemsDesc(mcp.Items)
	sortItemsDesc(subagents.Items)
	sortItemsDesc(skills.Items)
	return builtin, mcp, subagents, skills
}

// mcpServerName extracts the server label from an MCP adapter tool name
// "mcp.<server>.<tool>" → "<server>" (or the whole name if malformed).
func mcpServerName(toolName string) string {
	rest := strings.TrimPrefix(toolName, "mcp.")
	if i := strings.LastIndex(rest, "."); i > 0 {
		return rest[:i]
	}
	return rest
}

// sortItemsDesc orders sub-items by token cost, heaviest first (ties broken by
// label for stable output).
func sortItemsDesc(items []ContextSegment) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Tokens != items[j].Tokens {
			return items[i].Tokens > items[j].Tokens
		}
		return items[i].Label < items[j].Label
	})
}

// tokensOf estimates tokens for a single string using the same bytes/4
// heuristic as estimateTokens, keeping all segment sizes on one scale.
func tokensOf(s string) int { return len(s) / 4 }

// toolSchemaTokens estimates the token cost of the given tool definitions sent
// each turn: each tool's name, description, and JSON input schema.
func toolSchemaTokens(ts []llm.Tool) int {
	chars := 0
	for _, t := range ts {
		chars += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	return chars / 4
}

// prefaceTokens estimates the per-turn session-context preface (checkpoint, plan
// mode, and plan artifact) that appendTurnContext carries in the system suffix.
// These are sent every turn but not stored in a.messages, so they count toward
// the live context. Grouped under the "Checkpoint" label for the meter.
func prefaceTokens(a *Agent) int {
	chars := len(a.checkpointContext()) + len(a.planModeContext()) + len(a.planContext())
	return chars / 4
}
