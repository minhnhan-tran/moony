// workflow_approval.go — human-in-the-loop for workflow runs. A pause (a
// user-approval / user-input node, an agent node's pre-run approval gate, or an
// agent's mid-run tool-approval / ask_user question) emits a workflow_paused
// trace event carrying a runId + pauseId + mode, and blocks on a channel held in
// approvalHub; a separate POST /api/workflows/{wid}/resume?run={runId}&pause={id}
// &decision=approve|reject&text=… signals that channel so the run continues.
// Pauses live only as long as the run's SSE request, so the hub is an in-memory
// map keyed by runId+pauseId (the connector is a long-lived process; unlike a
// serverless lambda this is safe).

package web

import (
	"net/http"
	"sync"
)

// pauseResolution is what a resume request delivers to a waiting pause: whether
// it was approved (for approval mode) and any text the user supplied (the typed
// input, or the chosen option label for a question).
type pauseResolution struct {
	approved bool
	text     string
}

// approvalHub tracks pending workflow pauses across concurrent runs. The key is
// runID + "\x00" + pauseID so one run can pause several times (e.g. an agent node
// that asks more than one question) without the waiters colliding.
type approvalHub struct {
	mu      sync.Mutex
	pending map[string]chan pauseResolution
}

var workflowApprovals = &approvalHub{pending: map[string]chan pauseResolution{}}

func pauseKey(runID, pauseID string) string { return runID + "\x00" + pauseID }

// wait registers a waiter for (runID,pauseID) and blocks until resolve is called
// or done fires (the SSE request ended / was cancelled). On cancellation it
// returns a non-approval so the run unwinds cleanly.
func (h *approvalHub) wait(runID, pauseID string, done <-chan struct{}) pauseResolution {
	key := pauseKey(runID, pauseID)
	ch := make(chan pauseResolution, 1)
	h.mu.Lock()
	h.pending[key] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, key)
		h.mu.Unlock()
	}()
	select {
	case d := <-ch:
		return d
	case <-done:
		return pauseResolution{approved: false, text: ""}
	}
}

// resolve delivers a resolution to the waiter for (runID,pauseID), if one exists.
func (h *approvalHub) resolve(runID, pauseID string, d pauseResolution) bool {
	h.mu.Lock()
	ch := h.pending[pauseKey(runID, pauseID)]
	h.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- d:
		return true
	default:
		return false // already resolved
	}
}

// handleResumeWorkflow: POST /api/workflows/{wid}/resume?run={runId}&pause={id}
// &decision=approve|reject&text={text}. `pause` defaults to "approval" for back
// -compat with the original single-pause approval node. `text` carries a typed
// input or a chosen option label.
func (s *Server) handleResumeWorkflow(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run")
	if runID == "" {
		http.Error(w, "missing run id", http.StatusBadRequest)
		return
	}
	pauseID := r.URL.Query().Get("pause")
	if pauseID == "" {
		pauseID = "approval"
	}
	approved := r.URL.Query().Get("decision") != "reject"
	res := pauseResolution{approved: approved, text: r.URL.Query().Get("text")}
	if workflowApprovals.resolve(runID, pauseID, res) {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	http.Error(w, "no pending pause for that run (it may have timed out or already been answered)", http.StatusNotFound)
}
