package web

import (
	"os"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
	sessionpkg "github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// statusEvent builds the header + status-bar snapshot for a session: the static
// config (app version, model, route, reasoning, working dir, approval mode,
// sandbox) plus the project's current git branch/dirty count, and — when a live
// session is supplied — its accumulated token usage, estimated cost (or the
// authoritative usage quota when known), and context-window fill. It's
// seeded into a session's history at open time and re-emitted after each turn,
// so the browser header and status bar stay populated and current. Without it
// the standalone server sent no "status" event and both rendered empty.
func (s *Server) statusEvent(sess *sessionpkg.Session, live *session) wireEvent {
	// Prefer the live session's own agent config so per-session changes (/model,
	// /reasoning, /approval, /sandbox, /plan) are reflected; fall back to the
	// server template before the agent exists.
	cfg := s.cfg
	if live != nil && live.agent != nil {
		cfg = live.agent.Config()
	}
	model := cfg.Model
	if model == "" && sess != nil {
		model = sess.Model
	}
	workingDir := cfg.WorkingDir
	if workingDir == "" && sess != nil {
		workingDir = sess.WorkingDir
	}
	reasoning := ""
	if cfg.ReasoningLevel != "" && cfg.ReasoningLevel != config.ReasoningOff {
		reasoning = cfg.ReasoningLevel
	}
	approval := config.NormalizeApprovalPolicy(cfg.ApprovalPolicy)
	if live != nil && live.agent != nil && live.agent.PlanMode() {
		approval = "plan" // plan mode overrides the policy badge, matching the TUI
	}
	branch, dirty, _ := tools.GitInfo(workingDir)
	ahead, behind, upstream, _ := tools.GitAheadBehind(workingDir)
	st := &statusWire{
		Version:        config.Version,
		Model:          shortModel(model),
		Provider:       config.ProviderLabel(cfg.Provider),
		LLMProvider:    provider.Normalize(cfg.LLMProvider),
		Reasoning:      reasoning,
		WorkingDir:     prettyDir(workingDir),
		Approval:       approval,
		Sandbox:        config.SandboxLabel(cfg.SandboxMode),
		GitBranch:      branch,
		GitDirty:       dirty,
		GitAhead:       ahead,
		GitBehind:      behind,
		GitUpstream:    upstream,
		Host:           config.MachineName(),
		ContextPercent: -1, // unknown until a turn completes
	}
	// The active primary profile, so the browser can show it and offer a switcher.
	if live != nil && live.agent != nil {
		st.Agent = live.agent.ActiveAgent().Name
		st.MemoryCount = live.agent.MemoryBulletCount() // AGENTS.md bullet count for the composer pill
	}
	// The reasoning levels valid for THIS model (Gemini/Opus/others differ), so the
	// web picker offers the right set rather than a hardcoded one.
	for _, lvl := range config.ReasoningLevelsFor(model) {
		st.ReasoningLevels = append(st.ReasoningLevels, levelWire{ID: lvl, Desc: config.ReasoningDescription(lvl)})
	}
	if live != nil {
		in, out, lastIn, spend, budget, quotaKnown := live.usageSnapshot()
		cacheRead, cacheWrite := live.cacheSnapshot()
		st.TokensIn = in
		st.TokensOut = out
		st.CostUSD = config.CostUSDFull(model, in, out, cacheRead, cacheWrite) // cache-adjusted session estimate; quota (below) takes display priority
		st.ContextPercent = contextPercent(lastIn, cfg.ContextWindowFor(model))
		st.ContextTokens = lastIn
		st.ContextWindow = cfg.ContextWindowFor(model)
		st.QuotaSpend = spend
		st.QuotaBudget = budget
		st.QuotaKnown = quotaKnown
		st.QuotaName = live.quotaNameSnapshot()
		st.QuotaTokens, st.QuotaTokensMax, st.QuotaTokenRate, st.QuotaTokenRatePeriod = live.quotaTokenSnapshot()
	}
	return wireEvent{Type: "status", Status: st}
}

// contextPercent reports how full the model's context window is, given the most
// recent turn's input-token size. It returns -1 when unknown.
func contextPercent(used, window int) int {
	if used <= 0 || window <= 0 {
		return -1
	}
	pct := used * 100 / window
	if pct > 100 {
		pct = 100
	}
	return pct
}

// shortModel trims provider prefixes/suffixes for a compact header label,
// matching the terminal UI (e.g. "aws:anthropic.claude-x:1" -> "claude-x").
func shortModel(model string) string {
	if rest, ok := strings.CutPrefix(model, "aws:"); ok {
		model = rest
	} else if rest, ok := strings.CutPrefix(model, "gcp:"); ok {
		model = rest
	}
	model = strings.TrimPrefix(model, "anthropic.")
	if i := strings.IndexByte(model, ':'); i >= 0 {
		model = model[:i]
	}
	return model
}

// prettyDir abbreviates the user's home directory to ~ for display.
func prettyDir(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(os.PathSeparator)) {
			return "~" + p[len(home):]
		}
	}
	return p
}
