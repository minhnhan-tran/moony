package web

// The plugins "marketplace" endpoints powering the Plugins user-menu panel:
//   - built-in bundles (internal/plugins catalog): list + install/remove
//   - git marketplaces (Claude-Code style): add/list/remove/update a repo, and
//     install/remove the plugins it ships (by name@marketplace)
// A plugin fans out into the existing skills/agents/mcp install primitives plus
// slash-command files, so install/remove here also re-attach skills + subagents,
// (re)connect MCP, and refresh the command palette so every part lights up live.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/pysty1995/moony/internal/plugins"
)

// pluginCatalogItem is one row in the Plugins panel: a bundle plus what it
// contains and how much of it is currently installed.
type pluginCatalogItem struct {
	Name       string   `json:"name"`
	Title      string   `json:"title,omitempty"`
	Desc       string   `json:"desc,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	Agents     []string `json:"agents,omitempty"`
	MCP        []string `json:"mcp,omitempty"`
	Commands   []string `json:"commands,omitempty"` // command names, with leading slash
	Installed  bool     `json:"installed"`          // all components present
	Present    int      `json:"present"`            // components currently installed
	Components int      `json:"components"`         // total components in the bundle
}

// handlePluginsCatalog returns the built-in plugin bundles with per-bundle
// install state derived from the live skills/agents/mcp config + commands dir.
func (s *Server) handlePluginsCatalog(w http.ResponseWriter, r *http.Request) {
	wd, _, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	out := []pluginCatalogItem{}
	for _, st := range plugins.StatusAll(wd) {
		cmds := make([]string, 0, len(st.Commands))
		for _, c := range st.Commands {
			cmds = append(cmds, "/"+c.Name)
		}
		out = append(out, pluginCatalogItem{
			Name: st.Name, Title: st.Title, Desc: st.Desc,
			Skills: st.Skills, Agents: st.Agents, MCP: st.MCP, Commands: cmds,
			Installed: st.Installed, Present: st.Present, Components: st.Components,
		})
	}
	writeJSON(w, out)
}

// pluginMarketplacesResponse is the git-marketplace view: the added marketplaces
// and the plugins they ship (with install state), plus the built-in default
// marketplaces not yet added — surfaced as "suggested" so the UI can offer a
// one-click Load (clone + register) without the user pasting a URL.
type pluginMarketplacesResponse struct {
	Marketplaces []plugins.Marketplace        `json:"marketplaces"`
	Suggested    []plugins.Marketplace        `json:"suggested,omitempty"`
	Plugins      []plugins.MarketPluginStatus `json:"plugins"`
}

// handlePluginsMarketplaces lists added git marketplaces and their plugins, plus
// the default marketplaces not yet added (as loadable suggestions).
func (s *Server) handlePluginsMarketplaces(w http.ResponseWriter, r *http.Request) {
	wd, _, ok := s.catalogWD(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	added := plugins.Marketplaces()
	have := make(map[string]bool, len(added))
	for _, m := range added {
		have[m.Name] = true
	}
	var suggested []plugins.Marketplace
	for _, d := range plugins.DefaultMarketplaces() {
		if !have[d.Name] {
			suggested = append(suggested, d) // not cloned yet — offer a Load button
		}
	}
	writeJSON(w, pluginMarketplacesResponse{
		Marketplaces: added,
		Suggested:    suggested,
		Plugins:      plugins.MarketplacePlugins(wd),
	})
}

// handlePluginMarketplaceAdd clones a marketplace repo and registers it. The
// clone runs in the BACKGROUND (these repos are large — tens to hundreds of MB),
// reporting progress + the outcome as info/error events, so the request can't
// hang the UI for minutes or be cut by a relay/proxy idle timeout. 202 = started.
func (s *Server) handlePluginMarketplaceAdd(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.URL == "" {
		http.Error(w, "a git URL is required", http.StatusBadRequest)
		return
	}
	label := body.Name
	if label == "" {
		label = body.URL
	}
	go func() {
		sess.publishEvent(wireEvent{Type: "info", Text: "Adding plugin marketplace " + label + " — cloning the repo (large ones can take a minute)…"})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		m, err := plugins.AddMarketplace(ctx, body.Name, body.URL)
		if err != nil {
			sess.publishEvent(wireEvent{Type: "error", Text: "Could not add plugin marketplace " + label + ": " + err.Error()})
			return
		}
		sess.publishEvent(wireEvent{Type: "info", Text: "Added plugin marketplace " + m.Name + " (" + m.URL + ") — its plugins are now in the Plugins panel."})
	}()
	w.WriteHeader(http.StatusAccepted)
}

// handlePluginMarketplaceRemove deletes a marketplace's clone + registry entry.
func (s *Server) handlePluginMarketplaceRemove(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := plugins.RemoveMarketplace(body.Name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handlePluginInstall installs a bundle: a built-in catalog plugin (name), or a
// git-marketplace plugin (name + marketplace). Either way it surfaces every
// part live (re-attach skills/subagents, reconnect MCP, refresh commands).
func (s *Server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name        string `json:"name"`
		Marketplace string `json:"marketplace"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	wd := sess.agent.Config().WorkingDir

	var summary, label string
	var err error
	reconnectMCP := true
	if body.Marketplace != "" {
		summary, err = plugins.InstallFromMarketplace(body.Name, body.Marketplace, wd)
		label = body.Name + "@" + body.Marketplace
	} else {
		summary, err = plugins.Install(body.Name, true, wd)
		label = body.Name
		reconnectMCP = pluginHasMCP(body.Name)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.refreshAfterPlugin(wd, sess, reconnectMCP)
	sess.publishEvent(wireEvent{Type: "info", Text: "Installed plugin " + label + ": " + summary})
	writeJSON(w, map[string]any{"installed": true})
}

// handlePluginRemove removes a built-in (name) or marketplace (name+marketplace)
// plugin and refreshes the same live state as install.
func (s *Server) handlePluginRemove(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r)
	if sess == nil || sess.agent == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name        string `json:"name"`
		Marketplace string `json:"marketplace"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	wd := sess.agent.Config().WorkingDir

	var summary, label string
	var err error
	if body.Marketplace != "" {
		summary, err = plugins.RemoveFromMarketplace(body.Name, body.Marketplace, wd)
		label = body.Name + "@" + body.Marketplace
	} else {
		summary, err = plugins.Remove(body.Name, wd)
		label = body.Name
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.refreshAfterPlugin(wd, sess, true)
	sess.publishEvent(wireEvent{Type: "info", Text: "Removed plugin " + label + ": " + summary})
	writeJSON(w, map[string]any{"ok": true})
}

// pluginHasMCP reports whether a built-in catalog plugin bundles any MCP servers
// (so we only reconnect MCP when needed).
func pluginHasMCP(name string) bool {
	e, ok := plugins.CatalogEntryByName(name)
	return ok && len(e.MCP) > 0
}

// refreshAfterPlugin re-attaches skills/subagents, optionally reconnects MCP, and
// refreshes the command palette so bundled /commands appear — shared by install
// and remove (HTTP + slash command). It does not publish a summary; callers do.
func (s *Server) refreshAfterPlugin(wd string, sess *session, reconnectMCP bool) {
	s.reattachSkills(wd, sess)
	s.reattachSubAgents(wd, sess)
	if reconnectMCP {
		go func() { sess.publishEvent(wireEvent{Type: "info", Text: s.connectMCP(wd, sess)}) }()
	}
	sess.publishEvent(s.commandsEvent(sess)) // refresh the palette with bundled /commands
}
