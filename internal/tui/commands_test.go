package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

func TestParseCommandFile(t *testing.T) {
	src := "---\ndescription: review the staged diff\n---\n\nReview this diff:\n\n$ARGUMENTS\n"
	desc, body := parseCommandFile(src)
	if desc != "review the staged diff" {
		t.Errorf("description = %q", desc)
	}
	if !strings.HasPrefix(body, "Review this diff:") {
		t.Errorf("body = %q", body)
	}
}

func TestParseCommandFileNoFrontmatter(t *testing.T) {
	src := "Just a body, no frontmatter."
	desc, body := parseCommandFile(src)
	if desc != "" {
		t.Errorf("expected empty description, got %q", desc)
	}
	if body != "Just a body, no frontmatter." {
		t.Errorf("body = %q", body)
	}
}

func TestExpandUserCommand(t *testing.T) {
	cmd := userCommand{template: "Please review:\n\n$ARGUMENTS"}
	if got := expandUserCommand(cmd, "main.go"); got != "Please review:\n\nmain.go" {
		t.Errorf("ARGUMENTS not substituted: %q", got)
	}

	// No placeholder: args appended.
	cmd2 := userCommand{template: "Always run go vet"}
	if got := expandUserCommand(cmd2, "in ./..."); got != "Always run go vet\n\nin ./..." {
		t.Errorf("missing placeholder fallback: %q", got)
	}

	// No placeholder, no args: template unchanged.
	if got := expandUserCommand(cmd2, ""); got != "Always run go vet" {
		t.Errorf("template should pass through unchanged: %q", got)
	}
}

func TestLoadUserCommandsProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	globalDir := filepath.Join(home, ".config", "moony", "commands")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "review.md"),
		[]byte("---\ndescription: global review\n---\nGlobal body"), 0o644); err != nil {
		t.Fatal(err)
	}

	proj := t.TempDir()
	projDir := filepath.Join(proj, config.ProjectDir, "commands")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "review.md"),
		[]byte("---\ndescription: project review\n---\nProject body"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmds := loadUserCommands(proj)
	if len(cmds) != 1 || cmds[0].name != "/review" {
		t.Fatalf("expected one /review command, got %+v", cmds)
	}
	if cmds[0].description != "project review" || !strings.Contains(cmds[0].template, "Project body") {
		t.Fatalf("project should override global: %+v", cmds[0])
	}
}
