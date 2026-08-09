package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/quickactions"
)

// quickactions.go serves the user-editable preset prompts behind the web UI's
// quick-actions split button (the pill beside the turn pager) and its Manage ->
// Quick actions panel.
//
// Actions are stored in two scopes (see internal/quickactions): a committable
// per-project .moony/quickactions.json, and a global fallback used only when the
// project defines none. The panel edits one scope at a time; the button always
// renders the effective list.
//
// Every mutation replies with the same payload a GET would, so the client
// re-renders from server truth instead of guessing at local state.

// quickActionsPayload is what both the menu and the Manage panel read.
type quickActionsPayload struct {
	// Actions is the effective list the button renders (project, else global).
	Actions []quickactions.Action `json:"actions"`
	// Scope is which scope Actions came from ("project" or "global").
	Scope string `json:"scope"`
	// Project/Global are the raw per-scope lists, so the panel can show and
	// edit either without a second round trip.
	Project []quickactions.Action `json:"project"`
	Global  []quickactions.Action `json:"global"`
	// WorkingDir labels the project scope in the UI.
	WorkingDir string `json:"workingDir,omitempty"`
}

// scopeFrom maps the request's scope field onto a storage scope, defaulting to
// project — the common case, and the safer one (a mistake stays in the repo
// rather than following the user to every project).
func scopeFrom(s string) quickactions.Scope {
	if strings.EqualFold(strings.TrimSpace(s), string(quickactions.Global)) {
		return quickactions.Global
	}
	return quickactions.Project
}

func (s *Server) quickActionsStore() *quickactions.Store {
	return quickactions.Load(s.cfg.WorkingDir)
}

func (s *Server) handleGetQuickActions(w http.ResponseWriter, _ *http.Request) {
	st := s.quickActionsStore()
	writeJSON(w, quickActionsPayload{
		Actions:    st.List(),
		Scope:      string(st.EffectiveScope()),
		Project:    st.ListScope(quickactions.Project),
		Global:     st.ListScope(quickactions.Global),
		WorkingDir: s.cfg.WorkingDir,
	})
}

// quickActionRequest is the body for add/update/remove/move. Only the fields
// each operation needs are read.
type quickActionRequest struct {
	Scope string `json:"scope"`
	// OldPrompt identifies the row being edited (update only); the new values
	// come from Action.
	OldPrompt string                `json:"oldPrompt"`
	Prompt    string                `json:"prompt"`
	Action    quickactions.Action   `json:"action"`
	Delta     int                   `json:"delta"`
	Actions   []quickactions.Action `json:"actions"`
}

func (s *Server) handleAddQuickAction(w http.ResponseWriter, r *http.Request) {
	var in quickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := s.quickActionsStore().Add(in.Action, scopeFrom(in.Scope)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetQuickActions(w, r)
}

func (s *Server) handleUpdateQuickAction(w http.ResponseWriter, r *http.Request) {
	var in quickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.OldPrompt) == "" {
		http.Error(w, "oldPrompt is required", http.StatusBadRequest)
		return
	}
	if err := s.quickActionsStore().Update(in.OldPrompt, in.Action, scopeFrom(in.Scope)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetQuickActions(w, r)
}

func (s *Server) handleRemoveQuickAction(w http.ResponseWriter, r *http.Request) {
	var in quickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	if err := s.quickActionsStore().Remove(in.Prompt, scopeFrom(in.Scope)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetQuickActions(w, r)
}

// handleMoveQuickAction reorders one action; the first is the button's default.
func (s *Server) handleMoveQuickAction(w http.ResponseWriter, r *http.Request) {
	var in quickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Prompt) == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}
	if err := s.quickActionsStore().Move(in.Prompt, in.Delta, scopeFrom(in.Scope)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetQuickActions(w, r)
}

// handleResetQuickActions restores the built-in presets in one scope.
func (s *Server) handleResetQuickActions(w http.ResponseWriter, r *http.Request) {
	var in quickActionRequest
	_ = json.NewDecoder(r.Body).Decode(&in) // body optional; defaults to project
	if err := s.quickActionsStore().ResetDefaults(scopeFrom(in.Scope)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.handleGetQuickActions(w, r)
}
