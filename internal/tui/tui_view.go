// tui_view.go — view/rendering helpers (sidebar, header, status, menus, hints).

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// viewportHeight is the transcript viewport height for the CURRENT state. It
// reserves space for the header, the status row, the bottom panel, and the
// optional todos strip exactly as View() lays them out. refresh() sets the
// viewport to this height before scrolling so GotoBottom()/AtBottom() agree with
// what View() actually renders; otherwise the auto-follow under-scrolls and the
// last couple of lines of a long answer get clipped off the bottom.
func (m Model) viewportHeight() int {
	header := m.headerView()
	status := "\n" + m.statusView()
	bottom := m.bottomPanel()
	// The todos strip is suppressed when the sidebar is open (it hosts Todos),
	// matching View(); an empty strip reserves no height (lipgloss.Height("")
	// is 1, so it's guarded).
	todosH := 0
	if m.sidebarView() == "" {
		if todos := m.todosView(); todos != "" {
			todosH = lipgloss.Height(todos)
		}
	}
	vpH := m.height - lipgloss.Height(header) - lipgloss.Height(status) - lipgloss.Height(bottom) - todosH
	if vpH < 3 {
		vpH = 3
	}
	return vpH
}

// sidebarView renders the left panel (Todos + MCP). Returns "" when the sidebar
// is toggled off or the terminal is too narrow to fit it. The panel is fixed at
// sidebarWidth columns (content + a right divider border).
func (m Model) sidebarView() string {
	if !m.sidebarVisible() {
		return ""
	}
	// Inner content width: total minus the 1-col right border and 2 cols of
	// horizontal padding.
	inner := sidebarWidth - 3
	if inner < 8 {
		inner = 8
	}

	var sections []string
	if s := m.sidebarPlan(inner); s != "" {
		sections = append(sections, s)
	}
	if s := m.sidebarTodos(inner); s != "" {
		sections = append(sections, s)
	}
	if s := m.sidebarFiles(inner); s != "" {
		sections = append(sections, s)
	}
	if s := m.sidebarMCP(inner); s != "" {
		sections = append(sections, s)
	}
	if len(sections) == 0 {
		sections = append(sections, sidebarMutedStyle.Render("No plan, todos, files,\nor MCP servers yet."))
	}

	body := strings.Join(sections, "\n\n")
	return sidebarBoxStyle.Width(sidebarWidth - 1).Render(body)
}

// sidebarPlan renders the Plan section of the sidebar, or "" when there is no
// plan. The header shows the plan's status (draft vs approved) and the steps are
// listed (truncated to width), windowed to a handful of rows.
func (m Model) sidebarPlan(width int) string {
	p := m.plan
	if p == nil || len(p.Steps) == 0 {
		return ""
	}
	label := "Plan · draft"
	headStyle := todoInProgressStyle
	switch p.Status {
	case session.PlanApproved:
		label = "Plan · approved"
		headStyle = todoCompletedStyle
	case session.PlanDone:
		label = "Plan · done"
		headStyle = todoCompletedStyle
	}
	lines := []string{sidebarSectionStyle.Render(label)}
	if t := strings.TrimSpace(p.Title); t != "" {
		lines = append(lines, headStyle.Render(clip(t, width)))
	}

	const maxRows = 6
	for i, s := range p.Steps {
		if i >= maxRows {
			lines = append(lines, todoPendingStyle.Render(fmt.Sprintf("… %d more", len(p.Steps)-maxRows)))
			break
		}
		row := fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(s.Title))
		lines = append(lines, todoPendingStyle.Render(clip(row, width)))
	}
	if p.Status == session.PlanDraft {
		lines = append(lines, sidebarDimStyle.Render("/plan approve"))
	}
	return strings.Join(lines, "\n")
}

// sidebarTodos renders the Todos section of the sidebar, or "" when empty. It
// shows a completed count, then the active/pending items (truncated to width),
// windowed to a handful of rows.
func (m Model) sidebarTodos(width int) string {
	if len(m.todos) == 0 {
		return ""
	}
	completed := 0
	for _, t := range m.todos {
		if t.Status == tools.TodoCompleted || t.Status == tools.TodoCancelled {
			completed++
		}
	}

	lines := []string{sidebarSectionStyle.Render(fmt.Sprintf("Todos (%d)", len(m.todos)))}
	if completed > 0 {
		lines = append(lines, todoCompletedStyle.Render(fmt.Sprintf("✓ %d done", completed)))
	}

	const maxRows = 6
	shown := 0
	remaining := len(m.todos) - completed
	for _, t := range m.todos {
		if t.Status == tools.TodoCompleted || t.Status == tools.TodoCancelled {
			continue
		}
		if shown >= maxRows {
			lines = append(lines, todoPendingStyle.Render(fmt.Sprintf("… %d more", remaining-shown)))
			break
		}
		marker, style := "○", todoPendingStyle
		if t.Status == tools.TodoInProgress {
			marker, style = "◐", todoInProgressStyle
		}
		lines = append(lines, style.Render(marker+" "+clip(t.Content, width-2)))
		shown++
	}
	return strings.Join(lines, "\n")
}

// sidebarFiles renders the Modified-files section: each file the agent has
// written or edited this session (deduped, most-recent first), marked "+" when
// created and "~" when edited. Returns "" when nothing has changed. Files
// reverted via /undo drop off automatically since the list reads the live undo
// log. Paths are truncated from the left so the filename stays visible.
func (m Model) sidebarFiles(width int) string {
	files := m.agent.ModifiedFiles()
	if len(files) == 0 {
		return ""
	}

	lines := []string{sidebarSectionStyle.Render(fmt.Sprintf("Files (%d)", len(files)))}
	const maxRows = 6
	for i, f := range files {
		if i >= maxRows {
			lines = append(lines, sidebarDimStyle.Render(fmt.Sprintf("… %d more", len(files)-maxRows)))
			break
		}
		marker, style := "~", fileModifiedStyle
		if f.Created {
			marker, style = "+", fileCreatedStyle
		}
		lines = append(lines, style.Render(marker+" "+clipPath(f.Path, width-2)))
	}
	return strings.Join(lines, "\n")
}

// sidebarMCP renders the MCP section of the sidebar: one line per connected
// server with its tool count. Returns "" when no servers are connected.
func (m Model) sidebarMCP(width int) string {
	if len(m.mcpServers) == 0 {
		// Still connecting in the background: show a placeholder so the user sees
		// MCP is on its way rather than an empty panel.
		if m.startupActive {
			status := "connecting…"
			if m.serverStatus != "" {
				status = clip(strings.TrimPrefix(m.serverStatus, "mcp: "), width)
			}
			return sidebarSectionStyle.Render("MCP") + "\n" + sidebarMutedStyle.Render(status)
		}
		return ""
	}
	lines := []string{sidebarSectionStyle.Render(fmt.Sprintf("MCP (%d)", len(m.mcpServers)))}
	for _, s := range m.mcpServers {
		count := mcpCountStyle.Render(fmt.Sprintf(" (%d)", s.Tools))
		// Reserve room for the trailing " (N)" count when truncating the name.
		name := mcpServerStyle.Render(clip(s.Name, width-lipgloss.Width(count)-1))
		lines = append(lines, name+count)
	}
	return strings.Join(lines, "\n")
}

// approvalView renders the confirmation prompt for a pending mutating tool call.
func (m Model) approvalView() string {
	p := m.pending
	var b strings.Builder
	b.WriteString(approvalTitleStyle.Render(fmt.Sprintf("⚠ approve %s?", p.Tool)))
	b.WriteByte('\n')
	if p.Summary != "" {
		b.WriteString(toolMetaStyle.Render(p.Summary))
		b.WriteByte('\n')
	}
	if strings.TrimSpace(p.Diff) != "" {
		b.WriteString(renderDiff(p.Diff, 12))
		b.WriteByte('\n')
	}
	// Gates that re-confirm every time (git commit/push, destructive commands)
	// ignore "always allow", so don't advertise the key there: offering it and
	// then re-prompting anyway is indistinguishable from a broken setting.
	if p.AlwaysDisabled {
		hint := " approve   "
		b.WriteString(
			approvalKeyStyle.Render("y") + toolMetaStyle.Render(hint) +
				approvalKeyStyle.Render("n") + toolMetaStyle.Render(" deny"),
		)
		if p.AlwaysDisabledReason != "" {
			b.WriteByte('\n')
			b.WriteString(toolMetaStyle.Render(p.AlwaysDisabledReason))
		}
		return approvalBoxStyle.Width(m.width - 2).Render(b.String())
	}
	// "a" persists a command-scoped rule for bash (see alwaysAllowRule), so label
	// it "this command" there to set the right expectation; "this tool" otherwise.
	alwaysLabel := " always allow this tool"
	if p.Tool == "bash" {
		alwaysLabel = " always allow this command"
	}
	b.WriteString(
		approvalKeyStyle.Render("y") + toolMetaStyle.Render(" approve   ") +
			approvalKeyStyle.Render("n") + toolMetaStyle.Render(" deny   ") +
			approvalKeyStyle.Render("a") + toolMetaStyle.Render(alwaysLabel),
	)
	return approvalBoxStyle.Width(m.width - 2).Render(b.String())
}

// questionView renders the ask_user chooser for a pending question: the question
// text, then the options as a navigable list (with number shortcuts and, for
// multiple-choice, toggle checkboxes), then the key hints. It mirrors menuView's
// selection styling inside the approval card.
func (m Model) questionView() string {
	q := m.pendingQ
	var b strings.Builder
	b.WriteString(approvalTitleStyle.Render("? " + q.Question))
	b.WriteByte('\n')

	// Free-text entry mode: show the input and how to submit.
	if m.qTextMode {
		b.WriteString(m.input.View())
		b.WriteByte('\n')
		b.WriteString(menuDescStyle.Render("type answer · enter send · esc back"))
		return approvalBoxStyle.Width(m.width - 2).Render(b.String())
	}

	const maxRows = 10
	items := q.Options
	cursor := m.qCursor
	start, end := 0, len(items)
	if len(items) > maxRows {
		start = cursor - maxRows/2
		if start < 0 {
			start = 0
		}
		if start > len(items)-maxRows {
			start = len(items) - maxRows
		}
		end = start + maxRows
	}
	if start > 0 {
		b.WriteString(menuDescStyle.Render(fmt.Sprintf("    ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		it := items[i]
		num := "  " // only the first 9 options get a number shortcut
		if i < 9 {
			num = fmt.Sprintf("%d ", i+1)
		}
		mark := ""
		if q.Multiple {
			if m.qChosen[i] {
				mark = "[x] "
			} else {
				mark = "[ ] "
			}
		}
		if i == cursor {
			line := "› " + num + mark + it.Label
			if it.Description != "" {
				line += "  " + it.Description
			}
			b.WriteString(menuSelectedStyle.Render(line))
		} else {
			line := menuItemStyle.Render("  " + num + mark + it.Label)
			if it.Description != "" {
				line += menuDescStyle.Render("  " + it.Description)
			}
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	if end < len(items) {
		b.WriteString(menuDescStyle.Render(fmt.Sprintf("    ↓ %d more", len(items)-end)))
		b.WriteByte('\n')
	}
	hint := "↑↓ move · 1-9 jump · enter select · esc skip"
	if q.Multiple {
		hint = "↑↓ move · space toggle · 1-9 toggle · enter submit · esc skip"
	}
	if q.AllowText {
		hint += " · t type answer"
	}
	b.WriteString(menuDescStyle.Render(hint))
	return approvalBoxStyle.Width(m.width - 2).Render(b.String())
}

// menuView renders the open selection menu as the bottom panel. Long lists are
// windowed around the cursor so the menu never overflows the screen.
func (m Model) menuView() string {
	items := m.menu.items
	cursor := m.menu.cursor

	const maxRows = 10
	start, end := 0, len(items)
	if len(items) > maxRows {
		start = cursor - maxRows/2
		if start < 0 {
			start = 0
		}
		if start > len(items)-maxRows {
			start = len(items) - maxRows
		}
		end = start + maxRows
	}

	var b strings.Builder
	b.WriteString(menuTitleStyle.Render(m.menu.title))
	b.WriteByte('\n')
	if start > 0 {
		b.WriteString(menuDescStyle.Render(fmt.Sprintf("    ↑ %d more", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		it := items[i]
		num := "  " // only the first 9 items get a number shortcut
		if i < 9 {
			num = fmt.Sprintf("%d ", i+1)
		}
		if i == cursor {
			line := "› " + num + it.label
			if it.desc != "" {
				line += "  " + it.desc
			}
			if it.current {
				line += "  (current)"
			}
			b.WriteString(menuSelectedStyle.Render(line))
		} else {
			line := menuItemStyle.Render("  " + num + it.label)
			if it.desc != "" {
				line += menuDescStyle.Render("  " + it.desc)
			}
			if it.current {
				line += menuCurrentStyle.Render("  (current)")
			}
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	if end < len(items) {
		b.WriteString(menuDescStyle.Render(fmt.Sprintf("    ↓ %d more", len(items)-end)))
		b.WriteByte('\n')
	}
	b.WriteString(menuDescStyle.Render("↑↓ move · 1-9 jump · enter select · esc cancel"))
	return menuBoxStyle.Width(m.width - 2).Render(b.String())
}

// headerView renders the top banner: an app badge on the left and the model,
// reasoning level, the merged project-dir:branch status token, and approval
// mode on the right, with a rule beneath.
func (m Model) headerView() string {
	badge := headerBadgeStyle.Render("moony") + headerMetaStyle.Render(" v"+config.Version)

	mode := config.NormalizeApprovalPolicy(m.cfg.ApprovalPolicy)
	if m.agent.PlanMode() {
		mode = "plan" // overrides the approval policy since nothing mutates anyway
	}
	sep := headerSepStyle.Render("  ·  ")

	reasoning := ""
	// Show the reasoning level only when it actually takes effect for the
	// current route/model (anthropic, or aws: Claude models on the openai route).
	if config.ReasoningActive(m.cfg.LLMProvider, m.cfg.Model, m.cfg.ReasoningLevel) {
		reasoning = sep + headerMetaStyle.Render("reasoning:"+m.cfg.ReasoningLevel)
	}

	// Show the bash sandbox only when it's on (and effective on this platform).
	box := ""
	if label := config.SandboxLabel(m.cfg.SandboxMode); label != "" {
		box = sep + headerMetaStyle.Render(label)
	}

	// The project dir is merged with the git branch + dirty status into one bold
	// token (e.g. "~/code/app:main*").
	full := headerKeyStyle.Render(shortModel(m.cfg.Model)) + reasoning + sep +
		m.projectGitLabel(true) + sep +
		headerMetaStyle.Render(mode) + box
	// Compact form for narrow terminals: drop the directory but keep the branch.
	compact := headerKeyStyle.Render(shortModel(m.cfg.Model)) + reasoning
	if g := m.projectGitLabel(false); g != "" {
		compact += sep + g
	}
	compact += sep + headerMetaStyle.Render(mode) + box

	right := full
	gap := m.width - lipgloss.Width(badge) - lipgloss.Width(right) - 1
	if gap < 1 {
		right = compact
		gap = m.width - lipgloss.Width(badge) - lipgloss.Width(right) - 1
	}
	if gap < 1 {
		gap = 1
	}

	top := badge + strings.Repeat(" ", gap) + right + " "
	rule := ruleStyle.Render(strings.Repeat("─", max(m.width, 1)))
	return top + "\n" + rule
}

// statusView renders the bottom status line: current activity and key hints on
// the left, scroll position and token usage on the right.
// todosView renders the agent's current task list as a compact panel above the
// status bar. Returns "" when the list is empty (so it collapses entirely).
// Long lists are windowed: completed items collapse to a count, then up to
// `maxRows` non-completed items are shown.
func (m Model) todosView() string {
	if len(m.todos) == 0 {
		return ""
	}

	active := -1
	completed := 0
	for i, t := range m.todos {
		if t.Status == tools.TodoCompleted || t.Status == tools.TodoCancelled {
			completed++
		}
		if t.Status == tools.TodoInProgress && active < 0 {
			active = i
		}
	}

	const maxRows = 5
	var lines []string
	if completed > 0 {
		lines = append(lines, todoCompletedStyle.Render(fmt.Sprintf("✓ %d completed", completed)))
	}
	count := 0
	remaining := len(m.todos) - completed
	for _, t := range m.todos {
		if t.Status == tools.TodoCompleted || t.Status == tools.TodoCancelled {
			continue
		}
		if count >= maxRows {
			lines = append(lines, todoPendingStyle.Render(fmt.Sprintf("  … %d more", remaining-count)))
			break
		}
		marker, style := "○", todoPendingStyle
		switch t.Status {
		case tools.TodoInProgress:
			marker, style = "◐", todoInProgressStyle
		case tools.TodoCancelled:
			marker, style = "✗", todoCancelledStyle
		}
		lines = append(lines, style.Render(marker+" "+t.Content))
		count++
	}

	title := todoTitleStyle.Render("Todos")
	if active >= 0 {
		title += todoPendingStyle.Render(fmt.Sprintf("  (%d/%d)", active+1, len(m.todos)))
	} else {
		title += todoPendingStyle.Render(fmt.Sprintf("  (%d items)", len(m.todos)))
	}
	body := title + "\n" + strings.Join(lines, "\n")
	return todoBoxStyle.Width(m.width - 2).Render(body)
}

func (m Model) statusView() string {
	var left string
	switch m.state {
	case stateBusy:
		act := "working"
		if m.toolsInFlight > 1 {
			act = fmt.Sprintf("running %d tools", m.toolsInFlight)
		} else if m.activeTool != "" {
			act = "running " + m.activeTool
		}
		step := ""
		if m.curStep > 0 {
			step = statusDimStyle.Render(fmt.Sprintf("   step %d/%d", m.curStep, m.maxStep))
		}
		left = m.spinner.View() + statusBusyStyle.Render(act+"…") + step +
			statusDimStyle.Render("   ") + statusKeyStyle.Render("esc") + statusDimStyle.Render(" interrupt")
		if n := len(m.queued); n > 0 {
			left += statusDimStyle.Render("   ") + statusKeyStyle.Render(fmt.Sprintf("queued %d", n))
		}
	case stateAwaitingApproval:
		left = statusBusyStyle.Render("awaiting approval  ") +
			statusKeyStyle.Render("y") + statusDimStyle.Render(" / ") +
			statusKeyStyle.Render("n") + statusDimStyle.Render(" / ") +
			statusKeyStyle.Render("a")
	case stateAwaitingQuestion:
		left = statusBusyStyle.Render("awaiting answer  ") +
			statusKeyStyle.Render("↑↓") + statusDimStyle.Render(" move   ") +
			statusKeyStyle.Render("enter") + statusDimStyle.Render(" select   ") +
			statusKeyStyle.Render("esc") + statusDimStyle.Render(" skip")
	case stateMenu:
		left = statusBusyStyle.Render("selecting  ") +
			statusKeyStyle.Render("↑↓") + statusDimStyle.Render(" move   ") +
			statusKeyStyle.Render("enter") + statusDimStyle.Render(" select   ") +
			statusKeyStyle.Render("esc") + statusDimStyle.Render(" cancel")
	default:
		if m.compacting {
			left = m.spinner.View() + statusBusyStyle.Render("compacting older history…")
		} else if m.btwBusy {
			left = m.spinner.View() + statusBusyStyle.Render("answering side question…")
		} else {
			left = statusKeyStyle.Render("enter") + statusDimStyle.Render(" send") +
				statusDimStyle.Render("   ") + statusKeyStyle.Render("/") + statusDimStyle.Render(" commands") +
				statusDimStyle.Render("   ") + statusKeyStyle.Render("↑↓") + statusDimStyle.Render(" history") +
				statusDimStyle.Render("   ") + statusKeyStyle.Render("ctrl+y") + statusDimStyle.Render(" copy")
		}
	}
	left = statusDimStyle.Render(" ") + left

	var right string
	if !m.viewport.AtBottom() {
		right += statusDimStyle.Render(fmt.Sprintf("↓ %d%%   ", clampPct(m.viewport.ScrollPercent())))
	}
	if pill := m.relayPill(); pill != "" {
		right += pill + statusDimStyle.Render("   ")
	}
	right += statusTextStyle.Render(fmt.Sprintf("↑%s ↓%s", formatTokens(m.totalIn), formatTokens(m.totalOut)))
	// Prompt-cache hit rate (cached reads as a share of input). Shown only once
	// caching has engaged, so a zero/0% never clutters the bar.
	if m.totalCacheRead > 0 && m.totalIn > 0 {
		right += statusDimStyle.Render("   ") + statusTextStyle.Render(fmt.Sprintf("cache %d%%", 100*m.totalCacheRead/m.totalIn))
	}
	// Prefer the authoritative quota from the gateway when known; fall back to the
	// in-session estimate while the first fetch is in flight or if it failed.
	if q := formatQuotaShort(m.quotaFetched, m.quotaSpend, m.quotaBudget, m.quotaBalance, m.quotaBalanceSet); q != "" {
		right += statusDimStyle.Render("   ") + statusTextStyle.Render(q)
	} else if m.totalCostUSD > 0 {
		right += statusDimStyle.Render("   ") + statusTextStyle.Render(fmt.Sprintf("$%s~", formatUSD(m.totalCostUSD)))
	}
	// Context-window fill — drawn after the quota so it's right-most. Hidden
	// until the first turn completes (when we have a real measurement).
	if pct := contextPercent(m.lastTurnInTokens, m.cfg.ContextWindowFor(m.cfg.Model)); pct >= 0 {
		var label string
		if contextBarEnabled() {
			label = fmt.Sprintf("ctx %s %d%%", progressBar(pct, 8), pct)
		} else {
			label = fmt.Sprintf("ctx %d%%", pct)
		}
		right += statusDimStyle.Render("   ") + contextStyle(pct).Render(label)
	}
	right += statusDimStyle.Render(" ")

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + statusDimStyle.Render(strings.Repeat(" ", gap)) + right
}

// relayPill renders the relay status-bar pill: "relay ●" colored green when
// connected (or shared via another instance on this machine) and orange while
// connecting/retrying. Empty when no relay is configured, so it takes no space.
func (m Model) relayPill() string {
	if m.relayState == RelayOff || m.relayAddr == "" {
		return ""
	}
	style := statusRelayWaitStyle
	if m.relayState == RelayConnected || m.relayState == RelayShared {
		style = statusRelayOnStyle
	}
	return style.Render("relay ●") + statusDimStyle.Render(" "+m.relayState.label())
}

// commandHints renders a compact menu of slash commands matching the current
// input, shown only while the user is typing a command name.
func (m Model) commandHints() string {
	if m.state != stateIdle {
		return ""
	}
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") {
		return ""
	}
	// Once the user types a space they're entering arguments; hide the menu.
	if strings.Contains(val, " ") {
		return ""
	}

	matches := matchCommands(m.allCommands(), val)
	if len(matches) == 0 {
		return ""
	}

	// Align descriptions into a column.
	nameW := 0
	for _, c := range matches {
		if w := len(c.name) + len(c.args) + 1; w > nameW {
			nameW = w
		}
	}

	var b strings.Builder
	for i, c := range matches {
		if i > 0 {
			b.WriteByte('\n')
		}
		label := hintNameStyle.Render(c.name)
		if c.args != "" {
			label += " " + hintArgStyle.Render(c.args)
		}
		padW := nameW - len(c.name) - len(c.args)
		if c.args != "" {
			padW--
		}
		if padW < 1 {
			padW = 1
		}
		b.WriteString(" " + label + strings.Repeat(" ", padW+2) + hintDescStyle.Render(c.desc))
	}
	return b.String()
}

// mentionHints renders the live @-mention suggestion list shown above the input
// while the user is typing a trailing "@<partial>" token — the @-mention analog
// of commandHints. Pressing Tab completes the match (completeAtMention).
func (m Model) mentionHints() string {
	if m.state != stateIdle {
		return ""
	}
	_, partial, ok := trailingMention(m.input.Value())
	if !ok {
		return ""
	}
	lp := strings.ToLower(partial)
	var matches []mentionInfo
	for _, mi := range m.agentMentionList() {
		if strings.HasPrefix(strings.ToLower(mi.Name), lp) {
			matches = append(matches, mi)
		}
	}
	if len(matches) == 0 {
		return ""
	}

	// Align descriptions into a column, keyed off the displayed "@name" width.
	nameW := 0
	for _, mi := range matches {
		if w := len(mi.Name) + 1; w > nameW { // +1 for the leading '@'
			nameW = w
		}
	}
	var b strings.Builder
	for i, mi := range matches {
		if i > 0 {
			b.WriteByte('\n')
		}
		disp := "@" + mi.Name
		desc := mi.Desc
		if desc == "" {
			desc = "subagent"
		}
		padW := nameW - len(disp) + 2
		if padW < 1 {
			padW = 1
		}
		b.WriteString(" " + hintNameStyle.Render(disp) + strings.Repeat(" ", padW) + hintDescStyle.Render(desc))
	}
	return b.String()
}

// startupView renders the startup panel: one row per check, animated (spinner)
// while running and showing the final ✓/⚠ result afterward. It stays on screen
// after the checks finish (startupChecks is kept) so the results remain visible,
// and is dismissed once the user sends their first question (runUserMessage
// clears startupChecks). Returns "" when there are no checks to show (e.g.
// headless, or after dismissal). Stacked above the input box in bottomPanel, so
// viewportHeight reserves space for it automatically.
func (m Model) startupView() string {
	if len(m.startupChecks) == 0 {
		return ""
	}
	lines := make([]string, 0, len(m.startupChecks))
	for _, c := range m.startupChecks {
		var marker, label string
		switch c.state {
		case checkRunning:
			marker, label = m.spinner.View(), startupLabelStyle.Render(c.label)
		case checkOK:
			marker, label = startupOKStyle.Render("✓"), startupLabelStyle.Render(c.label)
		case checkWarn:
			marker, label = startupWarnStyle.Render("⚠"), startupLabelStyle.Render(c.label)
		default: // checkPending
			marker, label = startupPendingStyle.Render("◦"), startupPendingStyle.Render(c.label)
		}
		line := marker + " " + label
		if c.detail != "" {
			line += "  " + startupDetailStyle.Render(c.detail)
		}
		lines = append(lines, line)
	}
	return startupBoxStyle.Render(strings.Join(lines, "\n"))
}
