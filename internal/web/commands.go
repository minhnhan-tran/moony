package web

// Slash-command handling for the standalone web server. The terminal interprets
// these in its TUI; here we give the browser the same surface: a populated
// palette (commandCatalog) and server-side execution (runCommand) that emits
// info/menu/reset/status events back to the page. Config-changing commands mutate
// the session's own agent config (and persist it), so they're scoped per session.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/doctor"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/selfupdate"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/workflow"
)

// builtinCommands is the web command catalog. Terminal-only commands (/exit,
// /quit, /copy) are intentionally omitted.
var builtinCommands = []cmdWire{
	{Name: "/help", Desc: "show available commands"},
	{Name: "/cd", Args: "<dir>", Desc: "re-root this session at another folder (tools, skills, agents follow)"},
	{Name: "/reset", Desc: "start a new session (forget history)"},
	{Name: "/clear", Desc: "clear the transcript"},
	{Name: "/queue", Args: "[list|clear]", Desc: "list or empty the pending-message queue"},
	{Name: "/btw", Args: "<question>", Desc: "ask a quick side question (runs concurrently, doesn't touch the main thread)"},
	{Name: "/model", Args: "[id]", Desc: "show or set the model (the endpoint is derived from it)"},
	{Name: "/models", Desc: "pick a model (from the active provider)"},
	{Name: "/provider", Args: "[name]", Desc: "show or set the model provider: ollama (default), openai, anthropic, openrouter"},
	{Name: "/reasoning", Args: "[level]", Desc: "show or set the reasoning level"},
	{Name: "/approval", Args: "[policy]", Desc: "show or set the approval policy"},
	{Name: "/sandbox", Args: "[on|off]", Desc: "show or set the bash sandbox"},
	{Name: "/context", Args: "[n|auto]", Desc: "show the context-usage breakdown (or set the window size)"},
	{Name: "/plan", Args: "[description|approve|clear|show|auto]", Desc: "draft an approvable plan artifact, then execute it"},
	{Name: "/build", Desc: "exit plan mode"},
	{Name: "/agent", Args: "[name]", Desc: "switch the active agent profile (build/plan or a custom one)"},
	{Name: "/verify", Args: "[command|off]", Desc: "auto-run a build/test after edits and self-correct on failure"},
	{Name: "/selfreview", Args: "[on|off]", Desc: "fast-model review of each edit turn's diff for introduced bugs (saved)"},
	{Name: "/tokens", Desc: "token + context usage this session"},
	{Name: "/usage", Desc: "token usage this session (hosted providers show no quota here)"},
	{Name: "/doctor", Desc: "run environment checks"},
	{Name: "/skills", Desc: "list discovered skills"},
	{Name: "/mcps", Desc: "list configured MCP servers"},
	{Name: "/lsp", Desc: "list language servers (diagnostics)"},
	{Name: "/agents", Desc: "list subagent personas"},
	{Name: "/plugins", Desc: "install plugin bundles, or add git marketplaces (/plugins marketplace add <git-url>)"},
	{Name: "/workers", Desc: "manage worker agents (Claude Code / Optimus / Apple Gemini)"},
	{Name: "/compact", Desc: "summarize older history to free context"},
	{Name: "/goal", Args: "[description|criteria|budget|clear]", Desc: "set a stop condition (with criteria + token budget) the agent must satisfy"},
	{Name: "/checkpoint", Args: "[now|clear]", Desc: "show, refresh, or clear the distilled session state"},
	{Name: "/memory", Args: "[note]", Desc: "show or append project memory"},
	{Name: "/undo", Desc: "revert the last file change"},
	{Name: "/changes", Desc: "show a combined diff of this turn's file changes"},
	{Name: "/rewind", Args: "[n]", Desc: "roll back the last n turns (files + conversation)"},
	{Name: "/sessions", Desc: "list this project's sessions"},
	{Name: "/schedule", Args: "[add <spec> <prompt>|remove|enable|disable|run <id>]", Desc: "list or manage headless scheduled prompts"},
	{Name: "/permissions", Args: "[allow|deny|remove <rule>] [--global]", Desc: "list or edit tool permission rules"},
	{Name: "/init", Desc: "bootstrap an AGENTS.md for this repo"},
}

// commandCatalog returns the palette list: built-ins plus the project's custom
// commands.
func commandCatalog(workingDir string) []cmdWire {
	out := append([]cmdWire(nil), builtinCommands...)
	for _, c := range loadUserCommands(workingDir) {
		out = append(out, cmdWire{Name: c.name, Args: "[args]", Desc: c.description})
	}
	return out
}

func (s *Server) commandsEvent(sess *session) wireEvent {
	wd := ""
	if sess != nil && sess.agent != nil {
		wd = sess.agent.Config().WorkingDir
	}
	cmds := commandCatalog(wd)
	// /update is offered only by the headless host (the daemon wires the restart
	// hook); the terminal handles its own /update on the live session.
	if s.restart != nil {
		cmds = append(cmds, cmdWire{Name: "/update", Desc: "download the latest moony, install it, and restart"})
	}
	return wireEvent{Type: "commands", Commands: cmds, Mentions: s.mentionCatalog(sess)}
}

// mentionCatalog returns the @-mention candidates (enabled subagent personas +
// enabled workers) for the browser's @-mention dropdown, mirroring the names the
// server-side mention expansion will actually accept (and the terminal's list).
func (s *Server) mentionCatalog(sess *session) []mentionWire {
	out := []mentionWire{}
	if sess == nil || sess.agent == nil {
		return out
	}
	wd := sess.agent.Config().WorkingDir
	mgmt := manage.Load(wd)
	for _, p := range subagents.Load(wd) {
		if mgmt.AgentEnabled(p.Name) && p.SubagentCapable() {
			out = append(out, mentionWire{Name: p.Name, Kind: "agent", Desc: p.Description})
		}
	}
	if mgr := sess.agent.Workers(); mgr != nil {
		for _, c := range mgr.Configs() { // Configs() = enabled workers only
			desc := "worker"
			if c.Backend != "" {
				desc = "worker · " + c.Backend
			}
			out = append(out, mentionWire{Name: c.Name, Kind: "worker", Desc: desc})
		}
	}
	for _, sk := range skills.Load(wd) {
		if mgmt.SkillEnabled(sk.Name) {
			out = append(out, mentionWire{Name: sk.Name, Kind: "skill", Desc: sk.Description})
		}
	}
	// Saved Agent Builder workflows — dispatched via the run_workflow tool, the
	// @workflow analog of @agent/@worker.
	if wfs, err := workflow.Open(); err == nil {
		if list, err := wfs.List(); err == nil {
			for _, wf := range list {
				if n := strings.TrimSpace(wf.Name); n != "" {
					out = append(out, mentionWire{Name: n, Kind: "workflow", Desc: firstSentence(wf.Description)})
				}
			}
		}
	}
	// "@ultrathink" is a deep-dive MODE (kind "deep"), not an agent to dispatch:
	// it spawns parallel sub-agents to investigate, then synthesizes. Listed for
	// discoverability in the browser's @-mention dropdown.
	out = append(out, mentionWire{Name: "ultrathink", Kind: "deep", Desc: "Deep dive — spawn parallel sub-agents, then synthesize"})
	return out
}

// runCommand interprets a slash command for one (idle) session, emitting info,
// menu, reset, and refreshed-status events. Custom commands and /init expand
// into a normal turn instead.
func (s *Server) runCommand(sess *session, text string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	name := strings.ToLower(fields[0])
	arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	cfg := sess.agent.Config()
	info := func(msg string) { sess.publishEvent(wireEvent{Type: "info", Text: msg}) }

	// Custom commands expand into a normal turn.
	for _, uc := range loadUserCommands(cfg.WorkingDir) {
		if uc.name == name {
			sess.start(expandUserCommand(uc, arg), nil, nil)
			return
		}
	}

	switch name {
	case "/help":
		info(webHelpText())
	case "/cd":
		s.changeDir(sess, arg, info)
	case "/clear":
		sess.clearHistory()
		sess.publishEvent(wireEvent{Type: "reset"})
		return
	case "/reset":
		sess.agent.Reset()
		sess.clearHistory()
		sess.publishEvent(wireEvent{Type: "reset"})
		info("Conversation reset — started a new session.")
	case "/queue":
		// Inspect or empty the busy-time message queue. (Enqueuing happens by
		// sending while a turn runs; managing a queued item while busy uses the
		// composer's queued strip — a slash command typed while busy is itself
		// queued, so /queue is for idle use.)
		sub := strings.ToLower(strings.TrimSpace(arg))
		switch sub {
		case "", "list", "show":
			q := sess.queuedList()
			if len(q) == 0 {
				info("Queue is empty. Send a message while the agent is busy to enqueue it; it runs when the turn finishes.")
			} else {
				var b strings.Builder
				fmt.Fprintf(&b, "Queued messages (%d):", len(q))
				for i, item := range q {
					fmt.Fprintf(&b, "\n  %d. %s", i+1, strings.ReplaceAll(item, "\n", " ⏎ "))
				}
				info(b.String())
			}
		case "clear":
			n := sess.clearQueue()
			info(fmt.Sprintf("Cleared %d queued message(s).", n))
		default:
			info("Usage: /queue [list|clear]")
		}
	case "/model":
		if arg == "" {
			info(fmt.Sprintf("Current model: %s\nUse /models to pick from a list, or /model <id> to set one.", cfg.Model))
		} else {
			// SetModel also derives the endpoint from the id and resolves the
			// reasoning level. Mirror it onto the server-level config so the
			// shared LLM client (which holds a pointer to s.cfg) picks up the
			// new model AND its route.
			cfg.SetModel(arg)
			s.cfg.SetModel(arg)
			saveCfg(cfg, info, fmt.Sprintf("Model set to %s (%s)", shortModel(arg), provider.Label(cfg.LLMProvider)))
			// The cached system prompt embeds the model in its identity line;
			// rebuild it so the model doesn't keep self-identifying as the old one.
			sess.agent.RefreshSystemPrompt()
		}
	case "/models":
		s.emitModelsMenu(sess)
		return
	case "/provider", "/providers":
		if arg == "" {
			s.emitProviderMenu(sess)
			return
		}
		s.providerCommand(cfg, arg, info)
		// The old provider's quota data belongs to a different endpoint/account.
		// Clear it immediately and re-fetch from the new provider so the status
		// bar shows the correct figures without waiting for the next turn.
		if sess.refreshQuota != nil {
			sess.refreshQuota()
		}
		// Switching to Ollama: its model ids are server-specific (e.g. "gemma4:31b"),
		// so a cloud model carried over won't resolve. Auto-pick a served
		// model when the current one isn't on the server.
		if provider.Normalize(cfg.LLMProvider) == provider.Ollama {
			s.ensureOllamaModel(sess, cfg, info)
		}
		// The system prompt's identity line names the provider (e.g. "served via
		// a local model" vs a hosted provider); rebuild it so a live
		// /provider switch is reflected instead of the startup default.
		sess.agent.RefreshSystemPrompt()
	case "/reasoning", "/think", "/thinking":
		if arg == "" {
			s.emitReasoningMenu(sess)
			return
		}
		lvl := config.NormalizeReasoningFor(cfg.Model, strings.ToLower(arg))
		cfg.SetReasoningFor(cfg.Model, lvl)
		s.cfg.ReasoningLevel = cfg.ReasoningLevel
		saveCfg(cfg, info, "Reasoning level set to "+lvl)
	case "/approval":
		if arg == "" {
			info("Approval policy: " + config.NormalizeApprovalPolicy(cfg.ApprovalPolicy))
		} else {
			cfg.ApprovalPolicy = config.NormalizeApprovalPolicy(strings.ToLower(arg))
			saveCfg(cfg, info, "Approval policy set to "+cfg.ApprovalPolicy)
		}
	case "/sandbox":
		if arg == "" {
			info("Bash sandbox: " + config.SandboxLabel(cfg.SandboxMode))
		} else {
			cfg.SandboxMode = config.NormalizeSandboxMode(arg)
			sess.agent.SetSandbox(cfg.SandboxMode != config.SandboxOff, cfg.SandboxNetwork)
			saveCfg(cfg, info, "Bash sandbox set to "+config.SandboxLabel(cfg.SandboxMode))
		}
	case "/context":
		switch {
		case arg == "":
			eff := cfg.ContextWindowFor(cfg.Model)
			if cfg.ContextWindow > 0 {
				info(fmt.Sprintf("Context window: %d tokens (manual override). Use /context auto for %s's default.", eff, shortModel(cfg.Model)))
			} else {
				info(fmt.Sprintf("Context window: %d tokens (auto — %s's default). Use /context <n> (e.g. 1m) to override the ctx%% meter.", eff, shortModel(cfg.Model)))
			}
			// Publish the structured per-segment breakdown so the browser draws
			// its color-coded stacked bar (matching the terminal's inline bar).
			bd := sess.agent.ContextBreakdown()
			sess.publishEvent(wireEvent{Type: "context", Context: &contextWire{Segments: contextSegmentsWire(bd.Segments), Total: bd.Total, Window: eff}})
		case arg == "auto" || arg == "default" || arg == "off" || arg == "0":
			cfg.ContextWindow = 0
			saveCfg(cfg, info, fmt.Sprintf("Context window: auto (%d for %s).", config.ContextWindow(cfg.Model), shortModel(cfg.Model)))
		default:
			n, err := config.ParseTokenCount(arg)
			if err != nil || n <= 0 {
				info("usage: /context <tokens|1m|200k|auto>")
				return
			}
			cfg.ContextWindow = n
			saveCfg(cfg, info, fmt.Sprintf("Context window set to %d tokens — the ctx%% meter now uses this. /context auto to revert.", n))
		}
	case "/plan":
		s.planCommand(sess, arg, info)
		return
	case "/build":
		sess.agent.SetPlanMode(false)
		info("Plan mode OFF — mutating tools are available again.")
	case "/verify":
		// Configure the post-edit verification loop: bare shows the current
		// command, "off" disables it, anything else sets it (and persists).
		switch {
		case arg == "":
			if strings.TrimSpace(cfg.VerifyCommand) == "" {
				info("Auto-verify is off. Set it with /verify <command> — e.g. /verify go build ./... — to run it after each turn that edits code and self-correct on failure.")
			} else {
				info(fmt.Sprintf("Auto-verify: %s (up to %d fix attempt(s) per turn). Disable with /verify off.", cfg.VerifyCommand, cfg.VerifyMaxRetries))
			}
			return
		case strings.EqualFold(arg, "off"):
			cfg.VerifyCommand = ""
			s.cfg.VerifyCommand = ""
			saveCfg(cfg, info, "Auto-verify disabled.")
			return
		default:
			cfg.VerifyCommand = arg
			s.cfg.VerifyCommand = arg
			saveCfg(cfg, info, "Auto-verify command set: "+arg)
			return
		}
	case "/selfreview":
		// Toggle the optional fast-model self-review of each edit turn's diff.
		switch {
		case arg == "":
			if cfg.SelfReview {
				info("Self-review is ON — a fast-model reviewer checks each edit turn's diff for introduced bugs and feeds findings back once before finishing. Disable with /selfreview off.")
			} else {
				info("Self-review is off. Turn it on with /selfreview on to have a fast-model reviewer check each edit turn's diff for bugs before the turn finishes.")
			}
			return
		case strings.EqualFold(arg, "on"), strings.EqualFold(arg, "true"), arg == "1", strings.EqualFold(arg, "yes"):
			cfg.SelfReview = true
			s.cfg.SelfReview = true
			saveCfg(cfg, info, "Self-review enabled.")
			return
		case strings.EqualFold(arg, "off"), strings.EqualFold(arg, "false"), arg == "0", strings.EqualFold(arg, "no"):
			cfg.SelfReview = false
			s.cfg.SelfReview = false
			saveCfg(cfg, info, "Self-review disabled.")
			return
		default:
			info("Usage: /selfreview on|off")
			return
		}
	case "/agent":
		// Switch the active primary profile (build / plan / a custom persona),
		// parity with the TUI's /agent. Bare → a picker menu; with a name → set it.
		if arg == "" {
			s.emitAgentMenu(sess)
			return
		}
		if !sess.agent.SetAgent(arg) {
			info(fmt.Sprintf("Unknown or non-primary agent %q. Available: %s", arg, strings.Join(sess.agent.AgentNames(), ", ")))
			return
		}
		info("Switched to agent: " + sess.agent.ActiveAgent().Name)
		// fall through to refresh the header/status bar.
	case "/tokens":
		info(tokensSummary(sess, cfg))
	case "/usage":
		go func() {
			resp, err := sess.agent.Usage(context.Background())
			if err != nil || resp == nil {
				info("Usage data unavailable.")
				return
			}
			info(formatQuota(resp))
		}()
		return
	case "/doctor":
		go func() { info(doctor.Run(context.Background(), cfg, sess.agent.CheckAuth).String()) }()
		return
	case "/skills":
		if arg == "" {
			s.emitSkillsMenu(sess)
			return
		}
		info(s.skillsCommand(sess, arg))
		return
	case "/mcps":
		if f := strings.Fields(arg); len(f) > 0 && f[0] == "reload" {
			go func() { sess.publishEvent(wireEvent{Type: "info", Text: s.connectMCP(cfg.WorkingDir, sess)}) }()
			return
		}
		if arg == "" {
			s.emitMcpsMenu(sess)
			return
		}
		info(s.mcpsCommand(sess, arg))
		return
	case "/lsp":
		if f := strings.Fields(arg); len(f) > 0 && f[0] == "reload" {
			go func() { sess.publishEvent(wireEvent{Type: "info", Text: s.connectLSP(cfg.WorkingDir, sess)}) }()
			return
		}
		if arg == "" {
			s.emitLspMenu(sess)
			return
		}
		info(s.lspCommand(sess, arg))
		return
	case "/agents":
		if arg == "" {
			s.emitAgentsMenu(sess)
			return
		}
		info(s.agentsCommand(sess, arg))
		return
	case "/workers":
		if arg == "" {
			s.emitWorkersMenu(sess)
			return
		}
		info(s.workersCommand(sess, arg))
		return
	case "/plugins":
		if arg == "" {
			s.emitPluginsMenu(sess)
			return
		}
		info(s.pluginsCommand(sess, arg))
		return
	case "/compact":
		info("Compacting older history…")
		go func() {
			// Bound the summarization model call so a stalled upstream can't hang
			// the compaction forever (matches the terminal's timeout).
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			n, err := sess.agent.Compact(ctx)
			if err != nil {
				info("compact failed: " + err.Error())
				return
			}
			if n == 0 {
				info("Nothing to compact yet.")
				return
			}
			info(fmt.Sprintf("Compacted %d older message(s) into a summary.", n))
			sess.publishEvent(s.statusEvent(nil, sess))
		}()
		return
	case "/goal":
		s.goalCommand(sess, arg, info)
		return
	case "/checkpoint":
		switch strings.ToLower(arg) {
		case "":
			info(sess.agent.CheckpointStatus())
		case "clear", "off", "none", "remove":
			sess.agent.ClearCheckpoint()
			info("Session checkpoint cleared.")
		case "now", "refresh", "update":
			go func() {
				status, err := sess.agent.WriteCheckpoint(context.Background())
				if err != nil {
					info("checkpoint failed: " + err.Error())
					return
				}
				info(status)
			}()
		default:
			info("Usage: /checkpoint [now|clear] — show, refresh, or clear the distilled session state.")
		}
		return
	case "/memory":
		if arg == "" {
			info(sess.agent.MemoryView())
			return
		}
		note, category, global := agent.ParseMemoryFlags(arg)
		out, err := sess.agent.Remember(note, category, global)
		if err != nil {
			info("memory failed: " + err.Error())
		} else {
			info(out)
		}
		return
	case "/undo":
		out, err := sess.agent.Undo(context.Background())
		if err != nil {
			info("undo failed: " + err.Error())
		} else {
			info(out)
		}
		return
	// "/diff" is an accepted alias: it's the habitual name for this, and a user who
	// types it would otherwise get "unknown command" for a feature that is right there.
	case "/changes", "/diff":
		diff := strings.TrimSpace(sess.agent.TurnChangesDiff(0))
		if diff == "" {
			info("No files changed in the current turn.")
			return
		}
		// Fenced as `diff` so the client renders it with +/- highlighting, and the
		// pointer to /undo is what makes the diff actionable rather than just informative.
		info("Combined diff of this turn's changes (use /undo to revert all):\n\n```diff\n" + diff + "\n```")
		return
	case "/rewind":
		n := 1
		if a := strings.TrimSpace(arg); a != "" {
			v, err := strconv.Atoi(a)
			if err != nil || v < 1 {
				info("Usage: /rewind [n] — roll back the last n turns (files AND conversation).")
				return
			}
			n = v
		}
		out, err := sess.agent.Rewind(context.Background(), n)
		if err != nil {
			info("rewind failed: " + err.Error())
		} else {
			info(out)
		}
		return
	case "/sessions":
		info(s.sessionsSummary(cfg.WorkingDir))
		return
	case "/schedule":
		if arg == "" {
			s.emitSchedulesMenu(sess)
			return
		}
		s.scheduleCommand(sess, arg, info)
		return
	case "/permissions":
		info(permissionsCommand(sess, arg))
		return
	case "/init":
		sess.start(initPrompt, nil, nil)
		return
	case "/update":
		go s.runSelfUpdate(sess)
		return
	default:
		info("Unknown command. Try /help.")
		return
	}
	// Config-changing commands fall through here: refresh the header/status bar.
	sess.publishEvent(s.statusEvent(nil, sess))
}

// changeDir re-roots the live session at another folder (the /cd command): it
// resolves + validates the target (creating it one level under an existing parent,
// like the folder picker), persists the new working dir on the stored session, and
// re-roots the running agent via SetWorkingDir — so file tools, bash, skills,
// agents (@-mentions), and permissions all operate on the new project WITHOUT
// losing the conversation. Idle-only (the slash-command gate ensures no turn is in
// flight). MCP/LSP keep their original root until the session restarts.
func (s *Server) changeDir(live *session, arg string, info func(string)) {
	dir := strings.TrimSpace(arg)
	if dir == "" {
		info("Usage: /cd <dir> — re-root this session at another folder (e.g. /cd ~/Documents/Source\\ Code/IOSApp).")
		return
	}
	home, _ := os.UserHomeDir()
	abs, err := resolveNewSessionDir(dir, home)
	if err != nil {
		info("cd: " + err.Error())
		return
	}
	// Persist the new working dir on the stored session so a reopen/replay (and the
	// status snapshot) reflect it. Re-root the LIVE agent first: it can refuse (a
	// turn is running), and persisting the new dir before that would leave the saved
	// session pointing somewhere the live agent isn't.
	if err := live.agent.SetWorkingDir(abs); err != nil {
		info("cd: " + err.Error())
		return
	}
	if sess, lerr := s.store.Load(live.id); lerr == nil {
		sess.WorkingDir = abs
		_ = s.store.Save(sess)
	}
	s.attachWorkflowTool(live) // re-advertise saved workflows (run_workflow) for the new dir
	info("Working dir → " + abs + "\nTools, skills, agents, and permissions now resolve here. (MCP servers and LSP keep their original root until you restart the session.)")
	live.publishEvent(s.statusEvent(nil, live))
}

// runSelfUpdate handles the /update command for a headless (daemon-hosted)
// session: it checks the relay for a newer build, downloads + verifies it, and
// replaces the running binary in place, streaming progress to the browser. On
// success it invokes the restart hook (the daemon exits so its service manager
// relaunches the new build, after which the web UI's version banner clears on
// its own via the laptop-version poll). When no restart hook is wired (the
// terminal-hosted server), it self-updates and advises a manual restart instead
// of killing the process out from under the TUI. Runs on its own goroutine — a
// download can take many seconds and must not block the request path.
func (s *Server) runSelfUpdate(sess *session) {
	info := func(msg string) { sess.publishEvent(wireEvent{Type: "info", Text: msg}) }

	// Version check is a tiny GET — short timeout so an unreachable relay fails
	// fast; the download (below) gets a generous budget of its own.
	info("Checking for the latest moony…")
	vctx, vcancel := context.WithTimeout(context.Background(), 30*time.Second)
	latest, err := selfupdate.LatestVersion(vctx)
	vcancel()
	if err != nil {
		info("update failed: " + err.Error())
		return
	}
	if !config.VersionLess(config.Version, latest) {
		info(fmt.Sprintf("Already on the latest moony (v%s).", config.Version))
		return
	}
	info(fmt.Sprintf("Updating to v%s…", latest))
	// The binary is ~30 MB and the relay may sit behind a slow internal VIP; give
	// the download a generous budget so it isn't killed mid-transfer with "context
	// deadline exceeded".
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := selfupdate.Apply(ctx); err != nil {
		info("update failed: " + err.Error())
		return
	}
	if s.restart == nil {
		// Terminal-hosted server: don't kill the TUI; the user (or the TUI's own
		// /update) restarts on the new build.
		info(fmt.Sprintf("Updated to v%s. Restart moony to run the new build.", latest))
		return
	}
	info(fmt.Sprintf("Updated to v%s — restarting…", latest))
	s.restart()
}

// toggleValue is the menu-item value that flips an item's state: an enabled
// item's row disables it, a disabled item's row enables it.
func toggleValue(on bool, name string) string {
	if on {
		return "disable " + name
	}
	return "enable " + name
}

// connectMCP connects this session's enabled MCP servers and registers their
// tools on its agent. Called in the background at open and synchronously by
// /mcps reload. Best effort — failures are skipped so the session still works.
// defaultServerLog is the standalone server's log sink: stderr, which for the
// headless daemon is its service log. The TUI-embedded server overrides this
// (Server.SetLogf) so the lines can't corrupt the alt-screen UI.
func defaultServerLog(line string) { fmt.Fprintln(os.Stderr, line) }

func onOff(on bool) string {
	if on {
		return "on "
	}
	return "off"
}

func enabledWord(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

// webScope returns Global when a --global/-g flag is present, else Project.
func webScope(fields []string) manage.Scope {
	for _, f := range fields {
		if f == "--global" || f == "-g" {
			return manage.Global
		}
	}
	return manage.Project
}

func webHelpText() string {
	var b strings.Builder
	b.WriteString("Commands:\n")
	for _, c := range builtinCommands {
		n := c.Name
		if c.Args != "" {
			n += " " + c.Args
		}
		fmt.Fprintf(&b, "  %-22s %s\n", n, c.Desc)
	}
	b.WriteString("\nType / in the input to autocomplete. Custom commands live in .sqe/commands/<name>.md.")
	return b.String()
}

func saveCfg(cfg *config.Config, info func(string), msg string) {
	if err := cfg.Save(); err != nil {
		info(msg + " (not persisted: " + err.Error() + ")")
		return
	}
	info(msg)
}

// --- custom slash commands (mirrors the terminal's loader) ------------------
