package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

func TestSiblingTestPath(t *testing.T) {
	cases := []struct {
		src      string
		wantLang string
		wantTest string
		wantOK   bool
	}{
		{"internal/foo/bar.go", "go", "internal/foo/bar_test.go", true},
		{"internal/foo/bar_test.go", "", "", false}, // already a test
		{"web/app.ts", "ts", "web/app.test.ts", true},
		{"web/app.test.ts", "", "", false},
		{"web/app.spec.js", "", "", false},
		{"web/app.js", "js", "web/app.test.js", true},
		{"pkg/thing.py", "python", "pkg/test_thing.py", true},
		{"pkg/test_thing.py", "", "", false},
		{"pkg/thing_test.py", "", "", false},
		{"README.md", "", "", false},     // unsupported
		{"vendor/x/y.go", "", "", false}, // excluded tree
		{"node_modules/a.js", "", "", false},
		{"internal/testdata/z.go", "", "", false},
	}
	for _, c := range cases {
		lang, test, ok := siblingTestPath(filepath.FromSlash(c.src))
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.src, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if lang != c.wantLang || test != filepath.FromSlash(c.wantTest) {
			t.Errorf("%s: got (%q,%q) want (%q,%q)", c.src, lang, test, c.wantLang, c.wantTest)
		}
	}
}

func TestEligibleTestTargets(t *testing.T) {
	root := filepath.FromSlash("/w")
	mk := func(rel string, missing bool) fileSnapshot {
		return fileSnapshot{path: filepath.Join(root, filepath.FromSlash(rel)), missing: missing}
	}

	t.Run("basic go file", func(t *testing.T) {
		got := eligibleTestTargets([]fileSnapshot{mk("a/foo.go", false)}, root, 5)
		if len(got) != 1 || got[0].testRel != filepath.FromSlash("a/foo_test.go") || got[0].srcRel != filepath.FromSlash("a/foo.go") {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("skip when sibling test also changed", func(t *testing.T) {
		got := eligibleTestTargets([]fileSnapshot{
			mk("a/foo.go", false),
			mk("a/foo_test.go", false),
		}, root, 5)
		if len(got) != 0 {
			t.Fatalf("expected skip (sibling test edited this turn), got %+v", got)
		}
	})

	t.Run("skip deletions", func(t *testing.T) {
		if got := eligibleTestTargets([]fileSnapshot{mk("a/foo.go", true)}, root, 5); len(got) != 0 {
			t.Fatalf("deletion should be ineligible, got %+v", got)
		}
	})

	t.Run("dedupe and cap", func(t *testing.T) {
		got := eligibleTestTargets([]fileSnapshot{
			mk("a/foo.go", false),
			mk("a/foo.go", false), // dup
			mk("b/bar.go", false),
			mk("c/baz.go", false),
		}, root, 2)
		if len(got) != 2 {
			t.Fatalf("cap=2 should yield 2, got %d: %+v", len(got), got)
		}
	})

	t.Run("default cap when max<=0", func(t *testing.T) {
		got := eligibleTestTargets([]fileSnapshot{
			mk("a.go", false), mk("b.go", false), mk("c.go", false),
		}, root, 0)
		if len(got) != defaultTestgenMax {
			t.Fatalf("want default cap %d, got %d", defaultTestgenMax, len(got))
		}
	})
}

func TestRenderTestSuggestion(t *testing.T) {
	create := renderTestSuggestion(testTarget{srcRel: "a/foo.go", testRel: "a/foo_test.go", lang: "go", testExists: false}, "package a")
	if !strings.Contains(create, "Create `a/foo_test.go`") || !strings.Contains(create, "```go\npackage a\n```") {
		t.Errorf("create suggestion wrong:\n%s", create)
	}
	update := renderTestSuggestion(testTarget{srcRel: "a/foo.go", testRel: "a/foo_test.go", lang: "go", testExists: true}, "```go\npackage a\n```")
	if !strings.Contains(update, "Update `a/foo_test.go`") {
		t.Errorf("update suggestion wrong:\n%s", update)
	}
	// already-fenced draft is not double-fenced
	if strings.Contains(update, "``````") {
		t.Errorf("draft should not be double-fenced:\n%s", update)
	}
}

// testgenAgent builds a minimal agent with one changed source file and a
// scripted aux reply for the drafted test.
func testgenAgent(t *testing.T, enabled bool, reply string) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "widget.go")
	if err := os.WriteFile(src, []byte("package w\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snaps := &snapshotStore{turn: 1}
	snaps.entries = []fileSnapshot{{turn: 1, path: src, prior: []byte("package w\n")}}
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock(reply)}},
	}}
	return &Agent{
		cfg:       &config.Config{WorkingDir: dir, AutoTestGen: enabled, Model: "test-model", MaxToolOutputBytes: 16000},
		client:    fake,
		snapshots: snaps,
	}, dir
}

func TestMaybeSuggestTestsDisabled(t *testing.T) {
	a, _ := testgenAgent(t, false, "```go\npackage w\n```")
	var evs []Event
	a.maybeSuggestTests(context.Background(), func(e Event) { evs = append(evs, e) })
	if len(evs) != 0 {
		t.Fatalf("disabled testgen should emit nothing, got %d events", len(evs))
	}
}

func TestMaybeSuggestTestsEmits(t *testing.T) {
	draft := "```go\npackage w\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1,2) != 3 { t.Fail() } }\n```"
	a, _ := testgenAgent(t, true, draft)
	var infos []string
	a.maybeSuggestTests(context.Background(), func(e Event) {
		if i, ok := e.(Info); ok {
			infos = append(infos, i.Text)
		}
	})
	joined := strings.Join(infos, "\n")
	if !strings.Contains(joined, "Auto-testgen: drafting tests for 1 changed file") {
		t.Errorf("missing drafting notice:\n%s", joined)
	}
	if !strings.Contains(joined, "Auto-testgen suggestion") || !strings.Contains(joined, "widget_test.go") {
		t.Errorf("missing suggestion for widget_test.go:\n%s", joined)
	}
	if !strings.Contains(joined, "func TestAdd") {
		t.Errorf("suggestion should carry the drafted test body:\n%s", joined)
	}
}
