package web

// HTTP handlers backing the web UI's "Schedule" management modal. They expose
// the same machine-global schedule store the `/schedule` command and the daemon
// use (internal/schedule), scoped under the session API so the modal can list,
// add, toggle, remove, and run-now scheduled prompts. Adding a job roots its run
// in the calling session's working directory.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/schedule"
)

// scheduleItem is the wire shape for one scheduled job in the management modal.
type scheduleItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Trigger string `json:"trigger"`
	Prompt  string `json:"prompt"`
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"`
	Next    string `json:"next"`
	Last    string `json:"last"`
}

// scheduleItemsFrom maps stored schedules to their wire shape for the modal.
func scheduleItemsFrom(list []schedule.Schedule) []scheduleItem {
	out := []scheduleItem{}
	for _, s := range list {
		next := ""
		if !s.NextRun.IsZero() {
			next = s.NextRun.Format(time.RFC1123)
		}
		last := ""
		if s.LastStatus != "" {
			last = s.LastStatus
			if !s.LastRun.IsZero() {
				last += " (" + s.LastRun.Format(time.RFC1123) + ")"
			}
		}
		out = append(out, scheduleItem{
			ID:      s.ID,
			Name:    s.DisplayName(),
			Summary: s.Summary(),
			Trigger: s.Describe(),
			Prompt:  s.Prompt,
			Dir:     s.WorkingDir,
			Enabled: s.Enabled,
			Next:    next,
			Last:    last,
		})
	}
	return out
}

// handleListSchedules returns all scheduled jobs for the management modal.
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	if sess := s.lookup(r); sess == nil {
		http.NotFound(w, r)
		return
	}
	st, err := schedule.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list, err := st.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, scheduleItemsFrom(list))
}

// handleScheduleAdd registers a new job rooted in the session's working dir.
func (s *Server) handleScheduleAdd(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Spec   string `json:"spec"`
		Prompt string `json:"prompt"`
		Name   string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sc, err := schedule.ParseSpec(strings.TrimSpace(body.Spec))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}
	sc.Prompt = strings.TrimSpace(body.Prompt)
	sc.Name = strings.TrimSpace(body.Name)
	sc.WorkingDir = sess.agent.Config().WorkingDir
	st, err := schedule.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := st.Add(sc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleScheduleToggle enables or disables a job.
func (s *Server) handleScheduleToggle(w http.ResponseWriter, r *http.Request) {
	if sess := s.lookup(r); sess == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	st, err := schedule.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := st.SetEnabled(body.ID, body.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleScheduleRemove deletes a job.
func (s *Server) handleScheduleRemove(w http.ResponseWriter, r *http.Request) {
	if sess := s.lookup(r); sess == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	st, err := schedule.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := st.Remove(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleScheduleRun runs a job immediately, off the request path, and records
// its outcome. The run launches a normal headless session that's browsable in
// the web UI like any other.
func (s *Server) handleScheduleRun(w http.ResponseWriter, r *http.Request) {
	if sess := s.lookup(r); sess == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	st, err := schedule.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sc, ok := st.Get(body.ID)
	if !ok {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() {
		sid, runErr := schedule.Run(context.Background(), sc, exe)
		cur, ok := st.Get(sc.ID)
		if !ok {
			return
		}
		cur.LastRun = time.Now()
		if runErr != nil {
			cur.LastStatus = "error: " + runErr.Error()
		} else {
			cur.LastStatus = "ok (session " + sid + ")"
		}
		_ = st.Update(cur)
	}()
	writeJSON(w, map[string]any{"ok": true})
}
