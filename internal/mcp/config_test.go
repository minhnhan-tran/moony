package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byName(cfgs []Config) map[string]Config {
	m := map[string]Config{}
	for _, c := range cfgs {
		m[c.Name] = c
	}
	return m
}

// isolateHome points HOME/XDG at temp dirs so the loader can't see the real
// machine's Claude or sqe configs.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func TestLoadConfigsDiscoversClaude(t *testing.T) {
	home := isolateHome(t)
	wd := t.TempDir()

	writeFile(t, filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
		`{"mcpServers":{"desk":{"command":"deskcmd"}}}`)
	writeFile(t, filepath.Join(home, ".claude.json"),
		`{"mcpServers":{"user":{"command":"usercmd"}},"projects":{"`+wd+`":{"mcpServers":{"proj":{"command":"projcmd","args":["x"]}}}}}`)
	writeFile(t, filepath.Join(wd, ".mcp.json"),
		`{"mcpServers":{"remote":{"url":"https://example/mcp","headers":{"Authorization":"tok"}}}}`)

	cfgs, err := LoadConfigs(wd)
	if err != nil {
		t.Fatal(err)
	}
	got := byName(cfgs)
	for _, n := range []string{"desk", "user", "proj", "remote"} {
		if _, ok := got[n]; !ok {
			t.Errorf("missing auto-discovered Claude server %q (have %d servers)", n, len(cfgs))
		}
	}
	if got["proj"].Command != "projcmd" || len(got["proj"].Args) != 1 {
		t.Errorf("project-scoped server = %+v, want command=projcmd args=[x]", got["proj"])
	}
	if got["remote"].URL != "https://example/mcp" {
		t.Errorf("remote url = %q", got["remote"].URL)
	}
	foundHdr := false
	for _, e := range got["remote"].Env {
		if e == "Authorization=tok" {
			foundHdr = true
		}
	}
	if !foundHdr {
		t.Errorf("remote headers were not mapped onto env: %v", got["remote"].Env)
	}
}

func TestSqeConfigOverridesClaude(t *testing.T) {
	isolateHome(t)
	wd := t.TempDir()
	home, _ := os.UserHomeDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"dup":{"command":"claude"}}}`)

	base, _ := config.GlobalConfigDir()
	writeFile(t, filepath.Join(base, "mcp.json"), `{"servers":{"dup":{"command":"sqe"}}}`)

	cfgs, err := LoadConfigs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if c := byName(cfgs)["dup"]; c.Command != "sqe" {
		t.Errorf("sqe config should win over Claude; got command=%q", c.Command)
	}
}

func TestClaudeDiscoveryOptOut(t *testing.T) {
	home := isolateHome(t)
	wd := t.TempDir()
	t.Setenv("MOONY_MCP_CLAUDE", "0")
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"user":{"command":"usercmd"}}}`)

	cfgs, err := LoadConfigs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 0 {
		t.Errorf("MOONY_MCP_CLAUDE=0 should discover nothing; got %d servers", len(cfgs))
	}
}

func TestMalformedClaudeConfigIsSkipped(t *testing.T) {
	home := isolateHome(t)
	wd := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{ this is not valid json `)

	// A broken Claude file must not fail the load (it's not ours to validate).
	if _, err := LoadConfigs(wd); err != nil {
		t.Errorf("malformed Claude config should be skipped, got error: %v", err)
	}
}
