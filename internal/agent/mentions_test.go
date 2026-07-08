package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

func TestExpandMentionDirective(t *testing.T) {
	personas := map[string]bool{"explore": true}
	workers := map[string]bool{"claude": true}
	workflows := map[string]bool{"triage": true}
	td := t.TempDir()

	// Persona → task tool.
	got := expandMentionDirective("ask @explore to map auth", personas, nil, nil, false, td)
	if !strings.Contains(got, "ask @explore to map auth") {
		t.Error("original text should be preserved")
	}
	if !strings.Contains(got, "`task` tool with agent \"explore\"") {
		t.Errorf("expected a task directive, got:\n%s", got)
	}

	// Worker → delegate tool.
	if got := expandMentionDirective("get @claude to review", nil, workers, nil, false, td); !strings.Contains(got, "`delegate` tool with worker \"claude\"") {
		t.Errorf("expected a delegate directive, got:\n%s", got)
	}

	// Workflow → run_workflow tool (the new path).
	if got := expandMentionDirective("run @triage on this", nil, nil, workflows, false, td); !strings.Contains(got, "`run_workflow` tool with workflow \"triage\"") {
		t.Errorf("expected a run_workflow directive, got:\n%s", got)
	}

	// Unknown @token → untouched.
	in := "hey @nobody help"
	if got := expandMentionDirective(in, personas, workers, workflows, false, td); got != in {
		t.Errorf("unknown mention should be untouched; got %q", got)
	}

	// Nothing configured → untouched.
	if got := expandMentionDirective("ask @explore", nil, nil, nil, false, td); got != "ask @explore" {
		t.Errorf("no targets → unchanged; got %q", got)
	}
}

func TestExpandMentionDirectiveFileWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "triage"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := "look at @triage"
	if got := expandMentionDirective(in, nil, nil, map[string]bool{"triage": true}, false, dir); got != in {
		t.Errorf("a token resolving to a real file must not dispatch; got %q", got)
	}
}

func TestExpandMentionDirectivePathTokenIgnored(t *testing.T) {
	in := "see @cmd/main.go"
	if got := expandMentionDirective(in, map[string]bool{"cmd": true}, nil, nil, false, t.TempDir()); got != in {
		t.Errorf("a path-like @token must not dispatch; got %q", got)
	}
}

func TestExpandMentionDirectivePlanModeSkipsPersonaNotWorkflow(t *testing.T) {
	// In plan mode the task tool is blocked → persona mention is left untouched...
	in := "ask @explore"
	if got := expandMentionDirective(in, map[string]bool{"explore": true}, nil, nil, true, t.TempDir()); got != in {
		t.Errorf("persona mention in plan mode should be untouched; got %q", got)
	}
	// ...but workflows still dispatch (run_workflow isn't plan-blocked).
	if got := expandMentionDirective("run @triage", nil, nil, map[string]bool{"triage": true}, true, t.TempDir()); !strings.Contains(got, "`run_workflow` tool with workflow \"triage\"") {
		t.Errorf("workflow mention should still dispatch in plan mode, got:\n%s", got)
	}
}

func TestExpandMentionDirectiveDeduplicates(t *testing.T) {
	got := expandMentionDirective("@triage and @triage again", nil, nil, map[string]bool{"triage": true}, false, t.TempDir())
	if strings.Count(got, "`run_workflow` tool with workflow \"triage\"") != 1 {
		t.Errorf("a repeated mention should dispatch once, got:\n%s", got)
	}
}

func TestExpandMentionDirectiveTrailingPunctuation(t *testing.T) {
	for _, in := range []string{"do @triage.", "@triage!", "@triage?"} {
		if got := expandMentionDirective(in, nil, nil, map[string]bool{"triage": true}, false, t.TempDir()); !strings.Contains(got, "workflow \"triage\"") {
			t.Errorf("%q should dispatch to triage, got:\n%s", in, got)
		}
	}
}

// Multi-word names (workflows/workers can contain spaces) must match WHOLE, not
// be truncated at the first space — the reported bug.
func TestExpandMentionDirectiveMultiWordName(t *testing.T) {
	wf := map[string]bool{"Superpower Workflow": true}
	got := expandMentionDirective("@Superpower Workflow make an app", nil, nil, wf, false, t.TempDir())
	if !strings.Contains(got, `workflow "Superpower Workflow"`) {
		t.Errorf("multi-word workflow name should match whole, got:\n%s", got)
	}
	// A worker with spaces, mid-sentence, with trailing punctuation.
	wk := map[string]bool{"Claude Reviewer": true}
	if got := expandMentionDirective("please @Claude Reviewer, take a look", nil, wk, nil, false, t.TempDir()); !strings.Contains(got, `worker "Claude Reviewer"`) {
		t.Errorf("multi-word worker name should match whole, got:\n%s", got)
	}
}

// The longest matching name wins, so a multi-word name beats a same-prefixed
// shorter one and an exact-but-longer token isn't matched as the short name.
func TestExpandMentionDirectiveLongestWins(t *testing.T) {
	// Both "review" (agent) and "review pr" (workflow) exist; "@review pr" → the workflow.
	got := expandMentionDirective("@review pr now", map[string]bool{"review": true}, nil, map[string]bool{"review pr": true}, false, t.TempDir())
	if !strings.Contains(got, `workflow "review pr"`) || strings.Contains(got, `agent "review"`) {
		t.Errorf("longest name should win (@review pr → workflow), got:\n%s", got)
	}
	// "@reviewX" must match neither (not a name boundary).
	if got := expandMentionDirective("@reviewX", map[string]bool{"review": true}, nil, nil, false, t.TempDir()); strings.Contains(got, "agent dispatch") {
		t.Errorf("@reviewX should not match the 'review' agent, got:\n%s", got)
	}
}

// scanFileMentions extracts @<path> tokens at mention boundaries, trims trailing
// sentence punctuation, and ignores a non-boundary '@' (e.g. inside an email).
func TestScanFileMentions(t *testing.T) {
	got := scanFileMentions("see @cmd/main.go and @./x.py, email me@apple.com then @Makefile.")
	want := []string{"cmd/main.go", "./x.py", "Makefile"}
	if len(got) != len(want) {
		t.Fatalf("scanFileMentions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// expandFileMentions inlines the content of an @file that resolves to a real
// text file, preserves the prose, and leaves a non-file mention untouched.
func TestExpandFileMentions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello world\nsecond line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		cfg:   &config.Config{WorkingDir: dir, MaxToolOutputBytes: 30000},
		tools: tools.Default(dir, 30000),
	}

	const prose = "please look at @note.txt for context"
	out := a.expandFileMentions(prose)
	if !strings.HasPrefix(out, prose) {
		t.Errorf("prose not preserved at the top: %q", out)
	}
	if !strings.Contains(out, "attached files") {
		t.Errorf("expected an attached-files section, got %q", out)
	}
	if !strings.Contains(out, "note.txt") || !strings.Contains(out, "1: hello world") {
		t.Errorf("expected inlined numbered content, got %q", out)
	}

	// A token that isn't a real file leaves the prose untouched.
	if got := a.expandFileMentions("ask @nobody to help"); got != "ask @nobody to help" {
		t.Errorf("non-file mention should be untouched, got %q", got)
	}
}
