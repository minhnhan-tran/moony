// commands_menus.go — emit*Menu helpers that push picker/menu events to the web UI.

package web

import (
	"context"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/plugins"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/schedule"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
)

// emitSchedulesMenu shows scheduled jobs as a clickable picker. Each row's value
// flips its enabled state (toggleValue), so clicking a job enables/disables it;
// add/remove/run stay on the /schedule text sub-commands.
func (s *Server) emitSchedulesMenu(sess *session) {
	st, err := schedule.Open()
	if err != nil {
		sess.publishEvent(wireEvent{Type: "info", Text: "schedule: " + err.Error()})
		return
	}
	list, err := st.Load()
	if err != nil {
		sess.publishEvent(wireEvent{Type: "info", Text: "schedule: " + err.Error()})
		return
	}
	if len(list) == 0 {
		sess.publishEvent(wireEvent{Type: "info", Text: "No schedules yet. Add one with: /schedule add <every 30m|daily 09:00|once 2026-07-01T09:00> <prompt>"})
		return
	}
	items := make([]menuItemWire, 0, len(list))
	for _, sc := range list {
		items = append(items, menuItemWire{Label: sc.Summary(), Desc: sc.ID, Value: toggleValue(sc.Enabled, sc.ID), Current: sc.Enabled})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Schedules — click to toggle", Command: "/schedule", Items: items}})
}

func (s *Server) emitModelsMenu(sess *session) {
	go func() {
		models, err := sess.agent.ListModels(context.Background())
		cfg := sess.agent.Config()
		if err != nil {
			// On failure, fall back to a provider-scoped list (never the wrong
			// providers models) so the picker is never empty.
			sess.publishEvent(wireEvent{Type: "info", Text: config.ModelFetchNote(err)})
			models = config.ModelFallback(cfg.LLMProvider)
		}
		items := make([]menuItemWire, 0, len(models))
		for _, m := range models {
			label := shortModel(m.ID)
			// Show the pretty name (Label, via prettyModel client-side) over the raw
			// model id (Desc). The endpoint display_name (m.Name) is redundant with the
			// pretty name for Claude and empty for Gemini, so the id is more useful here.
			items = append(items, menuItemWire{Label: label, Desc: m.ID, Value: m.ID, Current: m.ID == cfg.Model})
		}
		sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Select a model", Command: "/model", Items: items}})
	}()
}

func (s *Server) emitReasoningMenu(sess *session) {
	cfg := sess.agent.Config()
	cur := cfg.GetReasoningFor(cfg.Model)
	levels := config.ReasoningLevelsFor(cfg.Model)
	items := make([]menuItemWire, 0, len(levels))
	for _, l := range levels {
		items = append(items, menuItemWire{Label: l, Value: l, Current: l == cur})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Select a reasoning level", Command: "/reasoning", Items: items}})
}

// emitProviderMenu offers the model providers (Ollama / OpenAI / Anthropic / …)
// as a clickable picker; choosing one round-trips
// "/provider <id>" back through runCommand (parity with the TUI's /provider).
func (s *Server) emitProviderMenu(sess *session) {
	cur := provider.Normalize(sess.agent.Config().LLMProvider)
	items := make([]menuItemWire, 0, len(provider.List()))
	for _, p := range provider.List() {
		items = append(items, menuItemWire{Label: p.Label, Desc: p.Desc, Value: p.ID, Current: p.ID == cur})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Select a provider", Command: "/provider", Items: items}})
}

// emitAgentMenu offers the selectable primary agent profiles (build / plan plus
// any primary-capable personas) as a picker; choosing one round-trips
// "/agent <name>" back through runCommand (parity with the TUI's /agent).
func (s *Server) emitAgentMenu(sess *session) {
	cur := sess.agent.ActiveAgent().Name
	names := sess.agent.AgentNames()
	items := make([]menuItemWire, 0, len(names))
	for _, n := range names {
		desc := ""
		switch strings.ToLower(n) {
		case config.AgentBuild:
			desc = "full access (read/write/run)"
		case config.AgentPlan:
			desc = "read-only planning mode"
		}
		items = append(items, menuItemWire{Label: n, Desc: desc, Value: n, Current: strings.EqualFold(n, cur)})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Select an agent", Command: "/agent", Items: items}})
}

// emitSkillsMenu shows the discovered skills as a clickable picker, like the
// route menu. Each row's value flips its state, so clicking an enabled skill
// disables it and vice-versa; the on/off state shows via the menu's checkmark.
// With no skills discovered it falls back to the same guidance info text the
// text listing used.
func (s *Server) emitSkillsMenu(sess *session) {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	found := skills.Load(cfg.WorkingDir)
	if len(found) == 0 {
		sess.publishEvent(wireEvent{Type: "info", Text: "No skills found. Add one with /skills new <name>, or place a SKILL.md under ~/.claude/skills or .claude/skills (MN_SKILLS=0 disables discovery)."})
		return
	}
	items := make([]menuItemWire, 0, len(found))
	for _, sk := range found {
		on := mgmt.SkillEnabled(sk.Name)
		items = append(items, menuItemWire{Label: sk.Name, Desc: sk.Description, Value: toggleValue(on, sk.Name), Current: on})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Skills — click to toggle", Command: "/skills", Items: items}})
}

// emitAgentsMenu shows the subagent personas as a clickable toggle picker.
func (s *Server) emitAgentsMenu(sess *session) {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	found := subagents.Load(cfg.WorkingDir)
	if len(found) == 0 {
		sess.publishEvent(wireEvent{Type: "info", Text: "No subagents found. Add one with /agents new <name>, or place a .md under ~/.config/moony/agents or .moony/agents."})
		return
	}
	items := make([]menuItemWire, 0, len(found))
	for _, p := range found {
		on := mgmt.AgentEnabled(p.Name)
		desc := p.Description
		if len(p.Tools) > 0 {
			desc += "  ·  tools: " + strings.Join(p.Tools, ", ")
		}
		items = append(items, menuItemWire{Label: p.Name, Desc: desc, Value: toggleValue(on, p.Name), Current: on})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Subagents — click to toggle", Command: "/agents", Items: items}})
}

// emitWorkersMenu shows the worker agents as a clickable toggle picker; clicking
// a row flips its enabled state. Set a worker's model with /workers model.
func (s *Server) emitWorkersMenu(sess *session) {
	mgr := sess.agent.Workers()
	if mgr == nil {
		sess.publishEvent(wireEvent{Type: "info", Text: "Workers are unavailable in this session."})
		return
	}
	mgr.Detect()
	all := mgr.All()
	if len(all) == 0 {
		sess.publishEvent(wireEvent{Type: "info", Text: "No workers available. Install Claude Code (`claude`) or Optimus (`optimus`), or add a worker to ./.moony/workers.json."})
		return
	}
	ready := mgr.Readiness()
	items := make([]menuItemWire, 0, len(all))
	for _, c := range all {
		model := c.Model
		if model == "" {
			model = "default model"
		}
		items = append(items, menuItemWire{Label: c.Name, Desc: fmt.Sprintf("%s · %s · %s", c.Backend, ready[c.Name], model), Value: toggleValue(c.Enabled(), c.Name), Current: c.Enabled()})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Workers — click to toggle (set model: /workers model <name> <provider/model>)", Command: "/workers", Items: items}})
}

// emitMcpsMenu shows the configured MCP servers as a clickable toggle picker.
// Like the skills menu, clicking a row flips its enabled state; apply with
// /mcps reload afterward.
func (s *Server) emitMcpsMenu(sess *session) {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	cfgs, err := mcp.LoadConfigs(cfg.WorkingDir)
	if err != nil || len(cfgs) == 0 {
		sess.publishEvent(wireEvent{Type: "info", Text: "No MCP servers configured. Add one with /mcps add <name> -- <cmd> [args] (or <name> <url>), or via your Claude config (auto-discovered; MN_MCP_CLAUDE=0 disables)."})
		return
	}
	items := make([]menuItemWire, 0, len(cfgs))
	for _, c := range cfgs {
		target := c.Command
		if c.URL != "" {
			target = c.URL
		}
		on := mgmt.MCPEnabled(c.Name)
		desc := target
		if st := sess.mcpStatusLabel(c.Name, on); st != "" {
			desc += "  ·  " + st
		}
		items = append(items, menuItemWire{Label: c.Name, Desc: desc, Value: toggleValue(on, c.Name), Current: on})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "MCP servers — click to toggle", Command: "/mcps", Items: items}})
}

// emitPluginsMenu shows the plugin bundles as a clickable picker; clicking a row
// installs an available bundle or removes an installed one.
func (s *Server) emitPluginsMenu(sess *session) {
	wd := sess.agent.Config().WorkingDir
	all := plugins.StatusAll(wd)
	items := make([]menuItemWire, 0, len(all))
	for _, st := range all {
		verb := "install"
		if st.Installed {
			verb = "remove"
		}
		desc := st.Desc
		if !st.Installed && st.Present > 0 {
			desc = fmt.Sprintf("%s  ·  %d/%d installed", desc, st.Present, st.Components)
		}
		items = append(items, menuItemWire{Label: st.Title, Desc: desc, Value: verb + " " + st.Name, Current: st.Installed})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Plugins — click to install/remove a bundle", Command: "/plugins", Items: items}})
}

// emitLspMenu shows the configured language servers as a clickable toggle
// picker; clicking a row flips its enabled state. Apply with /lsp reload.
func (s *Server) emitLspMenu(sess *session) {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	cfgs, err := lsp.LoadConfigs(cfg.WorkingDir)
	if err != nil || len(cfgs) == 0 {
		sess.publishEvent(wireEvent{Type: "info", Text: "No language servers configured. Add one to ~/.config/moony/lsp.json or ./.moony/lsp.json, then /lsp reload."})
		return
	}
	running := map[string]bool{}
	if sess.agent != nil {
		for _, n := range sess.agent.LSP().Running() {
			running[n] = true
		}
	}
	items := make([]menuItemWire, 0, len(cfgs))
	for _, c := range cfgs {
		on := mgmt.LSPEnabled(c.Name)
		desc := strings.Join(c.Extensions, " ")
		switch {
		case !on:
			desc = "disabled · " + desc
		case running[c.Name]:
			desc = "running · " + desc
		default:
			desc = "not running · " + desc
		}
		items = append(items, menuItemWire{Label: c.Name, Desc: desc, Value: toggleValue(on, c.Name), Current: on})
	}
	sess.publishTransient(wireEvent{Type: "menu", Menu: &menuWire{Title: "Language servers — click to toggle", Command: "/lsp", Items: items}})
}
