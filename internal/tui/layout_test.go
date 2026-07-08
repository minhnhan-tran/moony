package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/config"
)

// TestViewFillsTerminalHeight guards the layout math: the rendered view must be
// exactly the terminal height. This catches a spacer (the blank line above the
// status row) that isn't subtracted from the viewport height, which would push
// the bottom rows off-screen.
func TestViewFillsTerminalHeight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const w, h = 100, 30
	m := sized(t, newTestModel(t), w, h)
	m.trans.add(entry{kind: entryUser, text: "hi"})
	m.trans.add(entry{kind: entryAssistant, text: "hello there"})
	m.refresh()

	if got := lipgloss.Height(m.View()); got != h {
		t.Errorf("view height = %d, want %d (layout must fill exactly, including the status spacer)", got, h)
	}
}

// TestHeaderShowsVersion verifies the app version appears next to the badge.
func TestHeaderShowsVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)

	out := m.headerView()
	if !strings.Contains(out, "moony") {
		t.Fatalf("header missing the badge, got:\n%s", out)
	}
	if !strings.Contains(out, "v"+config.Version) {
		t.Errorf("header should show v%s next to the badge, got:\n%s", config.Version, out)
	}
}

// TestProjectGitLabel checks the merged project/branch/status token: dir only
// outside a repo, "<dir>:<branch>*" inside one, and just "<branch>*" when the
// directory is dropped for the compact header.
func TestProjectGitLabel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 120, 30)
	m.cfg.WorkingDir = "/tmp/app"

	// Outside a git repo: just the directory, no colon/branch.
	m.gitRepo = false
	if got := m.projectGitLabel(true); !strings.Contains(got, "/tmp/app") || strings.Contains(got, ":") {
		t.Errorf("non-repo label should be just the dir, got %q", got)
	}

	// Inside a repo with uncommitted changes: dir:branch + dirty marker.
	m.gitRepo = true
	m.gitBranch = "main"
	m.gitDirty = 3
	if got := m.projectGitLabel(true); !strings.Contains(got, "/tmp/app:main*") {
		t.Errorf("repo label should merge to dir:branch*, got %q", got)
	}

	// Clean repo: no dirty marker.
	m.gitDirty = 0
	if got := m.projectGitLabel(true); !strings.Contains(got, "/tmp/app:main") || strings.Contains(got, "*") {
		t.Errorf("clean repo label should be dir:branch with no '*', got %q", got)
	}

	// Compact form drops the directory but keeps the branch.
	m.gitDirty = 1
	if got := m.projectGitLabel(false); strings.Contains(got, "/tmp/app") || !strings.Contains(got, "main*") {
		t.Errorf("compact label should drop the dir, keep branch*, got %q", got)
	}
}

// TestHeaderMergesProjectAndGit verifies the header renders the merged
// "<dir>:<branch>*" token instead of separate directory and branch fields.
func TestHeaderMergesProjectAndGit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 200, 30)
	m.cfg.WorkingDir = "/tmp/app"
	m.gitRepo = true
	m.gitBranch = "main"
	m.gitDirty = 2

	out := m.headerView()
	if !strings.Contains(out, "/tmp/app:main*") {
		t.Errorf("header should show the merged dir:branch* token, got:\n%s", out)
	}
}

// TestTranscriptLabels checks the speaker labels: the human defaults to "You"
// (or the set username) and the assistant is "sqe code agent".
func TestTranscriptLabels(t *testing.T) {
	tr := &transcript{width: 80}
	tr.add(entry{kind: entryUser, text: "hi there"})
	tr.add(entry{kind: entryAssistant, text: "hello"})

	out := tr.render(-1)
	if !strings.Contains(out, "You") {
		t.Errorf("user label should default to 'You', got:\n%s", out)
	}
	if !strings.Contains(out, "sqe code agent") {
		t.Errorf("assistant label should be 'sqe code agent', got:\n%s", out)
	}

	// A resolved username replaces "You".
	tr.userLabel = "minhnhan_tran"
	out = tr.render(-1)
	if !strings.Contains(out, "minhnhan_tran") {
		t.Errorf("custom user label not rendered, got:\n%s", out)
	}
}

func TestSetUserName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)

	m.SetUserName("jappleseed")
	if m.trans.userLabel != "jappleseed" {
		t.Errorf("SetUserName did not set the label, got %q", m.trans.userLabel)
	}
	// Blank names are ignored so the existing label stands.
	m.SetUserName("   ")
	if m.trans.userLabel != "jappleseed" {
		t.Errorf("blank name should be ignored, got %q", m.trans.userLabel)
	}
}

// TestProviderDisplayedAsWireRoute checks the wire route is labeled
// "anthropic-native" / "openai-compatible" (shown on /model and in the status).
func TestProviderDisplayedAsWireRoute(t *testing.T) {
	if got := config.ProviderLabel(config.ProviderAnthropic); !strings.Contains(got, "anthropic-native") {
		t.Errorf("anthropic route should label as 'anthropic-native', got %q", got)
	}
	if got := config.ProviderLabel(config.ProviderOpenAI); !strings.Contains(got, "openai-compatible") {
		t.Errorf("openai route should label as 'openai-compatible', got %q", got)
	}
}

// TestCompactShowsProgressThenClears verifies /compact flips on a visible
// "compacting" progress state (spinner + status text) and that the done message
// clears it — so the UI doesn't just look frozen on "Compacting older history…".
func TestCompactShowsProgressThenClears(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	next, cmd := m.command("/compact")
	m = next.(Model)
	if !m.compacting {
		t.Fatal("/compact should set compacting=true so the spinner ticks")
	}
	if cmd == nil {
		t.Fatal("/compact should return a command (spinner tick + async compact)")
	}
	if out := m.statusView(); !strings.Contains(out, "compacting older history") {
		t.Errorf("status bar should show compaction progress while running; got:\n%s", out)
	}

	// The async result clears the progress state.
	next, _ = m.Update(compactDoneMsg{dropped: 2})
	m = next.(Model)
	if m.compacting {
		t.Error("compactDoneMsg should clear the compacting state")
	}
	if out := m.statusView(); strings.Contains(out, "compacting older history") {
		t.Errorf("status bar should stop showing compaction after it finishes; got:\n%s", out)
	}
}
