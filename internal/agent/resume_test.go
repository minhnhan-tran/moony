package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// TestResumeLoadsSessionHistory verifies Resume swaps the active session and
// seeds the agent's history from it.
func TestResumeLoadsSessionHistory(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "test", WorkingDir: dir}
	ag := New(cfg, nil, tools.NewRegistry(dir), store, nil)
	if ag.History() != 0 {
		t.Fatalf("fresh session history = %d, want 0", ag.History())
	}

	other := session.New("test", dir)
	other.Messages = []llm.Message{
		llm.UserText("hello from the old session"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("hi")}},
	}
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}

	ag.Resume(other)
	if ag.SessionID() != other.ID {
		t.Errorf("SessionID = %q, want %q", ag.SessionID(), other.ID)
	}
	if ag.History() != 2 {
		t.Errorf("history after resume = %d, want 2", ag.History())
	}
}

// TestResumeNoStoreIsNoop verifies Resume does nothing without persistence.
func TestResumeNoStoreIsNoop(t *testing.T) {
	dir := t.TempDir()
	ag := New(&config.Config{Model: "test", WorkingDir: dir}, nil, tools.NewRegistry(dir), nil, nil)
	ag.Resume(session.New("test", dir)) // must not panic or change anything
	if ag.History() != 0 {
		t.Errorf("history = %d, want 0 (no store: resume is a no-op)", ag.History())
	}
}
