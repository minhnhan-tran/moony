package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// mcpToolPrefix is how MCP-bridged tools are named in the registry:
// "mcp.<server>.<tool>". Built-in tools never carry it, so it cleanly
// distinguishes gateable MCP tools from the always-on built-ins.
const mcpToolPrefix = "mcp."

// toolsetServer extracts the MCP server name from a tool name, or "" when the
// tool is not an MCP tool. "mcp.codebase-memory.search_graph" -> "codebase-memory".
func toolsetServer(name string) string {
	if !strings.HasPrefix(name, mcpToolPrefix) {
		return ""
	}
	rest := name[len(mcpToolPrefix):]
	if i := strings.LastIndex(rest, "."); i >= 0 {
		return rest[:i]
	}
	return rest
}

// builtinToolGroups maps a lazy-gateable built-in group to its member tool names.
// When lazyBuiltins is on these groups are gated like dormant MCP servers — their
// schemas stay out of the advertised tool list until the model enables the group
// — trimming cold-context tokens and tool-selection surface for capabilities many
// sessions never touch. Core tools (read/write/edit/search/bash/web_fetch/…) are
// intentionally NOT grouped: they must always be available.
var builtinToolGroups = map[string][]string{
	"git":     {"git_status", "git_diff", "git_log", "git_branch", "git_commit", "git_worktree"},
	"browser": {"browser"},
}

// builtinGroupBlurbs is a one-line "what it's for" note shown next to each
// built-in group in the dormant-toolset listing (enable_toolset's description),
// so the model can pick the right group to enable WITHOUT seeing the gated tools'
// own descriptions.
var builtinGroupBlurbs = map[string]string{
	"git":     "git status / diff / log / branch / commit / worktree",
	"browser": "drive a real browser for JS-heavy / interactive pages",
}

// builtinToolToGroup is the reverse index of builtinToolGroups (tool name → group).
var builtinToolToGroup = func() map[string]string {
	m := make(map[string]string)
	for g, names := range builtinToolGroups {
		for _, n := range names {
			m[n] = g
		}
	}
	return m
}()

// builtinGroupOf returns the lazy built-in group a tool belongs to, or "".
func builtinGroupOf(name string) string { return builtinToolToGroup[name] }

// toolsetOf returns the gateable toolset a tool belongs to — its MCP server (for
// mcp.* tools) or its built-in group — or "" when the tool is core/ungateable.
func toolsetOf(name string) string {
	if srv := toolsetServer(name); srv != "" {
		return srv
	}
	return builtinGroupOf(name)
}

// mcpServers groups the currently-registered MCP tools by server, preserving a
// stable order. The values are the short tool names (without the
// "mcp.<server>." prefix) for use in summaries. It works off tool NAMES (never
// the full schemas) so it is safe to call from a tool's Description() — building
// schemas calls Description(), so touching Schemas() here would recurse.
func (a *Agent) mcpServers(names []string) map[string][]string {
	servers := map[string][]string{}
	for _, name := range names {
		srv := toolsetServer(name)
		if srv == "" {
			continue
		}
		short := name[len(mcpToolPrefix)+len(srv)+1:]
		servers[srv] = append(servers[srv], short)
	}
	return servers
}

// builtinGroupsPresent returns the lazy built-in groups that actually have a
// member registered, mapping group → the member tool names present. Like
// mcpServers it works off names only (safe from Description()).
func (a *Agent) builtinGroupsPresent(names []string) map[string][]string {
	groups := map[string][]string{}
	for _, name := range names {
		if g := builtinGroupOf(name); g != "" {
			groups[g] = append(groups[g], name)
		}
	}
	return groups
}

// knownToolsets is the union of the registry's MCP servers and present built-in
// groups (toolset name → member tool names), for enable_toolset lookups and
// summaries. A built-in group wins a name collision with an MCP server (the
// built-in names are reserved).
func (a *Agent) knownToolsets(names []string) map[string][]string {
	m := a.mcpServers(names)
	for g, tools := range a.builtinGroupsPresent(names) {
		m[g] = tools
	}
	return m
}

// toolsetEnabled reports whether a toolset (MCP server or built-in group) has
// been activated this session.
func (a *Agent) toolsetEnabled(server string) bool {
	a.toolsetMu.Lock()
	defer a.toolsetMu.Unlock()
	return a.enabledToolsets[server]
}

// enableToolset marks a toolset (MCP server or built-in group) active so its
// tools become visible. Returns false if the name is unknown (no tools
// registered under it).
func (a *Agent) enableToolset(server string, known map[string][]string) bool {
	if _, ok := known[server]; !ok {
		return false
	}
	a.toolsetMu.Lock()
	defer a.toolsetMu.Unlock()
	a.enabledToolsets[server] = true
	return true
}

// dormantToolsets returns the sorted names of toolsets currently gated out: MCP
// servers (when lazyMCP) and built-in groups (when lazyBuiltins) not yet enabled.
func (a *Agent) dormantToolsets() []string {
	names := a.tools.Names()
	var out []string
	if a.lazyMCP {
		for srv := range a.mcpServers(names) {
			if !a.toolsetEnabled(srv) {
				out = append(out, srv)
			}
		}
	}
	if a.lazyBuiltins {
		for g := range a.builtinGroupsPresent(names) {
			if !a.toolsetEnabled(g) {
				out = append(out, g)
			}
		}
	}
	sort.Strings(out)
	return out
}

// gateToolsets drops the tools of dormant toolsets (MCP servers and built-in
// groups) from the advertised tool list, keeping core built-ins and
// already-enabled toolsets, and hides the enable_toolset meta-tool itself when
// there is nothing left to enable.
func (a *Agent) gateToolsets(schemas []llm.Tool) []llm.Tool {
	dormant := map[string]bool{}
	for _, s := range a.dormantToolsets() { // empty when both lazy flags are off
		dormant[s] = true
	}
	out := make([]llm.Tool, 0, len(schemas))
	for _, t := range schemas {
		if t.Name == enableToolsetName {
			// Only advertise enable_toolset while something is dormant (so it's
			// absent entirely when nothing is gated).
			if len(dormant) == 0 {
				continue
			}
			out = append(out, t)
			continue
		}
		if ts := toolsetOf(t.Name); ts != "" && dormant[ts] {
			continue
		}
		out = append(out, t)
	}
	return out
}

const enableToolsetName = "enable_toolset"

// enableToolsetTool lets the model activate a dormant toolset — an MCP server or
// a built-in group (git/browser) — so its tools (kept out of the
// request to save tokens) become available. Its description is built dynamically
// from the registry, so the model always sees the current set of dormant toolsets
// and their tools — this is what keeps the gated capability discoverable without
// paying for every toolset's full schema up front.
type enableToolsetTool struct {
	a *Agent
}

func (t *enableToolsetTool) Name() string { return enableToolsetName }

func (t *enableToolsetTool) Description() string {
	dormant := t.a.dormantToolsets()
	if len(dormant) == 0 {
		return "Activate a dormant toolset. (No dormant toolsets right now.)"
	}
	known := t.a.knownToolsets(t.a.tools.Names())
	var b strings.Builder
	b.WriteString("Activate a dormant toolset, making its tools callable for the rest of the session. ")
	b.WriteString("These toolsets are available but their schemas are kept out of the tool list to save tokens; ")
	b.WriteString("call this with the toolset name ONLY when the task needs it. Available dormant toolsets:\n")
	for _, name := range dormant {
		tools := append([]string(nil), known[name]...)
		sort.Strings(tools)
		kind := "MCP"
		if _, ok := builtinToolGroups[name]; ok {
			kind = "built-in"
		}
		sample := tools
		const maxSample = 6
		more := ""
		if len(sample) > maxSample {
			more = fmt.Sprintf(", … (+%d more)", len(sample)-maxSample)
			sample = sample[:maxSample]
		}
		blurb := ""
		if bb := builtinGroupBlurbs[name]; bb != "" {
			blurb = " — " + bb
		}
		fmt.Fprintf(&b, "- %s (%s, %d tools): %s%s%s\n", name, kind, len(tools), strings.Join(sample, ", "), more, blurb)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (t *enableToolsetTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"server": {
				"type": "string",
				"description": "Name of the dormant toolset to activate — an MCP server or a built-in group like \"git\" (see this tool's description for the list)."
			}
		},
		"required": ["server"]
	}`)
}

func (t *enableToolsetTool) Mutates() bool { return false }

func (t *enableToolsetTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	in.Server = strings.TrimSpace(in.Server)
	if in.Server == "" {
		return "", fmt.Errorf("server is required")
	}
	known := t.a.knownToolsets(t.a.tools.Names())
	if !t.a.enableToolset(in.Server, known) {
		avail := make([]string, 0, len(known))
		for s := range known {
			avail = append(avail, s)
		}
		sort.Strings(avail)
		return "", fmt.Errorf("unknown toolset %q; available: %s", in.Server, strings.Join(avail, ", "))
	}
	// List the now-callable full tool names by filtering the registry by toolset —
	// works for both MCP tools (mcp.<server>.<tool>) and built-in group members.
	var full []string
	for _, n := range t.a.tools.Names() {
		if toolsetOf(n) == in.Server {
			full = append(full, n)
		}
	}
	sort.Strings(full)
	return fmt.Sprintf("Activated toolset %q. %d tools are now available: %s",
		in.Server, len(full), strings.Join(full, ", ")), nil
}
