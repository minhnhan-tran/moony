package mcp

import (
	"path/filepath"
	"testing"
)

func TestAddAndRemoveServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	wd := t.TempDir()

	if _, err := AddServer("fs", "npx", []string{"-y", "server-fs"}, "", nil, false, wd); err != nil {
		t.Fatal(err)
	}
	cfgs, err := LoadConfigs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if got := byName(cfgs)["fs"]; got.Command != "npx" || len(got.Args) != 2 {
		t.Errorf("added server not loaded correctly: %+v", got)
	}

	// Remote server with an auth header that references an env var, written to
	// the global file. The header value must be ${VAR}-expanded at load time.
	t.Setenv("MCP_TEST_TOKEN", "s3cret")
	if _, err := AddServer("remote", "", nil, "https://h/mcp", map[string]string{"Authorization": "Bearer ${MCP_TEST_TOKEN}"}, true, wd); err != nil {
		t.Fatal(err)
	}
	cfgs, _ = LoadConfigs(wd)
	remote := byName(cfgs)["remote"]
	if remote.URL != "https://h/mcp" {
		t.Error("remote server not added to the global config")
	}
	if !containsEnv(remote.Env, "Authorization=Bearer s3cret") {
		t.Errorf("auth header not written/expanded; Env=%v", remote.Env)
	}

	if _, err := RemoveServer("fs", wd); err != nil {
		t.Fatal(err)
	}
	cfgs, _ = LoadConfigs(wd)
	if _, ok := byName(cfgs)["fs"]; ok {
		t.Error("fs should be removed")
	}
	if _, err := RemoveServer("nonexistent", wd); err == nil {
		t.Error("removing a server that isn't in an sqe config should error")
	}
}

// containsEnv reports whether env contains the exact "KEY=value" entry.
func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func TestParseAddArgs(t *testing.T) {
	name, cmd, args, url, global, err := ParseAddArgs([]string{"fs", "--", "npx", "-y", "pkg"})
	if err != nil || name != "fs" || cmd != "npx" || len(args) != 2 || url != "" || global {
		t.Errorf("command form parsed wrong: name=%q cmd=%q args=%v url=%q global=%v err=%v", name, cmd, args, url, global, err)
	}
	name, cmd, _, url, global, err = ParseAddArgs([]string{"r", "https://h/mcp", "--global"})
	if err != nil || name != "r" || url != "https://h/mcp" || cmd != "" || !global {
		t.Errorf("url form parsed wrong: name=%q cmd=%q url=%q global=%v err=%v", name, cmd, url, global, err)
	}
	if _, _, _, _, _, err := ParseAddArgs([]string{"onlyname"}); err == nil {
		t.Error("missing command/url should error")
	}
}
