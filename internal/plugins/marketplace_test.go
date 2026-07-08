package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/mcp"
)

// writeFixtureMarketplace creates a minimal Claude-style marketplace repo at
// dest: a marketplace.json listing one "hello" plugin that bundles a command,
// an agent, a skill, and an MCP server.
func writeFixtureMarketplace(t *testing.T, dest string) {
	t.Helper()
	write := func(rel, content string) {
		p := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude-plugin/marketplace.json", `{"name":"demo","plugins":[{"name":"hello","source":"./hello","description":"Hi there"}]}`)
	write("hello/.claude-plugin/plugin.json", `{"name":"hello","version":"0.1.0"}`)
	write("hello/commands/greet.md", "---\ndescription: greet someone\n---\nSay hi to $ARGUMENTS")
	write("hello/agents/helper.md", "---\ndescription: a helper persona\n---\nYou help.")
	write("hello/skills/howto/SKILL.md", "---\nname: howto\ndescription: how to do the thing\n---\n# Howto")
	write("hello/.mcp.json", `{"mcpServers":{"demo-mcp":{"command":"echo","args":["${root}"]}}}`)
}

func TestMarketplaceAddInstallRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	wd := t.TempDir()

	orig := cloneRepo
	cloneRepo = func(_ context.Context, _, dest string) error { writeFixtureMarketplace(t, dest); return nil }
	t.Cleanup(func() { cloneRepo = orig })

	// Add: name is derived from the URL, manifest is validated, registry updated.
	m, err := AddMarketplace(context.Background(), "", "https://example.com/demo.git")
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if m.Name != "demo" {
		t.Fatalf("derived name = %q, want demo", m.Name)
	}
	if len(Marketplaces()) != 1 {
		t.Fatalf("registry has %d marketplaces, want 1", len(Marketplaces()))
	}

	// Listed, not installed, with all components scanned.
	plugs := MarketplacePlugins(wd)
	if len(plugs) != 1 || plugs[0].Name != "hello" || plugs[0].Installed {
		t.Fatalf("listing = %+v", plugs)
	}
	p := plugs[0]
	if len(p.Commands) != 1 || len(p.Agents) != 1 || len(p.Skills) != 1 || len(p.MCP) != 1 {
		t.Fatalf("component scan wrong: %+v", p)
	}

	// Install fans the parts into the standard dirs + registers the MCP server.
	if _, err := InstallFromMarketplace("hello", "demo", wd); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !MarketplacePlugins(wd)[0].Installed {
		t.Error("plugin should report installed")
	}
	cfg, _ := config.GlobalConfigDir()
	mustExist(t, filepath.Join(cfg, "commands", "greet.md"))
	mustExist(t, filepath.Join(cfg, "agents", "helper.md"))
	mustExist(t, filepath.Join(home, ".claude", "skills", "howto", "SKILL.md"))
	if cfgs, _ := mcp.LoadConfigs(wd); !hasServer(cfgs, "demo-mcp") {
		t.Error("mcp server demo-mcp not registered")
	}

	// Remove reverses exactly what install wrote.
	if _, err := RemoveFromMarketplace("hello", "demo", wd); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if MarketplacePlugins(wd)[0].Installed {
		t.Error("plugin should report uninstalled")
	}
	mustNotExist(t, filepath.Join(cfg, "moony", "commands", "greet.md"))
	mustNotExist(t, filepath.Join(home, ".claude", "skills", "howto"))
	if cfgs, _ := mcp.LoadConfigs(wd); hasServer(cfgs, "demo-mcp") {
		t.Error("mcp server should be unregistered after remove")
	}

	// RemoveMarketplace clears the registry + clone.
	if err := RemoveMarketplace("demo"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if len(Marketplaces()) != 0 {
		t.Error("registry should be empty after RemoveMarketplace")
	}
}

func TestSplitRef(t *testing.T) {
	if n, m := SplitRef("foo@bar"); n != "foo" || m != "bar" {
		t.Errorf("SplitRef(foo@bar) = (%q,%q)", n, m)
	}
	if n, m := SplitRef("builtin"); n != "builtin" || m != "" {
		t.Errorf("SplitRef(builtin) = (%q,%q)", n, m)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s to be gone", path)
	}
}

func hasServer(cfgs []mcp.Config, name string) bool {
	for _, c := range cfgs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// TestEnsureDefaultMarketplaces verifies the built-in defaults are cloned and
// registered (idempotently), so their plugins show up without a manual add.
func TestEnsureDefaultMarketplaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	orig := cloneRepo
	cloneRepo = func(_ context.Context, _, dest string) error { writeFixtureMarketplace(t, dest); return nil }
	t.Cleanup(func() { cloneRepo = orig })

	EnsureDefaultMarketplaces(context.Background())

	reg := Marketplaces()
	for _, d := range DefaultMarketplaces() {
		if _, ok := marketplaceByName(d.Name); !ok {
			t.Errorf("default marketplace %q (%s) was not registered", d.Name, d.URL)
		}
	}
	if len(reg) != len(DefaultMarketplaces()) {
		t.Errorf("registered %d marketplaces, want %d defaults", len(reg), len(DefaultMarketplaces()))
	}

	// Idempotent: a second run doesn't duplicate.
	EnsureDefaultMarketplaces(context.Background())
	if got := len(Marketplaces()); got != len(DefaultMarketplaces()) {
		t.Errorf("after re-run, %d marketplaces, want %d (idempotent)", got, len(DefaultMarketplaces()))
	}

	// A best-effort clone failure is skipped, not fatal.
	cloneRepo = func(_ context.Context, _, _ string) error { return context.Canceled }
	_ = RemoveMarketplace(DefaultMarketplaces()[0].Name)
	EnsureDefaultMarketplaces(context.Background()) // must not panic; failed default just stays absent
}
