// Command moony is a terminal coding agent that works with many model providers
// (Ollama by default — local, no key — plus OpenAI, Anthropic, OpenRouter, and more).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/app"
	"github.com/pysty1995/moony/internal/bridge"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/daemon"
	"github.com/pysty1995/moony/internal/domains"
	"github.com/pysty1995/moony/internal/format"
	"github.com/pysty1995/moony/internal/hooks"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/relay"
	"github.com/pysty1995/moony/internal/schedule"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/tui"
	"github.com/pysty1995/moony/internal/web"
	"github.com/pysty1995/moony/internal/worker"
)

func main() {
	// Before any subcommand or the TUI reads global config, relocate it from the
	// pre-fix macOS location (~/Library/Application Support/moony) to
	// ~/.config/moony when needed. Best-effort; a no-op on Linux and
	// after the first migration.
	if err := config.MigrateLegacyConfigDir(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not migrate legacy config dir:", err)
	}
	// Same idea, for the sqe-code-agent -> moony product rename:
	// relocate ~/.config/sqe-code-agent to ~/.config/moony when
	// needed. Best-effort; a no-op once migrated.
	if err := config.MigrateRenamedConfigDir(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not migrate renamed config dir:", err)
	}
	// Materialize the built-in build/plan agents as editable files in the global
	// agents dir (~/.config/moony/agents) so they're first-class,
	// customizable entries in /agents out of the box. Best-effort and idempotent
	// (existing files are never overwritten).
	if _, err := subagents.SeedGlobalDefaultAgents(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not seed default agents:", err)
	}
	// Seed the global AGENTS.md (house rules, e.g. CHANGELOG.md/versioning
	// convention) on a fresh install so every project's agent sees it without a
	// manual setup step. Best-effort and idempotent (never overwrites an
	// existing global AGENTS.md).
	if _, err := config.SeedGlobalAGENTS(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not seed global AGENTS.md:", err)
	}
	// Subcommands are handled before the TUI's flag parsing. `daemon` runs (and
	// manages) the always-on, headless relay connector that keeps this machine
	// available in the web UI without a hand-launched terminal.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		os.Exit(runDaemonCmd(os.Args[2:]))
	}
	// `schedule` manages recurring/one-shot prompts that run headlessly (via the
	// daemon, or `schedule tick` from cron); see internal/schedule.
	if len(os.Args) > 1 && os.Args[1] == "schedule" {
		os.Exit(runScheduleCmd(os.Args[2:]))
	}
	// `session-key` manages session-history encryption at rest (status, and
	// rewrap/rotate — re-encrypt all sessions under the currently-resolved key).
	if len(os.Args) > 1 && os.Args[1] == "session-key" {
		os.Exit(runSessionKeyCmd(os.Args[2:]))
	}

	var (
		modelFlag     = flag.String("model", "", "model identifier for the active provider (e.g. llama3.1:8b, gpt-4o, claude-sonnet-4-5)")
		reasoningFlag = flag.String("reasoning", "", "extended-thinking level: off, low, medium, or high")
		dirFlag       = flag.String("C", "", "working directory for the agent (default: current dir)")
		continueOld   = flag.Bool("continue", false, "resume the most recent session for this directory")
		sessionID     = flag.String("session", "", "resume a specific session by id")
		approvalFlag  = flag.String("approval", "", "approval policy: prompt, safe, auto, or danger-skip-permission")
		noApproval    = flag.Bool("no-approval", false, "auto-run mutating tools without confirmation prompts (alias for --approval auto)")
		sandboxFlag   = flag.String("sandbox", "", "macOS: confine bash writes to the working dir — on or off")
		printFlag     = flag.String("print", "", "headless mode: run a single prompt non-interactively and exit (also reads stdin)")
		printShort    = flag.String("p", "", "alias for --print")
		jsonFlag      = flag.Bool("json", false, "headless mode: emit a single JSON result object instead of streamed text")
		showVersion   = flag.Bool("version", false, "print version and exit")
	)
	var rootFlags multiFlag
	flag.Var(&rootFlags, "root", "additional workspace root the agent may read/write, beyond -C (repeatable; multi-root workspace). Also via MN_ROOTS.")
	flag.Parse()

	if *showVersion {
		fmt.Printf("moony %s\n", config.Version)
		return
	}

	config.LoadDotEnv() // local-dev .env (won't override real env / Whisper)
	cfg := config.Load()

	// First-run: materialize the default global permission/domain rules (file
	// read/edit pre-approved; web_fetch limited to Apple hosts). Idempotent and
	// never overwrites existing files, so user edits via the menus/commands win.
	permissions.EnsureGlobalDefaults()
	domains.EnsureDefaults()
	if *modelFlag != "" {
		// Setting the model also derives the endpoint and restores the reasoning
		// level remembered for that model (the --reasoning flag below still wins).
		cfg.SetModel(*modelFlag)
		// Mark it explicit so resuming a session doesn't restore that session's
		// saved model over the one asked for on the command line.
		cfg.ModelExplicit = true
	}
	if *reasoningFlag != "" {
		cfg.ReasoningLevel = config.NormalizeReasoning(*reasoningFlag)
		cfg.ReasoningExplicit = true
	}
	if *approvalFlag != "" {
		cfg.ApprovalPolicy = config.NormalizeApprovalPolicy(*approvalFlag)
	}
	if *noApproval {
		cfg.ApprovalPolicy = config.ApprovalAuto
	}
	if *sandboxFlag != "" {
		cfg.SandboxMode = config.NormalizeSandboxMode(*sandboxFlag)
	}
	if *dirFlag != "" {
		if err := os.Chdir(*dirFlag); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot change to %s: %v\n", *dirFlag, err)
			os.Exit(1)
		}
		if wd, err := os.Getwd(); err == nil {
			cfg.WorkingDir = wd
		}
	}

	// Session persistence (best effort: continue without it on failure).
	store, err := session.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: session persistence disabled: %v\n", err)
		store = nil
	}
	sess := resolveSession(store, *sessionID, *continueOld, cfg)

	client := llm.NewClient(cfg)
	registry := tools.Default(cfg.WorkingDir, cfg.MaxToolOutputBytes)
	registry.SetSandbox(cfg.SandboxMode != config.SandboxOff, cfg.SandboxNetwork)
	// Multi-root: whitelist extra workspace roots from --root and MN_ROOTS so
	// file tools and the bash sandbox can operate across several repos at once.
	registry.AddRoots(rootFlags...)
	if env := strings.TrimSpace(os.Getenv("MN_ROOTS")); env != "" {
		registry.AddRoots(filepath.SplitList(env)...)
	}
	todos := tools.NewTodoStore()
	registry.RegisterTodo(todos)
	plan := tools.NewPlanStore()
	registry.RegisterPlan(plan)
	memory := tools.NewMemorySink()
	registry.RegisterRemember(memory)
	userMemory := tools.NewMemorySink()
	registry.RegisterRememberUser(userMemory)
	ag := agent.New(cfg, client, registry, store, sess)
	ag.AttachTodoStore(todos)
	ag.AttachPlanStore(plan)
	ag.AttachMemory(memory)
	ag.AttachUserMemory(userMemory)
	ag.AttachPermissions(permissions.Load(cfg.WorkingDir))
	// Reap everything the registry started on exit — background bash jobs
	// (bash background:true) and a headless browser — so nothing outlives the
	// session.
	defer registry.Close()

	hookSet, err := hooks.New(cfg.WorkingDir, func(msg string) {
		fmt.Fprintln(os.Stderr, "hook: "+msg)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: hook config invalid, hooks disabled: %v\n", err)
	} else if !hookSet.Empty() {
		ag.AttachHooks(hookSet)
	}

	mgmt := manage.Load(cfg.WorkingDir)
	ag.AttachSubAgents(manage.Filter(subagents.Load(cfg.WorkingDir), func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled))
	ag.AttachSkills(manage.Filter(skills.Load(cfg.WorkingDir), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))
	// Let chat turns run saved Agent Builder workflows via the run_workflow tool
	// (@workflow), reusing the same node executors the builder + web sessions use.
	ag.AttachWorkflows(web.WorkflowToolFuncs(cfg))

	// The built-in default plugin marketplaces (claude-code, apple-genai) are no
	// longer auto-cloned at startup — those repos are large (tens–hundreds of MB)
	// and a background clone of both on first run is a lot of unprompted download,
	// and fails silently in egress-restricted/offline environments anyway. Instead
	// the Plugins panel lists them as one-click "Load" suggestions (see
	// plugins.DefaultMarketplaces + the web /plugins/marketplaces handler), so the
	// user pulls a marketplace on demand when they want its plugins.

	// Load the MCP + LSP configs (used by both the headless and interactive
	// paths). Invalid config is warned and skipped — never fatal.
	mcpCfgs, err := mcp.LoadConfigs(cfg.WorkingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: mcp config invalid, skipping: %v\n", err)
	}
	mcpCfgs = manage.Filter(mcpCfgs, func(c mcp.Config) string { return c.Name }, mgmt.MCPEnabled)
	lspCfgs, err := lsp.LoadConfigs(cfg.WorkingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: lsp config invalid, skipping: %v\n", err)
	}
	lspCfgs = manage.Filter(lspCfgs, func(c lsp.Config) string { return c.Name }, mgmt.LSPEnabled)
	connectMCP := mcp.Enabled() && len(mcpCfgs) > 0

	// Decide headless vs interactive up front: headless (-p/--print, piped stdin)
	// has no TUI to surface connect status in, so it connects MCP/LSP
	// synchronously below (the single prompt needs the full tool set
	// immediately). The interactive TUI connects them in the BACKGROUND once the
	// program is on screen (see further down), so startup is instant and the
	// connect progress is shown live in the TUI.
	prompt, isHeadless := headlessPrompt(*printFlag, *printShort)

	mgr := &mcp.Manager{}
	var lspMgr *lsp.Manager
	if isHeadless {
		if connectMCP {
			mgr = mcp.Register(context.Background(), registry, mcpCfgs, func(msg string) {
				fmt.Fprintln(os.Stderr, msg)
			})
		}
		lspMgr = lsp.Start(context.Background(), cfg.WorkingDir, lspCfgs, func(msg string) {
			fmt.Fprintln(os.Stderr, "lsp: "+msg)
		})
		if !lspMgr.Empty() {
			ag.AttachLSP(lspMgr)
		}
	}

	// Auto-connect to a DAP adapter when MN_DAP_ADDR is set. Non-fatal:
	// a bad address just means the dap_* tools will error "not connected" until
	// the user calls dap_connect manually.
	if dapAddr := strings.TrimSpace(os.Getenv("MN_DAP_ADDR")); dapAddr != "" {
		if err := ag.AttachDAP(dapAddr); err != nil {
			fmt.Fprintf(os.Stderr, "warning: DAP auto-connect to %s failed: %v\n", dapAddr, err)
		}
	}
	defer mgr.Close()
	defer func() {
		if lspMgr != nil {
			lspMgr.Close()
		}
	}()

	// Connect any configured code formatters. The agent runs them on changed
	// files after every successful write_file/edit_file (before diagnostics).
	fmtCfgs, err := format.LoadConfigs(cfg.WorkingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: format config invalid, skipping: %v\n", err)
	}
	if fmtMgr := format.New(cfg.WorkingDir, fmtCfgs); !fmtMgr.Empty() {
		ag.AttachFormatter(fmtMgr)
	}

	// Connect any configured worker agents (Claude Code / Optimus) behind the
	// `delegate` tool. Held-open worker processes (ACP) are reaped on exit.
	workerCfgs, err := worker.Load(cfg.WorkingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: workers.json invalid, skipping: %v\n", err)
	}
	workerMgr := worker.New(cfg.WorkingDir, workerCfgs)
	workerMgr.Detect()            // auto-add installed engines (claude/optimus)
	go workerMgr.ProbeReadiness() // probe `--version` in the background for /workers
	ag.AttachWorkers(workerMgr)
	defer workerMgr.CloseAll()

	// Rebuild the system prompt now that worker tools are registered (and, in
	// headless mode, MCP/LSP too). In the interactive TUI, MCP/LSP connect in the
	// background and re-refresh the prompt when they land (see startupDoneMsg), so
	// this initial build covers the built-ins + workers the first frame needs.
	ag.RefreshSystemPrompt()

	// Headless mode: -p/--print (and/or piped stdin and positional args) runs a
	// single prompt non-interactively, prints the result, and exits — no TUI.
	// Closures capture mgr/lspMgr so we can clean up before os.Exit (deferred
	// cleanups don't run on Exit).
	if isHeadless {
		cleanup := func() {
			registry.Close()
			workerMgr.CloseAll()
			mgr.Close()
			if lspMgr != nil {
				lspMgr.Close()
			}
		}
		os.Exit(runHeadless(context.Background(), ag, prompt, *jsonFlag, cleanup))
	}

	// This terminal's session can be driven from a browser over a relay (see
	// MN_RELAY / the /relay command): the terminal dials out to the relay and
	// serves the full multi-project app, with its live session wired through the
	// hub. The hub is created now so the controller can be wired before any
	// remote viewer connects.
	hub := bridge.New()

	// Past the headless os.Exit above: this is the interactive TUI, where a user
	// can answer approval prompts — required for git commit/push and leaving plan
	// mode (hard-gated, see Agent.SetInteractive).
	ag.SetInteractive(true)

	model := tui.New(cfg, ag, store)
	// The user, MCP, and LSP are all checked in the background once the
	// program is on screen (see the startup goroutine below), shown as an
	// animated loader. The user is always resolved; the MCP/LSP rows appear
	// only when those will actually connect. While the sequence runs the model
	// holds model turns until tools are registered.
	model.BeginStartupChecks(connectMCP, len(lspCfgs) > 0)
	model.SetRelay(cfg.RelayAddr) // seed the relay status pill (connecting if configured)
	model.SetHub(hub)
	// The TUI's Init() kicks off a silent quota+pricing fetch; no startup
	// goroutine needed here.

	// Detect the terminal background once, while we still own stdin in cooked
	// mode, and lock it in. This makes the UI adapt to light/dark terminals
	// without lipgloss probing the terminal at render time — a runtime probe
	// would race Bubble Tea's input reader and leak escape sequences into the
	// input box.
	lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())

	// Mouse-wheel scrolling is ON by default. It does take over click-drag text
	// selection; hold ⌥ Option (macOS) to select natively, or use /copy to copy
	// the last response. Set MN_MOUSE=0 to disable mouse capture entirely
	// (native selection works; scroll with the keyboard: shift+↑/↓, pgup/pgdn).
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("MN_MOUSE"))); v != "0" && v != "off" && v != "false" {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	prog := tea.NewProgram(model, opts...)

	// Background startup sequence: now that the program exists, run the three
	// startup checks in order — user, then MCP, then LSP — streaming
	// progress into the TUI's animated loader via prog.Send. All the network I/O
	// happens here; the connected MCP tools are registered on the TUI's update
	// loop (applyMCP), so the registry mutation never races a turn. (prog.Send
	// queues until Run() starts the event loop just below.)
	//
	// Over the relay (saved /relay or MN_RELAY), the terminal serves the FULL
	// multi-project app (every project/session on this machine), with this
	// terminal's LIVE session wired through the hub — so a remote browser browses
	// the whole session tree AND drives the in-terminal session bidirectionally.
	// Opening any other session of the terminal's project switches the terminal
	// to it, so the whole project is drivable in sync, one session at a time.
	relayAddr := cfg.RelayAddr
	go func() {
		ctx := context.Background()

		// 1. Local user. Resolves the OS username that labels the human's
		//    messages and tags this terminal's connector on the relay (per-user
		//    isolation hides this laptop from other users in the web tree). With
		//    the Ollama-only backend there's no cloud auth to establish here.
		prog.Send(tui.StartupUserStep())
		appleUser := ""
		errMsg := ""
		if u, uerr := user.Current(); uerr == nil {
			appleUser = u.Username
		} else {
			errMsg = uerr.Error()
		}
		prog.Send(tui.UserResultMsg(appleUser, errMsg))

		// 2. Relay connector. Launched here (after the local user resolves the
		//    owner) as its own goroutine so it doesn't block the MCP/LSP checks.
		//    The relay keeps one connector per machine (keyed owner+host,
		//    last-wins), so only one moony here may dial it — two would
		//    evict each other in a reconnect loop (the flapping relay pill). Claim
		//    the single per-machine connector slot in the background and promote
		//    to the live connector whenever it's free:
		//      - hold it -> serve this terminal as the live tunnel; serveRelay
		//        drives the status pill (connecting -> connected) and we publish
		//        that state so standby instances can mirror it.
		//      - held by another local instance (a daemon, or another terminal) ->
		//        mirror the holder's PUBLISHED relay state: "shared" (reachable in
		//        the web UI through that holder via the shared session store) only
		//        while it is actually connected, otherwise the holder's
		//        connecting/retrying state — so a downed relay never shows a
		//        misleading green "shared". Re-check on a timer so we promote to
		//        the live tunnel if it exits.
		if relayAddr != "" {
			report := tui.RelayReporter(prog)
			onRelayVersion := tui.RelayVersionReporter(prog)
			go func() {
				for {
					release, ok := daemon.TryHoldConnector()
					if !ok {
						report(standbyRelayState(daemon.ConnectorStatus()))
						time.Sleep(3 * time.Second)
						continue
					}
					// We own the slot. Publish "connecting" right away (clearing
					// any stale value from a previous holder) so standbys don't
					// briefly see an old state, then route browser messages/
					// approvals/interrupts/switches into the TUI (idempotent: just
					// sets the hub callbacks) and serve this machine's full app
					// with this terminal's session wired in as the live one.
					daemon.PublishConnectorStatus("connecting")
					tui.BindController(hub, prog)
					srv, _, err := app.BuildServer(cfg)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: relay disabled (cannot open session store): %v\n", err)
						release()
						return
					}
					// This server runs in the SAME process as the alt-screen TUI, so
					// its MCP connect traces must NOT hit os.Stderr (that paints over
					// the Bubble Tea frame and garbles the UI until a resize). Route
					// them into the TUI's status line instead — e.g. when a browser or
					// the iOS app opens a session and its MCP servers connect.
					srv.SetLogf(func(line string) { prog.Send(tui.ServerLogMsg(line)) })
					srv.AttachLiveMirror(hub, ag.SessionID()) // make this terminal's session live in the app
					// Publish each relay state change (for standbys) alongside
					// reporting it to this terminal's pill.
					holderReport := func(s tui.RelayState) {
						daemon.PublishConnectorStatus(relayStatusToken(s))
						report(s)
					}
					// serveRelay retries forever, so it doesn't return in practice;
					// if it ever does, drop the slot so another instance can take
					// over. (We intentionally don't release on normal process exit:
					// the next claimant reclaims our stale lock by dead pid — see
					// TryHoldConnector.)
					serveRelay(relayAddr, config.MachineName(), appleUser, strings.TrimSpace(os.Getenv("MN_RELAY_TOKEN")), srv.Handler(), holderReport, onRelayVersion)
					release()
					return
				}
			}()
		}

		// 3. MCP. ConnectAll does the network I/O only; the tools are registered
		//    on the update loop (applyMCP) when MCPResultMsg lands.
		if connectMCP {
			prog.Send(tui.StartupMCPStep())
			connectedMCP := mcp.ConnectAll(ctx, mcpCfgs, func(msg string) {
				prog.Send(tui.ServerLogMsg(msg))
			})
			prog.Send(tui.MCPResultMsg(connectedMCP))
		}

		// 4. LSP.
		if len(lspCfgs) > 0 {
			prog.Send(tui.StartupLSPStep())
			startedLSP := lsp.Start(ctx, cfg.WorkingDir, lspCfgs, func(msg string) {
				prog.Send(tui.ServerLogMsg("lsp: " + msg))
			})
			prog.Send(tui.LSPResultMsg(startedLSP))
		}

		prog.Send(tui.StartupDoneMsg())
	}()

	final, err := prog.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	// /update replaced this binary in place and asked to restart: now that the
	// TUI has quit (terminal restored), re-exec the updated executable with the
	// same args so the user lands back on the new build.
	if m, ok := final.(tui.Model); ok {
		// Close the MCP/LSP servers the TUI connected in the background (they're
		// owned by the model now, not main's deferred cleanup). Skipped on
		// restart, which re-execs and never returns — matching the old behavior
		// where the deferred Close also didn't run on exec.
		if m.ShouldRestart() {
			if exe, err := os.Executable(); err == nil {
				_ = syscall.Exec(exe, os.Args, os.Environ())
			}
		}
		m.CloseServers()
	}
}

// runDaemonCmd handles `moony daemon <run|install|uninstall|start|stop|status>`,
// the always-on relay connector that keeps this machine available in the web UI.
func runDaemonCmd(args []string) int {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "run":
		return daemonRun(args)
	case "install":
		return daemonInstall(args)
	case "uninstall":
		return daemonSimple(daemon.Uninstall, "uninstalled")
	case "start":
		return daemonSimple(daemon.Start, "started")
	case "stop":
		return daemonSimple(daemon.Stop, "stopped")
	case "status":
		s, err := daemon.Status()
		if err != nil {
			fmt.Fprintln(os.Stderr, "daemon status:", err)
			return 1
		}
		fmt.Println("moony daemon:", s)
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: moony daemon <run|install|uninstall|start|stop|status> [--relay host:port]")
		return 2
	}
}

func daemonSimple(fn func() error, done string) int {
	if err := fn(); err != nil {
		fmt.Fprintln(os.Stderr, "daemon:", err)
		return 1
	}
	fmt.Println("moony daemon " + done + ".")
	return 0
}

// daemonRun is the long-lived process the launchd/systemd unit executes: it
// serves this machine's multi-project app to the relay, headlessly, until a
// signal stops it.
func daemonRun(args []string) int {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	relayFlag := fs.String("relay", "", "relay host:port to dial (overrides MN_RELAY / saved /relay)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	config.LoadDotEnv()
	cfg := config.Load()
	if r := strings.TrimSpace(*relayFlag); r != "" {
		cfg.RelayAddr = r
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := daemon.Serve(ctx, cfg, logger.Printf); err != nil && ctx.Err() == nil {
		logger.Printf("daemon exited: %v", err)
		return 1
	}
	logger.Printf("daemon stopped")
	return 0
}

// runScheduleCmd handles `moony schedule <sub>`: manage recurring or
// one-shot prompts that run headlessly. The daemon fires due schedules; this
// command also lets you add/list/remove them and run/tick them by hand.
func runScheduleCmd(args []string) int {
	st, err := schedule.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "schedule:", err)
		return 1
	}
	if len(args) == 0 {
		return scheduleUsage()
	}
	rest := args[1:]
	switch args[0] {
	case "add":
		return scheduleAdd(st, rest)
	case "list", "ls":
		return scheduleList(st)
	case "remove", "rm", "delete":
		return scheduleSimple(rest, st.Remove, "removed")
	case "enable":
		return scheduleSimple(rest, func(id string) error { return st.SetEnabled(id, true) }, "enabled")
	case "disable":
		return scheduleSimple(rest, func(id string) error { return st.SetEnabled(id, false) }, "disabled")
	case "run":
		return scheduleRunOne(st, rest)
	case "tick":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "schedule:", err)
			return 1
		}
		schedule.RunDue(context.Background(), st, exe, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
		return 0
	default:
		return scheduleUsage()
	}
}

// runSessionKeyCmd implements `session-key <status|rewrap>` for the at-rest
// session-history encryption. Key selection follows the same env vars the store
// uses (MN_SESSION_KEY / _KEY_CMD / _KEY_FILE / _PASSPHRASE, MN_SESSION_ENCRYPT).
func runSessionKeyCmd(args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	store, err := session.NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "session-key:", err)
		return 1
	}
	switch sub {
	case "status":
		if store.Encrypted() {
			fmt.Println("session history: ENCRYPTED (AES-256-GCM) at", store.Dir())
		} else {
			fmt.Println("session history: PLAINTEXT (encryption disabled) at", store.Dir())
		}
		return 0
	case "rewrap", "rotate":
		// Re-encrypt every session under the currently-resolved key. To rotate,
		// the operator points the key env at the NEW key and runs this.
		n, err := store.Rewrap()
		if err != nil {
			fmt.Fprintln(os.Stderr, "session-key rewrap:", err)
			return 1
		}
		fmt.Printf("rewrapped %d session(s) under the current key\n", n)
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: moony session-key <status|rewrap>")
		fmt.Fprintln(os.Stderr, "  BYOK env: MN_SESSION_KEY (base64/hex 32B) | MN_SESSION_KEY_CMD | MN_SESSION_KEY_FILE | MN_SESSION_PASSPHRASE")
		fmt.Fprintln(os.Stderr, "  mode:     MN_SESSION_ENCRYPT=auto (default) | required | off")
		return 2
	}
}

func scheduleUsage() int {
	fmt.Fprintln(os.Stderr, "usage: moony schedule <add|list|remove|enable|disable|run|tick>")
	fmt.Fprintln(os.Stderr, `  add [-C dir] [-name n] "<spec>" "<prompt>"   spec: "every 30m" | "daily 09:00" | "once 2026-07-01T09:00"`)
	fmt.Fprintln(os.Stderr, "  Schedules fire while the daemon runs (moony daemon install); each run is a normal, web-browsable session.")
	fmt.Fprintln(os.Stderr, "  Note: scheduled runs are headless and auto-approve tool calls (deny rules still apply), so a schedule runs the prompt unattended in its working dir.")
	fmt.Fprintln(os.Stderr, "  Note: do not run `tick`/`run` from cron while the daemon is running; the daemon already fires due schedules.")
	return 2
}

func scheduleAdd(st *schedule.Store, args []string) int {
	fs := flag.NewFlagSet("schedule add", flag.ContinueOnError)
	dir := fs.String("C", "", "working directory for the run (default: current dir)")
	name := fs.String("name", "", "optional name for the schedule")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return scheduleUsage()
	}
	sc, err := schedule.ParseSpec(pos[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "schedule:", err)
		return 1
	}
	sc.Prompt = strings.Join(pos[1:], " ")
	sc.Name = *name
	if sc.WorkingDir = strings.TrimSpace(*dir); sc.WorkingDir == "" {
		sc.WorkingDir, _ = os.Getwd()
	}
	added, err := st.Add(sc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "schedule:", err)
		return 1
	}
	fmt.Printf("Added schedule %s — %s — next run %s\n", added.ID, added.Describe(), added.NextRun.Format(time.RFC1123))
	return 0
}

func scheduleList(st *schedule.Store) int {
	list, err := st.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "schedule:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Println("No schedules. Add one with: moony schedule add \"every 1h\" \"<prompt>\"")
		return 0
	}
	fmt.Println(schedule.FormatList(list))
	return 0
}

func scheduleSimple(args []string, fn func(string) error, done string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: moony schedule "+done[:len(done)-1]+" <id>")
		return 2
	}
	if err := fn(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "schedule:", err)
		return 1
	}
	fmt.Printf("Schedule %s %s.\n", args[0], done)
	return 0
}

func scheduleRunOne(st *schedule.Store, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: moony schedule run <id>")
		return 2
	}
	s, ok := st.Get(args[0])
	if !ok {
		fmt.Fprintln(os.Stderr, "schedule: not found:", args[0])
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "schedule:", err)
		return 1
	}
	fmt.Printf("Running schedule %s…\n", s.ID)
	sid, runErr := schedule.Run(context.Background(), s, exe)
	s.LastRun = time.Now()
	if runErr != nil {
		s.LastStatus = "error: " + runErr.Error()
		_ = st.Update(s)
		fmt.Fprintln(os.Stderr, "schedule run failed:", runErr)
		return 1
	}
	s.LastStatus = "ok (session " + sid + ")"
	_ = st.Update(s)
	fmt.Printf("Done — session %s (browse it in the web UI).\n", sid)
	return 0
}

// daemonInstall installs + starts the background service, resolving the relay
// from --relay, else the saved /relay address / MN_RELAY.
func daemonInstall(args []string) int {
	fs := flag.NewFlagSet("daemon install", flag.ContinueOnError)
	relayFlag := fs.String("relay", "", "relay host:port the connector dials out to")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	config.LoadDotEnv()
	relay := strings.TrimSpace(*relayFlag)
	if relay == "" {
		relay = config.Load().RelayAddr // saved /relay or MN_RELAY
	}
	if relay == "" {
		relay = daemon.DefaultRelay // fall back to the production relay
	}
	if err := daemon.Install(relay); err != nil {
		fmt.Fprintln(os.Stderr, "daemon install:", err)
		return 1
	}
	// Also save the relay as the interactive default so a hand-run moony
	// connects to the same relay (it takes over the connector while open; the
	// daemon yields and resumes on exit).
	cfg := config.Load()
	cfg.RelayAddr = relay
	if err := cfg.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "note: daemon installed, but couldn't save the relay as the interactive default:", err)
	}
	fmt.Printf("moony daemon installed and started (relay %s).\n", relay)
	fmt.Println("This machine will stay available in the web UI. A hand-run `moony` uses the same relay; check the daemon with: moony daemon status")
	return 0
}

// multiFlag collects a repeatable string flag (e.g. --root a --root b) so one CLI
// flag can be given more than once. Used for additional workspace roots.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, strings.TrimSpace(v)); return nil }

// serveRelay keeps a best-effort outbound connection to a relay (see cmd/relay),
// serving the mirror handler over it so a remote browser can view and drive this
// terminal session. owner is the local OS username that launched this
// terminal, so the relay shows it only to that user. It retries on failure and
// stays silent — the TUI owns the terminal, so this must never print — and is
// torn down with the process when the TUI exits. Enabled by the saved relay
// address (or MN_RELAY). report is called on each state change so the TUI can
// show a status pill.
func serveRelay(addr, host, owner, token string, h http.Handler, report func(tui.RelayState), onRelayVersion func(string)) {
	if report == nil {
		report = func(tui.RelayState) {}
	}
	for {
		report(tui.RelayConnecting)
		if conn, err := net.Dial("tcp", addr); err == nil {
			report(tui.RelayConnected)
			_ = relay.ServeConnector(conn, host, owner, token, config.Version, h, onRelayVersion)
		}
		report(tui.RelayRetrying)
		time.Sleep(3 * time.Second)
	}
}

// relayStatusToken maps the connector holder's pill state to the opaque token it
// publishes (via daemon.PublishConnectorStatus) for standby instances to mirror.
func relayStatusToken(s tui.RelayState) string {
	switch s {
	case tui.RelayConnected:
		return "connected"
	case tui.RelayRetrying:
		return "retrying"
	default: // connecting / anything else
		return "connecting"
	}
}

// standbyRelayState maps the holder's published status to the pill a standby
// instance shows. It only reflects "shared" (reachable in the web UI through the
// holder) while the holder is actually connected; if the holder can't reach the
// relay, the standby shows that same not-connected state rather than a misleading
// green "shared".
func standbyRelayState(holderStatus string) tui.RelayState {
	switch holderStatus {
	case "connected":
		return tui.RelayShared
	case "retrying":
		return tui.RelayRetrying
	default: // connecting, or unknown (older holder / nothing published yet)
		return tui.RelayConnecting
	}
}

// resolveSession loads a session to resume per the flags, or returns nil to
// start fresh. A nil store disables resume.
func resolveSession(store *session.Store, id string, continueOld bool, cfg *config.Config) *session.Session {
	if store == nil {
		return nil
	}
	switch {
	case id != "":
		sess, err := store.Load(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not load session %s: %v\n", id, err)
			return nil
		}
		return sess
	case continueOld:
		sess, err := store.Latest(cfg.WorkingDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not find a session to continue: %v\n", err)
			return nil
		}
		return sess // may be nil if none exist yet
	default:
		return nil
	}
}
