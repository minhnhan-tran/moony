// workflow_handlers.go — Agent Builder CRUD: list / create / load / save / delete
// visual workflow graphs. Workflows are global on the machine (no session
// needed), so these are laptop-scoped (not session-scoped) routes; the relay
// tunnels them to the connector at /l/{host}/api/workflows.

package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/workflow"
)

// workflowSummary is the lightweight shape the Agent Builder sidebar list reads.
type workflowSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Nodes     int    `json:"nodes"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// newWorkflowID returns a random url-safe id like "wf_3f9a1c8e2b4d6f80".
func newWorkflowID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "wf_" + hex.EncodeToString(b[:])
}

// handleListWorkflows: GET /api/workflows
func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	st, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list, err := st.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]workflowSummary, 0, len(list))
	for _, wf := range list {
		out = append(out, workflowSummary{ID: wf.ID, Name: wf.Name, Nodes: len(wf.Nodes), UpdatedAt: wf.UpdatedAt})
	}
	writeJSON(w, out)
}

// handleCreateWorkflow: POST /api/workflows  {name}
func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Untitled workflow"
	}
	st, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Seed with a Start node so the canvas opens with an entry point.
	wf := &workflow.Workflow{
		ID:   newWorkflowID(),
		Name: name,
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeStart, Position: workflow.Position{X: 80, Y: 160}, Data: workflow.NodeData{Label: "Start"}},
		},
		Edges: []workflow.Edge{},
	}
	if err := st.Save(wf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, wf)
}

// handleGetWorkflow: GET /api/workflows/{wid}
func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	st, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wf, err := st.Load(r.PathValue("wid"))
	if errors.Is(err, workflow.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, wf)
}

// handleSaveWorkflow: PUT /api/workflows/{wid}  (full workflow body)
func (s *Server) handleSaveWorkflow(w http.ResponseWriter, r *http.Request) {
	var wf workflow.Workflow
	if err := json.NewDecoder(r.Body).Decode(&wf); err != nil {
		http.Error(w, "invalid workflow JSON", http.StatusBadRequest)
		return
	}
	// The id in the path is authoritative — a mismatching body id is ignored.
	wf.ID = r.PathValue("wid")
	st, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Preserve CreatedAt across saves when the client doesn't echo it back.
	if wf.CreatedAt == "" {
		if prev, e := st.Load(wf.ID); e == nil {
			wf.CreatedAt = prev.CreatedAt
		}
	}
	if err := st.Save(&wf); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "updatedAt": wf.UpdatedAt})
}

// handleDeleteWorkflow: DELETE /api/workflows/{wid}
func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	st, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := st.Delete(r.PathValue("wid")); err != nil && !errors.Is(err, workflow.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
