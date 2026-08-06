// tui_status.go — usage, context-window, and status-line formatting for the TUI.

package tui

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// formatUsage renders the quota/usage summary that /usage prints. It includes
// total spend (with budget if known), rate limits, and a per-model breakdown
// sorted by spend descending.
// applyUsage records a fetched quota/usage snapshot and overlays the
// authoritative per-model pricing onto the cost calculator. Called by both
// the silent refresh (after Init / TurnDone) and /usage so the persistent
// status-bar quota and the cost counter always agree with the latest fetch.
func (m *Model) applyUsage(r *llm.UsageResponse) {
	if r == nil {
		return
	}
	m.quotaSpend = r.Usage.Spend
	m.quotaBudget = r.Quota.Budget.Spend
	m.quotaBalance, m.quotaBalanceSet = r.Balance, r.BalanceSet
	m.quotaResetTime = r.Quota.Budget.ResetTime
	m.quotaTokens = r.Usage.InputTokens + r.Usage.OutputTokens
	m.quotaTokensMax = r.Quota.Budget.InputTokens + r.Quota.Budget.OutputTokens
	m.quotaTokenRate = r.Quota.Rates.Tokens
	m.quotaTokenRatePeriod = r.Quota.Rates.TokensPeriod
	if r.IsProject() {
		m.quotaProject = r.ProjectName
	} else {
		m.quotaProject = ""
	}
	m.quotaFetched = true
	m.quotaErr = ""
	for id, mdl := range r.Quota.Models {
		config.OverlayRuntimePricing(id, mdl.Pricing.InputTokenCost, mdl.Pricing.OutputTokenCost, mdl.Pricing.CacheReadTokenCost, mdl.Pricing.CacheWriteTokenCost)
	}
	for id, mdl := range r.Usage.Models {
		config.OverlayRuntimePricing(id, mdl.Pricing.InputTokenCost, mdl.Pricing.OutputTokenCost, mdl.Pricing.CacheReadTokenCost, mdl.Pricing.CacheWriteTokenCost)
	}
}

// formatQuotaShort renders a compact "$spend/$budget" or "$spend 24h" suitable
// for the status bar. Returns "" when no quota has been fetched yet.
func formatQuotaShort(fetched bool, spend, budget float64) string {
	if !fetched {
		return ""
	}
	if budget > 0 {
		return fmt.Sprintf("$%s/$%s", formatUSD(spend), formatUSD(budget))
	}
	return fmt.Sprintf("$%s 24h", formatUSD(spend))
}

// contextPercent reports how full the context window is as an integer 0-100.
// Returns -1 when there's no measurement yet (no turn has completed) so the
// caller can hide the indicator.
func contextPercent(used, window int) int {
	if used <= 0 || window <= 0 {
		return -1
	}
	pct := used * 100 / window
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// progressBar renders a fixed-width fill bar (default 8 cells) using Unicode
// block characters with partial-block precision. With 8 cells × 8 sub-cell
// fills, the bar resolves changes as small as ~1.5% so it animates smoothly
// turn-to-turn even though the underlying percentage is only an integer.
//
//	pct < 13%   → "▉░░░░░░░"
//	pct = 50%   → "████░░░░"
//	pct = 95%   → "███████▌"
func progressBar(pct, width int) string {
	if width <= 0 {
		return ""
	}
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	// Total filled "eighths" across all cells: 0 .. width*8.
	eighths := pct * width * 8 / 100
	full := eighths / 8
	rem := eighths % 8

	var b strings.Builder
	for i := 0; i < full && i < width; i++ {
		b.WriteRune('█')
	}
	if full < width {
		// One partial cell, then empties. The empty cell uses U+2591 LIGHT
		// SHADE so the bar is visually present even at 0%.
		if rem > 0 {
			b.WriteRune([]rune{0, '▏', '▎', '▍', '▌', '▋', '▊', '▉'}[rem])
			for i := full + 1; i < width; i++ {
				b.WriteRune('░')
			}
		} else {
			for i := full; i < width; i++ {
				b.WriteRune('░')
			}
		}
	}
	return b.String()
}

// contextBarEnabled reports whether the status bar's ctx indicator should
// include a graphical progress bar. Disabled when MN_CTX_BAR=off (or 0/false)
// for users on terminals that render Unicode blocks poorly.
func contextBarEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("MN_CTX_BAR")))
	switch v {
	case "off", "0", "false", "no", "text":
		return false
	}
	return true
}

// contextStyle picks the status-bar style for a given fill percentage:
// normal / warn (≥75%) / alarm (≥90%).
func contextStyle(pct int) lipgloss.Style {
	switch {
	case pct >= 90:
		return statusCtxAlarmStyle
	case pct >= 75:
		return statusCtxWarnStyle
	default:
		return statusCtxNormStyle
	}
}

func formatUsage(r *llm.UsageResponse) string {
	if r == nil {
		return "No usage data."
	}
	var b strings.Builder
	if r.IsProject() {
		fmt.Fprintf(&b, "Project: %s (%s)\n", r.ProjectName, r.ProjectID)
	} else if r.DSID != "" {
		fmt.Fprintf(&b, "Account: dsid %s\n", r.DSID)
	}

	// Budget. Three cases, and conflating them would misinform:
	//   spend + cap   -> a percentage is meaningful
	//   spend only    -> absolute spend, no invented denominator
	//   balance only  -> credit REMAINING, which is the opposite of spend
	used := r.Usage.Spend
	cap := r.Quota.Budget.Spend
	switch {
	case cap > 0 && used > 0:
		pct := used / cap * 100
		fmt.Fprintf(&b, "Budget: $%s / $%s (%.1f%% used)", formatUSD(used), formatUSD(cap), pct)
	case r.HasBalance():
		fmt.Fprintf(&b, "Credit remaining: $%s", formatUSD(r.Balance))
		if cap > 0 {
			fmt.Fprintf(&b, " of $%s", formatUSD(cap))
		}
	default:
		fmt.Fprintf(&b, "Spend (24h): $%s", formatUSD(used))
	}
	if r.Quota.Budget.ResetTime != "" {
		fmt.Fprintf(&b, " — resets %s", r.Quota.Budget.ResetTime)
	}
	b.WriteByte('\n')

	if r.Quota.Rates.Calls > 0 || r.Quota.Rates.Tokens > 0 {
		fmt.Fprintf(&b, "Rates: %d calls / %s, %d tokens / %s\n",
			r.Quota.Rates.Calls, r.Quota.Rates.CallsPeriod,
			r.Quota.Rates.Tokens, r.Quota.Rates.TokensPeriod)
	}

	fmt.Fprintf(&b, "Calls: %d   Tokens: ↑%d ↓%d", r.Usage.Calls, r.Usage.InputTokens, r.Usage.OutputTokens)
	if r.Usage.CacheReadTokens > 0 || r.Usage.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "   Cache: r%d/w%d", r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens)
	}
	b.WriteByte('\n')

	if len(r.Usage.Models) > 0 {
		// Sort by spend descending for readability.
		type row struct {
			id string
			d  llm.UsageModelData
		}
		rows := make([]row, 0, len(r.Usage.Models))
		for id, d := range r.Usage.Models {
			rows = append(rows, row{id, d})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].d.Spend > rows[j].d.Spend })

		b.WriteString("\nPer model:\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "  %-50s %4d calls   $%s   ↑%d ↓%d\n",
				shortModel(r.id), r.d.Calls, formatUSD(r.d.Spend),
				r.d.InputTokens, r.d.OutputTokens)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// addContextStatus appends the /context output to the transcript: the
// window-size header (as dim info) followed, when an agent is present, by the
// color-coded usage breakdown as a pre-styled entry so the transcript renderer
// leaves its ANSI colors intact (entryInfo would re-style and flatten them).
// For web viewers it also publishes a structured ContextStats event so the
// browser can draw its own color-coded bar (ANSI can't render there).
func (m *Model) addContextStatus() {
	eff := m.cfg.ContextWindowFor(m.cfg.Model)
	m.trans.add(entry{kind: entryInfo, text: m.contextStatus()})
	if m.agent != nil {
		m.trans.add(entry{kind: entryRaw, text: m.contextBreakdown(eff)})
		if m.hub != nil {
			bd := m.agent.ContextBreakdown()
			m.hub.PublishEvent(agent.ContextStats{Segments: bd.Segments, Total: bd.Total, Window: eff})
		}
	}
}

// contextStatus describes the context-window size used for the ctx% meter:
// either the user's manual override or the active model's default. The
// estimated per-segment breakdown is rendered separately (see contextBreakdown)
// so its color-coded bar can be emitted as a pre-styled entry that the
// transcript won't re-style.
func (m Model) contextStatus() string {
	eff := m.cfg.ContextWindowFor(m.cfg.Model)
	if m.cfg.ContextWindow > 0 {
		return fmt.Sprintf("Context window: %d tokens (manual override; /context auto for %s's default)", eff, shortModel(m.cfg.Model))
	}
	return fmt.Sprintf("Context window: %d tokens (auto — %s's default; /context <n> like 1m to override)", eff, shortModel(m.cfg.Model))
}

// segmentColor maps a context-breakdown segment label to its bar/legend color,
// so the colors stay consistent regardless of which segments are present (and
// match the web UI's CONTEXT_SEGMENT_COLORS). Unknown labels fall back to dim.
func segmentColor(label string) lipgloss.AdaptiveColor {
	switch label {
	case "System":
		return colorPrimary // blue
	case "Memory files":
		return colorUser // green
	case "Built-in tools":
		return colorTool // orange
	case "MCP tools":
		return ad("169", "212") // magenta
	case "Subagents":
		return ad("36", "44") // teal
	case "Skills":
		return ad("178", "221") // gold
	case "Checkpoint":
		return colorAccent // pink
	case "History":
		return colorDim // grey
	default:
		return colorDim
	}
}

// contextBreakdown renders the agent's estimated per-segment context usage as a
// stacked, color-coded bar over the model's window, followed by an aligned
// table (segment · est tokens · % of window). Returns a friendly note when the
// estimate is empty (e.g. a brand-new session).
func (m Model) contextBreakdown(window int) string {
	bd := m.agent.ContextBreakdown()
	if bd.Total == 0 {
		return "Estimated usage: (empty — start a turn to populate the context)"
	}

	const barWidth = 32
	pct := 0
	if window > 0 {
		pct = bd.Total * 100 / window
		if pct > 100 {
			pct = 100 // a runaway estimate can't blow past a full bar
		}
	}

	// Stacked bar: each segment takes a share of the *filled* portion (Total),
	// and the filled portion itself is Total/window of the bar. Remainder is
	// drawn as light shade (free window). With no known window we can't show a
	// fill ratio, so leave the bar empty and rely on the per-segment table.
	filledCells := 0
	if window > 0 {
		filledCells = bd.Total * barWidth / window
		if filledCells > barWidth {
			filledCells = barWidth
		}
		if filledCells == 0 && bd.Total > 0 {
			filledCells = 1 // show at least one cell when any context is used
		}
	}
	var bar strings.Builder
	used := 0
	for i, s := range bd.Segments {
		cells := 0
		if bd.Total > 0 {
			cells = s.Tokens * filledCells / bd.Total
		}
		if i == len(bd.Segments)-1 {
			cells = filledCells - used // give the rounding remainder to the last segment
		}
		if cells < 0 {
			cells = 0 // defensive: floor-division guarantees this, but never panic
		}
		used += cells
		style := lipgloss.NewStyle().Foreground(segmentColor(s.Label))
		bar.WriteString(style.Render(strings.Repeat("█", cells)))
	}
	for i := used; i < barWidth; i++ {
		bar.WriteString(statusDimStyle.Render("░"))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Estimated context usage — ~%s tokens (%d%% of window):\n",
		formatTokenCount(bd.Total), pct)
	b.WriteString("  " + bar.String() + "\n")
	for _, s := range bd.Segments {
		segPct := 0
		if window > 0 {
			segPct = s.Tokens * 100 / window
		}
		swatch := lipgloss.NewStyle().Foreground(segmentColor(s.Label)).Render("■")
		fmt.Fprintf(&b, "  %s %-14s %8s tok  %3d%%\n",
			swatch, s.Label, formatTokenCount(s.Tokens), segPct)
		// Itemize a category's contributors (MCP servers, agents, skills, …) as
		// an indented tree under it, Claude-Code style.
		for i, it := range s.Items {
			branch := "├─"
			if i == len(s.Items)-1 {
				branch = "└─"
			}
			line := fmt.Sprintf("       %s %s: %s tok", branch, it.Label, formatTokenCount(it.Tokens))
			b.WriteString(statusDimStyle.Render(line) + "\n")
		}
	}
	b.WriteString("  (estimates; ~bytes/4. The status-bar ctx% reflects the last turn's measured input.)")
	return b.String()
}

// formatTokenCount renders a token count compactly and monotonically: small
// values verbatim (999), thousands as "1.2k"/"12k", millions as "1.2m". It
// rounds to keep the sequence monotonic so a smaller count never prints larger
// than a bigger one (e.g. 9_999 -> "10k", and 10_000 -> "10k" too).
func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1_000_000)
	case n >= 9_950:
		// At/above the point where one-decimal-k would round to "10.0k", switch
		// to integer-k so it reads "10k" instead — and stays consistent with
		// larger values like 12_345 -> "12k".
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// approvalStatus summarizes the current approval policy and the options.
func (m Model) approvalStatus() string {
	cur := config.NormalizeApprovalPolicy(m.cfg.ApprovalPolicy)
	return fmt.Sprintf("Approval policy: %s\n"+
		"  prompt  confirm every write/edit/bash/task (default)\n"+
		"  safe    auto-run read-only bash (ls, cat, grep, git status, go test, …); still prompt writes/edits/task\n"+
		"  auto    never prompt; auto-run everything except the git commit/push hard gate\n"+
		"  danger-skip-permission  auto-approve everything incl. git commit/push (plan-draft approval still required)\n"+
		"Use /approval <level> to change it (saved).", cur)
}

// sandboxStatus describes the current bash-sandbox setting.
func (m Model) sandboxStatus() string {
	if config.NormalizeSandboxMode(m.cfg.SandboxMode) == config.SandboxOff {
		return "Bash sandbox: off. Use /sandbox on to confine bash writes to the working directory (macOS sandbox-exec, Linux bwrap)."
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return "Bash sandbox: on, but it only takes effect on macOS and Linux (no-op here)."
	}
	net := "network allowed"
	if !m.cfg.SandboxNetwork {
		net = "network denied"
	}
	tool := "sandbox-exec"
	if runtime.GOOS == "linux" {
		tool = "bwrap"
	}
	return "Bash sandbox: on — bash writes confined to the working directory plus build caches via " + tool + " (" + net + "). Use /sandbox off to disable."
}

// relayStatus describes the relay connector: the live state of any running
// connection plus the saved address (which applies on the next start). Used by
// /relay with no argument.
func (m Model) relayStatus() string {
	if m.relayAddr == "" && m.cfg.RelayAddr == "" {
		return "No relay configured. Use /relay <host:port> to auto-connect on startup so a remote browser can browse this machine's projects and sessions (saved)."
	}
	var b strings.Builder
	if m.relayAddr != "" {
		state := m.relayState.label()
		if state == "" {
			state = "off"
		}
		fmt.Fprintf(&b, "Relay %s — %s. ", m.relayAddr, state)
	}
	if m.cfg.RelayAddr != m.relayAddr { // a /relay change is pending a restart
		if m.cfg.RelayAddr == "" {
			b.WriteString("Saved: disabled (applies on next start). ")
		} else {
			fmt.Fprintf(&b, "Saved: %s (applies on next start). ", m.cfg.RelayAddr)
		}
	}
	b.WriteString("Use /relay <host:port> to set (saved), /relay off to disable.")
	return b.String()
}

// reasoningStatus describes a reasoning level change, noting when it has no
// effect on the current route or model.
func (m Model) reasoningStatus(level string) string {
	msg := "Reasoning level set to " + level
	if level == config.ReasoningOff {
		return msg
	}
	if !config.ReasoningActive(m.cfg.LLMProvider, m.cfg.Model, level) {
		// The model's route doesn't transmit extended thinking for this id.
		return msg + " (note: extended thinking is not sent for this model)"
	}
	if m.cfg.Provider == config.ProviderAnthropic && !config.ModelSupportsThinking(m.cfg.Model) {
		msg += fmt.Sprintf(" (note: %s may not support extended thinking)", shortModel(m.cfg.Model))
	}
	return msg
}

// quotaName labels the quota source for the web status-bar tooltip: a
// project name when one is in use, "personal" for a personal quota, or "" before
// the first successful usage fetch.
func quotaName(fetched bool, project string) string {
	if !fetched {
		return ""
	}
	if project != "" {
		return project
	}
	return "personal"
}
