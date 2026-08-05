package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/pysty1995/moony/internal/agent"
)

// HTTP surface for the background-jobs panel.
//
// Read paths go through the store's VIEW api (tools.JobStore.Jobs/JobTail), which reads
// by absolute offset and never advances the cursor `bash_output` consumes. Tailing a job
// in the browser therefore cannot starve the model of output it hasn't read yet — an
// open panel must not change what the agent knows.

// jobsResponse is the status-only list payload. Output is deliberately absent: a
// long-running job's whole log would otherwise be re-sent on every poll.
type jobsResponse struct {
	Jobs []jobWire `json:"jobs"`
	// Policy lets the UI warn up front whether starting a job will prompt for approval.
	Policy string `json:"policy"`
}

type jobWire struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	Started   string `json:"started"`
	State     string `json:"state"`
	Exit      int    `json:"exit"`
	Err       string `json:"err,omitempty"`
	Running   bool   `json:"running"`
	OutputLen int    `json:"outputLen"`
	// TTY drives the panel's keyboard: only a pty job has somewhere to send input.
	TTY bool `json:"tty"`
}

// jobTailResponse carries an incremental slice of one job's output.
type jobTailResponse struct {
	jobWire
	Output string `json:"output"`
	// Cursor is the absolute offset to pass back next time. Independent of the model's
	// read cursor.
	Cursor int `json:"cursor"`
}

// handleJobs lists this session's background jobs (status only).
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	store := sess.agent.Registry().Jobs()
	out := jobsResponse{Jobs: []jobWire{}, Policy: sess.agent.ApprovalPolicyName()}
	for _, j := range store.Jobs() {
		out.Jobs = append(out.Jobs, jobWire{
			ID:        j.ID,
			Command:   j.Command,
			Started:   j.Started.Format("2006-01-02T15:04:05Z07:00"),
			State:     j.State,
			Exit:      j.Exit,
			Err:       j.Err,
			Running:   j.Running,
			OutputLen: j.OutputLen,
			TTY:       j.TTY,
		})
	}
	writeJSON(w, out)
}

// handleJobTail returns one job's output after ?cursor=N.
func (s *Server) handleJobTail(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSpace(r.PathValue("job"))
	if id == "" {
		http.Error(w, "job id required", http.StatusBadRequest)
		return
	}
	// A malformed cursor is treated as 0 (send everything) rather than an error: the
	// client's job is to render logs, and failing the whole request over a bad query
	// string would blank the panel instead of resynchronizing it.
	from, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("cursor")))

	info, ok := sess.agent.Registry().Jobs().JobTail(id, from)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, jobTailResponse{
		jobWire: jobWire{
			ID:        info.ID,
			Command:   info.Command,
			Started:   info.Started.Format("2006-01-02T15:04:05Z07:00"),
			State:     info.State,
			Exit:      info.Exit,
			Err:       info.Err,
			Running:   info.Running,
			OutputLen: info.OutputLen,
			TTY:       info.TTY,
		},
		Output: info.Output,
		Cursor: info.Cursor,
	})
}

// handleJobKill terminates one background job.
func (s *Server) handleJobKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSpace(r.PathValue("job"))
	if !sess.agent.Registry().Jobs().KillJob(id) {
		http.NotFound(w, r)
		return
	}
	sess.publishEvent(wireEvent{Type: "jobs_changed"})
	writeJSON(w, map[string]any{"ok": true})
}

// handleJobStart starts a NEW background job from the browser.
//
// Security: this is the one job route that executes something, so it runs the command
// through the SAME gates as a model-issued bash call — the destructive-command refusal,
// the approval policy, and the approval prompt (see agent.StartBackgroundJobApproved).
// Without that, on a hosted relay where a session is reachable by other members, this
// endpoint would be an unauthenticated remote shell.
func (s *Server) handleJobStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Command string `json:"command"`
		TTY     bool   `json:"tty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Command) == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}

	// A turn in flight owns the approval channel; a second concurrent prompt would
	// race it and could steal the turn's answer. Refuse instead, so the user retries
	// once the turn is done rather than getting a mysteriously mis-applied decision.
	if !sess.claimForJobPrompt() {
		http.Error(w, "a turn is in progress — wait for it to finish before starting a job", http.StatusConflict)
		return
	}
	defer sess.releaseJobPrompt()

	id, err := sess.agent.StartBackgroundJobApproved(r.Context(), sess.emit, body.Command, body.TTY)
	if err != nil {
		if errors.Is(err, agent.ErrJobDenied) {
			// Not a server error: the user said no.
			writeJSON(w, map[string]any{"ok": false, "denied": true})
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sess.publishEvent(wireEvent{Type: "jobs_changed"})
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// handleJobInput types into a running TTY job: answering a prompt, or driving a shell
// the user started themselves.
//
// Unlike a start this doesn't prompt for approval — the job was gated when it started,
// and confirming every line would make a terminal unusable — but the destructive-command
// refusal still applies (see agent.SendJobInput). Non-TTY jobs reject input outright:
// they run with no stdin at all.
func (s *Server) handleJobInput(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSpace(r.PathValue("job"))
	if id == "" {
		http.Error(w, "job id required", http.StatusBadRequest)
		return
	}
	var body struct {
		Data string `json:"data"`
		// Enter defaults to true: the common case is typing a line, and a client that
		// wants a raw keystroke (^C) says so explicitly.
		Enter *bool `json:"enter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	enter := body.Enter == nil || *body.Enter
	if err := sess.agent.SendJobInput(id, body.Data, enter); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
