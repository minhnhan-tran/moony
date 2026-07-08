// commands_handlers.go — JSON list HTTP handlers for the manage panel and their item types.

package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/worker"
)

// skillItem and mcpItem are the JSON shapes the manage panel reads.
type skillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
}

// agentItem is the JSON shape the manage panel reads for subagent personas.
type agentItem struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tools       []string `json:"tools,omitempty"`
	Source      string   `json:"source"`
	Enabled     bool     `json:"enabled"`
}

type mcpItem struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Tools     int    `json:"tools,omitempty"`
	Error     string `json:"error,omitempty"`
}

// lspItem is the JSON shape the manage panel reads for language servers.
type lspItem struct {
	Name       string   `json:"name"`
	Target     string   `json:"target"` // command + extensions, for display
	Extensions []string `json:"extensions,omitempty"`
	Enabled    bool     `json:"enabled"`
	Running    bool     `json:"running"`
}

// workerItem is the JSON shape the manage panel reads for worker agents.
type workerItem struct {
	Name    string `json:"name"`
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
	Enabled bool   `json:"enabled"`
	Ready   string `json:"ready"`             // ready | not ready | missing | checking…
	Custom  bool   `json:"custom,omitempty"`  // defined in workers.json (vs auto-detected) — removable
	Install string `json:"install,omitempty"` // shell one-liner that installs the engine (when known)
}

// handleListSkills lists a session's discovered skills with their enabled state,
// for the management panel.
func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	wd := sess.agent.Config().WorkingDir
	mgmt := manage.Load(wd)
	out := []skillItem{}
	for _, sk := range skills.Load(wd) {
		out = append(out, skillItem{Name: sk.Name, Description: sk.Description, Source: sk.Source, Enabled: mgmt.SkillEnabled(sk.Name)})
	}
	writeJSON(w, out)
}

// handleListAgents lists a session's discovered subagent personas with their
// enabled state, for the management panel.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	wd := sess.agent.Config().WorkingDir
	mgmt := manage.Load(wd)
	out := []agentItem{}
	for _, p := range subagents.Load(wd) {
		out = append(out, agentItem{Name: p.Name, Description: p.Description, Tools: p.Tools, Source: p.Source, Enabled: mgmt.AgentEnabled(p.Name)})
	}
	writeJSON(w, out)
}

// handleListWorkers lists a session's worker agents (with backend, model,
// enabled state, and engine readiness) for the management panel.
func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	mgr := sess.agent.Workers()
	out := []workerItem{}
	if mgr != nil {
		mgr.Detect()
		ready := mgr.Readiness()
		for _, c := range mgr.All() {
			out = append(out, workerItem{Name: c.Name, Backend: c.Backend, Model: c.Model, Enabled: c.Enabled(), Ready: ready[c.Name], Custom: c.Configured(), Install: worker.InstallCommand(c.Backend)})
		}
	}
	writeJSON(w, out)
}

// handleListMCPs lists a session's configured MCP servers with their enabled
// state, for the management panel.
func (s *Server) handleListMCPs(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	wd := sess.agent.Config().WorkingDir
	mgmt := manage.Load(wd)
	cfgs, _ := mcp.LoadConfigs(wd)
	out := []mcpItem{}
	for _, c := range cfgs {
		target := c.Command
		if c.URL != "" {
			target = c.URL
		}
		st := sess.mcpConnStatus(c.Name)
		out = append(out, mcpItem{Name: c.Name, Target: target, Enabled: mgmt.MCPEnabled(c.Name), Connected: st.connected, Tools: st.tools, Error: st.err})
	}
	writeJSON(w, out)
}

// handleListLSP lists a session's configured language servers with their enabled
// and live running state, for the management panel.
func (s *Server) handleListLSP(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	wd := sess.agent.Config().WorkingDir
	mgmt := manage.Load(wd)
	cfgs, _ := lsp.LoadConfigs(wd)
	running := map[string]bool{}
	for _, n := range sess.agent.LSP().Running() {
		running[n] = true
	}
	out := []lspItem{}
	for _, c := range cfgs {
		target := c.Command + " · " + strings.Join(c.Extensions, " ")
		out = append(out, lspItem{
			Name:       c.Name,
			Target:     target,
			Extensions: c.Extensions,
			Enabled:    mgmt.LSPEnabled(c.Name),
			Running:    running[c.Name],
		})
	}
	writeJSON(w, out)
}

// mcpConn is a server's live connection state from this session's MCP manager.
type mcpConn struct {
	connected bool
	tools     int
	failed    bool
	err       string
}

// mcpConnStatus reports the live connection state of the named server from this
// session's MCP manager. Nil-safe: returns the zero value (neither connected nor
// failed) when MCP isn't connected for this session (e.g. SQE_MCP=0, or before
// the first connect/reload).
func (s *session) mcpConnStatus(name string) mcpConn {
	mgr := s.mcpManager()
	if mgr == nil {
		return mcpConn{}
	}
	if info, ok := mgr.Connected(name); ok {
		return mcpConn{connected: true, tools: info.Tools}
	}
	if e, ok := mgr.Failure(name); ok {
		return mcpConn{failed: true, err: e}
	}
	return mcpConn{}
}

// mcpStatusLabel is a short human label for a server's connection state, used in
// the /mcps menu desc and text listing. Empty when there's nothing useful to add
// (a disabled server that was never attempted).
func (s *session) mcpStatusLabel(name string, enabled bool) string {
	st := s.mcpConnStatus(name)
	switch {
	case st.connected:
		return fmt.Sprintf("connected · %d tools", st.tools)
	case st.failed:
		return "failed: " + st.err
	case enabled:
		return "not connected"
	default:
		return ""
	}
}
