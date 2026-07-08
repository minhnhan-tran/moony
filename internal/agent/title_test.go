package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// titleAgent builds a store-backed agent (persistence on) wired to a scripted
// streamer, so auto-titling — which only runs with a store — can be exercised.
func titleAgent(t *testing.T, fake *scriptedStreamer) (*Agent, *session.Store) {
	t.Helper()
	isolateConfig(t)
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Model:              "test-model",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	return New(cfg, fake, tools.NewRegistry(dir), store, nil), store
}

func TestAutoTitleReplacesHeuristic(t *testing.T) {
	t.Setenv("SQE_AUTO_TITLE", "1")
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Here is a dark mode toggle.")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Add Dark Mode Toggle")}}, // the title call
	}}
	ag, store := titleAgent(t, fake)

	var titles []string
	err := ag.Run(context.Background(), "please add a dark mode toggle to settings", func(ev Event) {
		if tu, ok := ev.(TitleUpdate); ok {
			titles = append(titles, tu.Title)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("model calls = %d, want 2 (turn + one title call)", fake.calls)
	}
	if len(titles) != 1 || titles[0] != "Add Dark Mode Toggle" {
		t.Fatalf("TitleUpdate events = %v, want [\"Add Dark Mode Toggle\"]", titles)
	}
	// The model title must be persisted, replacing the first-message heuristic.
	saved, err := store.Load(ag.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "Add Dark Mode Toggle" {
		t.Errorf("persisted title = %q, want %q", saved.Title, "Add Dark Mode Toggle")
	}
}

func TestAutoTitleRunsOnlyOnce(t *testing.T) {
	t.Setenv("SQE_AUTO_TITLE", "1")
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("first answer")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("First Task Title")}}, // title call (turn 1)
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("second answer")}},    // turn 2 — must NOT title again
	}}
	ag, _ := titleAgent(t, fake)

	var titleEvents int
	emit := func(ev Event) {
		if _, ok := ev.(TitleUpdate); ok {
			titleEvents++
		}
	}
	if err := ag.Run(context.Background(), "do the first thing", emit); err != nil {
		t.Fatal(err)
	}
	if err := ag.Run(context.Background(), "do the second thing", emit); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 3 {
		t.Errorf("model calls = %d, want 3 (turn1 + title + turn2, no second title)", fake.calls)
	}
	if titleEvents != 1 {
		t.Errorf("title events = %d, want 1 (auto-title runs once per session)", titleEvents)
	}
}

func TestAutoTitleDisabled(t *testing.T) {
	t.Setenv("SQE_AUTO_TITLE", "0")
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag, store := titleAgent(t, fake)

	var titled bool
	if err := ag.Run(context.Background(), "build me a thing", func(ev Event) {
		if _, ok := ev.(TitleUpdate); ok {
			titled = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Errorf("model calls = %d, want 1 (no extra title call when disabled)", fake.calls)
	}
	if titled {
		t.Error("no TitleUpdate expected when SQE_AUTO_TITLE=0")
	}
	saved, err := store.Load(ag.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "build me a thing" {
		t.Errorf("title = %q, want the first-message heuristic %q", saved.Title, "build me a thing")
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Add Dark Mode Toggle  ", "Add Dark Mode Toggle"},
		{`"Quoted Title"`, "Quoted Title"},
		{"Title: Fix The Bug", "Fix The Bug"},
		{"Refactor Parser.", "Refactor Parser"},
		{"Line one\nLine two", "Line one"},
		{"**Bold Title**", "Bold Title"},
		{"Collapse    inner   spaces", "Collapse inner spaces"},
	}
	for _, c := range cases {
		if got := sanitizeTitle(c.in); got != c.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
