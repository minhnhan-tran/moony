package web

// The agents "marketplace" endpoints powering the Agents user-menu panel: list
// the catalog + discovered personas with state, install a catalog persona
// (writing its `.md` to the global agents dir), and enable/disable or remove.
// Mirrors skills_market.go — installing a persona is just a file write.

import (
	"encoding/json"
	"net/http"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/subagents"
)

// agentCatalogItem is one row in the Agents panel: a catalog persona or an
// already-discovered ("custom") one, with its install/enable state.
type agentCatalogItem struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"desc,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Source      string   `json:"source,omitempty"` // global | project | optimus | claude (discovered)
	Model       string   `json:"model,omitempty"`  // pinned per-agent model id ("" = inherit parent)
	Installed   bool     `json:"installed"`
	Enabled     bool     `json:"enabled"`
	Removable   bool     `json:"removable"`        // false for imported (optimus/claude) personas
	Custom      bool     `json:"custom,omitempty"` // discovered but not in the catalog
}

// reattachSubAgents refreshes the agent's enabled-persona set after a change so
// it takes effect immediately.
func (s *Server) reattachSubAgents(wd string, sess *session) {
	mgmt := manage.Load(wd)
	sess.agent.AttachSubAgents(manage.Filter(subagents.Load(wd), func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled))
}

// handleAgentsCatalog returns the marketplace: every catalog persona plus any
// discovered-but-not-cataloged ("custom") one, with live state.
func (s *Server) handleAgentsCatalog(w http.ResponseWriter, r *http.Request) {
	wd, sess, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Copy any agents discovered from other coding agents (Optimus/Claude) into
	// the sqe config so they become editable here (model-settable, removable)
	// rather than read-only imports. Idempotent; re-attach so the now-owned
	// personas are live for this session — only the store-backed session has a
	// web agent to re-attach (the live mirror's agent lives in the terminal).
	if copied, _ := subagents.MaterializeExternals(wd); len(copied) > 0 && sess != nil {
		s.reattachSubAgents(wd, sess)
	}
	mgmt := manage.Load(wd)
	// The active backend (for blanking stale cross-provider model pins below) comes
	// from the session's agent, or the connector config for the live mirror.
	llmProvider := s.cfg.LLMProvider
	if sess != nil && sess.agent != nil {
		llmProvider = sess.agent.Config().LLMProvider
	}
	// displayModel blanks a pinned model the current backend can't serve (a stale
	// cross-provider pin), so the picker shows "inherit" — matching what actually
	// runs (subConfig ignores an unservable pin) — instead of listing, say, a
	// Floodgate model while on Ollama/AFM. The pin stays in the persona's .md, so
	// switching back to its backend restores it.
	displayModel := func(model string) string {
		if model == "" || llm.ModelServableBy(model, llmProvider) {
			return model
		}
		return ""
	}
	discovered := map[string]subagents.Persona{}
	for _, p := range subagents.Load(wd) {
		discovered[p.Name] = p
	}

	out := []agentCatalogItem{}
	seen := map[string]bool{}
	for _, e := range subagents.Catalog() {
		seen[e.Name] = true
		p, installed := discovered[e.Name]
		item := agentCatalogItem{
			Name: e.Name, Title: e.Title, Description: e.Description, Tools: e.Tools,
			Installed: installed, Enabled: mgmt.AgentEnabled(e.Name), Removable: true,
		}
		if installed {
			item.Source = p.Source
			item.Model = displayModel(p.Model)
			item.Removable = p.Source == "global" || p.Source == "project"
		}
		out = append(out, item)
	}
	for _, p := range subagents.Load(wd) {
		if seen[p.Name] {
			continue
		}
		out = append(out, agentCatalogItem{
			Name: p.Name, Description: p.Description, Tools: p.Tools, Source: p.Source,
			Model:     displayModel(p.Model),
			Installed: true, Enabled: mgmt.AgentEnabled(p.Name),
			Removable: p.Source == "global" || p.Source == "project", Custom: true,
		})
	}
	writeJSON(w, out)
}

// handleAgentInstall installs a catalog persona (writes its `.md` to the global
// agents dir) and re-attaches so it's immediately available.
func (s *Server) handleAgentInstall(w http.ResponseWriter, r *http.Request) {
	wd, sess, ok := s.catalogWD(r) // store-backed session or the live terminal mirror
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if _, err := subagents.Install(body.Name, true, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if sess != nil {
		s.reattachSubAgents(wd, sess)
	}
	writeJSON(w, map[string]any{"installed": true})
}

// handleAgentToggle enables/disables a persona (global scope) and re-attaches.
func (s *Server) handleAgentToggle(w http.ResponseWriter, r *http.Request) {
	wd, sess, ok := s.catalogWD(r) // store-backed session or the live terminal mirror
	if !ok {
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
	if err := manage.Load(wd).SetAgent(body.Name, body.Enabled, manage.Global); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sess != nil {
		s.reattachSubAgents(wd, sess)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleAgentRemove deletes a discovered persona's file and re-attaches.
func (s *Server) handleAgentRemove(w http.ResponseWriter, r *http.Request) {
	wd, sess, ok := s.catalogWD(r) // store-backed session or the live terminal mirror
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if _, err := subagents.Remove(body.Name, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if sess != nil {
		s.reattachSubAgents(wd, sess)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// agentModelOption is one row in the per-agent model picker.
type agentModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleAgentModels returns the models a persona can be pinned to: the live list
// from the active backend, falling back to the curated list when the fetch fails
// — but ONLY on Floodgate, since that curated list is Floodgate-specific (Claude
// + Gemini ids). On AFM/Ollama a failed fetch returns an empty list rather than
// offering models the backend can't serve (which the user could then pin and
// break the sub-agent); the UI still shows "inherit" plus any existing pin. The
// empty id ("inherit the parent's model") is added by the UI, not here.
func (s *Server) handleAgentModels(w http.ResponseWriter, r *http.Request) {
	// Resolve the model list + active backend from the session's agent, or — for
	// the live mirror (no web agent) — from the connector's own config/client.
	var (
		models []config.ModelInfo
		err    error
	)
	if s.routeToHub(r.PathValue("id")) {
		models, err = llm.NewClient(s.cfg).ListModels(r.Context())
	} else {
		sess := s.lookup(r)
		if sess == nil || sess.agent == nil {
			http.NotFound(w, r)
			return
		}
		models, err = sess.agent.ListModels(r.Context())
	}
	if err != nil || len(models) == 0 {
		models = config.AllKnownModels()
	}
	out := make([]agentModelOption, 0, len(models))
	for _, m := range models {
		out = append(out, agentModelOption{ID: m.ID, Name: m.Name})
	}
	writeJSON(w, out)
}

// handleAgentSetModel pins (or clears, when model is empty) a persona's
// per-agent model by rewriting its `.md` frontmatter, then re-attaches so the
// change takes effect for subsequent task-tool delegations.
func (s *Server) handleAgentSetModel(w http.ResponseWriter, r *http.Request) {
	// Resolve the working dir for either a store-backed web session OR the live
	// terminal mirror (catalogWD returns the launch dir + a nil session for the
	// mirror). The persona's model lives in its working-dir .md file, so no live web
	// agent is required — previously this used s.lookup, which returns nil for the
	// live-mirror session id and 404'd, even though the catalog/models reads worked.
	wd, sess, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if _, err := subagents.SetModel(body.Name, body.Model, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if sess != nil { // re-attach only a store-backed session; the mirror's agent lives in the terminal
		s.reattachSubAgents(wd, sess)
	}
	writeJSON(w, map[string]any{"ok": true, "model": body.Model})
}
