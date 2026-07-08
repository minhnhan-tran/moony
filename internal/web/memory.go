package web

// memory.go: the web "session memory" editor — list, add, edit, and delete the
// bullets of every section in TWO places: the PROJECT AGENTS.md (this working
// directory) and the GLOBAL one (~/.config/moony/AGENTS.md, shared
// across every project) — shown side by side so a user can pick which one a
// note belongs in. Mirrors the skills/mcp handler shape (lookup → agent → JSON).

import (
	"encoding/json"
	"net/http"
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
	writeJSON(w, map[string]any{"ok": true})
}
