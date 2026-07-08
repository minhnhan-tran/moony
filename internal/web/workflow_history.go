// workflow_history.go — Agent Builder run history + durable resume + node
// templates. History/resume read the RunStore written during a run; templates are
// a small built-in catalog the UI can instantiate into a new workflow.

package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/worker"
	"github.com/pysty1995/moony/internal/workflow"
)

// handleListRuns: GET /api/workflows/{wid}/runs → recent run records (newest first).
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	store, err := workflow.OpenRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runs, err := store.List(r.PathValue("wid"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, runs)
}

// handleGetRun: GET /api/workflows/{wid}/runs/{runId} → one run record (full trace).
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	store, err := workflow.OpenRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rec, err := store.Load(r.PathValue("runId"))
	if errors.Is(err, workflow.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rec)
}

// handleResumeRun: POST /api/workflows/{wid}/runs/{runId}/resume → SSE.
// Durable resume of a PAUSED or FAILED run: reloads the saved variable bus and
// continues the graph from the recorded current node, streaming a fresh trace and
// updating the same run record. (This is distinct from the live HITL
// /resume?run=… which answers an in-flight approval over the open SSE stream.)
func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	wfStore, err := workflow.Open()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wf, err := wfStore.Load(r.PathValue("wid"))
	if errors.Is(err, workflow.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runStore, err := workflow.OpenRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rec, err := runStore.Load(r.PathValue("runId"))
	if errors.Is(err, workflow.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rec.Status != workflow.RunPaused && rec.Status != workflow.RunFailed {
		http.Error(w, "only paused or failed runs can be resumed (this one is "+rec.Status+")", http.StatusConflict)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	send := func(ev workflow.RunEvent) {
		b, e := json.Marshal(ev)
		if e != nil {
			return
		}
		w.Write([]byte("data: "))
		w.Write(b)
		w.Write([]byte("\n\n"))
		flusher.Flush()
	}

	client := llm.NewClient(s.cfg)
	runner := s.workflowNodeRunner(client)
	defer runner.Close() // tear down MCP/worker resources on every exit (incl. panic/cancel)
	runner.runID = rec.ID
	runner.emit = send
	runner.done = r.Context().Done()
	send(workflow.RunEvent{Type: "run_id", Output: rec.ID})

	// Continue from the recorded node with the saved bus. Reset to running; the
	// recorder re-persists the outcome. The trace is appended to the existing one.
	rec.Status = workflow.RunRunning
	recEmit := s.recordingEmit(send, rec, runner, runStore)
	opts := workflow.RunOptions{Vars: rec.Vars, StartNodeID: rec.CurrentNode, Input: rec.Input}
	now := func() int64 { return time.Now().UnixMilli() }
	_ = workflow.RunWithOptions(r.Context(), wf, opts, runner.run, recEmit, now)
}

// --- node templates ---------------------------------------------------------

// handleWorkflowTemplates: GET /api/workflows/templates → built-in starter graphs
// the UI can instantiate. Returns whole-workflow shapes (nodes+edges) keyed by id.
func (s *Server) handleWorkflowTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, workflow.Templates())
}

// --- capabilities -----------------------------------------------------------

// capabilityItem is one selectable agent/tool/MCP-tool in the builder dropdowns.
type capabilityItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// workflowCapabilities is the real set of capabilities this connector exposes,
// so the Agent Builder's Agent/Tool/MCP node selectors offer actual choices
// rather than free text.
type workflowCapabilities struct {
	Agents  []capabilityItem `json:"agents"`  // subagent personas (delegation targets)
	Tools   []capabilityItem `json:"tools"`   // built-in tool names
	MCP     []capabilityItem `json:"mcp"`     // connected MCP tools (mcp.<server>.<tool>)
	Skills  []capabilityItem `json:"skills"`  // Agent Skills an agent node can load
	Workers []capabilityItem `json:"workers"` // out-of-process worker engines (delegate targets)
}

// handleWorkflowCapabilities: GET /api/workflows/capabilities → the connector's
// real subagents, built-in tools, and connected MCP tools. Used to populate the
// Agent Builder node selectors from moony itself.
func (s *Server) handleWorkflowCapabilities(w http.ResponseWriter, r *http.Request) {
	wd := s.cfg.WorkingDir
	caps := workflowCapabilities{Agents: []capabilityItem{}, Tools: []capabilityItem{}, MCP: []capabilityItem{}, Skills: []capabilityItem{}, Workers: []capabilityItem{}}

	// Subagent personas (same source as the Agents marketplace + the task tool).
	for _, p := range subagents.Load(wd) {
		caps.Agents = append(caps.Agents, capabilityItem{Name: p.Name, Description: p.Description})
	}

	// Agent Skills the agent can load on demand (the `skill` tool).
	for _, sk := range skills.Load(wd) {
		caps.Skills = append(caps.Skills, capabilityItem{Name: sk.Name, Description: firstSentence(sk.Description)})
	}

	// Worker engines (delegate targets) configured/auto-detected for this machine.
	wcfgs, _ := worker.Load(wd)
	for _, c := range wcfgs {
		if c.Disabled {
			continue
		}
		caps.Workers = append(caps.Workers, capabilityItem{Name: c.Name, Description: firstSentence(c.Desc)})
	}

	// Built-in tools: names + descriptions from a default registry.
	reg := tools.Default(wd, s.cfg.MaxToolOutputBytes)
	for _, name := range reg.Names() {
		desc := ""
		if t := reg.Get(name); t != nil {
			desc = firstSentence(t.Description())
		}
		caps.Tools = append(caps.Tools, capabilityItem{Name: name, Description: desc})
	}

	// Connected MCP tools: connect this session's enabled servers and enumerate
	// their tools as mcp.<server>.<tool>. Skipped when MCP is disabled.
	if mcp.Enabled() {
		mgmt := manage.Load(wd)
		cfgs, _ := mcp.LoadConfigs(wd)
		cfgs = manage.Filter(cfgs, func(c mcp.Config) string { return c.Name }, mgmt.MCPEnabled)
		if len(cfgs) > 0 {
			mgr := mcp.ConnectAll(r.Context(), cfgs, func(string) {})
			defer mgr.Close()
			for _, it := range mgr.ToolCatalog() {
				caps.MCP = append(caps.MCP, capabilityItem{Name: it.Name, Description: firstSentence(it.Description)})
			}
		}
	}

	writeJSON(w, caps)
}

// firstSentence trims a tool description down to a short label for a dropdown.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 100 {
		s = s[:99] + "…"
	}
	return s
}
