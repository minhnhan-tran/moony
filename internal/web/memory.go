package web

// memory.go: the web "project memory" editor — list, add, edit, and delete the
// bullets of every section in TWO places: the PROJECT AGENTS.md (this working
// directory) and the GLOBAL one (~/.config/moony/AGENTS.md, shared
// across every project) — shown side by side so a user can pick which one a
// note belongs in. Mirrors the skills/mcp handler shape (lookup → agent → JSON).

import (
	"encoding/json"
	"net/http"

	"github.com/pysty1995/moony/internal/config"
)

// handleListMemory returns both AGENTS.md's sections (each with its bullets),
// keyed by scope so the client can render them as two clearly labeled groups.
func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{
		"project": sess.agent.AgentsSections(false),
		"global":  sess.agent.AgentsSections(true),
	})
}

// handleSaveMemory adds a bullet to a section, or edits one in place when `old`
// (the prior exact text) is supplied. `global` selects which AGENTS.md — project
// (default, false) or the cross-project global one. Empty text is rejected.
func (s *Server) handleSaveMemory(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Section string `json:"section"`
		Old     string `json:"old"`
		Text    string `json:"text"`
		Global  bool   `json:"global"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	var err error
	if body.Old != "" {
		err = sess.agent.UpdateBullet(body.Section, body.Old, body.Text, body.Global)
	} else {
		err = sess.agent.AddBullet(body.Section, body.Text, body.Global)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Re-emit status so the composer pill's bullet count refreshes immediately.
	sess.publishEvent(s.statusEvent(nil, sess))
	s.propagateMemory(sess, body.Global)
	writeJSON(w, map[string]any{"ok": true})
}

// handleReplaceMemorySection overwrites a whole section's bullet list at once,
// from raw multi-line markdown text — the memory editor's per-section textarea
// save path (an alternative to the single-bullet save/delete above). Empty text
// removes the section entirely.
func (s *Server) handleReplaceMemorySection(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Section string `json:"section"`
		Text    string `json:"text"`
		Global  bool   `json:"global"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := sess.agent.ReplaceSection(body.Section, body.Text, body.Global); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess.publishEvent(s.statusEvent(nil, sess))
	s.propagateMemory(sess, body.Global)
	writeJSON(w, map[string]any{"ok": true})
}

// handleDeleteMemory removes the bullet whose text matches exactly (idempotent)
// from the selected scope's AGENTS.md.
func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Text   string `json:"text"`
		Global bool   `json:"global"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	if err := sess.agent.DeleteBullet(body.Text, body.Global); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess.publishEvent(s.statusEvent(nil, sess))
	s.propagateMemory(sess, body.Global)
	writeJSON(w, map[string]any{"ok": true})
}

// propagateMemory reloads the system prompt of every OTHER live session that
// shares the edited scope, so a memory edit is visible to sibling sessions on
// their next message instead of only to the tab that made it. Project AGENTS.md
// is per-working-dir (shared by every session in that project); the global one is
// shared by all of them, so a global edit fans out everywhere.
//
// The editing session has already rebuilt its own prompt inside AddBullet/
// UpdateBullet/ReplaceSection/DeleteBullet, so it is skipped here. Prompts are
// rebuilt off the request goroutine because buildSystemPrompt walks the tree and
// reads memory files; a running turn already captured its prompt and is
// unaffected either way.
func (s *Server) propagateMemory(editor *session, global bool) {
	var wd string
	if editor != nil && editor.agent != nil {
		wd = editor.agent.Config().WorkingDir
	}
	s.mu.Lock()
	peers := make([]*session, 0, len(s.sessions))
	for _, other := range s.sessions {
		if other == editor || other == nil || other.agent == nil {
			continue
		}
		// A project edit only touches sessions rooted at the same working dir.
		if !global && other.agent.Config().WorkingDir != wd {
			continue
		}
		peers = append(peers, other)
	}
	s.mu.Unlock()
	for _, peer := range peers {
		go peer.agent.RefreshSystemPrompt()
	}
}

// handleCodebaseMap serves the project's CODEBASE_MAP.md for the "Codebase Map"
// tab of the Project Memory & Notes modal. GET returns the current file (or a
// flag that it doesn't exist yet). POST regenerates the structural section (and
// creates the file if missing) using the session's symbol map — the same
// generation the @map mention and the auto-refresh hook use — so the user can
// materialize/refresh the map from the UI without sending a message.
func (s *Server) handleCodebaseMap(w http.ResponseWriter, r *http.Request) {
	wd, sess, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPut {
		// Saving the hand-authored pinned section from the UI.
		var body struct {
			Pinned string `json:"pinned"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := config.WriteCodebaseMapPinned(wd, body.Pinned); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if sess != nil && sess.agent != nil {
			sess.agent.RefreshSystemPrompt()
		}
	}
	if r.Method == http.MethodPost {
		if sess == nil || sess.agent == nil || sess.agent.Registry() == nil {
			http.Error(w, "codebase map can't be generated for this session", http.StatusConflict)
			return
		}
		structure := sess.agent.Registry().CodebaseMapStructural()
		if err := config.WriteCodebaseMapStructure(wd, structure); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	content, exists := config.ReadCodebaseMap(wd)
	writeJSON(w, map[string]any{
		"exists":    exists,
		"content":   content,
		"structure": config.CodebaseMapStructure(wd),
		"narrative": config.CodebaseMapNarrative(wd),
		"pinned":    config.CodebaseMapPinned(wd),
		"path":      config.CodebaseMapFile,
	})
}
