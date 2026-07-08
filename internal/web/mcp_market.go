package web

// The MCP "marketplace" endpoints powering the MCP user-menu panel: list the
// catalog + configured servers with live connection state, install a catalog
// server (registering it in mcp.json and connecting it), and enable/disable or
// remove. Mirrors lsp_market.go. Installing doesn't run a package manager — MCP
// servers run via npx/uvx (fetched on first connect) — but the connect itself
// can be slow, so it's backgrounded (result reported to the session).

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
)

// mcpCatalogItem is one row in the MCP panel: a catalog server or an
// already-configured ("custom") one, with its install/enable/connection state.
type mcpCatalogItem struct {
	Name      string   `json:"name"`
	Title     string   `json:"title,omitempty"`
	Desc      string   `json:"desc,omitempty"`
	Target    string   `json:"target,omitempty"` // command or URL, for display
	Needs     []string `json:"needs,omitempty"`  // required env vars (display hint)
	Installed bool     `json:"installed"`
	Enabled   bool     `json:"enabled"`
	Connected bool     `json:"connected"`
	Failed    bool     `json:"failed"`
	Error     string   `json:"error,omitempty"`
	Custom    bool     `json:"custom,omitempty"`
}

// handleMcpsCatalog returns the marketplace: every catalog server plus any
// configured-but-not-cataloged ("custom") server, with live state.
func (s *Server) handleMcpsCatalog(w http.ResponseWriter, r *http.Request) {
	wd, sess, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mgmt := manage.Load(wd)
	cfgs, _ := mcp.LoadConfigs(wd)
	configured := map[string]mcp.Config{}
	for _, c := range cfgs {
		configured[c.Name] = c
	}

	out := []mcpCatalogItem{}
	seen := map[string]bool{}
	add := func(name, title, desc, target string, needs []string, custom bool) {
		// Live connection status comes from the session's agent; the live mirror
		// has no web agent, so it shows installed/enabled without connect state.
		connected, failed, errStr := false, false, ""
		if sess != nil {
			st := sess.mcpConnStatus(name)
			connected, failed, errStr = st.connected, st.failed, st.err
		}
		out = append(out, mcpCatalogItem{
			Name: name, Title: title, Desc: desc, Target: target, Needs: needs,
			Installed: configuredHas(configured, name), Enabled: mgmt.MCPEnabled(name),
			Connected: connected, Failed: failed, Error: errStr, Custom: custom,
		})
	}
	for _, e := range mcp.Catalog() {
		seen[e.Name] = true
		target := e.URL
		if target == "" {
			target = strings.TrimSpace(e.Command + " " + strings.Join(e.Args, " "))
		}
		add(e.Name, e.Title, e.Desc, target, e.Needs, false)
	}
	for _, c := range cfgs {
		if seen[c.Name] {
			continue
		}
		target := c.URL
		if target == "" {
			target = c.Command
		}
		add(c.Name, "", "", target, nil, true)
	}
	writeJSON(w, out)
}

func configuredHas(m map[string]mcp.Config, name string) bool { _, ok := m[name]; return ok }

// handleMcpInstall registers a catalog server in the global mcp.json (with
// ${root} substituted to the project dir) and connects it in the background.
func (s *Server) handleMcpInstall(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	entry, ok := mcp.CatalogEntryByName(body.Name)
	if !ok {
		http.Error(w, "unknown MCP server", http.StatusBadRequest)
		return
	}
	wd := sess.agent.Config().WorkingDir
	args := make([]string, len(entry.Args))
	for i, a := range entry.Args {
		args[i] = strings.ReplaceAll(a, "${root}", wd)
	}
	if _, err := mcp.AddServer(entry.Name, entry.Command, args, entry.URL, entry.Headers, true, wd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Connecting can be slow (npx/uvx fetch on first run), so do it in the
	// background and report the result to the session, like /mcps reload.
	go func() { sess.publishEvent(wireEvent{Type: "info", Text: s.connectMCP(wd, sess)}) }()
	writeJSON(w, map[string]any{"installed": true})
}

// handleMcpToggle enables/disables a server (global scope) and reconnects.
func (s *Server) handleMcpToggle(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	wd := sess.agent.Config().WorkingDir
	if err := manage.Load(wd).SetMCP(body.Name, body.Enabled, manage.Global); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() { sess.publishEvent(wireEvent{Type: "info", Text: s.connectMCP(wd, sess)}) }()
	writeJSON(w, map[string]any{"ok": true})
}

// handleMcpRemove unregisters a server from mcp.json and reconnects.
func (s *Server) handleMcpRemove(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	wd := sess.agent.Config().WorkingDir
	if _, err := mcp.RemoveServer(body.Name, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	go func() { sess.publishEvent(wireEvent{Type: "info", Text: s.connectMCP(wd, sess)}) }()
	writeJSON(w, map[string]any{"ok": true})
}
