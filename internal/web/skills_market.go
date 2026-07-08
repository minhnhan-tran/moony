package web

// The skills "marketplace" endpoints powering the Skills user-menu panel: list
// the catalog + discovered skills with state, install a catalog skill (writing
// its SKILL.md to the global skills dir), and enable/disable or remove. Mirrors
// lsp_market.go, but installing a skill is just a file write (no subprocess), so
// it's synchronous.

import (
	"encoding/json"
	"net/http"

	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/skills"
)

// skillCatalogItem is one row in the Skills panel: a catalog skill or an
// already-discovered ("custom") one, with its install/enable state.
type skillCatalogItem struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"desc,omitempty"`
	Source      string `json:"source,omitempty"` // claude | agents | project (discovered)
	Installed   bool   `json:"installed"`        // discovered on disk
	Enabled     bool   `json:"enabled"`
	Removable   bool   `json:"removable"`        // false for the shared ~/.agents dir
	Custom      bool   `json:"custom,omitempty"` // discovered but not in the catalog
}

// reattachSkills refreshes the agent's enabled-skill set after a change so it
// takes effect immediately (mirrors connectLSP/connectMCP, but purely local).
func (s *Server) reattachSkills(wd string, sess *session) {
	mgmt := manage.Load(wd)
	sess.agent.AttachSkills(manage.Filter(skills.Load(wd), func(sk skills.Skill) string { return sk.Name }, mgmt.SkillEnabled))
}

// handleSkillsCatalog returns the marketplace: every catalog skill plus any
// discovered-but-not-cataloged ("custom") skill, with live state.
func (s *Server) handleSkillsCatalog(w http.ResponseWriter, r *http.Request) {
	wd, _, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mgmt := manage.Load(wd)
	discovered := map[string]skills.Skill{}
	for _, sk := range skills.Load(wd) {
		discovered[sk.Name] = sk
	}

	out := []skillCatalogItem{}
	seen := map[string]bool{}
	for _, e := range skills.Catalog() {
		seen[e.Name] = true
		sk, installed := discovered[e.Name]
		item := skillCatalogItem{
			Name: e.Name, Title: e.Title, Description: e.Description,
			Installed: installed, Enabled: mgmt.SkillEnabled(e.Name), Removable: true,
		}
		if installed {
			item.Source = sk.Source
			item.Removable = sk.Source != "agents"
		}
		out = append(out, item)
	}
	for _, sk := range skills.Load(wd) {
		if seen[sk.Name] {
			continue
		}
		out = append(out, skillCatalogItem{
			Name: sk.Name, Description: sk.Description, Source: sk.Source,
			Installed: true, Enabled: mgmt.SkillEnabled(sk.Name), Removable: sk.Source != "agents", Custom: true,
		})
	}
	writeJSON(w, out)
}

// handleSkillInstall installs a catalog skill (writes its SKILL.md to the global
// skills dir) and re-attaches so it's immediately available.
func (s *Server) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
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
	if _, err := skills.Install(body.Name, true, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.reattachSkills(wd, sess)
	writeJSON(w, map[string]any{"installed": true})
}

// handleSkillToggle enables/disables a skill (global scope) and re-attaches.
func (s *Server) handleSkillToggle(w http.ResponseWriter, r *http.Request) {
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
	if err := manage.Load(wd).SetSkill(body.Name, body.Enabled, manage.Global); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reattachSkills(wd, sess)
	writeJSON(w, map[string]any{"ok": true})
}

// handleSkillRemove deletes a discovered skill's directory and re-attaches.
func (s *Server) handleSkillRemove(w http.ResponseWriter, r *http.Request) {
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
	if _, err := skills.Remove(body.Name, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.reattachSkills(wd, sess)
	writeJSON(w, map[string]any{"ok": true})
}
