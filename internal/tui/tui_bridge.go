// tui_bridge.go — relay/bridge Hub integration: publish state to the web mirror.

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/bridge"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// SetRelay records the configured relay address and seeds the pill's initial
// state. A non-empty addr starts as "connecting" (the background connector is
// dialing); empty leaves the pill off.
func (m *Model) SetRelay(addr string) {
	m.relayAddr = strings.TrimSpace(addr)
	if m.relayAddr != "" {
		m.relayState = RelayConnecting
	} else {
		m.relayState = RelayOff
	}
}

// RelayReporter returns a callback the background relay connector calls on each
// state change; it delivers the change into the given program's Update loop via
// Send (the same pattern as BindController), so the status pill stays current.
func RelayReporter(prog *tea.Program) func(RelayState) {
	return func(s RelayState) { prog.Send(relayStatusMsg{state: s}) }
}

// RelayVersionReporter returns a callback the background connector calls with the
// relay's build version (from the tunnel handshake); it delivers it into the
// Update loop so the TUI can warn when this terminal is behind the relay.
func RelayVersionReporter(prog *tea.Program) func(string) {
	return func(v string) { prog.Send(relayVersionMsg{version: v}) }
}

// SetHub attaches the bridge hub that mirrors this session to web viewers. Pass
// nil (or never call) to disable mirroring. It also seeds the hub's active
// session id so the multi-project web server can route to the right session.
func (m *Model) SetHub(h *bridge.Hub) {
	m.hub = h
	if h != nil && m.agent != nil {
		h.SetActiveSession(m.agent.SessionID())
	}
}

// publishUser/publishState/publishError/hubReset mirror conversation moments to
// web viewers. Each is a no-op when no hub is attached.
func (m Model) publishUser(text string) {
	if m.hub != nil {
		m.hub.PublishUser(text)
	}
}

func (m Model) publishState(state string) {
	if m.hub != nil {
		m.hub.PublishState(state)
	}
}

// publishQueue mirrors the busy-time message queue to web viewers so their
// composer queue strip matches the terminal's. No-op without a hub.
func (m Model) publishQueue() {
	if m.hub != nil {
		m.hub.PublishQueue(m.queued)
	}
}

func (m Model) publishError(text string) {
	if m.hub != nil {
		m.hub.PublishError(text)
	}
}

func (m Model) hubReset() {
	if m.hub != nil {
		m.hub.Reset()
	}
}

// publishInfo mirrors an informational line to web viewers (rendered like the
// agent's Info events). No-op without a hub.
func (m Model) publishInfo(text string) {
	if m.hub != nil {
		m.hub.PublishEvent(agent.Info{Text: text})
	}
}

// publishCommands mirrors the slash-command list and the @-mention candidate
// list to web viewers' palette and @-mention dropdown.
func (m Model) publishCommands() {
	if m.hub == nil {
		return
	}
	all := m.allCommands()
	cmds := make([]bridge.Command, 0, len(all))
	for _, c := range all {
		cmds = append(cmds, bridge.Command{Name: c.name, Args: c.args, Desc: c.desc})
	}
	mentions := make([]bridge.Mention, 0)
	for _, mi := range m.agentMentionList() {
		mentions = append(mentions, bridge.Mention{Name: mi.Name, Kind: mi.Kind, Desc: mi.Desc})
	}
	m.hub.PublishCommands(cmds, mentions)
}

// publishSessions mirrors the project's saved sessions to web viewers' sidebar.
// Selecting one there sends "/sessions <id>" back, switching the terminal.
func (m Model) publishSessions() {
	if m.hub == nil || m.store == nil {
		return
	}
	items, _ := m.sessionItems()
	out := make([]bridge.SessionInfo, 0, len(items))
	for _, it := range items {
		out = append(out, bridge.SessionInfo{ID: it.value, Title: it.label, Detail: it.desc, Active: it.current})
	}
	m.hub.PublishSessions(out)
}

// publishMenu mirrors a clickable picker to web viewers in place of a
// terminal-only selection menu. Selecting an item there sends "command value"
// back as input (handled by remoteSubmit while idle). No-op without a hub.
func (m Model) publishMenu(title, command string, items []bridge.MenuItem) {
	if m.hub == nil || len(items) == 0 {
		return
	}
	m.hub.PublishMenu(bridge.Menu{Title: title, Command: command, Items: items})
}

// publishModelsMenu mirrors a fetched model list as a clickable picker.
func (m Model) publishModelsMenu(models []config.ModelInfo) {
	items := make([]bridge.MenuItem, 0, len(models))
	seen := false
	for _, mi := range models {
		label := mi.Name
		if label == "" {
			label = shortModel(mi.ID)
		}
		items = append(items, bridge.MenuItem{Label: label, Desc: mi.ID, Value: mi.ID, Current: mi.ID == m.cfg.Model})
		seen = seen || mi.ID == m.cfg.Model
	}
	if !seen && m.cfg.Model != "" {
		items = append([]bridge.MenuItem{{Label: shortModel(m.cfg.Model), Desc: m.cfg.Model, Value: m.cfg.Model, Current: true}}, items...)
	}
	m.publishMenu(fmt.Sprintf("Select a model (%d)", len(items)), "/model", items)
}

// publishReasoningMenu mirrors the extended-thinking picker as a clickable menu.
func (m Model) publishReasoningMenu() {
	levels := config.ReasoningLevelsFor(m.cfg.Model)
	items := make([]bridge.MenuItem, 0, len(levels))
	for _, lvl := range levels {
		items = append(items, bridge.MenuItem{Label: lvl, Desc: config.ReasoningDescription(lvl), Value: lvl, Current: lvl == m.cfg.ReasoningLevel})
	}
	m.publishMenu("Select reasoning level", "/reasoning", items)
}

// publishStatus mirrors the terminal status bar (model, usage, cost/quota,
// context fill, current activity) to web viewers. No-op without a hub.
func (m Model) publishStatus() {
	if m.hub == nil {
		return
	}
	reasoning := ""
	if m.cfg.ReasoningLevel != "" && m.cfg.ReasoningLevel != config.ReasoningOff {
		reasoning = m.cfg.ReasoningLevel
	}
	// Approval mode mirrors the header badge: plan mode overrides the policy
	// since nothing mutates in plan mode anyway.
	approval := config.NormalizeApprovalPolicy(m.cfg.ApprovalPolicy)
	if m.agent.PlanMode() {
		approval = "plan"
	}
	mcps := make([]bridge.MCPServer, 0, len(m.mcpServers))
	for _, s := range m.mcpServers {
		mcps = append(mcps, bridge.MCPServer{Name: s.Name, Tools: s.Tools, Resources: s.Resources, Prompts: s.Prompts})
	}
	var files []bridge.ModFile
	for _, f := range m.agent.ModifiedFiles() {
		files = append(files, bridge.ModFile{Path: f.Path, Created: f.Created})
	}
	// Explain an in-force quota fallback in the mirrored status, so a web client
	// watching a terminal session sees why the provider changed.
	var fbFrom, fbReset string
	if m.agent != nil {
		if fb := m.agent.QuotaFallbackInfo(); fb.Switched {
			fbFrom = fb.PrimaryProvider
			if !fb.ResetAt.IsZero() {
				fbReset = fb.ResetAt.Local().Format(time.RFC3339)
			}
		}
	}
	m.hub.PublishStatus(bridge.Status{
		Version:              config.Version,
		Model:                shortModel(m.cfg.Model),
		Provider:             config.ProviderLabel(m.cfg.Provider),
		LLMProvider:          provider.Normalize(m.cfg.LLMProvider),
		Reasoning:            reasoning,
		WorkingDir:           prettyDir(m.cfg.WorkingDir),
		Approval:             approval,
		Sandbox:              config.SandboxLabel(m.cfg.SandboxMode),
		GitBranch:            m.gitBranch,
		GitDirty:             m.gitDirty,
		GitAhead:             m.gitAhead,
		GitBehind:            m.gitBehind,
		GitUpstream:          m.gitUpstream,
		Host:                 config.MachineName(),
		Relay:                m.relayState.label(),
		TokensIn:             m.totalIn,
		TokensOut:            m.totalOut,
		CostUSD:              m.totalCostUSD,
		QuotaSpend:           m.quotaSpend,
		QuotaBudget:          m.quotaBudget,
		QuotaKnown:           m.quotaFetched,
		QuotaName:            quotaName(m.quotaFetched, m.quotaProject),
		QuotaTokens:          m.quotaTokens,
		QuotaTokensMax:       m.quotaTokensMax,
		QuotaTokenRate:       m.quotaTokenRate,
		QuotaTokenRatePeriod: m.quotaTokenRatePeriod,
		QuotaFallbackFrom:    fbFrom,
		QuotaFallbackResetAt: fbReset,
		ContextPercent:       contextPercent(m.lastTurnInTokens, m.cfg.ContextWindowFor(m.cfg.Model)),
		ActiveTool:           m.activeTool,
		ToolsInFlight:        m.toolsInFlight,
		Agent:                m.agent.ActiveAgent().Name,
		MCP:                  mcps,
		Files:                files,
	})
}

// BindController wires a hub's remote-control callbacks to deliver into the
// given program's Update loop via Send. Call it after tea.NewProgram so browser
// viewers can drive the agent through the same paths as local input.
func BindController(h *bridge.Hub, prog *tea.Program) {
	h.SetController(
		func(text string) { prog.Send(remoteSubmitMsg{text: text}) },
		func(d agent.Decision) { prog.Send(remoteDecisionMsg{d: d}) },
		func() { prog.Send(remoteInterruptMsg{}) },
		func(id string) { prog.Send(remoteSwitchMsg{id: id}) },
	)
	h.SetAnswerController(func(labels []string) { prog.Send(remoteAnswerMsg{labels: labels}) })
	h.SetQueueController(func(action string, index int, text string) {
		prog.Send(remoteQueueMsg{action: action, index: index, text: text})
	})
}
