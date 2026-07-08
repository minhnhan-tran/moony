package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// isolateConfig points HOME / XDG_CONFIG_HOME at fresh temp dirs so tests never
// pick up the developer's real global AGENTS.md, and returns the moony
// global config dir (created) where a global instructions file would live.
func isolateConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir, err := config.GlobalConfigDir()
	if err != nil {
		t.Fatalf("GlobalConfigDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadAgentsMDIncludesGlobal(t *testing.T) {
	cfgDir := isolateConfig(t)
	if err := os.WriteFile(filepath.Join(cfgDir, "AGENTS.md"), []byte("GLOBAL_AGENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := fakeRepoTree(t) // has root + sub AGENTS.md

	files := loadAgentsMD(filepath.Join(root, "sub"))
	if len(files) != 3 {
		t.Fatalf("expected global + root + sub = 3 files, got %d: %+v", len(files), files)
	}
	// Global comes first (least specific), flagged as global.
	if !files[0].global || !strings.Contains(files[0].body, "GLOBAL_AGENTS") {
		t.Errorf("first entry should be the global file, got %+v", files[0])
	}
	if files[1].global || !strings.Contains(files[1].body, "ROOT_AGENTS") {
		t.Errorf("second entry should be the repo root file, got %+v", files[1])
	}
	if !strings.Contains(files[2].body, "SUB_AGENTS") {
		t.Errorf("third entry should be the sub file, got %+v", files[2])
	}
	// The global path is shown tilde-abbreviated, not as an absolute home path.
	if strings.HasPrefix(files[0].path, "/") {
		t.Errorf("global path should be tilde-abbreviated, got %q", files[0].path)
	}
}

func TestLoadAgentsMDClaudeFallback(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Repo root has only CLAUDE.md — it should be picked up as a fallback.
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("CLAUDE_ROOT"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := loadAgentsMD(root)
	if len(files) != 1 || !strings.Contains(files[0].body, "CLAUDE_ROOT") {
		t.Fatalf("CLAUDE.md should be ingested as a fallback; got %+v", files)
	}
}

func TestLoadAgentsMDPrefersAgentsOverClaude(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// When both exist in a directory, AGENTS.md wins.
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("AGENTS_WINS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("CLAUDE_LOSES"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := loadAgentsMD(root)
	if len(files) != 1 {
		t.Fatalf("expected exactly one file when both exist, got %d: %+v", len(files), files)
	}
	if !strings.Contains(files[0].body, "AGENTS_WINS") || strings.Contains(files[0].body, "CLAUDE_LOSES") {
		t.Errorf("AGENTS.md should take precedence over CLAUDE.md, got %q", files[0].body)
	}
}

func TestBuildSystemPromptLabelsGlobalAndProject(t *testing.T) {
	cfgDir := isolateConfig(t)
	if err := os.WriteFile(filepath.Join(cfgDir, "AGENTS.md"), []byte("GLOBAL_RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := fakeRepoTree(t)

	cfg := &config.Config{Model: "anthropic.claude-sonnet-4-6", WorkingDir: filepath.Join(root, "sub")}
	prompt := buildSystemPrompt(cfg, tools.Default(cfg.WorkingDir, 30000))

	for _, want := range []string{"Global instructions", "GLOBAL_RULES", "Project instructions", "ROOT_AGENTS", "SUB_AGENTS"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q; got:\n%s", want, prompt)
		}
	}
	// Global guidance must appear before project guidance (least specific first).
	if strings.Index(prompt, "GLOBAL_RULES") > strings.Index(prompt, "ROOT_AGENTS") {
		t.Error("global instructions should precede project instructions")
	}
}

func TestTildePath(t *testing.T) {
	home := "/Users/dev"
	cases := map[string]string{
		"/Users/dev/.config/x":  "~/.config/x",
		"/Users/dev":            "~",
		"/etc/elsewhere":        "/etc/elsewhere",
		"/Users/developer/file": "/Users/developer/file", // not a prefix-with-separator match
	}
	for in, want := range cases {
		if got := tildePath(in, home); got != want {
			t.Errorf("tildePath(%q) = %q, want %q", in, got, want)
		}
	}
}
