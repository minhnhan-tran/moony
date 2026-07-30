package web

// Task Board HTTP handlers — the web UI's parallel-run queue. Tasks are held on
// the session's *taskboard.Board (created lazily, rooted at the agent's working
// dir). Each task runs headlessly in its own git worktree; the UI polls
// GET .../taskboard for live status and reviews/apply/discards finished ones.

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// handleListTasks: GET .../taskboard → {tasks:[…]}.
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"tasks": sess.taskBoard(exe).List()})
}

// handleTaskAdd: POST .../taskboard/add {title?, prompt, dependsOn?[]}.
func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var body struct {
		Title     string   `json:"title"`
		Prompt    string   `json:"prompt"`
		DependsOn []string `json:"dependsOn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	t, err := sess.taskBoard(exe).Enqueue(body.Title, body.Prompt, body.DependsOn)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "task": t})
}

// handleTaskApply: POST .../taskboard/apply {id}.
func (s *Server) handleTaskApply(w http.ResponseWriter, r *http.Request) {
	sess, exe := s.sessionAndExe(w, r)
	if sess == nil {
		return
	}
	id := taskID(r)
	detail, err := sess.taskBoard(exe).Apply(id)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "detail": detail})
}

// handleTaskDiscard: POST .../taskboard/discard {id}.
func (s *Server) handleTaskDiscard(w http.ResponseWriter, r *http.Request) {
	sess, exe := s.sessionAndExe(w, r)
	if sess == nil {
		return
	}
	if err := sess.taskBoard(exe).Discard(taskID(r)); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// sessionAndExe resolves the session + binary path, writing an error otherwise.
func (s *Server) sessionAndExe(w http.ResponseWriter, r *http.Request) (*session, string) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return nil, ""
	}
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, ""
	}
	return sess, exe
}

// taskID pulls the task id from the JSON body (falling back to the ?id= query).
func taskID(r *http.Request) string {
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if id := strings.TrimSpace(body.ID); id != "" {
		return id
	}
	return strings.TrimSpace(r.URL.Query().Get("id"))
}
