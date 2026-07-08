package mcp

// The MCP "marketplace": a curated catalog of well-known MCP servers the web UI
// can install. Installing registers the server in mcp.json (see AddServer) and
// connects it. Most entries run via npx/uvx (fetched on first connect) or are
// remote URLs, so there's no separate binary install. The exception is
// "codebase-memory", which runs a prebuilt binary that must already be on PATH
// (install it yourself, e.g. from github.com/DeusData/codebase-memory-mcp).
//
// Args may contain the "${root}" placeholder; the install handler substitutes
// the session's working directory (e.g. for the filesystem/git servers).

// CatalogEntry is one installable MCP server.
type CatalogEntry struct {
	Name    string            // unique id and mcp.json key, e.g. "filesystem"
	Title   string            // display label
	Desc    string            // one-line description
	Command string            // stdio command (empty for a remote URL server)
	Args    []string          // command args; may contain "${root}"
	URL     string            // remote HTTP+SSE endpoint (empty for stdio)
	Headers map[string]string // HTTP headers for a remote server; values may use ${ENV_VAR}
	Needs   []string          // env vars the user must set for it to work (display hint)
}

// Catalog returns the built-in marketplace of MCP servers. The npm/PyPI package
// names are the official @modelcontextprotocol / mcp-server-* servers.
var catalog = []CatalogEntry{
	{Name: "filesystem", Title: "Filesystem", Desc: "Read/write files under a directory (scoped to the project root).",
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "${root}"}},
	{Name: "memory", Title: "Memory", Desc: "A persistent knowledge-graph memory the model can read and write.",
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-memory"}},
	{Name: "sequential-thinking", Title: "Sequential Thinking", Desc: "A scratchpad tool for structured step-by-step reasoning.",
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-sequential-thinking"}},
	{Name: "everything", Title: "Everything (demo)", Desc: "The reference server exercising every MCP feature — handy for testing.",
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-everything"}},
	{Name: "fetch", Title: "Fetch", Desc: "Fetch a URL and convert it to markdown for the model.",
		Command: "uvx", Args: []string{"mcp-server-fetch"}},
	{Name: "git", Title: "Git", Desc: "Read and operate on a git repository (the project root).",
		Command: "uvx", Args: []string{"mcp-server-git", "--repository", "${root}"}},
	{Name: "time", Title: "Time", Desc: "Current time and timezone conversions.",
		Command: "uvx", Args: []string{"mcp-server-time"}},
	{Name: "github", Title: "GitHub", Desc: "Query and manage GitHub repos, issues, and PRs.",
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}, Needs: []string{"GITHUB_PERSONAL_ACCESS_TOKEN"}},
	{Name: "codebase-memory", Title: "Codebase Memory", Desc: "Local code knowledge-graph: structural search, call/data-flow tracing, and architecture overview. Optional; requires the codebase-memory-mcp binary on PATH.",
		Command: "codebase-memory-mcp"},
}

// Catalog returns the built-in marketplace of MCP servers.
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
