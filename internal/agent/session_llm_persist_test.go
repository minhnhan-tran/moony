package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// TestSessionLLMSelectionPersistsAcrossReopen verifies gap #1: a session's
// per-session provider/model/reasoning selection is snapshotted on persist and
// restored into a fresh config when the session is reopened (mirroring a
// process restart), rather than falling back to the global default.
func TestSessionLLMSelectionPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// First "process": create a session seeded from the global default, then
	// switch its model/provider/reasoning (as /model, /provider, /reasoning do)
	// and persist.
	cfg1 := &config.Config{Model: "default-model", LLMProvider: "default-provider", ReasoningLevel: "auto", WorkingDir: dir}
	ag1 := New(cfg1, nil, tools.NewRegistry(dir), store, nil)
	ag1.cfg.Model = "llama3"
	ag1.cfg.LLMProvider = "ollama"
	ag1.cfg.ReasoningLevel = "high"
	ag1.persist()

	id := ag1.sess.ID

	// Sanity: the persisted record carries the switched selection.
	saved, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Model != "llama3" || saved.Provider != "ollama" || saved.Reasoning != "high" {
		t.Fatalf("persisted selection = %q/%q/%q, want llama3/ollama/high", saved.Model, saved.Provider, saved.Reasoning)
	}

	// Second "process": a fresh config re-seeded from the global default (as on
	// restart). Reopening the session must restore its own selection, not the
	// default.
	cfg2 := &config.Config{Model: "default-model", LLMProvider: "default-provider", ReasoningLevel: "auto", WorkingDir: dir}
	New(cfg2, nil, tools.NewRegistry(dir), store, saved)
	if cfg2.Model != "llama3" {
		t.Errorf("reopened cfg.Model = %q, want llama3 (restored)", cfg2.Model)
	}
	if cfg2.LLMProvider != "ollama" {
		t.Errorf("reopened cfg.LLMProvider = %q, want ollama (restored)", cfg2.LLMProvider)
	}
	if cfg2.ReasoningLevel != "high" {
		t.Errorf("reopened cfg.ReasoningLevel = %q, want high (restored)", cfg2.ReasoningLevel)
	}
}

// TestOldSessionKeepsDefaultLLMSelection verifies the restore is guarded: a
// session saved before Provider/Reasoning were persisted (empty fields) must
// leave the fresh config at its seeded default rather than blanking it.
func TestOldSessionKeepsDefaultLLMSelection(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// An "old" session: only Model set, Provider/Reasoning empty.
	old := session.New("", dir)
	old.Provider = ""
	old.Reasoning = ""
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Model: "default-model", LLMProvider: "default-provider", ReasoningLevel: "auto", WorkingDir: dir}
	loaded, err := store.Load(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	New(cfg, nil, tools.NewRegistry(dir), store, loaded)
	if cfg.LLMProvider != "default-provider" {
		t.Errorf("cfg.LLMProvider = %q, want default-provider (unchanged for old session)", cfg.LLMProvider)
	}
	if cfg.ReasoningLevel != "auto" {
		t.Errorf("cfg.ReasoningLevel = %q, want auto (unchanged for old session)", cfg.ReasoningLevel)
	}
}

// TestResumeRestoresLLMSelection verifies Resume (an in-process session
// switch, e.g. the TUI's /sessions picker) also restores the target session's
// LLM selection into the shared *Agent's config -- not just agent.New's
// reopen-in-a-fresh-process path. Without this, switching sessions in one
// running process would keep whichever model/provider/reasoning the
// PREVIOUSLY active session had, rather than the one being switched to.
func TestResumeRestoresLLMSelection(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "default-model", LLMProvider: "default-provider", ReasoningLevel: "auto", WorkingDir: dir}
	ag := New(cfg, nil, tools.NewRegistry(dir), store, nil)

	other := session.New("llama3", dir)
	other.Provider = "ollama"
	other.Reasoning = "high"
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}

	ag.Resume(other)
	if cfg.Model != "llama3" {
		t.Errorf("cfg.Model after Resume = %q, want llama3", cfg.Model)
	}
	if cfg.LLMProvider != "ollama" {
		t.Errorf("cfg.LLMProvider after Resume = %q, want ollama", cfg.LLMProvider)
	}
	if cfg.ReasoningLevel != "high" {
		t.Errorf("cfg.ReasoningLevel after Resume = %q, want high", cfg.ReasoningLevel)
	}
}
