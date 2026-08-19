package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

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
	out := jobsResponse{Jobs: []jobWire{}, Policy: sess.agent.ApprovalPolicyName()}
	out.Jobs = sessionJobWires(sess)
	writeJSON(w, out)
}

// jobWatchInterval is how often a session with at least one SSE viewer re-checks its job
// list. 1s keeps the runtime clock and byte count feeling live without meaningful cost:
// the digest check below means an unchanged list publishes nothing.
const jobWatchInterval = time.Second

// sessionJobWires snapshots a session's jobs in wire form. Shared by the REST list and
// the SSE watcher so the strip and the panel can never disagree about a job's state.
//
// Always returns a non-nil slice: the JSON must be [] rather than null, and an EMPTY list
// is a meaningful update (it is what clears the strip when the last job is reaped).
func sessionJobWires(sess *session) []jobWire {
	out := []jobWire{}
	if sess == nil || sess.agent == nil {
		return out
	}
	reg := sess.agent.Registry()
	if reg == nil {
		return out
	}
	store := reg.Jobs()
	if store == nil {
		// Hub-routed session or a live mirror: jobs live on another machine, so there is
		// nothing local to report. Not an error, just an empty list.
		return out
	}
	for _, j := range store.Jobs() {
		out = append(out, jobWire{
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
	return out
}

// jobsDigest is a cheap change key for the watcher: republish only when something a
// viewer can actually see has changed.
//
// OutputLen is included so a job that is producing output but not changing state still
// refreshes (the strip shows a live byte count); Cursor is not, because the browser's
// tail cursor is per-viewer and must not drive a broadcast.
func jobsDigest(list []jobWire) string {
	if len(list) == 0 {
		return ""
	}
	b := make([]byte, 0, len(list)*32)
	for _, j := range list {
		b = append(b, j.ID...)
		b = append(b, ':')
		b = append(b, j.State...)
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(j.OutputLen), 10)
		b = append(b, ';')
	}
	return string(b)
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

// handleJobDelete terminates one background job (if running) and removes it from the store.
func (s *Server) handleJobDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "DELETE or POST required", http.StatusMethodNotAllowed)
		return
	}
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSpace(r.PathValue("job"))
	if !sess.agent.Registry().Jobs().DeleteJob(id) {
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

	// When no prompt is needed (auto / danger-skip, non-destructive command) answer
	// synchronously: the caller gets the job id in the response and nothing can block.
	if !sess.agent.BackgroundJobNeedsApproval(body.Command, body.TTY) {
		defer sess.releaseJobPrompt()
		id, err := sess.agent.StartBackgroundJobApproved(r.Context(), sess.emit, body.Command, body.TTY)
		if err != nil {
			if errors.Is(err, agent.ErrJobDenied) {
				writeJSON(w, map[string]any{"ok": false, "denied": true})
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sess.publishEvent(wireEvent{Type: "jobs_changed"})
		writeJSON(w, map[string]any{"ok": true, "id": id})
		return
	}

	// An approval IS needed, so this must not be answered inline. Blocking the request
	// here deadlocks the browser: the prompt is rendered by the very UI whose fetch is
	// still pending, so the user cannot answer it and the request hangs forever. Reply
	// 202 immediately and finish the work in the background, reporting the outcome over
	// SSE as a "job_start" event keyed by ticket.
	ticket := newJobTicket()
	go s.startJobApproved(sess, ticket, body.Command, body.TTY)

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true, "pending": true, "ticket": ticket})
}

// jobApprovalTimeout bounds how long a background start waits for an answer.
//
// A backstop, not the primary mechanism: without it an unanswered prompt pins the
// session's approval channel (blocking every later job start with a 409) and leaks the
// goroutine for the life of the process. Generous enough for someone to read the prompt
// and decide.
const jobApprovalTimeout = 2 * time.Minute

// startJobApproved runs the approval gate and starts the job, publishing the outcome.
//
// Runs on its own goroutine with its own context: the HTTP request that triggered it has
// already been answered, so r.Context() would be cancelled immediately and would abort
// the prompt before the user ever saw it.
func (s *Server) startJobApproved(sess *session, ticket, command string, tty bool) {
	defer sess.releaseJobPrompt()

	ctx, cancel := context.WithTimeout(context.Background(), jobApprovalTimeout)
	defer cancel()

	id, err := sess.agent.StartBackgroundJobApproved(ctx, sess.emit, command, tty)
	switch {
	case err == nil:
		sess.publishEvent(wireEvent{Type: "jobs_changed"})
		sess.publishEvent(wireEvent{
			Type: "job_start", Ticket: ticket, Outcome: "started", JobID: id, Command: command,
		})
	case errors.Is(err, agent.ErrJobDenied):
		// The user said no: a normal outcome, not a failure.
		sess.publishEvent(wireEvent{
			Type: "job_start", Ticket: ticket, Outcome: "denied", Command: command,
		})
	case errors.Is(err, context.DeadlineExceeded):
		sess.publishEvent(wireEvent{
			Type: "job_start", Ticket: ticket, Outcome: "failed", Command: command,
			Text: "timed out waiting for approval — start the job again if you still want it",
		})
	default:
		sess.publishEvent(wireEvent{
			Type: "job_start", Ticket: ticket, Outcome: "failed", Command: command, Text: err.Error(),
		})
	}
}

// newJobTicket returns an id correlating a 202 response with its later job_start event.
// Every SSE viewer of the session sees the event, so the ticket is what lets the tab that
// asked recognise its own outcome instead of reacting to someone else's.
func newJobTicket() string {
	// Deliberately avoids crypto/rand here: jobs_test.go structurally forbids any
	// consuming-read call in this file, because such a call on a job would silently
	// swallow bytes the model has not seen. A ticket is not a secret — it only has to be
	// unique — so the clock plus a counter is enough, and keeps that guard exact.
	n := atomic.AddUint64(&jobTicketSeq, 1)
	return "jt-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(n, 36)
}

var jobTicketSeq uint64

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
