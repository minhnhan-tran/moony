package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// stubMCPTool is a minimal tools.Tool with an arbitrary (mcp.*) name.
type stubMCPTool struct{ name string }

func (s stubMCPTool) Name() string            { return s.name }
func (s stubMCPTool) Description() string     { return "stub " + s.name }
func (s stubMCPTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubMCPTool) Mutates() bool           { return false }
func (s stubMCPTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func hasVisible(a *Agent, name string) bool {
	for _, t := range a.visibleTools() {
		if t.Name == name {
			return true
		}
	}
	return false
}

func TestLazyMCPGating(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(stubMCPTool{name: "mcp.demo.do_thing"})
	reg.Register(stubMCPTool{name: "mcp.demo.other"})

	cfg := &config.Config{WorkingDir: dir, LazyMCPTools: true}
	a := New(cfg, nil, reg, nil, nil)

	// Dormant by default: MCP tools hidden, enable_toolset advertised.
	if hasVisible(a, "mcp.demo.do_thing") {
		t.Error("dormant MCP tool should not be advertised")
	}
	if !hasVisible(a, enableToolsetName) {
		t.Error("enable_toolset should be advertised while a server is dormant")
	}

	// Activate the server.
	out, err := (&enableToolsetTool{a: a}).Execute(context.Background(), json.RawMessage(`{"server":"demo"}`))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.Contains(out, "mcp.demo.do_thing") {
		t.Errorf("activation should list the now-available tools, got: %s", out)
	}

	// Now MCP tools are visible and enable_toolset hides itself (nothing dormant).
	if !hasVisible(a, "mcp.demo.do_thing") {
		t.Error("activated MCP tool should be advertised")
	}
	if hasVisible(a, enableToolsetName) {
		t.Error("enable_toolset should hide once no servers are dormant")
	}

	// Unknown server is rejected.
	if _, err := (&enableToolsetTool{a: a}).Execute(context.Background(), json.RawMessage(`{"server":"nope"}`)); err == nil {
		t.Error("activating an unknown toolset should error")
	}
}

func TestLazyMCPDisabledShowsAll(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(stubMCPTool{name: "mcp.demo.do_thing"})

	cfg := &config.Config{WorkingDir: dir, LazyMCPTools: false}
	a := New(cfg, nil, reg, nil, nil)

	if !hasVisible(a, "mcp.demo.do_thing") {
		t.Error("with lazyMCP off, MCP tools should always be advertised")
	}
	if hasVisible(a, enableToolsetName) {
		t.Error("with lazyMCP off, enable_toolset should not be advertised")
	}
}

func TestToolsetOf(t *testing.T) {
	cases := map[string]string{
		"mcp.demo.do_thing": "demo",        // MCP server
		"git_status":        "git",         // built-in group
		"git_commit":        "git",         //
		"browser":           "browser",     //
		"apple_fetch":       "apple-fetch", //
		"read_file":         "",            // core: ungateable
		"bash":              "",            //
		"web_fetch":         "",            // deliberately NOT grouped
	}
	for name, want := range cases {
		if got := toolsetOf(name); got != want {
			t.Errorf("toolsetOf(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestLazyBuiltinToolGating mirrors TestLazyMCPGating for the built-in tool
// groups: git/browser/apple-fetch are gated until enabled, while core tools are
// never gated. (stubMCPTool is reused as a generic named stub.)
func TestLazyBuiltinToolGating(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(stubMCPTool{name: "git_status"})
	reg.Register(stubMCPTool{name: "git_diff"})
	reg.Register(stubMCPTool{name: "read_file"}) // core: must never be gated

	cfg := &config.Config{WorkingDir: dir, LazyBuiltinTools: true}
	a := New(cfg, nil, reg, nil, nil)

	// Dormant by default: git group hidden, core tool visible, enable_toolset shown.
	if hasVisible(a, "git_status") || hasVisible(a, "git_diff") {
		t.Error("dormant built-in git group should not be advertised")
	}
	if !hasVisible(a, "read_file") {
		t.Error("core tools must never be gated")
	}
	if !hasVisible(a, enableToolsetName) {
		t.Error("enable_toolset should be advertised while the git group is dormant")
	}

	// Activate the git group.
	out, err := (&enableToolsetTool{a: a}).Execute(context.Background(), json.RawMessage(`{"server":"git"}`))
	if err != nil {
		t.Fatalf("enable git: %v", err)
	}
	if !strings.Contains(out, "git_status") || !strings.Contains(out, "git_diff") {
		t.Errorf("activation should list the now-available git tools, got: %s", out)
	}
	if !hasVisible(a, "git_status") {
		t.Error("activated git tool should be advertised")
	}
	if hasVisible(a, enableToolsetName) {
		t.Error("enable_toolset should hide once nothing is dormant")
	}
}

func TestLazyBuiltinDisabledShowsAll(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	reg.Register(stubMCPTool{name: "git_status"})

	cfg := &config.Config{WorkingDir: dir, LazyBuiltinTools: false}
	a := New(cfg, nil, reg, nil, nil)

	if !hasVisible(a, "git_status") {
		t.Error("with lazyBuiltins off, git tools should always be advertised")
	}
	if hasVisible(a, enableToolsetName) {
		t.Error("with nothing gated, enable_toolset should not be advertised")
	}
}
