package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// fakeRepoTree creates: <tmp>/.git/, <tmp>/AGENTS.md, <tmp>/sub/AGENTS.md.
func fakeRepoTree(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT_AGENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("SUB_AGENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadAgentsMDWalksToRepoRoot(t *testing.T) {
	isolateConfig(t)
	root := fakeRepoTree(t)

	files := loadAgentsMD(filepath.Join(root, "sub"))
	if len(files) != 2 {
		t.Fatalf("expected 2 AGENTS.md files (root + sub), got %d: %+v", len(files), files)
	}
	// Root-first ordering means the most specific file is appended last.
	if !strings.Contains(files[0].body, "ROOT_AGENTS") {
		t.Errorf("first file should be the root one, got %q", files[0].body)
	}
	if !strings.Contains(files[1].body, "SUB_AGENTS") {
		t.Errorf("second file should be the sub one, got %q", files[1].body)
	}
}

func TestLoadAgentsMDStopsAtRepoBoundary(t *testing.T) {
	isolateConfig(t)
	// Outer dir contains an AGENTS.md but is NOT a repo (no .git).
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "AGENTS.md"), []byte("OUTSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "repo")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "AGENTS.md"), []byte("INSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := loadAgentsMD(inner)
	if len(files) != 1 || !strings.Contains(files[0].body, "INSIDE") {
		t.Fatalf("walk should stop at repo root; got %+v", files)
	}
}

func TestBuildSystemPromptIncludesAgentsMD(t *testing.T) {
	isolateConfig(t)
	root := fakeRepoTree(t)
	cfg := &config.Config{Model: "anthropic.claude-sonnet-4-6", WorkingDir: filepath.Join(root, "sub")}
	registry := tools.Default(cfg.WorkingDir, 30000)
	prompt := buildSystemPrompt(cfg, registry)
	if !strings.Contains(prompt, "ROOT_AGENTS") || !strings.Contains(prompt, "SUB_AGENTS") {
		t.Fatalf("system prompt should include both AGENTS.md files; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Project instructions") {
		t.Fatal("system prompt should label the project instructions section")
	}
}

func TestCompactReturnsZeroWhenNothingToCompact(t *testing.T) {
	cfg := &config.Config{CompactKeepTokens: 1_000_000}
	a := &Agent{cfg: cfg, messages: []llm.Message{llm.UserText("hi")}}
	dropped, err := a.Compact(nil)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if dropped != 0 {
		t.Errorf("expected dropped=0 for short history, got %d", dropped)
	}
}
