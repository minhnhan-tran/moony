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

// A session's provider and model are restored as a pair. When only one half can
// be restored -- the other was pinned at startup with MN_PROVIDER / MN_MODEL --
// taking the restorable half alone would aim the agent at one backend while
// carrying another's model id. The UI reads the restored half and looks right,
// so the mismatch only shows up as traffic to the wrong endpoint.
func TestReopenKeepsProviderAndModelCoherent(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.Config
		sess         session.Session
		wantProvider string
		wantModel    string
	}{
		{
			// The reported bug: MN_PROVIDER=ollama, reopen an Anthropic session.
			// Restoring its model would post claude ids to localhost:11434.
			name:         "pinned provider rejects a foreign session model",
			cfg:          config.Config{LLMProvider: "ollama", Model: "llama3.1:8b", ProviderExplicit: true},
			sess:         session.Session{Provider: "anthropic", Model: "claude-sonnet-4-5"},
			wantProvider: "ollama",
			wantModel:    "llama3.1:8b",
		},
		{
			// The mirror image: MN_MODEL pins a local model, the session wants a
			// hosted provider that cannot serve it.
			name:         "pinned model rejects a foreign session provider",
			cfg:          config.Config{LLMProvider: "ollama", Model: "llama3.1:8b", ModelExplicit: true},
			sess:         session.Session{Provider: "anthropic", Model: "claude-sonnet-4-5"},
			wantProvider: "ollama",
			wantModel:    "llama3.1:8b",
		},
		{
			// Nothing pinned: the pair restores as before.
			name:         "unpinned restores both",
			cfg:          config.Config{LLMProvider: "ollama", Model: "llama3.1:8b"},
			sess:         session.Session{Provider: "anthropic", Model: "claude-sonnet-4-5"},
			wantProvider: "anthropic",
			wantModel:    "claude-sonnet-4-5",
		},
		{
			// A pinned provider that CAN serve the session's model still restores it.
			name:         "pinned provider keeps a model it can serve",
			cfg:          config.Config{LLMProvider: "anthropic", Model: "claude-sonnet-4-5", ProviderExplicit: true},
			sess:         session.Session{Provider: "anthropic", Model: "claude-opus-4-1"},
			wantProvider: "anthropic",
			wantModel:    "claude-opus-4-1",
		},
		{
			// Gateway ids (a provider prefix on the model) must not be misread as
			// a foreign family and dropped.
			name:         "gateway-style model id restores",
			cfg:          config.Config{LLMProvider: "ollama", Model: "llama3.1:8b"},
			sess:         session.Session{Provider: "anthropic", Model: "anthropic.claude-opus-5"},
			wantProvider: "anthropic",
			wantModel:    "anthropic.claude-opus-5",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			applySessionLLMSelection(&cfg, &tc.sess)
			if cfg.LLMProvider != tc.wantProvider {
				t.Errorf("provider = %q, want %q", cfg.LLMProvider, tc.wantProvider)
			}
			if cfg.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", cfg.Model, tc.wantModel)
			}
		})
	}
}
