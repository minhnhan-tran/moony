// Package tui implements the terminal user interface for moony using
// Bubble Tea, modeled loosely on OpenCode's chat experience.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/attach"
	"github.com/pysty1995/moony/internal/bridge"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/plugins"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/schedule"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
)

// minInputLines is the input box's resting height (a roomier prompt);
// maxInputLines caps how tall it grows as the user types.
const (
	minInputLines = 3
	maxInputLines = 6
)

// sidebarWidth is the total column width of the left sidebar panel (including
// its right border), used when the panel is toggled on (ctrl+b).
const sidebarWidth = 28

// runState is the agent's current activity.
type runState int

const (
	stateIdle runState = iota
	stateBusy
	stateAwaitingApproval
	stateAwaitingQuestion
	stateMenu
)

// menuKind identifies which selection menu is open.
type menuKind int

const (
	menuModels menuKind = iota
	menuReasoning
	menuProviders
	menuSessions
	menuSkills
	menuMcps
	menuAgents
	menuWorkers
	menuPlugins
	menuSchedules
)

// menuItem is one selectable row in a menu.
type menuItem struct {
	label   string // primary text
	desc    string // secondary, dimmed text
	value   string // value applied on selection
	current bool   // whether this is the active setting
	install bool   // marketplace row: selecting installs it (vs toggling an existing one)
	kind    string // marketplace item type for menuPlugins: "bundle" | "skill" | "mcp"
}

// menu is an in-TUI selection list (e.g. for /models or /reasoning).
type menu struct {
	title  string
	kind   menuKind
	items  []menuItem
	cursor int
}

// slashCommand describes an in-app command for help and autocompletion.
type slashCommand struct {
	name string
	args string
	desc string
}

var slashCommands = []slashCommand{
	{"/help", "", "show commands and keys"},
	{"/cd", "<dir>", "re-root this session at another folder (tools, skills, agents follow)"},
	{"/init", "", "bootstrap AGENTS.md by surveying the repo"},
	{"/attach", "<path>", "attach an image, PDF, or Office file to the next message"},
	{"/paste", "", "attach an image from the macOS clipboard to the next message"},
	{"/memory", "[note]", "show project memory, or add a note (also: start a line with #)"},
	{"/plan", "[description|approve|add|clear|auto]", "draft an approvable plan artifact, then execute it"},
	{"/build", "", "leave plan mode; mutating tools are re-enabled"},
	{"/agent", "[name|init]", "list/switch agent profiles (build/plan or a custom one); init re-seeds the editable build/plan files"},
	{"/verify", "[command|off]", "auto-run a build/test after edits and self-correct on failure"},
	{"/selfreview", "[on|off]", "fast-model review of each edit turn's diff for introduced bugs (saved)"},
	{"/approval", "[level]", "set approval policy: prompt/safe/auto/danger-skip-permission (saved)"},
	{"/permissions", "[allow|deny|remove <rule>] [--global]", "list or edit persisted allow/deny rules"},
	{"/domains", "[allow|deny|remove <domain>]", "list or edit web_fetch domain rules"},
	{"/sandbox", "[on|off]", "confine bash writes to the working dir, macOS (saved)"},
	{"/relay", "[addr|off]", "auto-connect to a relay for remote access (saved)"},
	{"/model", "[id]", "show or set the model — the endpoint is derived from it (saved)"},
	{"/models", "", "pick a model from the active provider"},
	{"/provider", "[name]", "show or set the provider: ollama, openai, anthropic, openrouter"},
	{"/reasoning", "[level]", "set thinking level: auto/off/low/medium/high (saved)"},
	{"/compact", "", "summarize older history to free up context"},
	{"/goal", "[description|criteria|budget|clear]", "set a stop condition (criteria + token budget) the agent must satisfy"},
	{"/checkpoint", "[now|clear]", "show, refresh, or clear the session's distilled state snapshot"},
	{"/undo", "", "revert files written/edited in the most recent turn"},
	{"/rewind", "[n]", "roll back the last n turns — files AND conversation (default 1)"},
	{"/tokens", "", "show token usage this session"},
	{"/usage", "", "show token usage this session (hosted providers show no quota here)"},
	{"/doctor", "", "check environment & config (auth, network, MCP/LSP, Go env)"},
	{"/update", "", "download the latest moony, install it, and restart"},
	{"/skills", "[enable|disable|new|remove <name>]", "list skills, toggle, create, or delete one"},
	{"/mcps", "[enable|disable|add|remove|reload]", "list MCP servers, toggle, add, remove, or reload"},
	{"/lsp", "[enable|disable <name>] [--global] | reload", "list language servers, toggle, or reload"},
	{"/agents", "[enable|disable|new|remove <name>]", "list subagents, toggle, create, or delete one"},
	{"/workers", "[enable|disable|model|reset|add|remove <name> ...]", "list worker agents, toggle, set model, reset, add, or remove"},
	{"/plugins", "[install|remove <name>[@market] | marketplace add <git-url>]", "marketplace: browse & install skills/MCPs/bundles, or add git marketplaces"},
	{"/context", "[n|auto]", "show the context-usage breakdown (or set the window size)"},
	{"/sessions", "[id]", "switch between recent sessions for this directory"},
	{"/schedule", "[add <spec> <prompt>|remove|enable|disable|run <id>]", "list or manage headless scheduled prompts"},
	{"/copy", "", "copy the last response to the clipboard"},
	{"/clear", "", "clear the screen"},
	{"/queue", "[list|clear]", "list or empty the pending-message queue"},
	{"/btw", "<question>", "ask a quick side question without polluting the main thread"},
	{"/reset", "", "start a new session (forget history)"},
	{"/exit", "", "quit"},
}

// streamMsg carries one agent event (or a terminal signal) into the Bubble Tea
// update loop.
type streamMsg struct {
	ev   agent.Event
	done bool
	err  error
}

// remoteSubmitMsg, remoteDecisionMsg, remoteInterruptMsg, and remoteSwitchMsg
// are delivered via tea.Program.Send when a browser viewer drives the agent
// through the web mirror (see BindController). They flow through the SAME Update
// paths as local keyboard input, so the terminal stays the single owner of the
// agent and no extra locking is needed — Bubble Tea serializes every model
// update.
type remoteSubmitMsg struct{ text string }
type remoteDecisionMsg struct{ d agent.Decision }
type remoteInterruptMsg struct{}
type remoteSwitchMsg struct{ id string }
type remoteAnswerMsg struct{ labels []string }

// remoteQueueMsg applies a busy-time queue mutation requested by a browser
// viewer (see Hub.QueueControl). The terminal owns the authoritative queue
// (m.queued), so the mirror forwards add/remove/clear/pop here and the result is
// republished to every viewer via publishQueue.
type remoteQueueMsg struct {
	action string // "add" | "remove" | "clear" | "pop"
	index  int    // for "remove"
	text   string // for "add" (the message); for "pop" (the draft to stash)
}

// RelayState is the live state of the background relay connector, surfaced as a
// status-bar pill in the TUI (and mirrored to web viewers).
type RelayState int

const (
	RelayOff        RelayState = iota // no relay configured
	RelayConnecting                   // dialing the relay
	RelayConnected                    // tunnel up; a browser can view/drive
	RelayRetrying                     // dropped or unreachable; will retry
	RelayShared                       // another instance on this machine holds the
	//                                   connector slot; this session is still
	//                                   reachable in the web UI through it (shared
	//                                   session store). We promote to the live
	//                                   connector if that holder exits.
)

// label is the human word for a state ("" when off), reused by the TUI pill and
// the web mirror status.
func (s RelayState) label() string {
	switch s {
	case RelayConnecting:
		return "connecting"
	case RelayConnected:
		return "connected"
	case RelayRetrying:
		return "retrying"
	case RelayShared:
		return "shared"
	default:
		return ""
	}
}

// relayStatusMsg carries a relay state change from the background connector
// goroutine into the Update loop (delivered via RelayReporter -> prog.Send).
type relayStatusMsg struct{ state RelayState }

// relayVersionMsg carries the relay's build version (from the tunnel handshake)
// into the Update loop, so the TUI can warn when this terminal is behind it.
type relayVersionMsg struct{ version string }

// modelsLoadedMsg is delivered when an async /models fetch completes.
type modelsLoadedMsg struct {
	models []config.ModelInfo
	err    error
}

// compactDoneMsg is delivered when an async /compact finishes.
type compactDoneMsg struct {
	dropped int
	err     error
}

// btwDoneMsg is delivered when an async /btw side-question finishes.
type btwDoneMsg struct {
	question string
	answer   string
	err      error
}

// checkpointDoneMsg is delivered when an async `/checkpoint now` finishes.
type checkpointDoneMsg struct {
	status string
	err    error
}

// mcpPromptMsg is delivered when an async MCP prompts/get finishes.
type mcpPromptMsg struct {
	display string // what to show in the transcript (the user's slash text)
	body    string // what to send to the model
	err     error
}

// usageDoneMsg is delivered when an async /usage fetch finishes.
type usageDoneMsg struct {
	resp *llm.UsageResponse
	err  error
}

// attachStagedMsg carries the result of an async attachment resolution. Used for
// iWork files (Pages/Numbers/Keynote), whose conversion is a slow network round
// trip to the converter service, so it runs off the event loop.
type attachStagedMsg struct {
	att  attach.Attachment
	line string // error line, set when ok is false
	ok   bool
}

// doctorDoneMsg is delivered when an async /doctor check finishes.
type doctorDoneMsg struct {
	report string
}

// updateDoneMsg is delivered when the async /update finishes: either an error,
// "already current", or a successful install (which triggers a restart).
type updateDoneMsg struct {
	version string // the latest release version
	updated bool   // a new binary was installed
	already bool   // already on the latest
	err     error
}

// quotaRefreshMsg is delivered by the silent quota refresh (Init + post-turn).
// Unlike usageDoneMsg it never prints to the transcript on success — it just
// updates the persistent status-bar quota.
type quotaRefreshMsg struct {
	resp *llm.UsageResponse
	err  error
}

// mcpLogMsg is one progress line from the background startup sequence (see
// main.go). It's shown live in the TUI instead of being printed to stderr
// before the program starts.
type mcpLogMsg struct{ text string }

// The startup sequence runs three checks in order — user, MCP, LSP —
// driven by messages from main's background goroutine. startupStep identifies
// which check a message refers to; the model renders an animated loader (see
// startupView) while it's in flight, holding model turns until it finishes.
type startupStep int

const (
	stepUser startupStep = iota
	stepMCP
	stepLSP
)

// checkState is the lifecycle of a single startup check.
type checkState int

const (
	checkPending checkState = iota // not started yet (dim, ◦)
	checkRunning                   // in flight (spinner)
	checkOK                        // finished cleanly (green ✓)
	checkWarn                      // finished with a problem (red ⚠)
)

// startupCheck is one row of the startup loader.
type startupCheck struct {
	step   startupStep
	label  string
	state  checkState
	detail string // trailing summary, e.g. the username or "3 connected"
}

// startupStepMsg marks a check as now running (turns its row's marker into the
// spinner).
type startupStepMsg struct{ step startupStep }

// userResultMsg carries the local-user check outcome: the resolved
// username, or a non-empty errMsg when the session is unavailable.
type userResultMsg struct {
	user   string
	errMsg string
}

// mcpResultMsg carries the connected MCP manager (nil = nothing connected). Tool
// registration happens on the update loop in applyMCP, so it never races a turn
// goroutine (turns are held for the whole startup sequence).
type mcpResultMsg struct{ mgr *mcp.Manager }

// lspResultMsg carries the started LSP manager (nil = nothing started).
type lspResultMsg struct{ mgr *lsp.Manager }

// startupDoneMsg ends the startup sequence: the result handlers have already
// registered/attached, so this rebuilds the prompt, posts the summary line, and
// releases any held model turns.
type startupDoneMsg struct{}

// ServerLogMsg wraps a background startup progress line so main's goroutine can
// deliver it via (*tea.Program).Send.
func ServerLogMsg(text string) tea.Msg { return mcpLogMsg{text: text} }

// Exported constructors so main's background goroutine can drive the staged
// startup loader via (*tea.Program).Send.
func StartupUserStep() tea.Msg { return startupStepMsg{step: stepUser} }
func StartupMCPStep() tea.Msg  { return startupStepMsg{step: stepMCP} }
func StartupLSPStep() tea.Msg  { return startupStepMsg{step: stepLSP} }

// UserResultMsg reports the local-user check result (errMsg empty on
// success).
func UserResultMsg(user, errMsg string) tea.Msg {
	return userResultMsg{user: user, errMsg: errMsg}
}

// MCPResultMsg / LSPResultMsg wrap the connected managers (either may be nil).
func MCPResultMsg(mgr *mcp.Manager) tea.Msg { return mcpResultMsg{mgr: mgr} }
func LSPResultMsg(mgr *lsp.Manager) tea.Msg { return lspResultMsg{mgr: mgr} }

// StartupDoneMsg ends the startup sequence.
func StartupDoneMsg() tea.Msg { return startupDoneMsg{} }

// Model is the root Bubble Tea model.
type Model struct {
	cfg   *config.Config
	agent *agent.Agent
	store *session.Store

	// hub mirrors this session to web viewers and routes their control actions
	// back here. nil disables mirroring (the common case for tests).
	hub *bridge.Hub
	// modelsRemote is set while a /models fetch was triggered by a web viewer,
	// so the result is mirrored as a list (web can't drive the picker menu)
	// instead of opening the terminal menu.
	modelsRemote bool

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model
	trans    transcript

	// seenAnnounce tracks announcement ids already shown this session so the
	// live-refresh poll (announceRefreshCmd) only surfaces new notices.
	seenAnnounce map[string]bool

	width  int
	height int
	ready  bool

	state  runState
	ch     chan streamMsg
	cancel context.CancelFunc

	// compacting is true while an async /compact summarization is in flight, so
	// the spinner ticks and the status bar shows progress (the agent isn't in a
	// normal busy turn during this).
	compacting bool

	// btwBusy is true while a /btw side-question is in flight. Like compacting,
	// it shows a spinner but is distinct from a normal turn so transcript /
	// queue logic doesn't treat it as one.
	btwBusy bool

	// pending holds an in-flight approval request awaiting the user's decision.
	pending *agent.ApprovalRequest

	// pendingQ holds an in-flight ask_user question awaiting the user's choice,
	// with qCursor the highlighted option and qChosen the toggled set (for
	// multiple-choice questions).
	pendingQ *agent.QuestionRequest
	qCursor  int
	qChosen  map[int]bool
	// qTextMode routes keystrokes to the input for a free-text answer (only when
	// the question's AllowText is set); 't' enters it, esc returns to option nav.
	qTextMode bool

	// menu holds an open selection menu (e.g. /models), or nil.
	menu *menu

	// streamingIdx is the transcript index of the assistant entry currently
	// receiving text deltas, or -1 when none is open.
	streamingIdx int

	// thinkingIdx is the transcript index of the extended-thinking entry
	// currently receiving deltas, or -1 when none is open.
	thinkingIdx int

	// activeTool is the tool currently executing (shown in the status bar).
	activeTool string

	// toolsInFlight counts tool calls that have started but not yet finished.
	// With parallel execution several can run at once; the status bar shows a
	// count instead of a single name when more than one is active.
	toolsInFlight int

	// curStep/maxStep track progress through the current turn's tool-call loop
	// (shown in the status bar while busy; 0 when idle).
	curStep, maxStep int

	// git working-tree status for the header/status panel (refreshed async at
	// startup and after each turn). gitRepo is false outside a repo.
	gitBranch   string
	gitDirty    int
	gitRepo     bool
	gitAhead    int
	gitBehind   int
	gitUpstream string

	// Command history (most recent last). histIdx == len(history) means the
	// user is editing a fresh draft; histStash holds that draft while browsing.
	history   []string
	histIdx   int
	histStash string

	// queued holds raw user inputs typed while the agent was busy. They are
	// flushed one at a time, FIFO, after each turn returns to stateIdle. Each
	// item is dispatched through the same submit() path as a fresh keystroke,
	// so slash commands, `#` memory shortcuts, and attachments all behave
	// identically to typing them when idle.
	queued []string
	// poppedQueue is true when the input currently holds a queue item that Esc
	// popped back for editing (and the user hasn't touched it since). It lets a
	// repeated Esc DISCARD that popped item instead of re-stashing it onto the
	// queue — otherwise, with >1 queued message, Esc just cycles the last item
	// between the input and the queue forever and can never drain it. Cleared
	// the moment the user edits the input or the draft is submitted/queued.
	poppedQueue bool

	totalIn      int
	totalOut     int
	totalCostUSD float64
	// totalCacheRead/totalCacheWrite are the session's prompt-cache token totals
	// (subsets of totalIn): tokens served from the cache (billed cheap) and tokens
	// that wrote new cache entries. Surfaced by /tokens and the status bar so the
	// user can see caching engaging. Cumulative across the session like totalIn.
	totalCacheRead  int
	totalCacheWrite int

	// lastTurnInTokens is the input-token size of the most recently completed
	// turn. It's a good proxy for "current context fill" since the next turn
	// will send everything that turn sent plus that turn's response — so this
	// number is what the model just measured the conversation at. Cleared on
	// /reset and /clear.
	lastTurnInTokens int
	// Floodgate-reported quota for the current 24h period. Refreshed on Init
	// and after every TurnDone via refreshQuotaCmd; also kept in sync by
	// /usage. quotaFetched flips true on the first successful response so
	// the status bar can choose between "real quota" and the in-session
	// estimate.
	quotaSpend           float64
	quotaBudget          float64
	quotaResetTime       string
	quotaProject         string // non-empty when on a project-scoped account
	quotaFetched         bool
	quotaErr             string // last fetch error message (for /tokens diagnostics)
	quotaTokens          int    // tokens used in the quota period (24h)
	quotaTokensMax       int    // per-day token cap, when the budget carries one
	quotaTokenRate       int    // token rate limit (tokens per quotaTokenRatePeriod)
	quotaTokenRatePeriod string // rate-limit window, e.g. "4m"

	// todos is the agent's current task list (updated via TodosUpdated events).
	todos []tools.Todo

	// plan is the session's plan artifact (updated via PlanUpdated events and
	// when switching sessions); nil when there is no plan.
	plan *session.Plan

	// attachments are files/images staged (via @mentions, /attach, or /paste)
	// to send with the next user message; cleared once the turn starts.
	attachments []attach.Attachment

	// userCommands are custom slash commands loaded from
	// ~/.config/moony/commands and ./.sqe/commands.
	userCommands []userCommand

	// mcpPrompts surfaces MCP-supplied prompts as slash commands; each one
	// expands by calling prompts/get on the originating server.
	mcpPrompts []mcp.PromptDef

	// mcpServers summarizes the connected MCP servers (name + capability
	// counts) for the left sidebar's MCP section.
	mcpServers []mcp.ServerInfo

	// mcpMgr is the live MCP manager, so /mcps reload can reconnect without a
	// restart. May be nil when no servers are configured.
	mcpMgr *mcp.Manager

	// lspMgr is the live language-server manager, so /lsp reload can restart
	// servers without restarting the agent. May be nil when none are configured.
	lspMgr *lsp.Manager

	// MCP/LSP connect in the background after the TUI is on screen, alongside the
	// user check (see main.go), so startup is instant instead of blocking
	// on every server handshake. startupActive is true while that sequence runs;
	// startupChecks is the per-check state rendered as the animated loader (see
	// startupView). While active, model turns are held in `queued` and released
	// by startupDoneMsg — tool registration runs on this update loop, so it never
	// races a turn goroutine. serverStatus is the latest live progress line.
	startupActive bool
	startupChecks []startupCheck
	serverStatus  string

	// relayAddr is the configured relay host:port (empty = no relay); relayState
	// is the background connector's live state, shown as a status-bar pill.
	relayAddr  string
	relayState RelayState
	// relayVersionWarned guards the "a newer version is available" notice so it
	// fires at most once per session (the connector re-sends its version on every
	// reconnect).
	relayVersionWarned bool
	// restart is set by /update after a successful self-install; main() re-execs
	// the (now-replaced) binary once the program quits.
	restart bool

	// showSidebar toggles the left panel (Todos + MCP) on and off (ctrl+b).
	showSidebar bool
}

// New builds the root model. store may be nil to disable session features; the
// agent's existing history (e.g. a resumed session) is seeded into the view.
func New(cfg *config.Config, ag *agent.Agent, store *session.Store) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask moony to build, fix, or explain something…"
	// Render the "› " marker only on the first line; without this the textarea
	// repeats the prompt on every line, so a multi-line box shows a stack of
	// "›" markers.
	ta.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return inputPromptStyle.Render("› ")
		}
		return ""
	})
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(minInputLines)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = statusBusyStyle

	m := Model{
		cfg:          cfg,
		agent:        ag,
		store:        store,
		input:        ta,
		spinner:      sp,
		state:        stateIdle,
		streamingIdx: -1,
		thinkingIdx:  -1,
		todos:        ag.Todos(),
		plan:         ag.Plan(),
		userCommands: loadUserCommands(cfg.WorkingDir),
		// startupActive stays false by default: it's only switched on by
		// BeginStartupChecks when main launches the background startup sequence.
		// Headless and no-server sessions never call it, so turns are never held.
	}

	if seeded := seedEntries(ag.Messages()); len(seeded) > 0 {
		m.trans.entries = seeded
		m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Resumed session %s (%d messages). /reset to start fresh.", ag.SessionID(), ag.History())})
	} else {
		m.trans.add(entry{kind: entryBanner, text: welcomeBanner(cfg)})
	}
	return m
}

// Init satisfies tea.Model.
func (m Model) Init() tea.Cmd {
	// Mirror the current conversation so a browser viewer that connects before
	// the first new turn sees the full history — crucial when resuming a saved
	// session, whose prior messages would otherwise never reach the hub. (Reset
	// + replay; a no-op-ish empty replay for a fresh session.)
	m.publishTranscript()
	// Publish an initial status snapshot so a browser viewer's status bar is
	// populated (model/route/0 tokens) before the first turn.
	m.publishStatus()
	// Mirror the slash-command list so web viewers get a command palette.
	m.publishCommands()
	// Mirror the project's saved sessions so web viewers get a session list.
	m.publishSessions()
	// Kick off a silent quota fetch immediately so the status bar can show the
	// real Floodgate quota from the very first frame (and also overlay
	// authoritative pricing onto the cost calculator).
	cmds := []tea.Cmd{textarea.Blink, m.refreshQuotaCmd(), m.refreshGitCmd(), m.announceCmd(), announceRefreshCmd()}
	// Animate the startup loader spinner while the background checks run.
	if m.startupActive {
		cmds = append(cmds, m.spinner.Tick)
	}
	return tea.Batch(cmds...)
}

// Update satisfies tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		m.refresh()
		return m, nil

	case tea.KeyMsg:
		// While awaiting approval, capture the decision keys exclusively.
		if m.state == stateAwaitingApproval {
			switch msg.String() {
			case "y", "Y", "enter":
				return m.decide(agent.Approve)
			case "a", "A":
				return m.decide(agent.ApproveAlways)
			case "n", "N", "esc":
				return m.decide(agent.Deny)
			case "ctrl+c":
				if m.cancel != nil {
					m.cancel()
				}
				return m, tea.Quit
			}
			return m, nil
		}

		// While awaiting an ask_user answer, capture the chooser keys exclusively.
		if m.state == stateAwaitingQuestion && m.pendingQ != nil {
			n := len(m.pendingQ.Options)
			// Free-text entry mode: route typing to the input; enter submits the
			// typed answer, esc returns to option navigation.
			if m.qTextMode {
				switch msg.String() {
				case "enter":
					// Submit the toggled options plus the typed note as one answer
					// (either alone is fine). answerQuestion clears the box + mode.
					ans := m.selectedQLabels()
					if v := strings.TrimSpace(m.input.Value()); v != "" {
						ans = append(ans, v)
					}
					if len(ans) > 0 {
						return m.answerQuestion(ans)
					}
					return m, nil
				case "esc":
					// With no options there's nowhere to go back to → dismiss.
					if len(m.pendingQ.Options) == 0 {
						return m.answerQuestion(nil)
					}
					// Back to option nav; KEEP the typed draft so re-entering text
					// mode (t) after toggling another option doesn't lose it.
					m.qTextMode = false
					m.refresh()
					return m, nil
				default:
					var cmd tea.Cmd
					m.input, cmd = m.input.Update(msg)
					m.refresh()
					return m, cmd
				}
			}
			switch msg.String() {
			case "up", "k", "ctrl+p":
				if m.qCursor > 0 {
					m.qCursor--
				}
			case "down", "j", "ctrl+n":
				if m.qCursor < n-1 {
					m.qCursor++
				}
			case "home", "g":
				m.qCursor = 0
			case "end", "G":
				m.qCursor = n - 1
			case " ":
				// Space toggles the highlighted option for multiple-choice
				// questions; for single-choice it's a no-op (use enter).
				if m.pendingQ.Multiple {
					if m.qChosen == nil {
						m.qChosen = map[int]bool{}
					}
					m.qChosen[m.qCursor] = !m.qChosen[m.qCursor]
				}
			case "enter":
				return m.submitQuestion()
			case "t":
				// Enter free-text mode (only when the question allows it).
				if m.pendingQ.AllowText {
					m.qTextMode = true
					m.input.Focus()
					m.refresh()
					return m, textarea.Blink
				}
			case "esc":
				return m.answerQuestion(nil) // dismiss without choosing
			case "ctrl+c":
				if m.cancel != nil {
					m.cancel()
				}
				return m, tea.Quit
			default:
				// Number keys 1-9 jump to (and, for single-choice, pick) an option.
				if len(msg.String()) == 1 {
					if d := msg.String()[0]; d >= '1' && d <= '9' {
						if idx := int(d - '1'); idx < n {
							m.qCursor = idx
							if m.pendingQ.Multiple {
								if m.qChosen == nil {
									m.qChosen = map[int]bool{}
								}
								m.qChosen[idx] = !m.qChosen[idx]
							} else {
								return m.submitQuestion()
							}
						}
					}
				}
			}
			m.refresh()
			return m, nil
		}

		// While a selection menu is open, capture navigation keys exclusively.
		if m.state == stateMenu && m.menu != nil {
			switch msg.String() {
			case "up", "k", "ctrl+p":
				if m.menu.cursor > 0 {
					m.menu.cursor--
				}
			case "down", "j", "ctrl+n":
				if m.menu.cursor < len(m.menu.items)-1 {
					m.menu.cursor++
				}
			case "home", "g":
				m.menu.cursor = 0
			case "end", "G":
				m.menu.cursor = len(m.menu.items) - 1
			case "enter":
				return m.applyMenu()
			case "esc", "q":
				m.menu = nil
				m.state = stateIdle
			case "ctrl+c":
				return m, tea.Quit
			default:
				if n := digitKey(msg.String()); n >= 1 && n <= len(m.menu.items) {
					m.menu.cursor = n - 1
					return m.applyMenu()
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "esc":
			if m.state == stateBusy {
				// While busy, Esc first pops the most recent queued message back
				// into the input (if any) so the user can edit/discard it without
				// killing the running turn. Only when the queue is empty does Esc
				// fall through to the cancel-turn behavior.
				if n := len(m.queued); n > 0 {
					last := m.queued[n-1]
					m.queued = m.queued[:n-1]
					// If the user is mid-compose, stash the current draft on the
					// front of the queue so we don't clobber it. But if the input
					// merely holds a previously popped item (poppedQueue, untouched
					// since), discard it instead of re-stashing — otherwise a
					// repeated Esc would cycle the last item forever and the queue
					// could never be drained.
					if cur := strings.TrimSpace(m.input.Value()); cur != "" && !m.poppedQueue {
						m.queued = append([]string{cur}, m.queued...)
					}
					m.input.SetValue(last)
					m.poppedQueue = true
					m.publishQueue()
					m.refresh()
					return m, nil
				}
				// Queue empty: a popped item still sitting in the input is the
				// last one — drop it so Esc fully clears the queue before it
				// falls through to cancelling the turn.
				if m.poppedQueue && strings.TrimSpace(m.input.Value()) != "" {
					m.input.Reset()
					m.poppedQueue = false
					m.refresh()
					return m, nil
				}
				if m.cancel != nil {
					m.cancel()
					m.trans.add(entry{kind: entryInfo, text: "(cancelling…)"})
					m.refresh()
				}
			}
			return m, nil
		case "ctrl+b":
			// Toggle the left sidebar. Recompute layout-dependent widths so the
			// transcript reflows to fill the freed/occupied space immediately.
			m.showSidebar = !m.showSidebar
			m.resize(m.width, m.height)
			m.refresh()
			return m, nil
		case "ctrl+o":
			// Expand or collapse every tool call's output/diff at once.
			m.trans.toggleAll()
			m.refresh()
			return m, nil
		case "ctrl+y":
			// Copy the last assistant response to the clipboard (same as /copy).
			m.trans.add(entry{kind: entryInfo, text: m.copyLast()})
			m.refresh()
			return m, nil
		case "enter":
			if m.state == stateBusy {
				// Queue the typed message instead of dropping it. Empty/whitespace
				// input is ignored; non-empty drafts go onto the FIFO queue and
				// will be dispatched one per turn after the running turn ends.
				txt := strings.TrimRight(m.input.Value(), " \t\n")
				if strings.TrimSpace(txt) == "" {
					return m, nil
				}
				// /btw is the exception: it's a one-shot side-question designed to
				// run CONCURRENTLY with the active turn (its handler guards only on
				// compacting/btwBusy, never stateBusy). Queuing it would defer it
				// until the turn ends — defeating the point — so dispatch it now.
				// Every other slash command still queues (it runs on flush).
				if fields := strings.Fields(txt); len(fields) > 0 && fields[0] == "/btw" {
					return m.submit()
				}
				m.queued = append(m.queued, txt)
				m.input.Reset()
				m.poppedQueue = false
				m.publishQueue()
				m.refresh()
				return m, nil
			}
			return m.submit()
		case "tab":
			if m.state == stateIdle {
				if strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
					m.completeCommand()
					m.refresh()
					return m, nil
				}
				if m.completeAtMention() { // @agent / @worker autocompletion
					m.refresh()
					return m, nil
				}
				// Empty input: Tab toggles the build/plan agent (opencode-style).
				if strings.TrimSpace(m.input.Value()) == "" {
					if m.agent.PlanMode() {
						m.agent.SetPlanMode(false)
						m.trans.add(entry{kind: entryInfo, text: "Switched to build — mutating tools are available."})
					} else {
						m.agent.SetPlanMode(true)
						m.trans.add(entry{kind: entryInfo, text: "Switched to plan (read-only) — edits/task disabled, bash asks first. Tab again or /build to return."})
					}
					m.refresh()
					return m, nil
				}
			}
		case "up", "ctrl+p":
			if m.state == stateIdle && m.atInputTop() && len(m.history) > 0 {
				m.historyPrev()
				return m, nil
			}
		case "down", "ctrl+n":
			if m.state == stateIdle && m.atInputBottom() && m.histIdx < len(m.history) {
				m.historyNext()
				return m, nil
			}
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		case "shift+up":
			m.viewport.ScrollUp(1)
			return m, nil
		case "shift+down":
			m.viewport.ScrollDown(1)
			return m, nil
		}

	case spinner.TickMsg:
		if m.state == stateBusy || m.compacting || m.btwBusy || m.startupActive {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.MouseMsg:
		// A left-click on a collapsible tool call toggles its body. All other
		// mouse activity (wheel, motion) drives the viewport only — it must
		// never reach the textarea, or scroll bursts can leak into the input.
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft && m.toggleToolAtClick(msg) {
			return m, nil
		}
		var vcmd tea.Cmd
		m.viewport, vcmd = m.viewport.Update(msg)
		return m, vcmd

	case streamMsg:
		return m.handleStream(msg)

	case remoteSubmitMsg:
		// A browser viewer sent input. Route it exactly like locally typed text
		// (slash commands, # memory, or a prompt), but ignore it unless idle.
		return m.remoteSubmit(msg.text)

	case remoteDecisionMsg:
		// A browser viewer answered the pending approval.
		if m.state == stateAwaitingApproval && m.pending != nil {
			return m.decide(msg.d)
		}
		return m, nil

	case remoteAnswerMsg:
		// A browser viewer answered the pending ask_user question.
		if m.state == stateAwaitingQuestion && m.pendingQ != nil {
			return m.answerQuestion(msg.labels)
		}
		return m, nil

	case remoteInterruptMsg:
		// A browser viewer asked to cancel the running turn.
		if m.state == stateBusy && m.cancel != nil {
			m.cancel()
			m.trans.add(entry{kind: entryInfo, text: "(interrupted from web)"})
			m.refresh()
		}
		return m, nil

	case remoteSwitchMsg:
		// A browser viewer clicked another session of this project. Switch the
		// terminal to it (only meaningful while idle); switchSession reseeds the
		// transcript, republishes it to viewers, and updates the hub's active id.
		if m.state == stateIdle {
			return m.switchSession(msg.id)
		}
		return m, nil

	case remoteQueueMsg:
		// A browser viewer manipulated the busy-time message queue. The terminal
		// owns m.queued, so apply it here and republish so every viewer (and the
		// terminal's own queue strip) stays in sync. Note "add" is the busy-time
		// enqueue path for web input — the mirror forwards it here rather than
		// through Submit (which would defer it until idle and start a turn).
		switch msg.action {
		case "add":
			if txt := strings.TrimRight(msg.text, " \t\n"); strings.TrimSpace(txt) != "" {
				m.queued = append(m.queued, txt)
			}
		case "remove":
			if msg.index >= 0 && msg.index < len(m.queued) {
				m.queued = append(m.queued[:msg.index], m.queued[msg.index+1:]...)
			}
		case "clear":
			m.queued = nil
		case "pop":
			// Pop the most recent queued item; stash the viewer's in-progress draft
			// onto the front so it isn't lost (mirrors the terminal's Esc-pop). The
			// popped text is returned to the viewer's composer by the mirror itself
			// (read from the published queue), so we only mutate here.
			if n := len(m.queued); n > 0 {
				m.queued = m.queued[:n-1]
				if stash := strings.TrimSpace(msg.text); stash != "" {
					m.queued = append([]string{stash}, m.queued...)
				}
			}
		}
		m.publishQueue()
		m.refresh()
		return m, nil

	case relayStatusMsg:
		// The background relay connector changed state; update the pill (and
		// mirror it to web viewers via the status snapshot).
		if m.relayState != msg.state {
			m.relayState = msg.state
			m.publishStatus()
			m.refresh()
		}
		return m, nil

	case announceMsg:
		m.applyAnnouncements(msg.items)
		return m, nil
	case announceTickMsg:
		// Live refresh: re-fetch, then re-arm the tick. applyAnnouncements dedups
		// by id, so only announcements posted since the last poll are shown.
		return m, tea.Batch(m.announceCmd(), announceRefreshCmd())
	case relayVersionMsg:
		// The relay reported its build version over the tunnel handshake. If this
		// terminal is older, surface a one-time update notice (the mirror of the
		// web UI's version-behind banner).
		if !m.relayVersionWarned && config.VersionLess(config.Version, msg.version) {
			m.relayVersionWarned = true
			m.NoteWarning("A newer moony (v" + msg.version + ") is available from the relay — you're on v" + config.Version + ". Update with the relay's Download page (/download) or: curl -fsSL <relay>/install.sh | sh")
			m.refresh()
		}
		return m, nil

	case modelsLoadedMsg:
		return m.handleModelsLoaded(msg)

	case btwDoneMsg:
		m.btwBusy = false
		if msg.err != nil {
			note := "/btw failed: " + msg.err.Error()
			m.trans.add(entry{kind: entryInfo, text: note})
			m.publishInfo(note)
		} else {
			m.trans.add(entry{kind: entryAssistant, text: msg.answer, side: true})
			// Mirror the side answer to web viewers. There's no "side" concept on
			// the wire, so prefix it as an info line so it reads as the /btw reply
			// (the question was mirrored at dispatch) rather than a main-thread turn.
			m.publishInfo("↳ /btw: " + msg.answer)
		}
		m.refresh()
		return m, nil
	case compactDoneMsg:
		m.compacting = false
		// Publish the outcome to web viewers too: /compact arrives async, so the
		// result lands here (not in remoteSubmit's synchronous diff), and without
		// publishing it a browser mirror would sit on "Compacting older history…"
		// forever even though the terminal finished.
		if msg.err != nil {
			note := "compact failed: " + msg.err.Error()
			m.trans.add(entry{kind: entryError, text: note})
			m.publishError(note)
		} else if msg.dropped == 0 {
			note := "Nothing to compact yet."
			m.trans.add(entry{kind: entryInfo, text: note})
			m.publishInfo(note)
		} else {
			note := fmt.Sprintf("Compacted %d earlier messages into a summary.", msg.dropped)
			m.trans.add(entry{kind: entryInfo, text: note})
			m.publishInfo(note)
		}
		m.publishStatus() // context shrank; refresh the mirrored token/context readout
		m.refresh()
		return m, nil

	case checkpointDoneMsg:
		if msg.err != nil {
			note := "checkpoint failed: " + msg.err.Error()
			m.trans.add(entry{kind: entryError, text: note})
			m.publishError(note)
		} else {
			m.trans.add(entry{kind: entryInfo, text: msg.status})
			m.publishInfo(msg.status)
		}
		m.refresh()
		return m, nil

	case mcpPromptMsg:
		if msg.err != nil {
			m.trans.add(entry{kind: entryError, text: "mcp prompt failed: " + msg.err.Error()})
			m.refresh()
			return m, nil
		}
		return m.runUserMessage(msg.display, msg.body)

	case usageDoneMsg:
		if msg.err != nil {
			m.quotaErr = msg.err.Error()
			m.trans.add(entry{kind: entryError, text: "usage failed: " + msg.err.Error()})
		} else {
			m.trans.add(entry{kind: entryInfo, text: formatUsage(msg.resp)})
			m.applyUsage(msg.resp)
			m.publishStatus()
		}
		m.refresh()
		return m, nil

	case attachStagedMsg:
		if msg.ok {
			m.attachments = append(m.attachments, msg.att)
			m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Staged %s — sent with your next message (%d attached).", msg.att.Label(), len(m.attachments))})
		} else {
			m.trans.add(entry{kind: entryError, text: msg.line})
		}
		m.refresh()
		return m, nil

	case doctorDoneMsg:
		m.trans.add(entry{kind: entryInfo, text: msg.report})
		m.refresh()
		return m, nil

	case updateDoneMsg:
		switch {
		case msg.err != nil:
			m.trans.add(entry{kind: entryError, text: "Update failed: " + msg.err.Error()})
			m.refresh()
			return m, nil
		case msg.already:
			m.trans.add(entry{kind: entryInfo, text: "Already on the latest moony (v" + msg.version + ")."})
			m.refresh()
			return m, nil
		default:
			// Installed the new binary; quit so main can re-exec it.
			m.trans.add(entry{kind: entryInfo, text: "Updated to v" + msg.version + " — restarting…"})
			m.refresh()
			m.restart = true
			return m, tea.Quit
		}

	case gitInfoMsg:
		m.gitBranch, m.gitDirty, m.gitRepo = msg.branch, msg.dirty, msg.repo
		m.gitAhead, m.gitBehind, m.gitUpstream = msg.ahead, msg.behind, msg.upstream
		m.publishStatus()
		m.refresh()
		return m, nil

	case quotaRefreshMsg:
		// Silent: never prints to the transcript. Just refreshes the
		// persistent quota display + pricing overlay.
		if msg.err != nil {
			m.quotaErr = msg.err.Error()
		} else {
			m.applyUsage(msg.resp)
			m.publishStatus()
			m.refresh()
		}
		return m, nil

	case mcpLogMsg:
		// Live progress from the background startup sequence. We no longer echo
		// these lines into the transcript (the animated startup panel is the
		// single source of truth); just keep the latest for the sidebar header.
		m.serverStatus = msg.text
		m.refresh()
		return m, nil

	case startupStepMsg:
		// A check just started: switch its row to the spinner.
		m.setCheckState(msg.step, checkRunning, "")
		m.refresh()
		return m, nil

	case userResultMsg:
		if msg.errMsg != "" {
			m.setCheckState(stepUser, checkWarn, "unknown user")
			m.NoteWarning("Could not resolve the local user account; your messages will be labelled \"You\" and this terminal may not appear in your relay laptop list.\n  " + msg.errMsg)
		} else {
			m.setCheckState(stepUser, checkOK, msg.user)
			m.SetUserName(msg.user)
		}
		m.refresh()
		return m, nil

	case mcpResultMsg:
		// Register the connected MCP tools on this update loop (turns are held
		// for the whole startup, so the mutex-free registry is never raced).
		m.applyMCP(msg.mgr)
		m.refresh()
		return m, nil

	case lspResultMsg:
		m.applyLSP(msg.mgr)
		m.refresh()
		return m, nil

	case startupDoneMsg:
		// All checks done: rebuild the prompt so the "Available tools" list
		// reflects the now-connected servers, then release any model turns held
		// while startup ran. startupActive flips off (stops the spinner and
		// turn-holding) but startupChecks is kept so the panel stays on screen
		// with the final ✓/⚠ results.
		m.agent.RefreshSystemPrompt()
		m.startupActive = false
		m.serverStatus = ""
		m.publishCommands() // MCP prompts are now exposed as slash commands
		m.publishStatus()
		var cmds []tea.Cmd
		m, cmds = m.flushQueued(cmds)
		m.refresh()
		return m, tea.Batch(cmds...)
	}

	// Forward everything else to the input (when idle) and the viewport. Strip
	// any leaked terminal-control fragments first so a mouse-scroll burst that
	// got split across reads can't be typed into the input box.
	if m.state == stateIdle || m.state == stateBusy {
		if forward, keep := sanitizeForInput(msg); keep {
			var cmd tea.Cmd
			before := m.input.Value()
			m.input, cmd = m.input.Update(forward)
			// Once the user edits a popped draft it's a genuine draft again, so
			// Esc should stash (not discard) it.
			if m.input.Value() != before {
				m.poppedQueue = false
			}
			m.syncInputHeight()
			cmds = append(cmds, cmd)
		}
	}
	var vcmd tea.Cmd
	m.viewport, vcmd = m.viewport.Update(msg)
	cmds = append(cmds, vcmd)
	return m, tea.Batch(cmds...)
}

// queuedHints renders a compact preview of queued user messages for display
// above the input, or "" when the queue is empty. While the agent is busy,
// pressing Enter on a non-empty draft enqueues it instead of dropping the
// keystroke; this hint surfaces what's pending so the user can see what will
// be sent next. Esc pops the most recent item back into the input for
// editing or discard. Up to 3 items are shown verbatim (truncated to keep
// the panel small); any extra are summarized as "(+N more)".
func (m Model) queuedHints() string {
	if len(m.queued) == 0 {
		return ""
	}
	const maxShow = 3
	const maxLen = 80
	abbrev := func(s string) string {
		s = strings.ReplaceAll(s, "\n", " ⏎ ")
		// Truncate by runes, not bytes, so a multibyte glyph (the ⏎ above, or
		// any non-ASCII content) is never split into invalid UTF-8.
		if r := []rune(s); len(r) > maxLen {
			s = string(r[:maxLen-1]) + "…"
		}
		return s
	}
	lines := make([]string, 0, len(m.queued)+1)
	lines = append(lines, statusDimStyle.Render(fmt.Sprintf("queued (%d) — esc pops, /queue clear empties:", len(m.queued))))
	shown := len(m.queued)
	if shown > maxShow {
		shown = maxShow
	}
	for i := 0; i < shown; i++ {
		lines = append(lines, statusDimStyle.Render("  ↳ "+abbrev(m.queued[i])))
	}
	if extra := len(m.queued) - shown; extra > 0 {
		lines = append(lines, statusDimStyle.Render(fmt.Sprintf("  … (+%d more)", extra)))
	}
	return strings.Join(lines, "\n")
}

// submit sends the current input to the agent.
func (m Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	// While the background startup sequence runs, hold model turns (but not
	// slash / `#` commands, which don't start a turn) so the first turn runs with
	// the full tool set and registration never races the turn goroutine. Queue
	// it like a busy-time message — no history add here, matching the busy path;
	// startupDoneMsg drains the queue once the checks finish.
	if m.startupActive && !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "#") {
		m.input.Reset()
		m.syncInputHeight()
		m.queued = append(m.queued, text)
		m.trans.add(entry{kind: entryInfo, text: "Starting up… your message will run once the checks finish."})
		m.publishQueue()
		m.refresh()
		return m, nil
	}
	m.input.Reset()
	m.poppedQueue = false
	m.syncInputHeight()
	m.history = appendHistory(m.history, text)
	m.histIdx = len(m.history)
	m.histStash = ""

	// Handle slash commands locally.
	if strings.HasPrefix(text, "/") {
		return m.command(text)
	}
	// A leading "#" is the quick-memory shortcut (like Claude Code): save the
	// note to project memory instead of sending it to the model.
	if strings.HasPrefix(text, "#") {
		m.trans.add(entry{kind: entryInfo, text: m.remember(strings.TrimSpace(text[1:]), "", false)})
		m.refresh()
		return m, nil
	}
	// @-mentions of images/PDFs/Office files become attachments; @-mentions of
	// text/code files are inlined by expandMentions. The transcript still shows
	// the user's original text for readability.
	if atts, notes := attachmentMentions(text, m.cfg.WorkingDir); len(atts) > 0 || len(notes) > 0 {
		m.attachments = append(m.attachments, atts...)
		for _, n := range notes {
			m.trans.add(entry{kind: entryError, text: n})
		}
	}
	// Auto-enter read-only plan mode for a large/multi-step task (when enabled),
	// so the agent drafts a plan for approval before touching files.
	if m.agent.MaybeAutoPlan(text) {
		m.trans.add(entry{kind: entryInfo, text: autoPlanNotice})
		m.publishStatus()
	}
	// Additively capture a standalone directive ("always …", "never …", …) to
	// project memory; the prompt is still sent to the model below.
	if msg, ok := m.agent.AutoCaptureRule(text); ok {
		m.trans.add(entry{kind: entryInfo, text: msg})
	}
	return m.runUserMessage(text, m.expandPrompt(text))
}

// remoteSubmit handles input that arrived from a browser viewer. It mirrors
// submit()'s dispatch (slash command, # memory, or a prompt) with two
// differences: it only acts while idle, and it refuses app-quitting commands
// (a browser must not be able to kill the process). Because slash-command
// output is added to the local transcript, any new info/error lines the command
// produced are mirrored to viewers so the browser sees the result too.
func (m Model) remoteSubmit(text string) (tea.Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if text == "" {
		return m, nil
	}
	// While busy, only a /btw side-question is honored remotely — it runs
	// CONCURRENTLY with the active turn (its handler guards only on
	// compacting/btwBusy, never stateBusy). Every other busy-time input is
	// enqueued by the web mirror (via Hub.QueueControl → remoteQueueMsg), not
	// dispatched here, so ignore it rather than dropping or double-running it.
	if m.state != stateIdle {
		if fields := strings.Fields(text); len(fields) == 0 || fields[0] != "/btw" {
			return m, nil
		}
	}

	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		name := strings.ToLower(fields[0])
		hasArg := len(fields) > 1
		switch name {
		case "/exit", "/quit", "/q":
			m.publishInfo("Remote quit is disabled — exit from the terminal.")
			return m, nil
		case "/models":
			// Web can't drive the terminal's picker menu, so mark this fetch as
			// remote: handleModelsLoaded will mirror a clickable menu and stay idle.
			m.modelsRemote = true
		case "/reasoning", "/think", "/thinking":
			if !hasArg {
				m.publishReasoningMenu()
				return m, nil
			}
		case "/skills":
			if !hasArg {
				m.publishSkillsMenu() // browser can't drive the terminal menu; mirror a clickable one
				return m, nil
			}
		case "/mcps":
			if !hasArg {
				m.publishMcpsMenu()
				return m, nil
			}
		case "/agents":
			if !hasArg {
				m.publishAgentsMenu()
				return m, nil
			}
		case "/workers":
			if !hasArg {
				m.publishWorkersMenu()
				return m, nil
			}
		case "/schedule":
			if !hasArg {
				m.publishSchedulesMenu() // browser can't drive the terminal menu; mirror a clickable one
				return m, nil
			}
		case "/sessions":
			// Switching rebuilds the transcript and mirrors it itself, so handle
			// it directly rather than through the generic command()+diff path.
			if hasArg {
				return m.switchSession(fields[1])
			}
			m.publishSessionsMenu()
			return m, nil
		}
		before := len(m.trans.entries)
		res, cmd := m.command(text)
		nm, ok := res.(Model)
		if !ok {
			return res, cmd
		}
		// Mirror any info/error lines the command appended (commands that clear
		// the transcript, like /clear and /reset, shrink it and already emit a
		// reset to viewers, so guard the slice).
		if len(nm.trans.entries) >= before {
			for _, e := range nm.trans.entries[before:] {
				switch e.kind {
				case entryInfo:
					nm.publishInfo(e.text)
				case entryRaw:
					// Pre-styled blocks (e.g. the /context bar) are mirrored to
					// the web via their own structured event (see
					// addContextStatus), so skip the raw ANSI text here.
				case entryError:
					nm.publishError(e.text)
				}
			}
		}
		return nm, cmd
	}

	if strings.HasPrefix(text, "#") {
		note := m.remember(strings.TrimSpace(text[1:]), "", false)
		m.trans.add(entry{kind: entryInfo, text: note})
		m.publishInfo(note)
		m.refresh()
		return m, nil
	}

	if m.agent.MaybeAutoPlan(text) {
		m.trans.add(entry{kind: entryInfo, text: autoPlanNotice})
		m.publishInfo(autoPlanNotice)
		m.publishStatus()
	}
	if msg, ok := m.agent.AutoCaptureRule(text); ok {
		m.trans.add(entry{kind: entryInfo, text: msg})
		m.publishInfo(msg)
	}
	return m.runUserMessage(text, m.expandPrompt(text))
}

// runUserMessage starts a turn with the given prompt, while the transcript
// shows displayText (so user-typed input shows the original text and custom
// slash commands show what was invoked rather than the expanded template).
func (m Model) runUserMessage(displayText, prompt string) (tea.Model, tea.Cmd) {
	// Fold any staged attachments into the turn: build their content blocks for
	// the model and annotate the displayed text so the transcript shows them.
	var blocks []llm.ContentBlock
	if len(m.attachments) > 0 {
		labels := make([]string, 0, len(m.attachments))
		for _, a := range m.attachments {
			blocks = append(blocks, a.Block())
			labels = append(labels, a.Label())
		}
		note := "[attached: " + strings.Join(labels, ", ") + "]"
		if strings.TrimSpace(displayText) == "" {
			displayText = note
		} else {
			displayText += "\n" + note
		}
		m.attachments = nil // consumed by this turn
	}

	m.trans.add(entry{kind: entryUser, text: displayText})
	m.state = stateBusy
	// The startup panel is a pre-conversation status; once the user actually
	// sends a question, dismiss it for good so it doesn't crowd the transcript.
	m.startupChecks = nil
	m.streamingIdx = -1
	m.thinkingIdx = -1
	m.activeTool = ""
	m.todos = nil // hide the previous turn's task list; repopulates if the model calls todo_write
	// Keep the composer focused while the turn runs so the user can type ahead
	// and Enter to queue a follow-up message (drained when the turn ends). Key
	// forwarding to the input is gated to idle/busy (approval/question states are
	// excluded), so a focused composer never swallows y/n answer keys.
	m.input.Focus()

	// Mirror the user's turn to web viewers (no-op when not mirroring).
	m.publishUser(displayText)
	m.publishState(bridge.StateBusy)
	m.publishStatus()

	ch := make(chan streamMsg, 128)
	m.ch = ch
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	hub := m.hub // captured so the agent goroutine can tee events to viewers

	go func() {
		err := m.agent.RunWith(ctx, prompt, blocks, func(ev agent.Event) {
			ch <- streamMsg{ev: ev}
			if hub != nil {
				hub.PublishEvent(ev)
			}
		})
		ch <- streamMsg{done: true, err: err}
	}()

	m.refresh()
	m.viewport.GotoBottom() // a new turn jumps to the bottom even if scrolled up
	return m, tea.Batch(m.spinner.Tick, waitForStream(ch))
}

// command handles slash commands like /clear, /reset, /help, /model, /exit.
func (m Model) command(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(text)
	cmdName := strings.ToLower(fields[0])

	// User-defined slash commands take precedence over the unknown-command
	// fallback. They expand into a regular user message that goes to the model.
	for _, uc := range m.userCommands {
		if uc.name == cmdName {
			args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			expanded := expandUserCommand(uc, args)
			return m.runUserMessage(text, expanded)
		}
	}

	// MCP-supplied prompts are exposed as "/mcp.<server>.<prompt>". Fetch the
	// rendered template asynchronously, then submit it.
	for i := range m.mcpPrompts {
		if m.mcpPrompts[i].SlashCommand() == cmdName {
			args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			m.trans.add(entry{kind: entryInfo, text: "Fetching MCP prompt " + cmdName + "…"})
			m.refresh()
			return m, m.fetchMCPPromptCmd(i, args)
		}
	}

	switch cmdName {
	case "/exit", "/quit", "/q":
		return m, tea.Quit
	case "/clear":
		m.trans.entries = nil
		m.trans.add(entry{kind: entryBanner, text: welcomeBanner(m.cfg)})
		m.hubReset()
	case "/queue":
		// Manage the message queue. Without args, list pending items; with
		// "clear" empty it. The queue holds messages typed during a busy
		// turn, dispatched FIFO one-per-turn after the running turn ends.
		sub := ""
		if len(fields) >= 2 {
			sub = strings.ToLower(fields[1])
		}
		switch sub {
		case "", "list", "show":
			if len(m.queued) == 0 {
				m.trans.add(entry{kind: entryInfo, text: "Queue is empty. Press Enter while the agent is busy to enqueue a message; Esc pops the most recent."})
			} else {
				var b strings.Builder
				fmt.Fprintf(&b, "Queued messages (%d):", len(m.queued))
				for i, q := range m.queued {
					fmt.Fprintf(&b, "\n  %d. %s", i+1, strings.ReplaceAll(q, "\n", " ⏎ "))
				}
				m.trans.add(entry{kind: entryInfo, text: b.String()})
			}
		case "clear":
			n := len(m.queued)
			m.queued = nil
			m.publishQueue()
			m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Cleared %d queued message(s).", n)})
		default:
			m.trans.add(entry{kind: entryInfo, text: "Usage: /queue [list|clear]"})
		}
	case "/reset":
		m.agent.Reset()
		m.trans.entries = nil
		m.todos = nil          // fresh session: clear the task-list panel
		m.plan = nil           // and the plan artifact
		m.lastTurnInTokens = 0 // history is gone, so context is empty
		m.trans.add(entry{kind: entryInfo, text: "Conversation reset — started a new session."})
		if m.hub != nil {
			m.hub.SetActiveSession(m.agent.SessionID()) // reset mints a new session id
		}
		m.hubReset()
		m.publishSessions() // active session changed
	case "/model":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Current model: %s\nUse /models to pick from a list, or /model <id> to set one.", m.cfg.Model)})
		} else {
			// SetModel derives the endpoint from the id and resolves the
			// reasoning level.
			m.cfg.SetModel(fields[1])
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig(fmt.Sprintf("Model set to %s (%s)", shortModel(fields[1]), provider.Label(m.cfg.LLMProvider)))})
		}
	case "/models":
		m.trans.add(entry{kind: entryInfo, text: "Fetching models…"})
		m.refresh()
		return m, m.fetchModelsCmd()
	case "/provider", "/providers":
		if len(fields) < 2 {
			m.openProviderMenu()
		} else {
			m.trans.add(entry{kind: entryInfo, text: m.providerCmd(fields)})
		}
	case "/reasoning", "/think", "/thinking":
		if len(fields) < 2 {
			m.openReasoningMenu()
		} else {
			lvl := config.NormalizeReasoningFor(m.cfg.Model, strings.ToLower(fields[1]))
			m.cfg.SetReasoningFor(m.cfg.Model, lvl)
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig(m.reasoningStatus(lvl))})
		}
	case "/tokens":
		msg := fmt.Sprintf("Tokens this session — in: %d, out: %d, total: %d", m.totalIn, m.totalOut, m.totalIn+m.totalOut)
		// Prompt-cache split: how much of the input was served from cache (billed
		// cheap) vs wrote new cache entries. Zero cached reads means caching isn't
		// engaging on this route — the key signal the user is verifying.
		if m.totalIn > 0 {
			if m.totalCacheRead > 0 || m.totalCacheWrite > 0 {
				msg += fmt.Sprintf("\nPrompt cache — %d read from cache (%d%% of input), %d written",
					m.totalCacheRead, 100*m.totalCacheRead/m.totalIn, m.totalCacheWrite)
			} else {
				msg += "\nPrompt cache — 0 cached reads (caching not engaging on this route)"
			}
		}
		if m.lastTurnInTokens > 0 {
			cw := m.cfg.ContextWindowFor(m.cfg.Model)
			pct := contextPercent(m.lastTurnInTokens, cw)
			msg += fmt.Sprintf("\nContext: %d / %d tokens (%d%% of %s's window)", m.lastTurnInTokens, cw, pct, shortModel(m.cfg.Model))
		}
		if m.totalCostUSD > 0 {
			msg += fmt.Sprintf("\nEstimated cost (this session, cache-adjusted): $%s", formatUSD(m.totalCostUSD))
		}
		if m.quotaFetched {
			label := "Floodgate quota (24h)"
			if m.quotaProject != "" {
				label = "Floodgate quota (24h, project " + m.quotaProject + ")"
			}
			if m.quotaBudget > 0 {
				msg += fmt.Sprintf("\n%s: $%s / $%s", label, formatUSD(m.quotaSpend), formatUSD(m.quotaBudget))
			} else {
				msg += fmt.Sprintf("\n%s: $%s spent (no budget cap)", label, formatUSD(m.quotaSpend))
			}
		} else if m.quotaErr != "" {
			msg += "\nFloodgate quota: unavailable (" + m.quotaErr + ")"
		}
		m.trans.add(entry{kind: entryInfo, text: msg})
	case "/usage":
		m.trans.add(entry{kind: entryInfo, text: "Fetching Floodgate usage…"})
		m.refresh()
		return m, m.usageCmd()
	case "/doctor":
		m.trans.add(entry{kind: entryInfo, text: "Running environment checks…"})
		m.refresh()
		return m, m.doctorCmd()
	case "/update":
		m.trans.add(entry{kind: entryInfo, text: "Checking for the latest moony…"})
		m.refresh()
		return m, m.updateCmd()
	case "/skills":
		if len(fields) >= 2 {
			m.trans.add(entry{kind: entryInfo, text: m.skillsCmd(fields)})
		} else {
			return m.openSkillsMenu()
		}
	case "/mcps":
		switch {
		case len(fields) >= 2 && fields[1] == "reload":
			m.trans.add(entry{kind: entryInfo, text: m.reloadMCP()})
		case len(fields) >= 2:
			m.trans.add(entry{kind: entryInfo, text: m.mcpsCmd(fields)})
		default:
			return m.openMcpsMenu()
		}
	case "/lsp":
		if len(fields) >= 2 && fields[1] == "reload" {
			m.trans.add(entry{kind: entryInfo, text: m.reloadLSP()})
		} else {
			m.trans.add(entry{kind: entryInfo, text: m.lspCmd(fields)})
		}
	case "/agents":
		if len(fields) >= 2 {
			m.trans.add(entry{kind: entryInfo, text: m.agentsCmd(fields)})
		} else {
			return m.openAgentsMenu()
		}
	case "/compact":
		m.trans.add(entry{kind: entryInfo, text: "Compacting older history…"})
		m.compacting = true
		m.refresh()
		return m, tea.Batch(m.spinner.Tick, m.compactCmd())
	case "/btw":
		// /btw <question> — run a side-question that doesn't enter the main
		// conversation history. Shows the user's question as a side entry,
		// kicks off Agent.AskSide, and on completion appends a dimmed
		// assistant side entry.
		q := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "/btw"))
		if q == "" {
			m.trans.add(entry{kind: entryInfo, text: "/btw <question> — ask a quick side question without polluting the main thread"})
			m.refresh()
			return m, nil
		}
		if m.compacting || m.btwBusy {
			m.trans.add(entry{kind: entryInfo, text: "Another background task is already running — try again in a moment."})
			m.refresh()
			return m, nil
		}
		m.trans.add(entry{kind: entryUser, text: q, side: true})
		m.publishInfo("↳ /btw " + q) // mirror the side question to web viewers
		m.btwBusy = true
		m.refresh()
		return m, tea.Batch(m.spinner.Tick, m.btwCmd(q))
	case "/goal":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: m.agent.GoalStatus()})
			break
		}
		sub := strings.ToLower(fields[1])
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, fields[0])), fields[1]))
		switch sub {
		case "clear", "off", "none", "remove":
			m.agent.SetGoal("")
			m.trans.add(entry{kind: entryInfo, text: "Goal cleared — the agent stops normally."})
		case "budget":
			switch {
			case rest == "":
				m.trans.add(entry{kind: entryInfo, text: m.agent.GoalStatus()})
			case rest == "off" || rest == "none" || rest == "0":
				m.agent.SetGoalBudget(0)
				m.trans.add(entry{kind: entryInfo, text: "Goal token budget removed."})
			default:
				if n, err := config.ParseTokenCount(rest); err == nil && n > 0 {
					m.agent.SetGoalBudget(n)
					m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Goal token budget set to %d — the agent stops once that's spent pursuing the goal.", n)})
				} else {
					m.trans.add(entry{kind: entryInfo, text: "Usage: /goal budget <tokens|200k|off>"})
				}
			}
		case "criteria", "criterion":
			if rest == "" {
				m.trans.add(entry{kind: entryInfo, text: m.agent.GoalStatus()})
			} else {
				m.agent.SetGoalCriteria(agent.SplitCriteria(rest))
				m.trans.add(entry{kind: entryInfo, text: m.agent.GoalStatus()})
			}
		default:
			arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			m.agent.SetGoal(arg)
			m.trans.add(entry{kind: entryInfo, text: "Goal set. The agent won't stop until a judge confirms it's met; criteria are auto-proposed on the first check (override with /goal criteria, cap with /goal budget):\n  " + arg})
		}
	case "/checkpoint":
		arg := ""
		if len(fields) >= 2 {
			arg = strings.ToLower(strings.TrimSpace(fields[1]))
		}
		switch arg {
		case "":
			m.trans.add(entry{kind: entryInfo, text: m.agent.CheckpointStatus()})
		case "clear", "off", "none", "remove":
			m.agent.ClearCheckpoint()
			m.trans.add(entry{kind: entryInfo, text: "Session checkpoint cleared."})
		case "now", "refresh", "update":
			m.trans.add(entry{kind: entryInfo, text: "Refreshing the session checkpoint…"})
			m.refresh()
			return m, m.checkpointCmd()
		default:
			m.trans.add(entry{kind: entryInfo, text: "Usage: /checkpoint [now|clear] — show, refresh, or clear the distilled session state."})
		}
	case "/copy":
		m.trans.add(entry{kind: entryInfo, text: m.copyLast()})
	case "/sessions":
		if len(fields) >= 2 {
			return m.switchSession(fields[1])
		}
		return m.openSessionsMenu()
	case "/schedule":
		if len(fields) >= 2 {
			m.trans.add(entry{kind: entryInfo, text: m.scheduleCmd(fields)})
		} else {
			return m.openSchedulesMenu()
		}
	case "/help":
		m.trans.add(entry{kind: entryInfo, text: helpText()})
	case "/cd":
		arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		if arg == "" {
			m.trans.add(entry{kind: entryInfo, text: "Usage: /cd <dir> — re-root this session at another folder so tools, skills, and agents resolve there."})
			break
		}
		abs, err := resolveExistingDir(arg, m.cfg.WorkingDir)
		if err != nil {
			m.trans.add(entry{kind: entryInfo, text: "cd: " + err.Error()})
			break
		}
		m.agent.SetWorkingDir(abs)
		m.trans.add(entry{kind: entryInfo, text: "Working dir → " + abs + "\nTools, skills, agents, and permissions now resolve here. (MCP servers and LSP keep their original root until you restart.)"})
		m.publishStatus() // reflect the new dir in the status bar / header
	case "/plan":
		return m.planCmd(text, fields)
	case "/build":
		m.agent.SetPlanMode(false)
		m.trans.add(entry{kind: entryInfo, text: "Plan mode OFF — mutating tools are available again."})
	case "/agent":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: "Active agent: " + m.agent.ActiveAgent().Name + "\nAvailable: " + strings.Join(m.agent.AgentNames(), ", ") + "\nSwitch with /agent <name> (or Tab to toggle build/plan), or /agent init to scaffold editable build/plan files."})
			break
		}
		if strings.EqualFold(fields[1], "init") {
			m.trans.add(entry{kind: entryInfo, text: m.agentInitCmd()})
			break
		}
		name := fields[1]
		if !m.agent.SetAgent(name) {
			m.trans.add(entry{kind: entryInfo, text: "Unknown or non-primary agent " + name + ". Available: " + strings.Join(m.agent.AgentNames(), ", ")})
			break
		}
		m.trans.add(entry{kind: entryInfo, text: "Switched to agent: " + m.agent.ActiveAgent().Name})
	case "/verify":
		arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		switch {
		case arg == "":
			if strings.TrimSpace(m.cfg.VerifyCommand) == "" {
				m.trans.add(entry{kind: entryInfo, text: "Auto-verify is off. Set it with /verify <command> (e.g. /verify go build ./...) to run after edits and self-correct on failure."})
			} else {
				m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Auto-verify: %s (up to %d fix attempt(s) per turn). Disable with /verify off.", m.cfg.VerifyCommand, m.cfg.VerifyMaxRetries)})
			}
		case strings.EqualFold(arg, "off"):
			m.cfg.VerifyCommand = ""
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig("Auto-verify disabled.")})
		default:
			m.cfg.VerifyCommand = arg
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig("Auto-verify command set: " + arg)})
		}
	case "/selfreview":
		arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		switch {
		case arg == "":
			if m.cfg.SelfReview {
				m.trans.add(entry{kind: entryInfo, text: "Self-review is ON: a fast-model reviewer checks each edit turn's diff for introduced bugs and feeds findings back once before finishing. Disable with /selfreview off."})
			} else {
				m.trans.add(entry{kind: entryInfo, text: "Self-review is off. Turn it on with /selfreview on to have a fast-model reviewer check each edit turn's diff for bugs before the turn finishes."})
			}
		case strings.EqualFold(arg, "on"), strings.EqualFold(arg, "true"), arg == "1", strings.EqualFold(arg, "yes"):
			m.cfg.SelfReview = true
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig("Self-review enabled.")})
		case strings.EqualFold(arg, "off"), strings.EqualFold(arg, "false"), arg == "0", strings.EqualFold(arg, "no"):
			m.cfg.SelfReview = false
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig("Self-review disabled.")})
		default:
			m.trans.add(entry{kind: entryInfo, text: "Usage: /selfreview on|off"})
		}
	case "/approval":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: m.approvalStatus()})
		} else {
			m.cfg.ApprovalPolicy = config.NormalizeApprovalPolicy(strings.ToLower(fields[1]))
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig("Approval policy set to " + m.cfg.ApprovalPolicy)})
		}
	case "/permissions":
		m.trans.add(entry{kind: entryInfo, text: m.permissionsCmd(fields)})
	case "/domains":
		m.trans.add(entry{kind: entryInfo, text: m.domainsCmd(fields)})
	case "/workers":
		if len(fields) >= 2 {
			m.trans.add(entry{kind: entryInfo, text: m.workersCmd(fields)})
		} else {
			return m.openWorkersMenu()
		}
	case "/plugins":
		if len(fields) >= 2 {
			m.trans.add(entry{kind: entryInfo, text: m.pluginsCmd(fields)})
			m.userCommands = loadUserCommands(m.cfg.WorkingDir) // pick up any bundled /commands
			m.publishCommands()
		} else {
			return m.openPluginsMenu()
		}
	case "/sandbox":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: m.sandboxStatus()})
		} else {
			m.cfg.SandboxMode = config.NormalizeSandboxMode(fields[1])
			m.agent.SetSandbox(m.cfg.SandboxMode != config.SandboxOff, m.cfg.SandboxNetwork)
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig(m.sandboxStatus())})
		}
	case "/context":
		if len(fields) < 2 {
			m.addContextStatus()
		} else {
			switch strings.ToLower(strings.TrimSpace(fields[1])) {
			case "auto", "default", "off", "0":
				m.cfg.ContextWindow = 0
				m.trans.add(entry{kind: entryInfo, text: m.saveConfig(m.contextStatus())})
			default:
				if n, err := config.ParseTokenCount(fields[1]); err == nil && n > 0 {
					m.cfg.ContextWindow = n
					m.trans.add(entry{kind: entryInfo, text: m.saveConfig(m.contextStatus())})
				} else {
					m.trans.add(entry{kind: entryInfo, text: "usage: /context <tokens|1m|200k|auto>"})
				}
			}
		}
	case "/relay":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: m.relayStatus()})
		} else {
			switch strings.ToLower(strings.TrimSpace(fields[1])) {
			case "off", "none", "disable":
				m.cfg.RelayAddr = ""
			default:
				m.cfg.RelayAddr = strings.TrimSpace(fields[1])
			}
			note := "Relay disabled."
			if m.cfg.RelayAddr != "" {
				note = "Relay set to " + m.cfg.RelayAddr + "."
			}
			note += " Applies on next start — restart moony to (re)connect."
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig(note)})
		}
	case "/memory":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: m.agent.MemoryView()})
		} else {
			arg := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			note, category, global := agent.ParseMemoryFlags(arg)
			m.trans.add(entry{kind: entryInfo, text: m.remember(note, category, global)})
		}
	case "/undo":
		out, err := m.agent.Undo(context.Background())
		if err != nil {
			m.trans.add(entry{kind: entryError, text: "undo failed: " + err.Error()})
		} else {
			m.trans.add(entry{kind: entryInfo, text: out})
		}
	case "/rewind":
		n := 1
		if len(fields) > 1 {
			if v, err := strconv.Atoi(strings.TrimSpace(fields[1])); err == nil && v > 0 {
				n = v
			} else {
				m.trans.add(entry{kind: entryInfo, text: "usage: /rewind [n]  (roll back the last n turns; files + conversation)"})
				break
			}
		}
		out, err := m.agent.Rewind(context.Background(), n)
		if err != nil {
			m.trans.add(entry{kind: entryError, text: "rewind failed: " + err.Error()})
		} else {
			m.trans.add(entry{kind: entryInfo, text: out})
		}
	case "/attach":
		if len(fields) < 2 {
			m.trans.add(entry{kind: entryInfo, text: "Usage: /attach <path> — stage an image, PDF, Office, or iWork file for your next message."})
		} else {
			path := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			// iWork files convert via a slow network round-trip; resolve them off
			// the event loop so the terminal stays responsive.
			if full := m.attachFullPath(path); full != "" && attach.IsIWork(full) {
				m.trans.add(entry{kind: entryInfo, text: "Converting " + path + " to Office via the converter service…"})
				m.refresh()
				return m, m.stageAttachmentCmd(full, path)
			}
			m.trans.add(entry{kind: entryInfo, text: m.stageAttachment(path)})
		}
	case "/paste":
		m.trans.add(entry{kind: entryInfo, text: m.pasteClipboardImage()})
	case "/init":
		const initPrompt = "Please bootstrap an AGENTS.md for this repository.\n\n" +
			"1. Use list_dir, glob, and grep (and read_file when needed) to survey the codebase.\n" +
			"2. Identify: language(s), build/test commands, layout, conventions, anything unusual.\n" +
			"3. Write a concise AGENTS.md (200–500 words) with these sections:\n" +
			"   - **Project**: one-paragraph description.\n" +
			"   - **Build & test**: exact commands (e.g. `make build`, `make test`, `go vet ./...`).\n" +
			"   - **Layout**: top-level directories and what each holds.\n" +
			"   - **Conventions**: style/naming rules visible in the code.\n" +
			"   - **Caveats**: anything an agent should avoid touching or doing here.\n" +
			"4. Use write_file to save it as `AGENTS.md` at the repo root, then summarize what you wrote."
		return m.runUserMessage("/init", initPrompt)
	default:
		m.trans.add(entry{kind: entryInfo, text: "Unknown command. Try /help."})
	}
	// Many commands change model/route/reasoning or reset usage; mirror the
	// refreshed status bar to web viewers.
	m.publishStatus()
	m.refresh()
	return m, nil
}

// resolveExistingDir resolves a /cd target against the current working dir: it
// expands a leading ~, makes a relative path absolute against base, and requires
// the result to be an existing directory. Returns the absolute path or an error.
func resolveExistingDir(dir, base string) (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if dir == "~" {
			dir = home
		} else if strings.HasPrefix(dir, "~/") {
			dir = filepath.Join(home, dir[2:])
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("bad path: %s", dir)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("no such folder: %s", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	return abs, nil
}

// subAgentStepLine renders a delegated sub-agent's tool call as an indented,
// attributed progress line for the TUI transcript, e.g.
// "  ↳ explore → read_file internal/x.go". It prefers the agent-supplied summary
// and falls back to a one-line, truncated slice of the raw input.
func subAgentStepLine(agent, tool, summary, input string) string {
	detail := strings.TrimSpace(summary)
	if detail == "" {
		detail = strings.TrimSpace(input)
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if r := []rune(detail); len(r) > 80 {
		detail = string(r[:80]) + "…"
	}
	line := "  ↳ " + agent + " → " + tool
	if detail != "" {
		line += " " + detail
	}
	return line
}

// taskMetaLine renders a delegated sub-agent's completion footer as an indented
// line: "  ↳ explore done · 3.2s · $0.0120 · 5 tool calls · 1.2k↑ 800↓". Cost and
// tokens are shown only when present.
func taskMetaLine(e agent.TaskMeta) string {
	parts := []string{shortDur(e.DurationMS)}
	if e.CostUSD > 0 {
		parts = append(parts, "$"+formatUSD(e.CostUSD))
	}
	if e.Calls > 0 {
		unit := "tool calls"
		if e.Calls == 1 {
			unit = "tool call"
		}
		parts = append(parts, fmt.Sprintf("%d %s", e.Calls, unit))
	}
	if e.InTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s↑ %s↓", formatTokens(e.InTokens), formatTokens(e.OutTokens)))
	}
	return "  ↳ " + e.Agent + " done · " + strings.Join(parts, " · ")
}

// handleStream processes an agent event delivered over the channel.
func (m Model) handleStream(msg streamMsg) (tea.Model, tea.Cmd) {
	if msg.done {
		m.state = stateIdle
		m.cancel = nil
		m.streamingIdx = -1
		m.thinkingIdx = -1
		m.activeTool = ""
		m.toolsInFlight = 0
		m.curStep, m.maxStep = 0, 0
		if msg.err != nil && !isCanceled(msg.err) {
			note := config.TurnErrorNote(msg.err)
			m.trans.add(entry{kind: entryError, text: note})
			m.publishError(note)
		}
		m.publishState(bridge.StateIdle)
		m.publishStatus()
		m.publishSessions() // a new session is persisted/titled after its first turn
		m.input.Focus()
		m.refresh()
		// Chime when the agent finishes a turn cleanly (not on cancel/error).
		cmds := []tea.Cmd{textarea.Blink, m.refreshGitCmd()}
		if msg.err == nil {
			if c := playDoneSoundCmd(); c != nil {
				cmds = append(cmds, c)
			}
		}
		// Now idle again: drain the queued messages (FIFO) now that the turn
		// finished cleanly. Non-turn items (slash commands, `#` memory notes,
		// attachment-only messages) return to idle without starting a turn, so
		// flushQueued keeps dispatching until either the queue empties or one item
		// actually starts a new turn. Skipped on cancel/error so the user can
		// decide what to do with what's left.
		if msg.err == nil {
			m, cmds = m.flushQueued(cmds)
		}
		return m, tea.Batch(cmds...)
	}

	switch ev := msg.ev.(type) {
	case agent.TextDelta:
		m.thinkingIdx = -1 // close any open thinking block
		if m.streamingIdx < 0 {
			m.streamingIdx = m.trans.add(entry{kind: entryAssistant, text: ""})
		}
		m.trans.entries[m.streamingIdx].text += ev.Text

	case agent.ThinkingDelta:
		if m.thinkingIdx < 0 {
			m.thinkingIdx = m.trans.add(entry{kind: entryThinking, text: ""})
		}
		m.trans.entries[m.thinkingIdx].text += ev.Text

	case agent.StreamReset:
		// A transient stream error is being retried; discard the partial
		// assistant/thinking text so the re-streamed response replaces it cleanly
		// (the indices are kept so the next deltas refill the same blocks).
		if m.streamingIdx >= 0 {
			m.trans.entries[m.streamingIdx].text = ""
		}
		if m.thinkingIdx >= 0 {
			m.trans.entries[m.thinkingIdx].text = ""
		}

	case agent.ToolStart:
		// Delegated sub-agent tool call: the web nests these inside the parent
		// task card; the TUI lists them as indented progress lines (like worker
		// updates), without creating a tool entry or counting them in flight.
		if ev.Agent != "" {
			m.trans.add(entry{kind: entryInfo, text: subAgentStepLine(ev.Agent, ev.Name, ev.Summary, ev.Input)})
			break
		}
		m.streamingIdx = -1 // close any open assistant block
		m.thinkingIdx = -1
		m.activeTool = ev.Name
		m.toolsInFlight++
		m.trans.add(entry{
			kind:      entryTool,
			toolName:  ev.Name,
			toolInput: ev.Input,
			toolDiff:  ev.Diff,
			toolID:    ev.ID,
			toolModel: ev.Model,
		})
		m.publishStatus()

	case agent.ToolEnd:
		// Sub-agent tool result: surface only failures as an indented line; the
		// success/output is reflected in the sub-agent's final answer.
		if ev.Agent != "" {
			if ev.IsError {
				m.trans.add(entry{kind: entryInfo, text: "  ↳ " + ev.Agent + " ✗ " + ev.Name})
			}
			break
		}
		if m.toolsInFlight > 0 {
			m.toolsInFlight--
		}
		if m.toolsInFlight == 0 {
			m.activeTool = ""
		}
		// Match the result back to its ToolStart entry by id (calls can finish
		// out of order when running concurrently). Fall back to the most recent
		// unfinished tool entry when no id is present.
		if !m.trans.completeTool(ev.ID, ev.Output, ev.IsError) {
			for i := len(m.trans.entries) - 1; i >= 0; i-- {
				if m.trans.entries[i].kind == entryTool && !m.trans.entries[i].toolDone {
					m.trans.entries[i].toolOutput = ev.Output
					m.trans.entries[i].toolErr = ev.IsError
					m.trans.entries[i].toolDone = true
					break
				}
			}
		}
		m.publishStatus()

	case agent.Info:
		m.trans.add(entry{kind: entryInfo, text: ev.Text})

	case agent.TaskMeta:
		// A delegated sub-agent finished: add an indented footer summarizing the
		// delegation (duration · cost · tool calls · tokens), matching the
		// sub-agent step lines. Live-only, like the web task-meta footer.
		m.trans.add(entry{kind: entryInfo, text: taskMetaLine(ev)})

	case agent.WorkerActivity:
		// Live progress from a delegated worker / workflow node. Show discrete
		// tool/status lines (indented, dim); a workflow node's "output" kind carries
		// the block's model response, shown as its own indented line so you can see
		// what each node produced. Streamed text chunks ("text") aren't echoed — the
		// worker's full answer also arrives as the tool result.
		if ev.Kind == "tool" || ev.Kind == "info" {
			if text := strings.TrimSpace(ev.Text); text != "" {
				m.trans.add(entry{kind: entryInfo, text: "  ↳ " + ev.Worker + ": " + text})
			}
		} else if ev.Kind == "output" {
			if text := strings.TrimSpace(ev.Text); text != "" {
				m.trans.add(entry{kind: entryInfo, text: "  ⟹ " + text})
			}
		}

	case agent.ApprovalRequest:
		// Pause for the user's decision; do NOT re-issue waitForStream here.
		// The agent goroutine is blocked until decide() replies.
		req := ev
		m.pending = &req
		m.state = stateAwaitingApproval
		m.publishState(bridge.StateApproval)
		m.publishStatus()
		m.refresh()
		return m, nil

	case agent.QuestionRequest:
		// Pause for the user's choice (the ask_user tool). Like an approval, the
		// agent goroutine is blocked until answerQuestion() replies, so don't
		// re-issue waitForStream here.
		req := ev
		m.pendingQ = &req
		m.qCursor = 0
		m.qChosen = map[int]bool{}
		// Free-text questions use the composer input as the answer box, so clear it
		// (parking any in-progress queued draft would collide with the answer) —
		// this makes the box question-scoped so a typed note is never mixed with a
		// stale draft. Plain multiple-choice questions leave the composer untouched.
		if req.AllowText {
			m.input.Reset()
		}
		// A pure free-text question (no options) starts directly in text mode.
		m.qTextMode = req.AllowText && len(req.Options) == 0
		if m.qTextMode {
			m.input.Focus()
		}
		m.state = stateAwaitingQuestion
		m.publishState(bridge.StateQuestion)
		m.publishStatus()
		m.refresh()
		return m, nil

	case agent.TurnDone:
		m.streamingIdx = -1
		m.thinkingIdx = -1
		m.activeTool = ""
		m.toolsInFlight = 0
		// Accumulate the whole turn's usage (every model call), not just the
		// final call, so session totals + cost + the prompt-cache split reflect
		// reality. Some TurnDone paths (e.g. goal-budget stop) don't set
		// TurnUsage — fall back to the final call's usage there.
		u := ev.TurnUsage
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			u = ev.Usage
		}
		m.totalIn += u.InputTokens
		m.totalOut += u.OutputTokens
		m.totalCacheRead += u.CacheReadInputTokens
		m.totalCacheWrite += u.CacheCreationInputTokens
		// Cost: use the accurate per-turn total the agent already computed
		// (ev.CostUSD) — priced per call at whichever model actually served it
		// (session model, or the cheaper FastModel when AdaptiveModel/CheapReads
		// downshifted a round), plus aux + sub-agent spend. Recomputing here via
		// u.CostUSD(m.cfg.Model) would misprice every token at the current
		// session model and miss aux/sub-agent calls. Fall back to that only for
		// legacy TurnDone paths that don't set CostUSD.
		if ev.CostUSD > 0 {
			m.totalCostUSD += ev.CostUSD
		} else {
			m.totalCostUSD += u.CostUSD(m.cfg.Model)
		}
		// Snapshot the current context fill from the final call's prompt size.
		m.lastTurnInTokens = ev.Usage.InputTokens
		m.publishStatus()
		// Refresh the persistent quota display now that this turn has billed.
		m.refresh()
		return m, tea.Batch(waitForStream(m.ch), m.refreshQuotaCmd())

	case agent.TodosUpdated:
		m.todos = ev.Items

	case agent.PlanUpdated:
		m.plan = ev.Plan

	case agent.StepUpdate:
		// Live progress through the turn's tool loop. The event is also teed to
		// web viewers (toWire -> "step"); here we just update the status bar.
		m.curStep, m.maxStep = ev.Step, ev.Max
	}

	m.refresh()
	return m, waitForStream(m.ch)
}

// decide answers the pending approval request and resumes the agent.
func (m Model) decide(d agent.Decision) (tea.Model, tea.Cmd) {
	if m.pending == nil {
		return m, nil
	}
	m.pending.Reply <- d
	m.pending = nil
	m.state = stateBusy
	m.publishState(bridge.StateBusy)
	m.publishStatus()
	m.refresh()
	return m, tea.Batch(m.spinner.Tick, waitForStream(m.ch))
}

// answerQuestion replies to the pending ask_user question with the chosen
// option labels (empty = dismissed) and resumes the agent.
func (m Model) answerQuestion(labels []string) (tea.Model, tea.Cmd) {
	if m.pendingQ == nil {
		return m, nil
	}
	// For a free-text question the composer input WAS the answer box (reset on
	// arrival), so clear it now that we're done — otherwise the typed note would
	// linger as a composer draft. Plain multiple-choice never touched the input,
	// so leave any queued draft intact.
	if m.pendingQ.AllowText {
		m.input.Reset()
	}
	m.qTextMode = false
	m.pendingQ.Reply <- labels
	m.pendingQ = nil
	m.qChosen = nil
	m.qCursor = 0
	m.state = stateBusy
	m.publishState(bridge.StateBusy)
	m.publishStatus()
	m.refresh()
	return m, tea.Batch(m.spinner.Tick, waitForStream(m.ch))
}

// selectedQLabels returns the currently-selected option labels for the pending
// question: the toggled set in multiple-choice mode, else the label under the
// cursor. It excludes any free-text answer (added separately). Empty when there
// are no options (a pure free-text prompt) or nothing is selected.
func (m Model) selectedQLabels() []string {
	if m.pendingQ == nil {
		return nil
	}
	opts := m.pendingQ.Options
	var labels []string
	if m.pendingQ.Multiple && len(m.qChosen) > 0 {
		for i := range opts {
			if m.qChosen[i] {
				labels = append(labels, opts[i].Label)
			}
		}
	} else if m.qCursor >= 0 && m.qCursor < len(opts) {
		labels = []string{opts[m.qCursor].Label}
	}
	return labels
}

func (m Model) submitQuestion() (tea.Model, tea.Cmd) {
	if m.pendingQ == nil {
		return m, nil
	}
	ans := m.selectedQLabels()
	// Fold in a free-text note the user typed (then esc'd back to toggle options),
	// so submitting from option nav still carries it. Gated on AllowText — where
	// the input is question-scoped (reset on arrival) — so a plain multiple-choice
	// question never picks up a stray composer/queue draft.
	if m.pendingQ.AllowText {
		if v := strings.TrimSpace(m.input.Value()); v != "" {
			ans = append(ans, v)
		}
	}
	return m.answerQuestion(ans)
}

// View satisfies tea.Model.
func (m Model) View() string {
	if !m.ready {
		return "starting moony…"
	}

	header := m.headerView()
	// Prefix the status row with a blank line for breathing room between the
	// transcript (the answer) and the "enter send / commands" row. The newline
	// is folded into the status block — a standalone empty block gets collapsed
	// by JoinVertical, so it wouldn't reserve a line.
	status := "\n" + m.statusView()
	bottom := m.bottomPanel()
	todos := m.todosView()

	// When the sidebar is open it hosts the Todos section, so the horizontal
	// todos strip below the transcript is suppressed to avoid duplication.
	sidebar := m.sidebarView()
	if sidebar != "" {
		todos = ""
	}

	// Size the viewport around the (variable-height) panels so the header and
	// status bar always stay on screen. viewportHeight() repeats this math so
	// refresh()'s scroll position is computed against the identical height — if
	// the two disagree, GotoBottom() clips the last lines of the transcript.
	vpH := m.viewportHeight()
	m.viewport.Height = vpH

	// The transcript row is either the viewport alone, or the sidebar joined to
	// its left at the same height.
	main := m.viewport.View()
	if sidebar != "" {
		sidebar = lipgloss.NewStyle().Height(vpH).Render(sidebar)
		main = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
	}

	parts := []string{header, main}
	if todos != "" {
		parts = append(parts, todos)
	}
	parts = append(parts, status, bottom)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// bottomPanel renders the block drawn below the transcript and status row: the
// approval prompt, the menu picker, or the input box (with any staged-attachment
// and command-hint lines stacked above it). The leading blank line is folded in
// so JoinVertical preserves the breathing room above it (a standalone empty
// block would be collapsed). View() draws this and viewportHeight() measures it,
// so the height reserved always matches what's actually shown.
func (m Model) bottomPanel() string {
	var bottom string
	if m.state == stateAwaitingApproval && m.pending != nil {
		bottom = m.approvalView()
	} else if m.state == stateAwaitingQuestion && m.pendingQ != nil {
		bottom = m.questionView()
	} else if m.state == stateMenu && m.menu != nil {
		bottom = m.menuView()
	} else {
		boxStyle := inputBoxStyle
		if m.state == stateIdle {
			boxStyle = inputBoxFocusStyle
		}
		inputBox := boxStyle.Width(m.width - 2).Render(m.input.View())
		var stacked []string
		if sv := m.startupView(); sv != "" {
			stacked = append(stacked, sv)
		}
		if qh := m.queuedHints(); qh != "" {
			stacked = append(stacked, qh)
		}
		if att := m.stagedAttachmentsLine(); att != "" {
			stacked = append(stacked, statusDimStyle.Render(att))
		}
		if hints := m.commandHints(); hints != "" {
			stacked = append(stacked, hints)
		}
		if mh := m.mentionHints(); mh != "" {
			stacked = append(stacked, mh)
		}
		stacked = append(stacked, inputBox)
		bottom = lipgloss.JoinVertical(lipgloss.Left, stacked...)
	}
	return "\n" + bottom
}

// handleModelsLoaded opens the model picker with the live (merged) list, or
// falls back to the merged curated list on error.
func (m Model) handleModelsLoaded(msg modelsLoadedMsg) (tea.Model, tea.Cmd) {
	// Ignore stale results (e.g. a turn started meanwhile).
	if m.state != stateIdle {
		return m, nil
	}
	remote := m.modelsRemote
	m.modelsRemote = false

	models := msg.models
	if msg.err != nil {
		note := config.ModelFetchNote(msg.err)
		m.trans.add(entry{kind: entryInfo, text: note})
		if remote {
			m.publishInfo(note)
		}
		models = config.AllKnownModels()
	}
	if remote {
		// Web-driven: mirror a clickable picker and stay idle (a terminal menu
		// would block remote input). Selecting an item sends `/model <id>`.
		m.publishModelsMenu(models)
	} else {
		m.openModelsMenuFrom(models)
	}
	m.refresh()
	return m, nil
}

// openModelsMenu opens the model picker seeded with the merged curated list.
func (m *Model) openModelsMenu() {
	m.openModelsMenuFrom(config.AllKnownModels())
}

// openModelsMenuFrom builds the model picker from a model list, ensuring the
// current model is present and selected.
func (m *Model) openModelsMenuFrom(models []config.ModelInfo) {
	var items []menuItem
	seen := map[string]bool{}
	for _, mi := range models {
		label, desc := mi.Name, shortModel(mi.ID)
		if label == "" {
			label, desc = mi.ID, ""
		}
		items = append(items, menuItem{
			label:   label,
			desc:    desc,
			value:   mi.ID,
			current: mi.ID == m.cfg.Model,
		})
		seen[mi.ID] = true
	}
	if !seen[m.cfg.Model] {
		items = append([]menuItem{{
			label:   "Current model",
			desc:    shortModel(m.cfg.Model),
			value:   m.cfg.Model,
			current: true,
		}}, items...)
	}
	m.menu = &menu{title: fmt.Sprintf("Select a model (%d)", len(items)), kind: menuModels, items: items, cursor: currentItem(items)}
	m.state = stateMenu
}

// openReasoningMenu opens the extended-thinking level picker for the levels the
// current model supports.
func (m *Model) openReasoningMenu() {
	var items []menuItem
	for _, lvl := range config.ReasoningLevelsFor(m.cfg.Model) {
		items = append(items, menuItem{
			label:   lvl,
			desc:    config.ReasoningDescription(lvl),
			value:   lvl,
			current: lvl == m.cfg.ReasoningLevel,
		})
	}
	m.menu = &menu{title: "Select reasoning level", kind: menuReasoning, items: items, cursor: currentItem(items)}
	m.state = stateMenu
}

// openProviderMenu opens the model-provider picker (Apple Floodgate / Apple
// Foundation Models / Ollama). Selecting one re-dispatches "/provider <id>"
// through the command path so the menu and the typed command share one impl.
func (m *Model) openProviderMenu() {
	cur := provider.Normalize(m.cfg.LLMProvider)
	var items []menuItem
	for _, p := range provider.List() {
		items = append(items, menuItem{
			label:   p.Label,
			desc:    p.Desc,
			value:   p.ID,
			current: p.ID == cur,
		})
	}
	m.menu = &menu{title: "Select a provider", kind: menuProviders, items: items, cursor: currentItem(items)}
	m.state = stateMenu
}

// applyMenu applies the highlighted menu selection, persists it, and closes.
func (m Model) applyMenu() (tea.Model, tea.Cmd) {
	if m.menu == nil || len(m.menu.items) == 0 {
		m.state = stateIdle
		return m, nil
	}
	it := m.menu.items[m.menu.cursor]
	switch m.menu.kind {
	case menuModels:
		m.cfg.SetModel(it.value)
		m.trans.add(entry{kind: entryInfo, text: m.saveConfig(fmt.Sprintf("Model set to %s (%s)", shortModel(it.value), provider.Label(m.cfg.LLMProvider)))})
	case menuReasoning:
		m.cfg.SetReasoningFor(m.cfg.Model, it.value)
		m.trans.add(entry{kind: entryInfo, text: m.saveConfig(m.reasoningStatus(it.value))})
	case menuProviders:
		// Re-dispatch through the text path so the menu and the typed command
		// share one implementation (providerCmd: default-model switch, save,
		// prompt refresh).
		m.menu = nil
		m.state = stateIdle
		return m.command("/provider " + it.value)
	case menuSessions:
		m.menu = nil
		m.state = stateIdle
		return m.switchSession(it.value)
	case menuSkills:
		// Toggle the selected skill (it.current is its enabled state) and
		// re-attach the enabled set so the change takes effect immediately. A
		// marketplace row (it.install) installs the catalog skill first.
		m.menu = nil
		m.state = stateIdle
		mgmt := manage.Load(m.cfg.WorkingDir)
		if it.install {
			msg, isErr := m.installMarketplaceSkill(it.value)
			k := entryInfo
			if isErr {
				k = entryError
			}
			m.trans.add(entry{kind: k, text: msg})
			m.refresh()
			return m, nil
		}
		if err := mgmt.SetSkill(it.value, !it.current, manage.Project); err != nil {
			m.trans.add(entry{kind: entryError, text: "could not update skill: " + err.Error()})
		} else {
			m.agent.AttachSkills(manage.Filter(skills.Load(m.cfg.WorkingDir), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))
			m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Skill %q %s.", it.value, enabledWord(!it.current))})
		}
		m.refresh()
		return m, nil
	case menuMcps:
		// Toggle the selected MCP server; it connects/disconnects on reload. A
		// marketplace row (it.install) adds the catalog server first.
		m.menu = nil
		m.state = stateIdle
		mgmt := manage.Load(m.cfg.WorkingDir)
		if it.install {
			msg, isErr := m.installMarketplaceMCP(it.value)
			k := entryInfo
			if isErr {
				k = entryError
			}
			m.trans.add(entry{kind: k, text: msg})
			m.refresh()
			return m, nil
		}
		if err := mgmt.SetMCP(it.value, !it.current, manage.Project); err != nil {
			m.trans.add(entry{kind: entryError, text: "could not update MCP server: " + err.Error()})
		} else {
			m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("MCP server %q %s. Run /mcps reload to apply.", it.value, enabledWord(!it.current))})
		}
		m.refresh()
		return m, nil
	case menuPlugins:
		// Unified marketplace: install the selected bundle / skill / MCP server.
		m.menu = nil
		m.state = stateIdle
		if !it.install {
			m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("%q is already installed.", it.value)})
			m.refresh()
			return m, nil
		}
		switch it.kind {
		case "skill":
			msg, isErr := m.installMarketplaceSkill(it.value)
			k := entryInfo
			if isErr {
				k = entryError
			}
			m.trans.add(entry{kind: k, text: msg})
		case "mcp":
			msg, isErr := m.installMarketplaceMCP(it.value)
			k := entryInfo
			if isErr {
				k = entryError
			}
			m.trans.add(entry{kind: k, text: msg})
		case "bundle":
			if summary, err := plugins.Install(it.value, true, m.cfg.WorkingDir); err != nil {
				m.trans.add(entry{kind: entryError, text: "could not install plugin: " + err.Error()})
			} else {
				m = m.reattachAfterInstall()
				m.publishCommands()
				m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Installed plugin %q: %s (bundled MCP servers apply on /mcps reload).", it.value, summary)})
			}
		case "market":
			name, market := plugins.SplitRef(it.value)
			if summary, err := plugins.InstallFromMarketplace(name, market, m.cfg.WorkingDir); err != nil {
				m.trans.add(entry{kind: entryError, text: "could not install: " + err.Error()})
			} else {
				m = m.reattachAfterInstall()
				m.publishCommands()
				m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Installed %s@%s: %s (bundled MCP servers apply on /mcps reload).", name, market, summary)})
			}
		}
		m.refresh()
		return m, nil
	case menuAgents:
		// Toggle the selected subagent persona and re-attach the enabled set so
		// the task tool's persona list updates immediately.
		m.menu = nil
		m.state = stateIdle
		mgmt := manage.Load(m.cfg.WorkingDir)
		if err := mgmt.SetAgent(it.value, !it.current, manage.Project); err != nil {
			m.trans.add(entry{kind: entryError, text: "could not update agent: " + err.Error()})
		} else {
			m.reattachAgents(mgmt)
			m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Agent %q %s.", it.value, enabledWord(!it.current))})
		}
		m.refresh()
		return m, nil
	case menuWorkers:
		// Toggle the selected worker; persists to workers.json and applies live.
		m.menu = nil
		m.state = stateIdle
		if mgr := m.agent.Workers(); mgr != nil {
			if err := mgr.SetEnabled(it.value, !it.current); err != nil {
				m.trans.add(entry{kind: entryError, text: "could not update worker: " + err.Error()})
			} else {
				m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Worker %q %s. Set its model with: /workers model %s <provider/model>", it.value, enabledWord(!it.current), it.value)})
			}
		}
		m.refresh()
		return m, nil
	case menuSchedules:
		// The selected row encodes a management sub-command in it.value (e.g.
		// "disable <id>"); re-dispatch it through the text path so the menu and
		// the typed command share one implementation.
		m.menu = nil
		m.state = stateIdle
		if it.value == "" {
			m.refresh()
			return m, nil
		}
		return m.command("/schedule " + it.value)
	}
	m.menu = nil
	m.state = stateIdle
	m.publishStatus()
	m.refresh()
	return m, nil
}

// ensureOllamaModel makes the session's model one the Ollama server serves: it
// fetches the installed models and, if the current model isn't among them,
// switches to the first served model and returns its id. Returns "" when the
// model is left unchanged (already served, or the server couldn't be reached —
// /models then surfaces the error). Synchronous: a user-initiated /provider
// switch can briefly block on the local server.
func (m Model) ensureOllamaModel() string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, err := m.agent.ListModels(ctx)
	if err != nil || len(models) == 0 {
		return ""
	}
	for _, mi := range models {
		if mi.ID == m.cfg.Model {
			return "" // already served
		}
	}
	m.cfg.SetModel(models[0].ID)
	return models[0].ID
}

// projectGitLabel renders the merged project/branch/status token shown in the
// header: "<dir>:<branch><dirty>". Only the project name (the dir's final
// segment), the branch, and the dirty marker are bold; the parent path prefix
// and the colon stay dim (e.g. a dim "~/code/" + bold "app:main*"). When
// includeDir is false it drops the directory (compact header on narrow
// terminals), leaving just "<branch><dirty>"; outside a git repo it's just the
// dir (or "").
func (m Model) projectGitLabel(includeDir bool) string {
	var b strings.Builder
	if includeDir {
		parent, name := splitProjectName(prettyDir(m.cfg.WorkingDir))
		if parent != "" {
			b.WriteString(headerMetaStyle.Render(parent))
		}
		b.WriteString(headerProjectStyle.Render(name))
	}
	if m.gitRepo && m.gitBranch != "" {
		if includeDir {
			b.WriteString(headerSepStyle.Render(":"))
		}
		b.WriteString(headerProjectStyle.Render(m.gitBranch))
		if m.gitDirty > 0 {
			b.WriteString(headerProjectStyle.Render("*"))
		}
	}
	return b.String()
}

// completeCommand autocompletes the slash command in the input box.
func (m *Model) completeCommand() {
	val := strings.TrimSpace(m.input.Value())
	matches := matchCommands(m.allCommands(), val)
	switch {
	case len(matches) == 0:
		return
	case len(matches) == 1:
		m.input.SetValue(matches[0].name + " ")
	default:
		if cp := commonPrefix(matches); len(cp) > len(val) {
			m.input.SetValue(cp)
		}
	}
	m.input.CursorEnd()
	m.syncInputHeight()
}

// SetMCPPrompts wires server-supplied prompt templates into the slash-command
// surface. Each prompt becomes "/mcp.<server>.<prompt>"; running it calls the
// server's prompts/get and submits the rendered text as a user message.
func (m *Model) SetMCPPrompts(prompts []mcp.PromptDef) { m.mcpPrompts = prompts }

// SetMCPServers records the connected MCP servers (name + capability counts)
// for display in the left sidebar's MCP section.
func (m *Model) SetMCPServers(servers []mcp.ServerInfo) { m.mcpServers = servers }

// SetMCPManager wires the live MCP manager so /mcps reload can reconnect.
func (m *Model) SetMCPManager(mgr *mcp.Manager) { m.mcpMgr = mgr }

// SetLSPManager wires the live language-server manager so /lsp reload can
// restart servers without restarting the agent.
func (m *Model) SetLSPManager(mgr *lsp.Manager) { m.lspMgr = mgr }

// BeginStartupChecks sets up the animated startup loader. The user check is always
// checked; the MCP and LSP rows are added only when those will actually connect.
// While active, model turns are held (see submit) and released by startupDoneMsg.
// main calls it before tea.NewProgram and drives the rows via the Startup* msgs.
func (m *Model) BeginStartupChecks(showMCP, showLSP bool) {
	checks := []startupCheck{{step: stepUser, label: "Checking user", state: checkPending}}
	if showMCP {
		checks = append(checks, startupCheck{step: stepMCP, label: "Checking MCP", state: checkPending})
	}
	if showLSP {
		checks = append(checks, startupCheck{step: stepLSP, label: "Checking LSP", state: checkPending})
	}
	m.startupChecks = checks
	m.startupActive = true
}

// setCheckState updates one startup row's state (and detail, when non-empty).
func (m *Model) setCheckState(step startupStep, state checkState, detail string) {
	for i := range m.startupChecks {
		if m.startupChecks[i].step == step {
			m.startupChecks[i].state = state
			if detail != "" {
				m.startupChecks[i].detail = detail
			}
			return
		}
	}
}

// CloseServers shuts down the MCP and LSP managers the TUI connected. main calls
// it after the program exits, since (unlike before) the managers are created in
// the background startup rather than owned by main's deferred cleanup.
func (m Model) CloseServers() {
	if m.mcpMgr != nil {
		m.mcpMgr.Close()
	}
	if m.lspMgr != nil {
		m.lspMgr.Close()
	}
}

// applyMCP registers the background-connected MCP tools on the update loop, so
// the (mutex-free) registry mutation never races a turn goroutine — safe because
// turns are held for the whole startup sequence. mgr may be nil (nothing to do).
// It also records the MCP check's result (connected/failed counts).
func (m *Model) applyMCP(mgr *mcp.Manager) {
	if mgr != nil {
		mgr.RegisterTools(m.agent.Registry())
		m.mcpMgr = mgr
		m.mcpServers = mgr.Servers()
		m.mcpPrompts = mgr.Prompts()
	}
	detail := fmt.Sprintf("%d connected", len(m.mcpServers))
	state := checkOK
	if mgr != nil {
		if f := mgr.Failures(); len(f) > 0 {
			detail += fmt.Sprintf(", %d failed", len(f))
			state = checkWarn
		}
	}
	m.setCheckState(stepMCP, state, detail)
}

// applyLSP attaches the started language servers and records the LSP check's
// result (which servers are running). mgr may be nil/empty (nothing to do).
func (m *Model) applyLSP(mgr *lsp.Manager) {
	detail := "none"
	if mgr != nil && !mgr.Empty() {
		m.lspMgr = mgr
		m.agent.AttachLSP(mgr)
		if r := mgr.Running(); len(r) > 0 {
			detail = strings.Join(r, ", ")
		}
	}
	m.setCheckState(stepLSP, checkOK, detail)
}

// flushQueued drains queued user messages FIFO until one starts a turn (or the
// queue empties), preserving any in-progress composer draft. Shared by the
// post-turn drain (handleStream) and the post-connect release of messages held
// while MCP was connecting (submit's option-B gate).
func (m Model) flushQueued(cmds []tea.Cmd) (Model, []tea.Cmd) {
	if len(m.queued) == 0 {
		return m, cmds
	}
	draft := m.input.Value()
	for len(m.queued) > 0 {
		next := m.queued[0]
		m.queued = m.queued[1:]
		m.input.SetValue(next)
		m2, cmd := m.submit()
		m = m2.(Model)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.state != stateIdle {
			break // a turn (or menu/approval) started; resume on its completion
		}
	}
	// submit() resets the input on a successful send; restore the preserved draft
	// so the user picks up where they left off.
	if draft != "" && strings.TrimSpace(m.input.Value()) == "" {
		m.input.SetValue(draft)
		m.syncInputHeight()
	}
	m.publishQueue() // viewers see the shrunk queue after the drain
	m.refresh()
	return m, cmds
}

// SetUserName sets the speaker label for the human's messages (e.g. the
// local username). Ignored when empty so the "You" default stands.
func (m *Model) SetUserName(name string) {
	if strings.TrimSpace(name) != "" {
		m.trans.userLabel = strings.TrimSpace(name)
	}
}

// NoteWarning surfaces a prominent, actionable notice in the transcript at
// startup (e.g. the local user couldn't be resolved). It isn't a turn error, but it
// flags a problem the user should see immediately. Ignored when empty.
func (m *Model) NoteWarning(text string) {
	if strings.TrimSpace(text) != "" {
		m.trans.add(entry{kind: entryError, text: text})
	}
}

// ShouldRestart reports whether /update installed a new binary and the caller
// (main) should re-exec the executable after the program exits.
func (m Model) ShouldRestart() bool { return m.restart }

// allCommands returns the built-in slash commands plus any user-defined and
// MCP-supplied ones, suitable for hint matching and tab completion.
func (m Model) allCommands() []slashCommand {
	out := append([]slashCommand(nil), slashCommands...)
	for _, c := range m.userCommands {
		out = append(out, slashCommand{name: c.name, args: "[args]", desc: c.description})
	}
	for _, p := range m.mcpPrompts {
		desc := p.Prompt.Description
		if desc == "" {
			desc = "mcp prompt from " + p.ServerName
		}
		out = append(out, slashCommand{name: p.SlashCommand(), args: "[args]", desc: desc})
	}
	return out
}

// atInputTop reports whether the input cursor is on the first line.
func (m *Model) atInputTop() bool { return m.input.Line() == 0 }

// atInputBottom reports whether the input cursor is on the last line.
func (m *Model) atInputBottom() bool { return m.input.Line() >= m.input.LineCount()-1 }

// historyPrev recalls the previous command from history into the input.
func (m *Model) historyPrev() {
	if m.histIdx == len(m.history) {
		m.histStash = m.input.Value()
	}
	if m.histIdx > 0 {
		m.histIdx--
	}
	m.input.SetValue(m.history[m.histIdx])
	m.input.CursorEnd()
	m.syncInputHeight()
}

// historyNext moves toward the newest command, restoring the draft at the end.
func (m *Model) historyNext() {
	if m.histIdx >= len(m.history) {
		return
	}
	m.histIdx++
	if m.histIdx == len(m.history) {
		m.input.SetValue(m.histStash)
		m.histStash = ""
	} else {
		m.input.SetValue(m.history[m.histIdx])
	}
	m.input.CursorEnd()
	m.syncInputHeight()
}

// syncInputHeight grows or shrinks the input box to fit its content.
func (m *Model) syncInputHeight() {
	lines := m.input.LineCount()
	if lines < minInputLines {
		lines = minInputLines
	}
	if lines > maxInputLines {
		lines = maxInputLines
	}
	if m.input.Height() != lines {
		m.input.SetHeight(lines)
	}
}

// resize recomputes component dimensions and rebuilds the markdown renderer.
func (m *Model) resize(w, h int) {
	m.width = w
	m.height = h

	// A coarse initial height for viewport.New; the exact, state-aware height is
	// computed by viewportHeight() and applied on every refresh()/View(), which
	// run right after this on a WindowSizeMsg. Don't rely on this estimate for
	// scroll math — it doesn't account for the variable status/input/todos rows.
	const headerH, statusH, inputH = 2, 1, 3
	vpH := h - headerH - statusH - inputH
	if vpH < 3 {
		vpH = 3
	}

	// The transcript (and its viewport / markdown renderer) share the row with
	// the sidebar when it's open, so they only get the remaining width.
	mainW := m.mainWidth()

	if !m.ready {
		m.viewport = viewport.New(mainW, vpH)
		// Default of 3 lines/notch feels jumpy on a trackpad (which sends many
		// wheel events per scroll). 1 is too sluggish for a notched mouse, so
		// 2 is the smoothness sweet spot. Override with MOONY_WHEEL_DELTA.
		m.viewport.MouseWheelDelta = mouseWheelDelta()
		m.ready = true
	} else {
		m.viewport.Width = mainW
		m.viewport.Height = vpH
	}

	m.input.SetWidth(w - 4)
	m.trans.width = mainW - 2

	// Build the markdown renderer. Prefer a tweaked dark/light style that drops
	// the "## "/"### " prefixes glamour otherwise prepends to headings; fall
	// back to the named style for a custom GLAMOUR_STYLE.
	opts := []glamour.TermRendererOption{glamour.WithWordWrap(mainW - 4)}
	if cfg, ok := markdownStyleConfig(); ok {
		opts = append(opts, glamour.WithStyles(cfg))
	} else {
		opts = append(opts, glamour.WithStylePath(markdownStyle()))
	}
	if r, err := glamour.NewTermRenderer(opts...); err == nil {
		m.trans.renderer = r
	}
}

// mainWidth is the width available to the transcript/viewport row: the full
// terminal width minus the sidebar's footprint when it's shown.
func (m Model) mainWidth() int {
	w := m.width
	if m.sidebarVisible() {
		w -= sidebarWidth
	}
	if w < 20 {
		w = 20
	}
	return w
}

// sidebarVisible reports whether the left panel should be drawn: only when
// toggled on and the terminal is wide enough to spare the columns.
func (m Model) sidebarVisible() bool {
	return m.showSidebar && m.width >= sidebarWidth+40
}

// toggleToolAtClick maps a left-click to the collapsible tool entry under the
// cursor (accounting for the header offset, current scroll position, and the
// sidebar when shown) and toggles it. Returns true when a tool was toggled so
// the caller can swallow the event.
func (m *Model) toggleToolAtClick(msg tea.MouseMsg) bool {
	if !m.ready {
		return false
	}
	headerH := lipgloss.Height(m.headerView())
	// The click must land inside the viewport's vertical band…
	if msg.Y < headerH || msg.Y >= headerH+m.viewport.Height {
		return false
	}
	// …and, when the sidebar is open, to the right of it (clicks on the panel
	// itself are not transcript clicks).
	if m.sidebarVisible() && msg.X < sidebarWidth {
		return false
	}
	// Translate the screen row into a transcript line: subtract the header and
	// add how far the viewport is scrolled.
	line := msg.Y - headerH + m.viewport.YOffset
	idx, ok := m.trans.toolAt(line)
	if !ok {
		return false
	}
	m.trans.toggleCollapse(idx)
	m.refresh()
	return true
}

// refresh re-renders the transcript into the viewport and scrolls to the bottom.
func (m *Model) refresh() {
	if !m.ready {
		return
	}
	// Decide whether to auto-follow BEFORE touching the height, so the choice
	// reflects the layout the user was actually looking at last frame (a manual
	// scroll-up, even mid-stream, is respected).
	atBottom := m.viewport.AtBottom()
	// Sync the viewport height to exactly what View() will render this frame.
	// resize() only keeps a coarse estimate (it can't know the per-state height
	// of the status row / input / todos strip), so without this the bottom
	// scroll math would use a viewport a few lines too tall and GotoBottom()
	// would under-scroll — clipping the last 2-3 lines of a long answer.
	m.viewport.Height = m.viewportHeight()
	m.viewport.SetContent(m.trans.render(m.streamingIdx))
	if atBottom {
		m.viewport.GotoBottom()
	}
}

// sgrMouseFragmentRe matches the payload of an SGR mouse report
// (e.g. "<64;30;15M"), with or without a leading "[". Such fragments leak into
// the key stream when a fast scroll splits a "\x1b[<…M" sequence across read
// boundaries: the part after the ESC byte fails mouse/CSI detection and arrives
// as printable runes. A human never types this, so it's safe to strip.
var sgrMouseFragmentRe = regexp.MustCompile(`\[?<\d+;\d+;\d+[Mm]`)

// csiFragmentRe matches a whole standalone CSI fragment that lost its leading
// ESC (e.g. "[A", "[200~", "[3~", "[<…M"). Anchored so it only fires when the
// entire rune burst is the fragment, avoiding false positives on typed text.
var csiFragmentRe = regexp.MustCompile(`^\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]$`)

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

// scopeFromFields returns Global if a --global/-g flag is present, else Project.
func scopeFromFields(fields []string) manage.Scope {
	for _, f := range fields {
		if f == "--global" || f == "-g" {
			return manage.Global
		}
	}
	return manage.Project
}

func helpText() string {
	var b strings.Builder
	b.WriteString("Commands\n")
	for _, c := range slashCommands {
		name := c.name
		if c.args != "" {
			name += " " + c.args
		}
		fmt.Fprintf(&b, "  %-18s %s\n", name, c.desc)
	}
	b.WriteString("\nKeys\n")
	b.WriteString("  enter             send message\n")
	b.WriteString("  esc               interrupt the current turn\n")
	b.WriteString("  ↑ / ↓             browse command history (when the cursor is at the edge)\n")
	b.WriteString("  tab               autocomplete a slash command\n")
	b.WriteString("  ctrl+b            toggle the left sidebar (todos + modified files + MCP servers)\n")
	b.WriteString("  ctrl+o            expand/collapse all tool outputs (or click one to toggle it)\n")
	b.WriteString("  ctrl+y            copy the last response to the clipboard (same as /copy)\n")
	b.WriteString("  mouse wheel       scroll the transcript (on by default)\n")
	b.WriteString("  shift+↑ / ↓       scroll the transcript one line\n")
	b.WriteString("  pgup/pgdn         scroll the transcript a page (fn+↑/↓ on Mac)\n")
	b.WriteString("  ctrl+u / ctrl+d   scroll half a page\n")
	b.WriteString("  ctrl+c            quit\n")
	b.WriteString("\nCopy: use /copy or ctrl+y to copy the last response, or hold ⌥ Option and drag to select.")
	b.WriteString(" Set MOONY_MOUSE=0 to disable mouse capture (native selection; keyboard scroll only).")
	b.WriteString("\nApprovals: before bash/write/edit run, press y approve · n deny · a always allow that tool (saved to project /permissions)")
	b.WriteString("\nMemory: /memory shows notes; /memory <note> or a line starting with # saves one to AGENTS.md (the model can also save via its remember tool)")
	b.WriteString("\nGoal: /goal <description> sets a stop condition; the agent keeps working until an independent judge model confirms it's met. /goal clear removes it.")
	b.WriteString("\nCheckpoint: a distilled snapshot of the session's state is kept automatically and re-injected as context, so the agent keeps its understanding across compaction and resume. /checkpoint shows it; /checkpoint now refreshes it; /checkpoint clear removes it.")
	return strings.TrimRight(b.String(), "\n")
}

// sessionsList returns a short listing of recent sessions for the working dir.
func (m Model) sessionsList() string {
	if m.store == nil {
		return "Session persistence is disabled."
	}
	all, err := m.store.List()
	if err != nil {
		return "Could not list sessions: " + err.Error()
	}
	var b strings.Builder
	b.WriteString("Recent sessions for this directory (resume with --session <id> or --continue):\n")
	shown := 0
	for _, s := range all {
		if s.WorkingDir != m.cfg.WorkingDir {
			continue
		}
		marker := "  "
		if s.ID == m.agent.SessionID() {
			marker = "→ "
		}
		fmt.Fprintf(&b, "%s%s  %s  (%s)\n", marker, s.ID, s.Title, s.UpdatedAt.Format("Jan 2 15:04"))
		shown++
		if shown >= 10 {
			break
		}
	}
	if shown == 0 {
		return "No saved sessions for this directory yet."
	}
	return strings.TrimRight(b.String(), "\n")
}

// sessionItems returns recent sessions for this directory as menu items, newest
// first (capped), with the active session marked. ok is false when persistence
// is off or there are no sessions.
func (m Model) sessionItems() (items []menuItem, ok bool) {
	if m.store == nil {
		return nil, false
	}
	sessions, err := m.store.ListByDir(m.cfg.WorkingDir)
	if err != nil || len(sessions) == 0 {
		return nil, false
	}
	active := m.agent.SessionID()
	for i, s := range sessions {
		if i >= 20 {
			break
		}
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		items = append(items, menuItem{
			label:   title,
			desc:    s.UpdatedAt.Format("Jan 2 15:04") + " · " + s.ID,
			value:   s.ID,
			current: s.ID == active,
		})
	}
	return items, true
}

// openSkillsMenu shows the discovered skills as a selectable list; pressing
// enter on a row toggles it on/off. (Scaffold/remove stay on /skills new|remove.)
func (m Model) openSkillsMenu() (tea.Model, tea.Cmd) {
	mgmt := manage.Load(m.cfg.WorkingDir)
	found := skills.Load(m.cfg.WorkingDir)
	items := make([]menuItem, 0, len(found))
	have := map[string]bool{}
	for _, s := range found {
		have[s.Name] = true
		on := mgmt.SkillEnabled(s.Name)
		items = append(items, menuItem{label: s.Name, desc: s.Description, value: s.Name, current: on})
	}
	// Append the prebuilt marketplace: catalog skills not yet installed, as
	// one-keypress Install rows.
	for _, e := range skills.Catalog() {
		if have[e.Name] {
			continue
		}
		items = append(items, menuItem{label: e.Name, desc: "install · " + e.Description, value: e.Name, install: true})
	}
	if len(items) == 0 {
		m.trans.add(entry{kind: entryInfo, text: "No skills found. Add one with /skills new <name>, or place a SKILL.md under ~/.claude/skills or .claude/skills (MOONY_SKILLS=0 disables discovery)."})
		m.refresh()
		return m, nil
	}
	m.menu = &menu{title: "Skills — enter to toggle, or install from the marketplace", kind: menuSkills, items: items}
	m.state = stateMenu
	m.refresh()
	return m, nil
}

// openMcpsMenu shows the configured MCP servers as a selectable list; pressing
// enter on a row toggles it (apply with /mcps reload). add/remove/reload stay on
// the /mcps subcommands.
func (m Model) openMcpsMenu() (tea.Model, tea.Cmd) {
	mgmt := manage.Load(m.cfg.WorkingDir)
	cfgs, _ := mcp.LoadConfigs(m.cfg.WorkingDir)
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
	items := make([]menuItem, 0, len(cfgs))
	have := map[string]bool{}
	for _, c := range cfgs {
		have[c.Name] = true
		target := c.Command
		if c.URL != "" {
			target = c.URL
		}
		on := mgmt.MCPEnabled(c.Name)
		desc := target
		if s, ok := connected[c.Name]; ok {
			desc += fmt.Sprintf("  · connected (%d tools)", s.Tools)
		} else if e, ok := failed[c.Name]; ok {
			desc += "  · failed: " + e
		} else if on {
			desc += "  · not connected"
		}
		items = append(items, menuItem{label: c.Name, desc: desc, value: c.Name, current: on})
	}
	// Append the prebuilt marketplace: catalog servers not yet configured, as
	// one-keypress Install rows.
	for _, e := range mcp.Catalog() {
		if have[e.Name] {
			continue
		}
		desc := "install · " + e.Desc
		if len(e.Needs) > 0 {
			desc += " (needs " + strings.Join(e.Needs, ", ") + ")"
		}
		items = append(items, menuItem{label: e.Name, desc: desc, value: e.Name, install: true})
	}
	if len(items) == 0 {
		m.trans.add(entry{kind: entryInfo, text: "No MCP servers configured. Add one with /mcps add <name> -- <cmd> [args] (or <name> <url>), or via your Claude config (auto-discovered; MOONY_MCP_CLAUDE=0 disables)."})
		m.refresh()
		return m, nil
	}
	m.menu = &menu{title: "MCP servers — enter to toggle, or install from the marketplace (then /mcps reload)", kind: menuMcps, items: items}
	m.state = stateMenu
	m.refresh()
	return m, nil
}

// installMarketplaceSkill installs a catalog skill and re-attaches the enabled
// skill set so it's live immediately. Returns a status line and whether it
// errored, for the caller to surface.
func (m Model) installMarketplaceSkill(name string) (string, bool) {
	wd := m.cfg.WorkingDir
	summary, err := skills.Install(name, true, wd)
	if err != nil {
		return "could not install skill: " + err.Error(), true
	}
	mgmt := manage.Load(wd)
	m.agent.AttachSkills(manage.Filter(skills.Load(wd), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))
	return "Installed skill " + name + " — " + summary, false
}

// installMarketplaceMCP adds a catalog MCP server to mcp.json (${root} expanded
// for filesystem/git-style servers). It connects on the next /mcps reload.
func (m Model) installMarketplaceMCP(name string) (string, bool) {
	ce, ok := mcp.CatalogEntryByName(name)
	if !ok {
		return "unknown MCP server: " + name, true
	}
	args := make([]string, len(ce.Args))
	for i, a := range ce.Args {
		args[i] = strings.ReplaceAll(a, "${root}", m.cfg.WorkingDir)
	}
	if _, err := mcp.AddServer(ce.Name, ce.Command, args, ce.URL, ce.Headers, true, m.cfg.WorkingDir); err != nil {
		return "could not install MCP server: " + err.Error(), true
	}
	note := fmt.Sprintf("Installed MCP server %q. Run /mcps reload to connect.", ce.Name)
	if len(ce.Needs) > 0 {
		note += " First set: " + strings.Join(ce.Needs, ", ") + "."
	}
	return note, false
}

// reattachAfterInstall re-applies enabled skills/agents and reloads user commands
// after a bundle/marketplace install so new components are live. Returns the
// updated Model (m.userCommands changes); the caller should publishCommands().
func (m Model) reattachAfterInstall() Model {
	wd := m.cfg.WorkingDir
	mgmt := manage.Load(wd)
	m.agent.AttachSkills(manage.Filter(skills.Load(wd), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))
	m.reattachAgents(mgmt)
	m.userCommands = loadUserCommands(wd)
	return m
}

// openPluginsMenu shows the unified marketplace: plugin bundles plus individual
// catalog skills and MCP servers. Each row is marked installed or installable;
// pressing enter on an uninstalled row installs it.
func (m Model) openPluginsMenu() (tea.Model, tea.Cmd) {
	wd := m.cfg.WorkingDir
	var items []menuItem

	// Plugin bundles (a curated skill+agent+command+MCP set installed together).
	for _, st := range plugins.StatusAll(wd) {
		it := menuItem{label: st.Name, value: st.Name, kind: "bundle", desc: "bundle · " + st.Desc}
		switch {
		case st.Installed:
			it.current = true
		case st.Present > 0:
			it.install = true
			it.desc = fmt.Sprintf("bundle · partial %d/%d · %s", st.Present, st.Components, st.Desc)
		default:
			it.install = true
		}
		items = append(items, it)
	}
	// Individual catalog skills.
	haveSkill := map[string]bool{}
	for _, s := range skills.Load(wd) {
		haveSkill[s.Name] = true
	}
	for _, e := range skills.Catalog() {
		it := menuItem{label: e.Name, value: e.Name, kind: "skill", desc: "skill · " + e.Description}
		if haveSkill[e.Name] {
			it.current = true
		} else {
			it.install = true
		}
		items = append(items, it)
	}
	// Individual catalog MCP servers.
	haveMCP := map[string]bool{}
	if cfgs, _ := mcp.LoadConfigs(wd); cfgs != nil {
		for _, c := range cfgs {
			haveMCP[c.Name] = true
		}
	}
	for _, e := range mcp.Catalog() {
		desc := "mcp · " + e.Desc
		if len(e.Needs) > 0 {
			desc += " (needs " + strings.Join(e.Needs, ", ") + ")"
		}
		it := menuItem{label: e.Name, value: e.Name, kind: "mcp", desc: desc}
		if haveMCP[e.Name] {
			it.current = true
		} else {
			it.install = true
		}
		items = append(items, it)
	}
	// Plugins from git marketplaces (Claude-Code style), including the built-in
	// defaults. Each bundles skills/agents/commands/MCP; installing copies them in.
	for _, p := range plugins.MarketplacePlugins(wd) {
		desc := "market:" + p.Marketplace
		if p.Description != "" {
			desc = p.Description + " · " + desc
		}
		var comp []string
		if n := len(p.Skills); n > 0 {
			comp = append(comp, fmt.Sprintf("%d skills", n))
		}
		if n := len(p.MCP); n > 0 {
			comp = append(comp, fmt.Sprintf("%d mcp", n))
		}
		if n := len(p.Agents); n > 0 {
			comp = append(comp, fmt.Sprintf("%d agents", n))
		}
		if n := len(p.Commands); n > 0 {
			comp = append(comp, fmt.Sprintf("%d cmds", n))
		}
		if len(comp) > 0 {
			desc += " · " + strings.Join(comp, ", ")
		}
		it := menuItem{label: p.Name, value: p.Name + "@" + p.Marketplace, kind: "market", desc: desc}
		if p.Installed {
			it.current = true
		} else {
			it.install = true
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		m.trans.add(entry{kind: entryInfo, text: "Marketplace is empty."})
		m.refresh()
		return m, nil
	}
	m.menu = &menu{title: "Marketplace — enter to install (bundles · skills · MCP servers)", kind: menuPlugins, items: items}
	m.state = stateMenu
	m.refresh()
	return m, nil
}

// reattachAgents re-applies the enabled subagent personas to the task tool.
func (m Model) reattachAgents(mgmt *manage.Settings) {
	m.agent.AttachSubAgents(manage.Filter(subagents.Load(m.cfg.WorkingDir), func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled))
}

// agentInitCmd (re)materializes the canonical build/plan agent definitions into
// the global config agents dir (without clobbering existing files), then reloads
// personas so any new files take effect immediately.
func (m Model) agentInitCmd() string {
	names, err := subagents.SeedGlobalDefaultAgents()
	if err != nil {
		return "Could not scaffold default agents: " + err.Error()
	}
	dir, _ := subagents.GlobalAgentsDir()
	m.reattachAgents(manage.Load(m.cfg.WorkingDir))
	if len(names) == 0 {
		return "Default build/plan agents already present in " + dir + " (nothing overwritten). Edit build.md / plan.md there to customize them, or toggle them in /agents."
	}
	return "Seeded default agents into " + dir + ": " + strings.Join(names, ", ") +
		"\nThey now appear in /agents. Edit build.md / plan.md to customize, then /agent <name> to switch."
}

// openAgentsMenu shows the subagent personas as a selectable list; pressing
// enter on a row toggles it on/off. (Scaffold/remove stay on /agents new|remove.)
func (m Model) openAgentsMenu() (tea.Model, tea.Cmd) {
	mgmt := manage.Load(m.cfg.WorkingDir)
	found := subagents.Load(m.cfg.WorkingDir)
	if len(found) == 0 {
		m.trans.add(entry{kind: entryInfo, text: "No subagents found. Add one with /agents new <name>, or place a .md under ~/.config/moony/agents or .sqe/agents."})
		m.refresh()
		return m, nil
	}
	items := make([]menuItem, 0, len(found))
	for _, p := range found {
		desc := p.Description
		if p.Mode == "primary" {
			desc = "[primary profile — switch with /agent] " + desc
		}
		if p.Model != "" {
			desc += "  · model: " + p.Model
		}
		if len(p.Tools) > 0 {
			desc += "  · tools: " + strings.Join(p.Tools, ", ")
		}
		items = append(items, menuItem{label: p.Name, desc: desc, value: p.Name, current: mgmt.AgentEnabled(p.Name)})
	}
	m.menu = &menu{title: "Subagents — enter to toggle on/off", kind: menuAgents, items: items}
	m.state = stateMenu
	m.refresh()
	return m, nil
}

// openWorkersMenu shows the worker agents as a selectable list; pressing enter
// on a row toggles it on/off. Each row shows the engine and its readiness; set a
// worker's model with the /workers model <name> <provider/model> subcommand.
func (m Model) openWorkersMenu() (tea.Model, tea.Cmd) {
	mgr := m.agent.Workers()
	if mgr == nil {
		m.trans.add(entry{kind: entryInfo, text: "Workers are unavailable in this session."})
		m.refresh()
		return m, nil
	}
	mgr.Detect() // pick up engines installed since startup
	all := mgr.All()
	if len(all) == 0 {
		m.trans.add(entry{kind: entryInfo, text: "No workers available. Install Claude Code (`claude`) or Optimus (`optimus`), or add a worker to ./.sqe/workers.json."})
		m.refresh()
		return m, nil
	}
	ready := mgr.Readiness()
	items := make([]menuItem, 0, len(all))
	for _, c := range all {
		model := c.Model
		if model == "" {
			model = "default model"
		}
		desc := fmt.Sprintf("%s · %s · %s", c.Backend, ready[c.Name], model)
		items = append(items, menuItem{label: c.Name, desc: desc, value: c.Name, current: c.Enabled()})
	}
	m.menu = &menu{title: "Workers — enter to toggle · set model: /workers model <name> <provider/model>", kind: menuWorkers, items: items}
	m.state = stateMenu
	m.refresh()
	return m, nil
}

// openSessionsMenu opens the local (terminal) session picker.
func (m Model) openSessionsMenu() (tea.Model, tea.Cmd) {
	items, ok := m.sessionItems()
	if !ok {
		m.trans.add(entry{kind: entryInfo, text: "No saved sessions for this directory yet."})
		m.refresh()
		return m, nil
	}
	m.menu = &menu{title: "Switch session", kind: menuSessions, items: items, cursor: currentItem(items)}
	m.state = stateMenu
	m.refresh()
	return m, nil
}

// publishSessionsMenu mirrors a clickable session picker to web viewers.
func (m Model) publishSessionsMenu() {
	items, ok := m.sessionItems()
	if !ok {
		m.publishInfo("No saved sessions for this directory yet.")
		return
	}
	out := make([]bridge.MenuItem, 0, len(items))
	for _, it := range items {
		out = append(out, bridge.MenuItem{Label: it.label, Desc: it.desc, Value: it.value, Current: it.current})
	}
	m.publishMenu("Switch session", "/sessions", out)
}

// scheduleCmd handles the text sub-commands of /schedule (the no-arg listing
// opens a selectable menu via openSchedulesMenu): add, list, remove, enable,
// disable, run. It returns a line to append to the transcript.
func (m Model) scheduleCmd(fields []string) string {
	st, err := schedule.Open()
	if err != nil {
		return "schedule: " + err.Error()
	}
	switch strings.ToLower(fields[1]) {
	case "list", "ls":
		list, err := st.Load()
		if err != nil {
			return "schedule: " + err.Error()
		}
		if out := schedule.FormatList(list); out != "" {
			return out
		}
		return "No schedules yet. Add one with: /schedule add <every 30m|daily 09:00|once 2026-07-01T09:00> <prompt>"
	case "add":
		// Grammar: /schedule add <kind> <when> <prompt…>
		// e.g. "/schedule add every 30m run the smoke tests".
		if len(fields) < 5 {
			return "usage: /schedule add <every 30m|daily 09:00|once 2026-07-01T09:00> <prompt>"
		}
		sc, err := schedule.ParseSpec(fields[2] + " " + fields[3])
		if err != nil {
			return "schedule: " + err.Error()
		}
		sc.Prompt = strings.TrimSpace(strings.Join(fields[4:], " "))
		sc.WorkingDir = m.cfg.WorkingDir
		added, err := st.Add(sc)
		if err != nil {
			return "schedule: " + err.Error()
		}
		return fmt.Sprintf("Added schedule %s — %s — next run %s", added.ID, added.Describe(), added.NextRun.Format(time.RFC1123))
	case "remove", "rm", "delete":
		if len(fields) < 3 {
			return "usage: /schedule remove <id>"
		}
		if err := st.Remove(fields[2]); err != nil {
			return "schedule: " + err.Error()
		}
		return "Schedule " + fields[2] + " removed."
	case "enable", "disable":
		if len(fields) < 3 {
			return "usage: /schedule " + strings.ToLower(fields[1]) + " <id>"
		}
		on := strings.ToLower(fields[1]) == "enable"
		if err := st.SetEnabled(fields[2], on); err != nil {
			return "schedule: " + err.Error()
		}
		return fmt.Sprintf("Schedule %s %s.", fields[2], enabledWord(on))
	case "run":
		if len(fields) < 3 {
			return "usage: /schedule run <id>"
		}
		s, ok := st.Get(fields[2])
		if !ok {
			return "schedule: not found: " + fields[2]
		}
		exe, err := os.Executable()
		if err != nil {
			return "schedule: " + err.Error()
		}
		// Run off the UI goroutine — a headless run can take minutes and must not
		// block the event loop. The result lands as a browsable session and shows
		// up in /schedule list.
		go runScheduleOnce(st, s, exe)
		return fmt.Sprintf("Running schedule %s in the background — it'll appear as a session; see /schedule list for status.", s.ID)
	}
	return "usage: /schedule [add <spec> <prompt>|list|remove|enable|disable|run <id>]"
}

// runScheduleOnce executes one schedule immediately (off the UI goroutine) and
// records its outcome, mirroring `moony schedule run`.
func runScheduleOnce(st *schedule.Store, s schedule.Schedule, exe string) {
	sid, runErr := schedule.Run(context.Background(), s, exe)
	cur, ok := st.Get(s.ID)
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
}

// scheduleItems builds the schedule picker rows. Each row's value encodes the
// action selecting it performs — toggling enabled state — so the menu and the
// typed /schedule enable|disable command share one path.
func (m Model) scheduleItems() (items []menuItem, ok bool) {
	st, err := schedule.Open()
	if err != nil {
		return nil, false
	}
	list, err := st.Load()
	if err != nil || len(list) == 0 {
		return nil, false
	}
	for _, s := range list {
		verb := "disable"
		if !s.Enabled {
			verb = "enable"
		}
		items = append(items, menuItem{
			label:   s.Summary(),
			desc:    s.ID,
			value:   verb + " " + s.ID,
			current: s.Enabled,
		})
	}
	return items, true
}

// openSchedulesMenu shows scheduled jobs as a selectable list; pressing enter on
// a row toggles it enabled/disabled. (Add/remove/run stay on the /schedule
// sub-commands.)
func (m Model) openSchedulesMenu() (tea.Model, tea.Cmd) {
	items, ok := m.scheduleItems()
	if !ok {
		m.trans.add(entry{kind: entryInfo, text: "No schedules yet. Add one with: /schedule add <every 30m|daily 09:00|once 2026-07-01T09:00> <prompt>"})
		m.refresh()
		return m, nil
	}
	m.menu = &menu{title: "Schedules — enter toggles enabled", kind: menuSchedules, items: items, cursor: 0}
	m.state = stateMenu
	m.refresh()
	return m, nil
}

// publishSchedulesMenu mirrors a clickable schedules picker to web viewers. Each
// item's value carries the toggle action (the browser sends "/schedule <value>").
func (m Model) publishSchedulesMenu() {
	items, ok := m.scheduleItems()
	if !ok {
		m.publishInfo("No schedules yet. Add one with: /schedule add <every 30m|daily 09:00|once 2026-07-01T09:00> <prompt>")
		return
	}
	out := make([]bridge.MenuItem, 0, len(items))
	for _, it := range items {
		out = append(out, bridge.MenuItem{Label: it.label, Desc: it.desc, Value: it.value, Current: it.current})
	}
	m.publishMenu("Schedules — click to toggle", "/schedule", out)
}

// publishSkillsMenu mirrors a clickable skills toggle picker to web viewers. Each
// item's value carries the toggle action (the browser sends "/skills <value>"),
// matching the standalone web server's menu.
func (m Model) publishSkillsMenu() {
	mgmt := manage.Load(m.cfg.WorkingDir)
	found := skills.Load(m.cfg.WorkingDir)
	if len(found) == 0 {
		m.publishInfo("No skills found. Add one with /skills new <name>, or place a SKILL.md under ~/.claude/skills or .claude/skills (MOONY_SKILLS=0 disables discovery).")
		return
	}
	out := make([]bridge.MenuItem, 0, len(found))
	for _, s := range found {
		on := mgmt.SkillEnabled(s.Name)
		out = append(out, bridge.MenuItem{Label: s.Name, Desc: s.Description, Value: toggleAction(on) + s.Name, Current: on})
	}
	m.publishMenu("Skills — click to toggle", "/skills", out)
}

// publishMcpsMenu mirrors a clickable MCP toggle picker to web viewers.
func (m Model) publishMcpsMenu() {
	mgmt := manage.Load(m.cfg.WorkingDir)
	cfgs, _ := mcp.LoadConfigs(m.cfg.WorkingDir)
	if len(cfgs) == 0 {
		m.publishInfo("No MCP servers configured. Add one with /mcps add <name> -- <cmd> [args] (or <name> <url>), or via your Claude config (auto-discovered; MOONY_MCP_CLAUDE=0 disables).")
		return
	}
	connected := map[string]mcp.ServerInfo{}
	for _, s := range m.mcpServers {
		connected[s.Name] = s
	}
	out := make([]bridge.MenuItem, 0, len(cfgs))
	for _, c := range cfgs {
		target := c.Command
		if c.URL != "" {
			target = c.URL
		}
		on := mgmt.MCPEnabled(c.Name)
		if s, ok := connected[c.Name]; ok {
			target += fmt.Sprintf("  ·  connected (%d tools)", s.Tools)
		} else if on {
			target += "  ·  not connected"
		}
		out = append(out, bridge.MenuItem{Label: c.Name, Desc: target, Value: toggleAction(on) + c.Name, Current: on})
	}
	m.publishMenu("MCP servers — click to toggle", "/mcps", out)
}

// publishAgentsMenu mirrors a clickable subagent toggle picker to web viewers.
func (m Model) publishAgentsMenu() {
	mgmt := manage.Load(m.cfg.WorkingDir)
	found := subagents.Load(m.cfg.WorkingDir)
	if len(found) == 0 {
		m.publishInfo("No subagents found. Add one with /agents new <name>, or place a .md under ~/.config/moony/agents or .sqe/agents.")
		return
	}
	out := make([]bridge.MenuItem, 0, len(found))
	for _, p := range found {
		on := mgmt.AgentEnabled(p.Name)
		desc := p.Description
		if p.Model != "" {
			desc += "  ·  model: " + p.Model
		}
		if len(p.Tools) > 0 {
			desc += "  ·  tools: " + strings.Join(p.Tools, ", ")
		}
		out = append(out, bridge.MenuItem{Label: p.Name, Desc: desc, Value: toggleAction(on) + p.Name, Current: on})
	}
	m.publishMenu("Subagents — click to toggle", "/agents", out)
}

// publishWorkersMenu mirrors a clickable worker toggle picker to web viewers.
func (m Model) publishWorkersMenu() {
	mgr := m.agent.Workers()
	if mgr == nil {
		m.publishInfo("Workers are unavailable in this session.")
		return
	}
	mgr.Detect()
	all := mgr.All()
	if len(all) == 0 {
		m.publishInfo("No workers available. Install Claude Code (`claude`) or Optimus (`optimus`), or add a worker to ./.sqe/workers.json.")
		return
	}
	ready := mgr.Readiness()
	out := make([]bridge.MenuItem, 0, len(all))
	for _, c := range all {
		model := c.Model
		if model == "" {
			model = "default model"
		}
		out = append(out, bridge.MenuItem{Label: c.Name, Desc: fmt.Sprintf("%s · %s · %s", c.Backend, ready[c.Name], model), Value: toggleAction(c.Enabled()) + c.Name, Current: c.Enabled()})
	}
	m.publishMenu("Workers — click to toggle (set model: /workers model <name> <provider/model>)", "/workers", out)
}

// toggleAction is the "enable "/"disable " verb (with trailing space) for a menu
// item that flips an enabled item off and a disabled item on.
func toggleAction(on bool) string {
	if on {
		return "disable "
	}
	return "enable "
}

// switchSession loads a saved session, makes it active, rebuilds the transcript
// from its history, and mirrors the new conversation to web viewers.
func (m Model) switchSession(id string) (tea.Model, tea.Cmd) {
	if m.store == nil {
		m.trans.add(entry{kind: entryInfo, text: "Session persistence is disabled."})
		m.refresh()
		return m, nil
	}
	if id == m.agent.SessionID() {
		m.trans.add(entry{kind: entryInfo, text: "Already on that session."})
		m.refresh()
		return m, nil
	}
	sess, err := m.store.Load(id)
	if err != nil {
		note := "Could not load session " + id + ": " + err.Error()
		m.trans.add(entry{kind: entryError, text: note})
		m.publishError(note)
		m.refresh()
		return m, nil
	}
	m.agent.Resume(sess)
	m.todos = m.agent.Todos() // reflect the switched-to session's task list
	m.plan = m.agent.Plan()   // and its plan artifact
	m.trans.entries = seedEntries(m.agent.Messages())
	m.lastTurnInTokens = 0
	m.trans.add(entry{kind: entryInfo, text: fmt.Sprintf("Switched to session %s (%d messages).", id, m.agent.History())})
	if m.hub != nil {
		m.hub.SetActiveSession(m.agent.SessionID()) // route web viewers to the new session
	}
	m.publishTranscript() // reset + replay the new conversation to web viewers
	m.publishStatus()
	m.publishSessions() // active session changed
	m.refresh()
	return m, nil
}

// publishTranscript clears web viewers and replays the current transcript, so a
// session switch (or any full rebuild) is mirrored to the browser. No-op
// without a hub.
func (m Model) publishTranscript() {
	if m.hub == nil {
		return
	}
	m.hub.Reset()
	for _, e := range m.trans.entries {
		switch e.kind {
		case entryUser:
			m.hub.PublishUser(e.text)
		case entryAssistant:
			m.hub.PublishEvent(agent.TextDelta{Text: e.text})
		case entryThinking:
			m.hub.PublishEvent(agent.ThinkingDelta{Text: e.text})
		case entryTool:
			m.hub.PublishEvent(agent.ToolStart{ID: e.toolID, Name: e.toolName, Input: e.toolInput, Diff: e.toolDiff})
			m.hub.PublishEvent(agent.ToolEnd{ID: e.toolID, Name: e.toolName, Output: e.toolOutput, IsError: e.toolErr})
		case entryInfo:
			m.hub.PublishEvent(agent.Info{Text: e.text})
		case entryRaw:
			// The /context breakdown is recomputable from the live agent; emit
			// a fresh structured ContextStats so a reconnecting viewer gets the
			// color-coded bar rather than raw ANSI text.
			if m.agent != nil {
				bd := m.agent.ContextBreakdown()
				m.hub.PublishEvent(agent.ContextStats{Segments: bd.Segments, Total: bd.Total, Window: m.cfg.ContextWindowFor(m.cfg.Model)})
			}
		case entryError:
			m.hub.PublishError(e.text)
		}
	}
	// Mirror the session's plan artifact (if any) so a switched-to session
	// restores its plan panel for web viewers, matching the TUI sidebar.
	if m.plan != nil {
		m.hub.PublishEvent(agent.PlanUpdated{Plan: m.plan})
	}
}
