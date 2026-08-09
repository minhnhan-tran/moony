// commands_config.go — per-session config-mutating slash handlers (skills/agents/plugins/mcp/lsp/workers/provider/permissions/schedule/plan/goal).

package web

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/plugins"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/schedule"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/worker"
)

// mirroring the terminal's /permissions. With no argument it prints the active
// allow/deny lists; allow|deny|remove <rule> [--global] edits them (project
// scope by default, global with --global/-g) and re-lists.
func permissionsCommand(sess *session, arg string) string {
	if sess == nil || sess.agent == nil {
		return "Permission rules are unavailable in this session."
	}
	rules := sess.agent.Permissions()
	if rules == nil {
		return "Permission rules are unavailable in this session."
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return permissionsList(rules)
	}

	// Detect a --global / -g flag anywhere in the args and strip it.
	scope := permissions.Project
	kept := fields[:0]
	for _, a := range fields {
		if a == "--global" || a == "-g" {
			scope = permissions.Global
			continue
		}
		kept = append(kept, a)
	}
	args := kept
	if len(args) == 0 {
		return permissionsList(rules)
	}

	action := strings.ToLower(args[0])
	rule := strings.TrimSpace(strings.Join(args[1:], " "))
	if rule == "" {
		return "Usage: /permissions [allow|deny|remove <rule>] [--global]\n" +
			"A rule is a tool name (e.g. write_file) or a bash prefix (e.g. \"bash:git \")."
	}

	scopeName := "project"
	if scope == permissions.Global {
		scopeName = "global"
	}
	switch action {
	case "allow":
		if err := rules.Add(rule, false, scope); err != nil {
			return "Failed to add allow rule: " + err.Error()
		}
		return fmt.Sprintf("Added %s allow rule: %q\n\n%s", scopeName, rule, permissionsList(rules))
	case "deny":
		if err := rules.Add(rule, true, scope); err != nil {
			return "Failed to add deny rule: " + err.Error()
		}
		return fmt.Sprintf("Added %s deny rule: %q\n\n%s", scopeName, rule, permissionsList(rules))
	case "remove", "rm", "delete":
		if err := rules.Remove(rule, scope); err != nil {
			return "Failed to remove rule: " + err.Error()
		}
		return fmt.Sprintf("Removed %s rule: %q\n\n%s", scopeName, rule, permissionsList(rules))
	default:
		return "Unknown sub-command " + action + ". Use allow, deny, or remove."
	}
}

// permissionsList renders the active allow/deny rules.
func permissionsList(rules *permissions.Rules) string {
	allow, deny := rules.Allowed(), rules.Denied()
	var b strings.Builder
	b.WriteString("Permission rules (deny wins over allow; checked before the approval policy):\n")
	if len(deny) == 0 && len(allow) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, r := range deny {
		fmt.Fprintf(&b, "  deny   %s\n", r)
	}
	for _, r := range allow {
		fmt.Fprintf(&b, "  allow  %s\n", r)
	}
	b.WriteString("\nAdd with /permissions allow|deny <rule> [--global]; a rule is a tool name\n")
	b.WriteString("(e.g. write_file) or a bash command prefix (e.g. \"bash:git \", \"bash:rm -rf\").")
	return b.String()
}

// planCommand mirrors the terminal's /plan, managing the session's plan artifact
// and read-only plan mode from the web. Bare /plan enters plan mode; approve
// flips to build mode and executes the plan; clear discards it; show prints the
// status; any other text is treated as a description to draft a plan for. (The
// model can also drive the approve step itself via the exit_plan tool, which
// prompts for the same confirmation.)
func (s *Server) planCommand(sess *session, arg string, info func(string)) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		sess.agent.SetPlanMode(true)
		info("Plan mode ON (read-only) — write_file, edit_file, multi_edit, apply_patch, bash, and task are disabled. Draft a plan, then /plan approve to execute (or /build to leave).")
		sess.publishEvent(s.statusEvent(nil, sess))
		return
	}
	switch strings.ToLower(fields[0]) {
	case "approve", "ok", "go", "accept", "build", "execute":
		if !sess.agent.ApprovePlan() {
			info("No plan to approve. Use /plan <description> to draft one first.")
			return
		}
		sess.agent.SetPlanMode(false)
		info("Plan approved — executing it now. Mutating tools are enabled again.")
		// Surface the now-approved plan so the UIs update: the web Tasks-bar gate
		// only reveals the todos once the plan leaves "draft", and the plan bar's
		// status badge needs to flip to "approved". (The exit_plan path emits this
		// via PlanUpdated; the slash-command path must publish it explicitly.)
		sess.publishEvent(wireEvent{Type: "plan", Plan: planWireFrom(sess.agent.Plan())})
		sess.publishEvent(s.statusEvent(nil, sess))
		sess.start(agent.PlanExecutePrompt, nil, nil)
	case "clear", "off", "none", "remove", "discard", "cancel":
		sess.agent.ClearPlan()
		info("Plan cleared.")
		sess.publishEvent(s.statusEvent(nil, sess))
	case "show", "status", "view":
		info(sess.agent.PlanStatusLine())
	case "auto":
		cfg := sess.agent.Config()
		switch strings.ToLower(strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))) {
		case "", "show", "status":
			if cfg.AutoPlan {
				info("Auto-plan is ON — large/multi-step tasks auto-enter plan mode. /plan auto off to disable.")
			} else {
				info("Auto-plan is OFF. /plan auto on makes large/multi-step tasks auto-enter plan mode first.")
			}
		case "on", "true", "yes", "1", "enable":
			cfg.AutoPlan = true
			saveCfg(cfg, info, "Auto-plan ON — large/multi-step tasks will auto-enter plan mode.")
		case "off", "false", "no", "0", "disable":
			cfg.AutoPlan = false
			saveCfg(cfg, info, "Auto-plan OFF.")
		default:
			info("Usage: /plan auto [on|off]")
		}
	default:
		sess.agent.SetPlanMode(true)
		info("Plan mode ON (read-only) — drafting a plan. Review it, then /plan approve to execute (or /plan clear to discard).")
		sess.publishEvent(s.statusEvent(nil, sess))
		sess.start(agent.PlanDraftPrompt(strings.TrimSpace(arg)), nil, nil)
	}
}

// goalCommand mirrors the terminal's /goal: manage the session stop-condition,
// its completion criteria, and an optional token budget. Empty shows status;
// clear removes it; budget <n|off> caps effort; criteria <a; b; c> overrides the
// auto-proposed criteria; any other text sets the objective.
func (s *Server) goalCommand(sess *session, arg string, info func(string)) {
	if arg == "" {
		info(sess.agent.GoalStatus())
		return
	}
	fields := strings.Fields(arg)
	rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
	switch strings.ToLower(fields[0]) {
	case "clear", "off", "none", "remove":
		sess.agent.SetGoal("")
		info("Goal cleared — the agent stops normally.")
	case "budget":
		switch {
		case rest == "":
			info(sess.agent.GoalStatus())
		case rest == "off" || rest == "none" || rest == "0":
			sess.agent.SetGoalBudget(0)
			info("Goal token budget removed.")
		default:
			n, err := config.ParseTokenCount(rest)
			if err != nil || n <= 0 {
				info("Usage: /goal budget <tokens|200k|off>")
				return
			}
			sess.agent.SetGoalBudget(n)
			info(fmt.Sprintf("Goal token budget set to %d — the agent stops once that's spent pursuing the goal.", n))
		}
	case "criteria", "criterion":
		if rest == "" {
			info(sess.agent.GoalStatus())
			return
		}
		sess.agent.SetGoalCriteria(agent.SplitCriteria(rest))
		info(sess.agent.GoalStatus())
	default:
		sess.agent.SetGoal(arg)
		info("Goal set. The agent won't stop until a judge confirms it's met; completion criteria are auto-proposed on the first check (override with /goal criteria, cap effort with /goal budget):\n  " + arg)
	}
}

// scheduleList renders the current schedules (or a hint when there are none).
func (s *Server) scheduleList(st *schedule.Store) string {
	list, err := st.Load()
	if err != nil {
		return "schedule: " + err.Error()
	}
	if out := schedule.FormatList(list); out != "" {
		return out
	}
	return "No schedules yet. Add one with: /schedule add <every 30m|daily 09:00|once 2026-07-01T09:00> <prompt>"
}

// scheduleCommand handles the text sub-commands of /schedule for the web UI (the
// no-arg case opens a clickable menu via emitSchedulesMenu): add, list, remove,
// enable, disable, run.
func (s *Server) scheduleCommand(sess *session, arg string, info func(string)) {
	st, err := schedule.Open()
	if err != nil {
		info("schedule: " + err.Error())
		return
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		info(s.scheduleList(st))
		return
	}
	switch strings.ToLower(fields[0]) {
	case "list", "ls":
		info(s.scheduleList(st))
	case "add":
		// Grammar: add <kind> <when> <prompt…> — e.g. "add every 30m run the tests".
		if len(fields) < 4 {
			info("usage: /schedule add <every 30m|daily 09:00|once 2026-07-01T09:00> <prompt>")
			return
		}
		sc, err := schedule.ParseSpec(fields[1] + " " + fields[2])
		if err != nil {
			info("schedule: " + err.Error())
			return
		}
		sc.Prompt = strings.TrimSpace(strings.Join(fields[3:], " "))
		sc.WorkingDir = sess.agent.Config().WorkingDir
		added, err := st.Add(sc)
		if err != nil {
			info("schedule: " + err.Error())
			return
		}
		info(fmt.Sprintf("Added schedule %s — %s — next run %s", added.ID, added.Describe(), added.NextRun.Format(time.RFC1123)))
	case "remove", "rm", "delete":
		if len(fields) < 2 {
			info("usage: /schedule remove <id>")
			return
		}
		if err := st.Remove(fields[1]); err != nil {
			info("schedule: " + err.Error())
			return
		}
		info("Schedule " + fields[1] + " removed.")
	case "enable", "disable":
		if len(fields) < 2 {
			info("usage: /schedule " + strings.ToLower(fields[0]) + " <id>")
			return
		}
		on := strings.ToLower(fields[0]) == "enable"
		if err := st.SetEnabled(fields[1], on); err != nil {
			info("schedule: " + err.Error())
			return
		}
		state := "disabled"
		if on {
			state = "enabled"
		}
		info(fmt.Sprintf("Schedule %s %s.", fields[1], state))
	case "run":
		if len(fields) < 2 {
			info("usage: /schedule run <id>")
			return
		}
		sc, ok := st.Get(fields[1])
		if !ok {
			info("schedule: not found: " + fields[1])
			return
		}
		exe, err := os.Executable()
		if err != nil {
			info("schedule: " + err.Error())
			return
		}
		info(fmt.Sprintf("Running schedule %s in the background — it'll appear as a session; see /schedule for status.", sc.ID))
		// A headless run can take minutes; run it off the request path and report
		// the outcome back to the page when it finishes.
		go func() {
			sid, runErr := schedule.Run(context.Background(), sc, exe)
			cur, ok := st.Get(sc.ID)
			if !ok {
				return
			}
			cur.LastRun = time.Now()
			if runErr != nil {
				cur.LastStatus = "error: " + runErr.Error()
			} else {
				cur.LastStatus = "ok (session " + sid + ")"
			}
			_ = st.Update(cur)
			info(fmt.Sprintf("Schedule %s finished: %s", sc.ID, cur.LastStatus))
		}()
	default:
		info("usage: /schedule [add <spec> <prompt>|list|remove|enable|disable|run <id>]")
	}
}

// skillsCommand handles /skills: list with on/off markers, or enable/disable a
// skill (re-attaching the enabled set to the session's agent live).
func (s *Server) skillsCommand(sess *session, arg string) string {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	reattach := func() {
		sess.agent.AttachSkills(manage.Filter(skills.Load(cfg.WorkingDir), func(sk skills.Skill) string { return sk.Name }, mgmt.SkillEnabled))
	}
	f := strings.Fields(arg)
	if len(f) >= 1 {
		switch f[0] {
		case "enable", "disable":
			if len(f) < 2 {
				return "usage: /skills " + f[0] + " <name> [--global]"
			}
			on := f[0] == "enable"
			scope := webScope(f)
			if err := mgmt.SetSkill(f[1], on, scope); err != nil {
				return "could not update skill: " + err.Error()
			}
			reattach()
			return fmt.Sprintf("Skill %q %s (%s).", f[1], enabledWord(on), scope)
		case "new":
			if len(f) < 2 {
				return "usage: /skills new <name> [--global]"
			}
			path, err := skills.Scaffold(f[1], webScope(f) == manage.Global, cfg.WorkingDir)
			if err != nil {
				return "could not create skill: " + err.Error()
			}
			reattach()
			return "Created skill at " + path + " — edit its SKILL.md, then it's ready to load."
		case "remove":
			if len(f) < 2 {
				return "usage: /skills remove <name>"
			}
			path, err := skills.Remove(f[1], cfg.WorkingDir)
			if err != nil {
				return "could not remove skill: " + err.Error()
			}
			reattach()
			return "Removed skill at " + path
		}
	}
	found := skills.Load(cfg.WorkingDir)
	if len(found) == 0 {
		return "No skills found. Add one with /skills new <name>, or place a SKILL.md under ~/.claude/skills or .claude/skills (MN_SKILLS=0 disables discovery)."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Skills (%d):\n", len(found))
	for _, sk := range found {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", onOff(mgmt.SkillEnabled(sk.Name)), sk.Name, sk.Description)
	}
	b.WriteString("\n/skills enable|disable <name> [--global] · /skills new <name> · /skills remove <name>")
	return b.String()
}

// agentsCommand handles /agents enable|disable|new|remove (the no-arg listing
// opens a clickable menu via emitAgentsMenu).
func (s *Server) agentsCommand(sess *session, arg string) string {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	reattach := func() {
		sess.agent.AttachSubAgents(manage.Filter(subagents.Load(cfg.WorkingDir), func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled))
	}
	f := strings.Fields(arg)
	if len(f) >= 1 {
		switch f[0] {
		case "enable", "disable":
			if len(f) < 2 {
				return "usage: /agents " + f[0] + " <name> [--global]"
			}
			on := f[0] == "enable"
			scope := webScope(f)
			if err := mgmt.SetAgent(f[1], on, scope); err != nil {
				return "could not update agent: " + err.Error()
			}
			reattach()
			return fmt.Sprintf("Agent %q %s (%s).", f[1], enabledWord(on), scope)
		case "new":
			if len(f) < 2 {
				return "usage: /agents new <name> [--global]"
			}
			path, err := subagents.Scaffold(f[1], webScope(f) == manage.Global, cfg.WorkingDir)
			if err != nil {
				return "could not create agent: " + err.Error()
			}
			reattach()
			return "Created agent at " + path + " — edit its description, tools, and system prompt."
		case "remove":
			if len(f) < 2 {
				return "usage: /agents remove <name>"
			}
			path, err := subagents.Remove(f[1], cfg.WorkingDir)
			if err != nil {
				return "could not remove agent: " + err.Error()
			}
			reattach()
			return "Removed agent at " + path
		}
	}
	found := subagents.Load(cfg.WorkingDir)
	if len(found) == 0 {
		return "No subagents found. Add one with /agents new <name>, or place a .md under ~/.config/moony/agents or .moony/agents."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Subagents (%d):\n", len(found))
	for _, p := range found {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", onOff(mgmt.AgentEnabled(p.Name)), p.Name, p.Description)
	}
	b.WriteString("\n/agents enable|disable <name> [--global] · /agents new <name> · /agents remove <name>")
	return b.String()
}

// pluginsCommand handles /plugins: list bundles, or install/remove one. Install
// and remove fan out into the skills/agents/mcp/command primitives and refresh
// the live session so every bundled part takes effect immediately.
func (s *Server) pluginsCommand(sess *session, arg string) string {
	wd := sess.agent.Config().WorkingDir
	f := strings.Fields(arg)
	if len(f) >= 1 {
		switch f[0] {
		case "marketplace", "market", "mp":
			return s.pluginsMarketplaceCommand(f[1:])
		case "install", "add":
			if len(f) < 2 {
				return "usage: /plugins install <name>[@<marketplace>]"
			}
			name, market := plugins.SplitRef(f[1])
			var summary string
			var err error
			if market != "" {
				summary, err = plugins.InstallFromMarketplace(name, market, wd)
			} else {
				summary, err = plugins.Install(name, true, wd)
			}
			if err != nil {
				return "could not install plugin: " + err.Error()
			}
			s.refreshAfterPlugin(wd, sess, true)
			return fmt.Sprintf("Installed plugin %q: %s", f[1], summary)
		case "remove", "rm", "delete":
			if len(f) < 2 {
				return "usage: /plugins remove <name>[@<marketplace>]"
			}
			name, market := plugins.SplitRef(f[1])
			var summary string
			var err error
			if market != "" {
				summary, err = plugins.RemoveFromMarketplace(name, market, wd)
			} else {
				summary, err = plugins.Remove(name, wd)
			}
			if err != nil {
				return "could not remove plugin: " + err.Error()
			}
			s.refreshAfterPlugin(wd, sess, true)
			return fmt.Sprintf("Removed plugin %q: %s", f[1], summary)
		}
	}
	return pluginsListText(wd)
}

// pluginsMarketplaceCommand handles `/plugins marketplace <add|list|remove|update>`.
func (s *Server) pluginsMarketplaceCommand(f []string) string {
	if len(f) == 0 {
		return pluginsMarketplaceListText()
	}
	switch f[0] {
	case "add":
		if len(f) < 2 {
			return "usage: /plugins marketplace add <git-url> [name]"
		}
		name := ""
		if len(f) >= 3 {
			name = f[2]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m, err := plugins.AddMarketplace(ctx, name, f[1])
		if err != nil {
			return "could not add marketplace: " + err.Error()
		}
		return fmt.Sprintf("Added marketplace %q (%s). Run /plugins to see and install its plugins.", m.Name, m.URL)
	case "list", "ls":
		return pluginsMarketplaceListText()
	case "remove", "rm":
		if len(f) < 2 {
			return "usage: /plugins marketplace remove <name>"
		}
		if err := plugins.RemoveMarketplace(f[1]); err != nil {
			return "could not remove marketplace: " + err.Error()
		}
		return fmt.Sprintf("Removed marketplace %q (installed plugins stay; remove them with /plugins remove <name>@%s).", f[1], f[1])
	case "update", "pull":
		if len(f) < 2 {
			return "usage: /plugins marketplace update <name>"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := plugins.UpdateMarketplace(ctx, f[1]); err != nil {
			return "could not update marketplace: " + err.Error()
		}
		return fmt.Sprintf("Updated marketplace %q.", f[1])
	}
	return "usage: /plugins marketplace add <git-url> [name] | list | remove <name> | update <name>"
}

// pluginsListText lists built-in bundles and marketplace plugins with state.
func pluginsListText(wd string) string {
	var b strings.Builder
	b.WriteString("Plugins — built-in bundles (skills/agents/commands/MCP):\n")
	for _, st := range plugins.StatusAll(wd) {
		state := "available"
		if st.Installed {
			state = "installed"
		} else if st.Present > 0 {
			state = fmt.Sprintf("partial %d/%d", st.Present, st.Components)
		}
		fmt.Fprintf(&b, "- [%s] %s — %s\n", state, st.Name, st.Desc)
	}
	if mps := plugins.MarketplacePlugins(wd); len(mps) > 0 {
		b.WriteString("\nFrom marketplaces:\n")
		for _, p := range mps {
			state := "available"
			if p.Installed {
				state = "installed"
			}
			fmt.Fprintf(&b, "- [%s] %s@%s — %s\n", state, p.Name, p.Marketplace, p.Description)
		}
	}
	b.WriteString("\n/plugins install <name>[@<marketplace>] · /plugins remove <name>[@<marketplace>] · /plugins marketplace add <git-url>")
	return b.String()
}

// pluginsMarketplaceListText lists the added git marketplaces.
func pluginsMarketplaceListText() string {
	mps := plugins.Marketplaces()
	if len(mps) == 0 {
		return "No plugin marketplaces added. Add one with /plugins marketplace add <git-url>."
	}
	var b strings.Builder
	b.WriteString("Plugin marketplaces:\n")
	for _, m := range mps {
		fmt.Fprintf(&b, "- %s — %s\n", m.Name, m.URL)
	}
	b.WriteString("\n/plugins marketplace add <git-url> [name] · remove <name> · update <name>")
	return b.String()
}

// workersCommand handles /workers: list workers or toggle/set-model/reset one.
// Toggles and model changes persist to workers.json and apply to the live
// worker manager.
func (s *Server) workersCommand(sess *session, arg string) string {
	mgr := sess.agent.Workers()
	if mgr == nil {
		return "Workers are unavailable in this session."
	}
	mgr.Detect()
	if mgr.Empty() {
		return "No workers available. Install Claude Code (`claude`) or Optimus (`optimus`), or add a worker to ./.moony/workers.json."
	}
	f := strings.Fields(arg)
	if len(f) >= 1 {
		switch f[0] {
		case "enable", "disable":
			if len(f) < 2 {
				return "usage: /workers " + f[0] + " <name>"
			}
			if err := mgr.SetEnabled(f[1], f[0] == "enable"); err != nil {
				return "could not update worker: " + err.Error()
			}
			return fmt.Sprintf("Worker %q %s.\n\n%s", f[1], enabledWord(f[0] == "enable"), workersList(mgr))
		case "model":
			if len(f) < 2 {
				return "usage: /workers model <name> <provider/model>"
			}
			model := strings.TrimSpace(strings.Join(f[2:], " "))
			if err := mgr.SetModel(f[1], model); err != nil {
				return "could not set model: " + err.Error()
			}
			if model == "" {
				return fmt.Sprintf("Cleared the model override for %q.\n\n%s", f[1], workersList(mgr))
			}
			return fmt.Sprintf("Worker %q model set to %s.\n\n%s", f[1], model, workersList(mgr))
		case "reset":
			if len(f) < 2 {
				return "usage: /workers reset <name>"
			}
			mgr.Reset(f[1])
			return fmt.Sprintf("Reset worker %q.", f[1])
		case "add":
			if len(f) < 3 {
				return "usage: /workers add <name> <claude|optimus|gemini> [provider/model]"
			}
			model := strings.TrimSpace(strings.Join(f[3:], " "))
			if _, err := mgr.Add(f[1], f[2], model, "", true); err != nil {
				return "could not add worker: " + err.Error()
			}
			return fmt.Sprintf("Added worker %q (%s).\n\n%s", f[1], f[2], workersList(mgr))
		case "remove", "rm", "delete":
			if len(f) < 2 {
				return "usage: /workers remove <name>"
			}
			if err := mgr.Remove(f[1]); err != nil {
				return "could not remove worker: " + err.Error()
			}
			return fmt.Sprintf("Removed worker %q.\n\n%s", f[1], workersList(mgr))
		}
	}
	return workersList(mgr)
}

// workersList renders the configured workers and their state.
func workersList(mgr *worker.Manager) string {
	ready := mgr.Readiness()
	var b strings.Builder
	b.WriteString("Worker agents (the model dispatches to enabled ones via delegate):\n")
	for _, c := range mgr.All() {
		model := c.Model
		if model == "" {
			model = "(default)"
		}
		fmt.Fprintf(&b, "- [%s] %s (%s, %s) model: %s\n", onOff(c.Enabled()), c.Name, c.Backend, ready[c.Name], model)
	}
	b.WriteString("\n/workers enable|disable <name> · /workers model <name> <provider/model> · /workers reset <name>")
	return b.String()
}

// mcpsCommand handles /mcps: list, enable/disable, or add/remove a server. The
// web server doesn't connect MCP yet, so changes apply when it does (or in the
// terminal app after a restart).
func (s *Server) mcpsCommand(sess *session, arg string) string {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	f := strings.Fields(arg)
	if len(f) >= 1 {
		switch f[0] {
		case "enable", "disable":
			if len(f) < 2 {
				return "usage: /mcps " + f[0] + " <name> [--global]"
			}
			on := f[0] == "enable"
			scope := webScope(f)
			if err := mgmt.SetMCP(f[1], on, scope); err != nil {
				return "could not update MCP server: " + err.Error()
			}
			return fmt.Sprintf("MCP server %q %s (%s). Run /mcps reload to apply.", f[1], enabledWord(on), scope)
		case "add":
			name, command, cmdArgs, url, global, err := mcp.ParseAddArgs(f[1:])
			if err != nil {
				return err.Error()
			}
			path, aerr := mcp.AddServer(name, command, cmdArgs, url, nil, global, cfg.WorkingDir)
			if aerr != nil {
				return "could not add server: " + aerr.Error()
			}
			return fmt.Sprintf("Added MCP server %q to %s. Run /mcps reload to connect it.", name, path)
		case "remove":
			if len(f) < 2 {
				return "usage: /mcps remove <name>"
			}
			path, err := mcp.RemoveServer(f[1], cfg.WorkingDir)
			if err != nil {
				return "could not remove server: " + err.Error()
			}
			return "Removed MCP server from " + path + ". Run /mcps reload to apply."
		}
	}
	cfgs, err := mcp.LoadConfigs(cfg.WorkingDir)
	if err != nil || len(cfgs) == 0 {
		return "No MCP servers configured. Add one with /mcps add <name> -- <cmd> [args] (or <name> <url>), or via your Claude config (auto-discovered; MN_MCP_CLAUDE=0 disables)."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Configured MCP servers (%d):\n", len(cfgs))
	for _, c := range cfgs {
		target := c.Command
		if c.URL != "" {
			target = c.URL
		}
		on := mgmt.MCPEnabled(c.Name)
		line := fmt.Sprintf("- [%s] %s — %s", onOff(on), c.Name, target)
		if st := sess.mcpStatusLabel(c.Name, on); st != "" {
			line += "  (" + st + ")"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n/mcps enable|disable <name> [--global] · /mcps add <name> -- <cmd>|<url> · /mcps remove <name> · /mcps reload")
	return b.String()
}

func (s *Server) connectMCP(workingDir string, live *session) string {
	// Whatever the outcome (connected, nothing configured, MCP disabled, or no
	// agent), the initial connect attempt is complete on return — unblock any
	// first turn waiting on MCP readiness.
	defer live.markMCPReady()
	if live.agent == nil {
		return "no agent for this session"
	}
	if !mcp.Enabled() {
		return "MCP is disabled (MN_MCP=0)."
	}
	live.mcpMu.Lock() // serialize connect/reload so two callers don't race the manager
	defer live.mcpMu.Unlock()
	mgmt := manage.Load(workingDir)
	cfgs, _ := mcp.LoadConfigs(workingDir)
	cfgs = manage.Filter(cfgs, func(c mcp.Config) string { return c.Name }, mgmt.MCPEnabled)
	// Capture the per-server connect trace (command spawn, tools/list,
	// resources/list, prompts/list) so a server that won't connect is
	// debuggable. Each line also goes to the server log sink (s.logf) for
	// operators — which the TUI-embedded server redirects away from os.Stderr so
	// it can't garble the alt-screen UI (see Server.SetLogf).
	var trace []string
	logf := s.logf
	if logf == nil {
		logf = defaultServerLog
	}
	logger := func(line string) {
		trace = append(trace, line)
		logf(line)
	}
	// Bound the connect/reload attempt: it runs while holding live.mcpMu, so a
	// server that hangs on startup (stuck npx install, unresponsive docker
	// container, etc.) would otherwise wedge every later config reload for this
	// session indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mgr := live.mcpManager()
	if mgr == nil {
		mgr = mcp.ConnectAll(ctx, cfgs, logger)
		mgr.RegisterTools(live.agent.Registry())
		live.setMCP(mgr)
	} else {
		mgr.Reload(ctx, cfgs, logger)
	}
	// Tools just changed in the registry; rebuild the system prompt so its tool
	// list and tool-specific guidance (e.g. codebase-memory) reflect them.
	live.agent.RefreshSystemPrompt()
	// Report failures (with their reasons) so a server that won't connect is
	// debuggable from the info toast, not just a hover tooltip in the panel.
	fails := mgr.Failures()
	if len(fails) == 0 {
		return fmt.Sprintf("MCP reloaded — %d server(s) connected.", len(mgr.Servers()))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MCP reloaded — %d connected, %d failed:", len(mgr.Servers()), len(fails))
	for _, f := range fails {
		fmt.Fprintf(&b, "\n  • %s — %s", f.Name, f.Err)
	}
	// Append the full connect trace (only when something failed) so optional-step
	// failures and ordering are visible right in the UI, not just the server log.
	if len(trace) > 0 {
		b.WriteString("\n\nConnect log:")
		for _, line := range trace {
			b.WriteString("\n  " + line)
		}
	}
	return b.String()
}

// lspCommand handles /lsp enable|disable <name>. Adding/removing servers is done
// by editing lsp.json; this manages enable/disable (persisted) and reload.
func (s *Server) lspCommand(sess *session, arg string) string {
	cfg := sess.agent.Config()
	mgmt := manage.Load(cfg.WorkingDir)
	f := strings.Fields(arg)
	if len(f) >= 1 {
		switch f[0] {
		case "enable", "disable":
			if len(f) < 2 {
				return "usage: /lsp " + f[0] + " <name> [--global]"
			}
			on := f[0] == "enable"
			scope := webScope(f)
			if err := mgmt.SetLSP(f[1], on, scope); err != nil {
				return "could not update language server: " + err.Error()
			}
			return fmt.Sprintf("Language server %q %s (%s). Run /lsp reload to apply.", f[1], enabledWord(on), scope)
		}
	}
	return "usage: /lsp [enable|disable <name>] [--global] | reload"
}

// connectLSP (re)starts this session's language servers from the current
// (manage-filtered) config and attaches them to the agent, closing the previous
// manager. Serialized per session so two reloads don't race.
func (s *Server) connectLSP(workingDir string, live *session) string {
	if live.agent == nil {
		return "no agent for this session"
	}
	live.lspMu.Lock()
	defer live.lspMu.Unlock()
	mgmt := manage.Load(workingDir)
	cfgs, _ := lsp.LoadConfigs(workingDir)
	cfgs = manage.Filter(cfgs, func(c lsp.Config) string { return c.Name }, mgmt.LSPEnabled)
	old := live.agent.LSP()
	mgr := lsp.Start(context.Background(), workingDir, cfgs, nil)
	live.agent.AttachLSP(mgr)
	if old != nil {
		old.Close()
	}
	return fmt.Sprintf("LSP reloaded — %d server(s) running.", len(mgr.Running()))
}

// providerCommand backs the web /provider command: list the available model
// providers, or set the active one. Setting persists the choice and mirrors it
// onto the shared client config (s.cfg) so it takes effect immediately, exactly
// like /model. Mirrors the terminal's providerCmd.
func (s *Server) providerCommand(cfg *config.Config, arg string, info func(string)) {
	active := provider.Normalize(cfg.LLMProvider)
	name := strings.ToLower(strings.TrimSpace(arg))
	if name == "" {
		info(provider.ListText(active))
		return
	}
	p, ok := provider.Lookup(name)
	if !ok {
		info(fmt.Sprintf("Unknown provider %q.\n\n%s", name, provider.ListText(active)))
		return
	}
	cfg.LLMProvider = p.ID
	if s.cfg != nil {
		s.cfg.LLMProvider = p.ID
	}
	// Move onto this provider's default model — a model id is backend-specific, so
	// keeping the old one would feed e.g. a Claude id to a local model (or vice-versa).
	// SetModel re-derives the route + reasoning level; mirror it onto s.cfg too
	// (the shared LLM client holds a pointer to it), exactly like /model.
	switched := ""
	if dm := provider.DefaultModelFor(p.ID); dm != "" && dm != cfg.Model {
		cfg.SetModel(dm)
		if s.cfg != nil {
			s.cfg.SetModel(dm)
		}
		switched = fmt.Sprintf(" — model set to %s", shortModel(dm))
	}
	msg := fmt.Sprintf("Provider set to %s%s", p.Label, switched)
	saveCfg(cfg, info, msg)
	if hint := providerSetupHintWeb(cfg, p.ID); hint != "" {
		info(hint)
	}
	info(provider.ListText(p.ID))
}

// providerSetupHintWeb returns a setup reminder for a freshly selected hosted
// provider whose API key is missing.
func providerSetupHintWeb(cfg *config.Config, id string) string {
	switch id {
	case provider.OpenAI:
		if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
			return "⚠ Set MN_OPENAI_API_KEY (or OPENAI_API_KEY) — calls will fail until it's set."
		}
	case provider.Anthropic:
		if strings.TrimSpace(cfg.AnthropicAPIKey) == "" {
			return "⚠ Set MN_ANTHROPIC_API_KEY (or ANTHROPIC_API_KEY) — calls will fail until it's set."
		}
	case provider.OpenRouter:
		if strings.TrimSpace(cfg.OpenRouterAPIKey) == "" {
			return "⚠ Set MN_OPENROUTER_API_KEY (or OPENROUTER_API_KEY) — calls will fail until it's set."
		}
	}
	return ""
}

// ensureOllamaModel makes sure the session's model is one the Ollama server
// actually serves after a switch to the Ollama provider. It fetches the server's
// installed models; if the current model isn't among them, it switches to the
// first served model. Best-effort: on a fetch error it leaves the model as-is and
// nudges the user to set one with /models (so an unreachable server still gives a
// clear message rather than silently failing on the first turn).
func (s *Server) ensureOllamaModel(sess *session, cfg *config.Config, info func(string)) {
	models, err := sess.agent.ListModels(context.Background())
	if err != nil || len(models) == 0 {
		info("Pick an Ollama model with /models once your server is reachable (MN_OLLAMA_HOST).")
		return
	}
	for _, m := range models {
		if m.ID == cfg.Model {
			return // current model is served — keep it
		}
	}
	pick := models[0].ID
	cfg.SetModel(pick)
	if s.cfg != nil {
		s.cfg.SetModel(pick)
	}
	saveCfg(cfg, info, "Model set to "+pick)
}
