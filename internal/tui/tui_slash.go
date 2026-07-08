// tui_slash.go — slash-command handlers dispatched by command().

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/domains"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/plugins"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/worker"
)

// saveConfig persists the current model/reasoning preferences and returns a
// status line combining the action and any save error.
func (m Model) saveConfig(action string) string {
	// The system prompt bakes in config-derived facts (notably the "- Model: …
	// (served via …)" identity line, which depends on both the model and the
	// selected provider). It's cached on the agent and only rebuilt on explicit
	// refresh, so regenerate it here — every persisted config change (/model,
	// /provider, /reasoning, …) routes through saveConfig — to keep the prompt
	// from going stale mid-session (e.g. so /provider <name> stops the model from
	// self-identifying as its previous backend). Safe between turns; the running turn
	// captured a.system at its start.
	if m.agent != nil {
		m.agent.RefreshSystemPrompt()
	}
	if err := m.cfg.Save(); err != nil {
		return action + " (could not persist: " + err.Error() + ")"
	}
	return action + " — saved."
}

// workersCmd handles `/workers [enable|disable|model|reset|add|remove <name> …]`.
// With no sub-command it lists the configured workers; otherwise it toggles a
// worker, sets its model, resets its session, or adds/removes a worker —
// persisting changes to workers.json and applying them live.
func (m Model) workersCmd(fields []string) string {
	mgr := m.agent.Workers()
	if mgr == nil {
		return "Workers are unavailable in this session."
	}
	mgr.Detect() // pick up any engines installed since startup
	if mgr.Empty() {
		return "No workers available. Install Claude Code (`claude`) or Optimus (`optimus`), or add a worker to ./.sqe/workers.json (see the README's \"Worker agents\" section)."
	}
	if len(fields) < 2 {
		return workersList(mgr)
	}
	action := strings.ToLower(fields[1])
	switch action {
	case "enable", "disable":
		if len(fields) < 3 {
			return "Usage: /workers " + action + " <name>"
		}
		name := fields[2]
		if err := mgr.SetEnabled(name, action == "enable"); err != nil {
			return "Failed to " + action + " worker: " + err.Error()
		}
		return fmt.Sprintf("Worker %q %sd.\n\n%s", name, action, workersList(mgr))
	case "model":
		if len(fields) < 3 {
			return "Usage: /workers model <name> <provider/model>  (omit the model to clear the override)"
		}
		name := fields[2]
		model := strings.TrimSpace(strings.Join(fields[3:], " "))
		if err := mgr.SetModel(name, model); err != nil {
			return "Failed to set model: " + err.Error()
		}
		if model == "" {
			return fmt.Sprintf("Cleared the model override for %q (it uses the worker's default now).\n\n%s", name, workersList(mgr))
		}
		return fmt.Sprintf("Worker %q model set to %s.\n\n%s", name, model, workersList(mgr))
	case "reset":
		if len(fields) < 3 {
			return "Usage: /workers reset <name>"
		}
		mgr.Reset(fields[2])
		return fmt.Sprintf("Reset worker %q — its next dispatch starts a fresh session.", fields[2])
	case "add":
		if len(fields) < 4 {
			return "Usage: /workers add <name> <claude|optimus|apple-gemini> [provider/model]"
		}
		name, backend := fields[2], strings.ToLower(fields[3])
		model := strings.TrimSpace(strings.Join(fields[4:], " "))
		if _, err := mgr.Add(name, backend, model, "", true); err != nil {
			return "Failed to add worker: " + err.Error()
		}
		return fmt.Sprintf("Added worker %q (%s).\n\n%s", name, backend, workersList(mgr))
	case "remove", "rm", "delete":
		if len(fields) < 3 {
			return "Usage: /workers remove <name>"
		}
		if err := mgr.Remove(fields[2]); err != nil {
			return "Failed to remove worker: " + err.Error()
		}
		return fmt.Sprintf("Removed worker %q.\n\n%s", fields[2], workersList(mgr))
	default:
		return "Unknown sub-command " + action + ". Use enable, disable, model, reset, add, or remove."
	}
}

// workersList renders the configured workers and their state.
func workersList(mgr *worker.Manager) string {
	ready := mgr.Readiness()
	var b strings.Builder
	b.WriteString("Worker agents (the model dispatches to enabled ones via the delegate tool):\n")
	for _, c := range mgr.All() {
		state := "on "
		if c.Disabled {
			state = "off"
		}
		model := c.Model
		if model == "" {
			model = "(default)"
		}
		fmt.Fprintf(&b, "  [%s] %-12s %-8s %-10s model: %s\n", state, c.Name, c.Backend, ready[c.Name], model)
	}
	b.WriteString("\n/workers enable|disable <name> · /workers model <name> <provider/model> · /workers reset <name>")
	return b.String()
}

// permissionsCmd handles `/permissions [allow|deny|remove <rule>] [--global]`.
// With no sub-command it lists the active rules; with one it adds/removes a
// rule (project scope by default, global with --global) and persists it.
func (m Model) permissionsCmd(fields []string) string {
	rules := m.agent.Permissions()
	if rules == nil {
		return "Permission rules are unavailable in this session."
	}
	if len(fields) < 2 {
		return permissionsList(rules)
	}

	// Detect a --global / -g flag anywhere after the sub-command and strip it.
	scope := permissions.Project
	args := fields[1:]
	kept := args[:0]
	for _, a := range args {
		if a == "--global" || a == "-g" {
			scope = permissions.Global
			continue
		}
		kept = append(kept, a)
	}
	args = kept
	if len(args) < 1 {
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

// providerCmd shows the available model providers, or sets the active one. The
// choice is persisted and takes effect immediately (the client routes by
// config).
func (m Model) providerCmd(fields []string) string {
	active := provider.Normalize(m.cfg.LLMProvider)
	if len(fields) < 2 {
		return provider.ListText(active)
	}
	name := strings.ToLower(fields[1])
	p, ok := provider.Lookup(name)
	if !ok {
		return fmt.Sprintf("Unknown provider %q.\n\n%s", name, provider.ListText(active))
	}
	m.cfg.LLMProvider = p.ID
	// Move onto this provider's default model — a model id is backend-specific, so
	// keeping the old one would feed e.g. a Claude id to Ollama (or vice-versa).
	// SetModel also re-derives the wire route + reasoning level for the new id.
	switched := ""
	if dm := provider.DefaultModelFor(p.ID); dm != "" && dm != m.cfg.Model {
		m.cfg.SetModel(dm)
		switched = fmt.Sprintf(" — model set to %s", shortModel(dm))
	}
	// Ollama has no curated default (server-specific ids like "gemma4:31b"), so
	// auto-pick a served model when the current one isn't on the server.
	if p.ID == provider.Ollama {
		if picked := m.ensureOllamaModel(); picked != "" {
			switched = fmt.Sprintf(" — model set to %s", shortModel(picked))
		}
	}
	msg := m.saveConfig(fmt.Sprintf("Provider set to %s%s", p.Label, switched))
	if hint := providerSetupHint(m.cfg, p.ID); hint != "" {
		msg += "\n" + hint
	}
	return msg + "\n\n" + provider.ListText(p.ID)
}

// providerSetupHint returns a one-line setup reminder for a freshly selected
// provider (e.g. a missing API key), or "" when it looks ready to use.
func providerSetupHint(cfg *config.Config, id string) string {
	switch id {
	case provider.Ollama:
		return "Ollama uses the server at MOONY_OLLAMA_HOST (or http://localhost:11434). Pick a model with /models."
	case provider.OpenAI:
		if strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
			return "⚠ Set MOONY_OPENAI_API_KEY (or OPENAI_API_KEY) — calls will fail until it's set."
		}
	case provider.Anthropic:
		if strings.TrimSpace(cfg.AnthropicAPIKey) == "" {
			return "⚠ Set MOONY_ANTHROPIC_API_KEY (or ANTHROPIC_API_KEY) — calls will fail until it's set."
		}
	case provider.OpenRouter:
		if strings.TrimSpace(cfg.OpenRouterAPIKey) == "" {
			return "⚠ Set MOONY_OPENROUTER_API_KEY (or OPENROUTER_API_KEY) — calls will fail until it's set."
		}
	}
	return ""
}

// domainsCmd lists or edits the global web_fetch domain rules.
func (m Model) domainsCmd(fields []string) string {
	list := domains.Load()
	if len(fields) < 2 {
		return domainsList(list)
	}

	action := strings.ToLower(fields[1])
	domain := strings.TrimSpace(strings.Join(fields[2:], " "))
	if domain == "" {
		return "Usage: /domains [allow|deny|remove <domain>]\n" +
			"A domain is a host such as apple.com (also matches its subdomains)."
	}

	switch action {
	case "allow":
		if err := list.Add(domain, false); err != nil {
			return "Failed to add allow rule: " + err.Error()
		}
		return fmt.Sprintf("Added allow domain: %q\n\n%s", domain, domainsList(list))
	case "deny":
		if err := list.Add(domain, true); err != nil {
			return "Failed to add deny rule: " + err.Error()
		}
		return fmt.Sprintf("Added deny domain: %q\n\n%s", domain, domainsList(list))
	case "remove", "rm", "delete":
		if err := list.Remove(domain); err != nil {
			return "Failed to remove domain: " + err.Error()
		}
		return fmt.Sprintf("Removed domain: %q\n\n%s", domain, domainsList(list))
	default:
		return "Unknown sub-command " + action + ". Use allow, deny, or remove."
	}
}

// domainsList renders the active web_fetch domain rules.
func domainsList(list *domains.List) string {
	allow, deny := list.Allowed(), list.Denied()
	var b strings.Builder
	b.WriteString("Web fetch domain rules (deny wins; if any allow exists, only allowed hosts are fetchable):\n")
	if len(deny) == 0 && len(allow) == 0 {
		b.WriteString("  (none — every host is allowed)\n")
	}
	for _, r := range deny {
		fmt.Fprintf(&b, "  deny   %s\n", r)
	}
	for _, r := range allow {
		fmt.Fprintf(&b, "  allow  %s\n", r)
	}
	b.WriteString("\nAdd with /domains allow|deny <domain>; a domain is a host such as apple.com\n")
	b.WriteString("(it also matches subdomains like docs.apple.com). Rules are saved globally.")
	return b.String()
}

// remember saves a note to memory (project, or global) under an optional
// category, and returns a status line; it does not send anything to the model.
func (m Model) remember(note, category string, global bool) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return "Nothing to remember. Use /memory <note> or start a line with #."
	}
	out, err := m.agent.Remember(note, category, global)
	if err != nil {
		return "Could not save memory: " + err.Error()
	}
	return out
}

// copyLast copies the most recent assistant response to the clipboard and
// returns a status message.
func (m Model) copyLast() string {
	for i := len(m.trans.entries) - 1; i >= 0; i-- {
		if m.trans.entries[i].kind == entryAssistant {
			text := m.trans.entries[i].text
			if err := clipboard.WriteAll(text); err != nil {
				return "Could not copy to clipboard: " + err.Error()
			}
			return fmt.Sprintf("Copied last response to clipboard (%d chars).", len(text))
		}
	}
	return "Nothing to copy yet."
}

// skillsCmd handles /skills: list with on/off markers, or enable/disable a skill
// (the enabled set is re-attached to the agent live). Scope defaults to project;
// --global targets the user-level setting.
func (m Model) skillsCmd(fields []string) string {
	mgmt := manage.Load(m.cfg.WorkingDir)
	reattach := func() {
		m.agent.AttachSkills(manage.Filter(skills.Load(m.cfg.WorkingDir), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))
	}
	if len(fields) >= 2 {
		switch fields[1] {
		case "enable", "disable":
			if len(fields) < 3 {
				return "usage: /skills " + fields[1] + " <name> [--global]"
			}
			on := fields[1] == "enable"
			scope := scopeFromFields(fields)
			if err := mgmt.SetSkill(fields[2], on, scope); err != nil {
				return "could not update skill: " + err.Error()
			}
			reattach()
			return fmt.Sprintf("Skill %q %s (%s).", fields[2], enabledWord(on), scope)
		case "new":
			if len(fields) < 3 {
				return "usage: /skills new <name> [--global]"
			}
			path, err := skills.Scaffold(fields[2], scopeFromFields(fields) == manage.Global, m.cfg.WorkingDir)
			if err != nil {
				return "could not create skill: " + err.Error()
			}
			reattach()
			return "Created skill at " + path + "\nEdit its SKILL.md, then it's ready for the model to load."
		case "remove":
			if len(fields) < 3 {
				return "usage: /skills remove <name>"
			}
			path, err := skills.Remove(fields[2], m.cfg.WorkingDir)
			if err != nil {
				return "could not remove skill: " + err.Error()
			}
			reattach()
			return "Removed skill at " + path
		}
	}
	found := skills.Load(m.cfg.WorkingDir)
	if len(found) == 0 {
		return "No skills found. Add one with /skills new <name>, or place a SKILL.md under ~/.claude/skills or .claude/skills (MOONY_SKILLS=0 disables discovery)."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Skills (%d):\n", len(found))
	for _, s := range found {
		fmt.Fprintf(&b, "  [%s] %-18s %s\n", onOff(mgmt.SkillEnabled(s.Name)), s.Name, s.Description)
	}
	b.WriteString("\n/skills enable|disable <name> [--global] · /skills new <name> · /skills remove <name>")
	return strings.TrimRight(b.String(), "\n")
}

// agentsCmd handles /agents enable|disable|new|remove (the no-arg listing opens
// a selectable menu via openAgentsMenu).
func (m Model) agentsCmd(fields []string) string {
	mgmt := manage.Load(m.cfg.WorkingDir)
	switch fields[1] {
	case "enable", "disable":
		if len(fields) < 3 {
			return "usage: /agents " + fields[1] + " <name> [--global]"
		}
		on := fields[1] == "enable"
		scope := scopeFromFields(fields)
		if err := mgmt.SetAgent(fields[2], on, scope); err != nil {
			return "could not update agent: " + err.Error()
		}
		m.reattachAgents(mgmt)
		return fmt.Sprintf("Agent %q %s (%s).", fields[2], enabledWord(on), scope)
	case "new":
		if len(fields) < 3 {
			return "usage: /agents new <name> [--global]"
		}
		path, err := subagents.Scaffold(fields[2], scopeFromFields(fields) == manage.Global, m.cfg.WorkingDir)
		if err != nil {
			return "could not create agent: " + err.Error()
		}
		m.reattachAgents(mgmt)
		return "Created agent at " + path + "\nEdit its description, tools, and system prompt, then the model can delegate to it via the task tool."
	case "remove":
		if len(fields) < 3 {
			return "usage: /agents remove <name>"
		}
		path, err := subagents.Remove(fields[2], m.cfg.WorkingDir)
		if err != nil {
			return "could not remove agent: " + err.Error()
		}
		m.reattachAgents(mgmt)
		return "Removed agent at " + path
	}
	return "usage: /agents [enable|disable|new|remove <name>] (no args opens the picker)"
}

// pluginsCmd handles /plugins: list bundles, or install/remove one. Install and
// remove fan out into the skills/agents/mcp/command primitives; skills and
// subagents re-attach live, while MCP servers apply on the next /mcps reload or
// launch (matching how the terminal handles MCP).
func (m Model) pluginsCmd(fields []string) string {
	mgmt := manage.Load(m.cfg.WorkingDir)
	wd := m.cfg.WorkingDir
	reattach := func() {
		m.agent.AttachSkills(manage.Filter(skills.Load(wd), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))
		m.reattachAgents(mgmt)
	}
	if len(fields) >= 2 {
		switch fields[1] {
		case "marketplace", "market", "mp":
			return m.pluginsMarketplaceCmd(fields[2:])
		case "install", "add":
			if len(fields) < 3 {
				return "usage: /plugins install <name>[@<marketplace>]"
			}
			name, market := plugins.SplitRef(fields[2])
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
			reattach()
			return fmt.Sprintf("Installed plugin %q: %s\n(Bundled MCP servers, if any, apply on /mcps reload or next launch.)", fields[2], summary)
		case "remove", "rm", "delete":
			if len(fields) < 3 {
				return "usage: /plugins remove <name>[@<marketplace>]"
			}
			name, market := plugins.SplitRef(fields[2])
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
			reattach()
			return fmt.Sprintf("Removed plugin %q: %s", fields[2], summary)
		}
	}
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

// pluginsMarketplaceCmd handles `/plugins marketplace <add|list|remove|update>`.
func (m Model) pluginsMarketplaceCmd(f []string) string {
	if len(f) == 0 || f[0] == "list" || f[0] == "ls" {
		mps := plugins.Marketplaces()
		if len(mps) == 0 {
			return "No plugin marketplaces added. Add one with /plugins marketplace add <git-url>."
		}
		var b strings.Builder
		b.WriteString("Plugin marketplaces:\n")
		for _, mp := range mps {
			fmt.Fprintf(&b, "- %s — %s\n", mp.Name, mp.URL)
		}
		b.WriteString("\n/plugins marketplace add <git-url> [name] · remove <name> · update <name>")
		return b.String()
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
		mp, err := plugins.AddMarketplace(ctx, name, f[1])
		if err != nil {
			return "could not add marketplace: " + err.Error()
		}
		return fmt.Sprintf("Added marketplace %q (%s). Run /plugins to see and install its plugins.", mp.Name, mp.URL)
	case "remove", "rm":
		if len(f) < 2 {
			return "usage: /plugins marketplace remove <name>"
		}
		if err := plugins.RemoveMarketplace(f[1]); err != nil {
			return "could not remove marketplace: " + err.Error()
		}
		return fmt.Sprintf("Removed marketplace %q.", f[1])
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

// mcpsCmd handles /mcps: list with on/off markers, enable/disable, or add/remove
// a server. MCP changes apply on the next launch (servers connect at startup).
func (m Model) mcpsCmd(fields []string) string {
	mgmt := manage.Load(m.cfg.WorkingDir)
	if len(fields) >= 2 {
		switch fields[1] {
		case "enable", "disable":
			if len(fields) < 3 {
				return "usage: /mcps " + fields[1] + " <name> [--global]"
			}
			on := fields[1] == "enable"
			scope := scopeFromFields(fields)
			if err := mgmt.SetMCP(fields[2], on, scope); err != nil {
				return "could not update MCP server: " + err.Error()
			}
			return fmt.Sprintf("MCP server %q %s (%s). Run /mcps reload to apply.", fields[2], enabledWord(on), scope)
		case "add":
			name, command, cmdArgs, url, global, err := mcp.ParseAddArgs(fields[2:])
			if err != nil {
				return err.Error()
			}
			path, aerr := mcp.AddServer(name, command, cmdArgs, url, nil, global, m.cfg.WorkingDir)
			if aerr != nil {
				return "could not add server: " + aerr.Error()
			}
			return fmt.Sprintf("Added MCP server %q to %s — run /mcps reload to connect.", name, path)
		case "remove":
			if len(fields) < 3 {
				return "usage: /mcps remove <name>"
			}
			path, err := mcp.RemoveServer(fields[2], m.cfg.WorkingDir)
			if err != nil {
				return "could not remove server: " + err.Error()
			}
			return "Removed MCP server from " + path + " — run /mcps reload to apply."
		}
	}
	cfgs, _ := mcp.LoadConfigs(m.cfg.WorkingDir)
	if len(cfgs) == 0 {
		return "No MCP servers configured. Add one with /mcps add <name> -- <cmd> [args] (or <name> <url>), or via your Claude config (auto-discovered; MOONY_MCP_CLAUDE=0 disables)."
	}
	connected := map[string]mcp.ServerInfo{}
	for _, s := range m.mcpServers {
		connected[s.Name] = s
	}
	failed := map[string]string{}
	if m.mcpMgr != nil {
		for _, f := range m.mcpMgr.Failures() {
			failed[f.Name] = f.Err
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MCP servers (%d):\n", len(cfgs))
	for _, c := range cfgs {
		detail := ""
		if s, ok := connected[c.Name]; ok {
			detail = fmt.Sprintf("connected · %d tools, %d resources, %d prompts", s.Tools, s.Resources, s.Prompts)
		} else if e, ok := failed[c.Name]; ok {
			detail = "failed: " + e
		} else if mgmt.MCPEnabled(c.Name) {
			detail = "not connected"
		}
		fmt.Fprintf(&b, "  [%s] %-18s %s\n", onOff(mgmt.MCPEnabled(c.Name)), c.Name, detail)
	}
	b.WriteString("\n/mcps enable|disable <name> [--global] · /mcps add <name> -- <cmd>|<url> · /mcps remove <name> · /mcps reload")
	return strings.TrimRight(b.String(), "\n")
}

// reloadMCP reconnects the enabled MCP servers without a restart and refreshes
// the sidebar. Runs synchronously (the user invoked it), so the UI pauses
// briefly while servers handshake.
func (m *Model) reloadMCP() string {
	mgmt := manage.Load(m.cfg.WorkingDir)
	cfgs, _ := mcp.LoadConfigs(m.cfg.WorkingDir)
	cfgs = manage.Filter(cfgs, func(c mcp.Config) string { return c.Name }, mgmt.MCPEnabled)
	if m.mcpMgr == nil {
		m.mcpMgr = mcp.Register(context.Background(), m.agent.Registry(), cfgs, nil)
	} else {
		m.mcpMgr.Reload(context.Background(), cfgs, nil)
	}
	m.mcpServers = m.mcpMgr.Servers()
	m.mcpPrompts = m.mcpMgr.Prompts()
	// Tools just changed; rebuild the system prompt so tool-specific guidance
	// (e.g. the optional codebase-memory hints) reflects the now-connected servers.
	m.agent.RefreshSystemPrompt()
	return fmt.Sprintf("Reloaded MCP — %d server(s) connected.", len(m.mcpServers))
}

// lspCmd lists the configured language servers with their on/off + running
// state, or toggles one. Adding/removing servers is done by editing lsp.json;
// this manages enable/disable (persisted) and reload.
func (m Model) lspCmd(fields []string) string {
	mgmt := manage.Load(m.cfg.WorkingDir)
	if len(fields) >= 2 {
		switch fields[1] {
		case "enable", "disable":
			if len(fields) < 3 {
				return "usage: /lsp " + fields[1] + " <name> [--global]"
			}
			on := fields[1] == "enable"
			scope := scopeFromFields(fields)
			if err := mgmt.SetLSP(fields[2], on, scope); err != nil {
				return "could not update language server: " + err.Error()
			}
			return fmt.Sprintf("Language server %q %s (%s). Run /lsp reload to apply.", fields[2], enabledWord(on), scope)
		default:
			return "usage: /lsp [enable|disable <name>] [--global] | reload"
		}
	}

	cfgs, err := lsp.LoadConfigs(m.cfg.WorkingDir)
	if err != nil {
		return "lsp config invalid: " + err.Error()
	}
	if len(cfgs) == 0 {
		return "No language servers configured. Add one to ~/.config/moony/lsp.json or ./.sqe/lsp.json (see /doctor), then /lsp reload."
	}
	running := map[string]bool{}
	for _, n := range m.lspMgr.Running() {
		running[n] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Language servers (%d):\n", len(cfgs))
	for _, c := range cfgs {
		detail := strings.Join(c.Extensions, " ")
		switch {
		case !mgmt.LSPEnabled(c.Name):
			detail = "disabled · " + detail
		case running[c.Name]:
			detail = "running · " + detail
		default:
			detail = "not running · " + detail
		}
		fmt.Fprintf(&b, "  [%s] %-18s %s\n", onOff(mgmt.LSPEnabled(c.Name)), c.Name, detail)
	}
	b.WriteString("\n/lsp enable|disable <name> [--global] · /lsp reload")
	return strings.TrimRight(b.String(), "\n")
}

// reloadLSP restarts the enabled language servers without restarting the agent:
// it rebuilds the manager from the (manage-filtered) configs, attaches it, and
// closes the previous one. Synchronous (the user asked for it), so the UI pauses
// briefly while servers handshake.
func (m *Model) reloadLSP() string {
	mgmt := manage.Load(m.cfg.WorkingDir)
	cfgs, _ := lsp.LoadConfigs(m.cfg.WorkingDir)
	cfgs = manage.Filter(cfgs, func(c lsp.Config) string { return c.Name }, mgmt.LSPEnabled)
	old := m.lspMgr
	m.lspMgr = lsp.Start(context.Background(), m.cfg.WorkingDir, cfgs, func(string) {})
	m.agent.AttachLSP(m.lspMgr)
	if old != nil {
		old.Close()
	}
	return fmt.Sprintf("Reloaded LSP — %d server(s) running.", len(m.lspMgr.Running()))
}
