package web

// git.go: git endpoints for the web "git" modal. Reads (Phase 1): an overview
// (branch/upstream/ahead-behind + working-tree changes + recent commits), a
// working-tree diff (optionally per file), and a commit diff. Writes: Phase 2
// light index/working-tree ops (stage/unstage/discard) and Phase 3 publish ops
// (commit/push) — all user-driven from the modal, with the click + an in-modal
// confirm as the consent. These do NOT go through the agent's hard-gated git tools.

import (
	"encoding/json"
	"net/http"

	"github.com/pysty1995/moony/internal/tools"
)

// gitDiffMaxBytes caps a diff response so a huge change set can't flood the UI.
const gitDiffMaxBytes = 400_000

// handleGitOverview returns the read-only git snapshot for the session's repo.
func (s *Server) handleGitOverview(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	wd := sess.agent.Config().WorkingDir
	writeJSON(w, tools.GitOverviewFor(wd, gitDiffMaxBytes))
}

// handleGitDiff returns the working-tree diff (?path=… for one file, ?staged=1
// for the index).
func (s *Server) handleGitDiff(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	wd := sess.agent.Config().WorkingDir
	staged := r.URL.Query().Get("staged") == "1"
	path := r.URL.Query().Get("path")
	diff, err := tools.GitDiffFor(wd, staged, path, gitDiffMaxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"diff": diff})
}

// handleGitCommitDiff returns the diff a commit introduced (?hash=…).
func (s *Server) handleGitCommitDiff(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	wd := sess.agent.Config().WorkingDir
	diff, err := tools.GitCommitDiffFor(wd, r.URL.Query().Get("hash"), gitDiffMaxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"diff": diff})
}

// handleGitWrite performs a working-tree / index / publish action on the session's
// repo. The action is the {action} path segment:
//
//	stage | unstage | discard  — Phase 2 light writes (index + working tree only)
//	commit | push              — Phase 3 publish actions (a human typed the message
//	                             and confirmed in the modal)
//
// None of these go through the agent's hard-gated git_commit/git_push tools — this
// is the direct user-driven write path, where the click (and the modal's confirm
// for destructive/publishing actions) is the consent. After any action we re-emit
// status so the header token (branch · dirty · ahead/behind) and sidebar refresh.
func (s *Server) handleGitWrite(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Path    string `json:"path"`
		Message string `json:"message"`
		All     bool   `json:"all"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	wd := sess.agent.Config().WorkingDir
	var (
		err error
		out string
	)
	switch r.PathValue("action") {
	case "stage":
		err = tools.GitStage(wd, body.Path)
	case "unstage":
		err = tools.GitUnstage(wd, body.Path)
	case "discard":
		err = tools.GitDiscard(wd, body.Path)
	case "commit":
		out, err = tools.GitCommitChanges(wd, body.Message, body.All)
	case "push":
		out, err = tools.GitPush(wd)
	default:
		http.Error(w, "unknown git action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Refresh the header token (dirty count / ahead-behind) + sidebar.
	sess.publishEvent(s.statusEvent(nil, sess))
	writeJSON(w, map[string]any{"ok": true, "result": out})
}
