package agent

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// The codebase-memory guideline should appear in the system prompt only when the
// codebase-memory MCP tools are actually registered.
func TestBuildSystemPromptCodebaseMemoryGuideline(t *testing.T) {
	// Isolate the global config dir so we don't ingest the developer's real
	// global AGENTS.md.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &config.Config{WorkingDir: t.TempDir(), Model: "test-model"}

	const marker = "codebase-memory knowledge graph"

	// Absent without the tools.
	base := tools.Default(cfg.WorkingDir, 30000)
	if got := buildSystemPrompt(cfg, base); strings.Contains(got, marker) {
		t.Error("guideline should be absent when codebase-memory tools aren't registered")
	}

	// Present once a codebase-memory MCP tool is registered.
	withCBM := tools.Default(cfg.WorkingDir, 30000)
	withCBM.Register(&recordingTool{name: "mcp.codebase-memory.search_graph"})
	got := buildSystemPrompt(cfg, withCBM)
	if !strings.Contains(got, marker) {
		t.Fatalf("guideline should appear when codebase-memory tools are registered; prompt:\n%s", got)
	}
	// #2: the guideline references the tools by their BARE names and tells the
	// model to call each by the exact name from its tool list — it must NOT
	// hardcode the internal dotted "mcp.codebase-memory.X" form, which doesn't
	// match the sanitized wire schema the model actually sees (that mismatch made
	// the model fabricate a name like "mcp_codebase-memory_search_graph").
	for _, want := range []string{"search_graph", "trace_path", "get_architecture", "detect_changes", "tool list"} {
		if !strings.Contains(got, want) {
			t.Errorf("guideline should mention %q by its bare name / tool-list framing; prompt:\n%s", want, got)
		}
	}
	if strings.Contains(got, "mcp.codebase-memory.search_graph") {
		t.Errorf("guideline must not hardcode the dotted MCP name (it mismatches the model's schema); prompt:\n%s", got)
	}
	// The "Available tools" listing must show the sanitized, dispatch-safe form
	// (dots → underscores) — the same name addSuffixlessAliases accepts — so a
	// call copied from the listing resolves instead of missing the wire schema.
	if !strings.Contains(got, "mcp_codebase-memory_search_graph") {
		t.Errorf("tool listing should show the dispatch-safe sanitized name; prompt:\n%s", got)
	}
	// codebase-memory is now OPTIONAL: built-in tools come first. The guideline
	// must frame the graph as optional and must NOT force it as the FIRST action.
	if !strings.Contains(got, "OPTIONAL") {
		t.Errorf("codebase-memory guideline should be framed as optional; prompt:\n%s", got)
	}
	if strings.Contains(got, "FIRST action MUST be the codebase-memory") {
		t.Errorf("guideline must not force codebase-memory first; prompt:\n%s", got)
	}
	// Built-in navigation guidance (find_symbol / glob / grep) must be present
	// even when codebase-memory is available, since built-in tools come first.
	if !strings.Contains(got, "find_symbol") {
		t.Errorf("built-in find_symbol guidance should appear even with codebase-memory present; prompt:\n%s", got)
	}
	// It must tell the model the exact "project" name to pass (the normalized
	// working-dir identifier), so it doesn't call the tools with the raw path.
	if want := cbmProjectName(cfg.WorkingDir); !strings.Contains(got, want) {
		t.Errorf("guideline should embed the indexed project name %q; prompt:\n%s", want, got)
	}
	if !strings.Contains(got, "list_projects") {
		t.Error("guideline should point at list_projects as the fallback when the project isn't found")
	}
}

// cbmProjectName mirrors the codebase-memory server's project keying: absolute
// path, leading slash dropped, "/" and spaces turned into "-".
func TestCBMProjectName(t *testing.T) {
	cases := map[string]string{
		"/Users/admin/Documents/Source Code/moony": "Users-admin-Documents-Source-Code-moony",
		"/home/user/repo":                          "home-user-repo",
		"/tmp/My Project/x":                        "tmp-My-Project-x",
		"relative/path":                            "relative-path",
		"/trailing/slash/":                         "trailing-slash",
	}
	for in, want := range cases {
		if got := cbmProjectName(in); got != want {
			t.Errorf("cbmProjectName(%q) = %q, want %q", in, got, want)
		}
	}
}

// An unrelated MCP server must not trigger the codebase-memory guideline.
func TestBuildSystemPromptNoGuidelineForOtherMCP(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := &config.Config{WorkingDir: t.TempDir(), Model: "test-model"}
	reg := tools.Default(cfg.WorkingDir, 30000)
	reg.Register(&recordingTool{name: "mcp.filesystem.read_file"})
	if got := buildSystemPrompt(cfg, reg); strings.Contains(got, "codebase-memory knowledge graph") {
		t.Error("guideline should not appear for a non-codebase-memory MCP server")
	}
}
