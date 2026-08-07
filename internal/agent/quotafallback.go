package agent

// Quota-based automatic provider fallback.
//
// When a provider's spend crosses a configurable budget — or the provider hands
// back a hard daily-quota 429 — the agent switches to the next backend in
// cfg.ProviderFallbacks so work continues instead of failing for the rest of the
// quota period.
//
// Design notes worth knowing before changing this file:
//
//   - Switching a provider ALWAYS means switching the model too. Model ids are not
//     portable across backends (an `anthropic.claude-…` id means nothing to
//     Ollama), so every switch pairs cfg.LLMProvider with a model valid there:
//     the chain entry's own model if it pins one, else the backend's default.
//
//   - The restore only ever reverts the fallback's OWN change. If the user picks a
//     provider by hand after a hop, that deliberate choice wins and the automatic
//     restore stays out of the way.
//
//   - Everything needed to undo a hop is persisted on the session record, so a
//     restart doesn't strand the session on the fallback backend forever.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/session"
)

// quotaFallbackState remembers what we switched away from, so the original
// provider can be restored once the quota period rolls over.
type quotaFallbackState struct {
	mu sync.Mutex
	// primaryProvider/primaryModel/primaryReasoning are the provider, model and
	// reasoning level in use before the FIRST fallback of this period, so a chain
	// of hops still returns all the way home rather than to the previous hop.
	primaryProvider string
	primaryModel    string
	// primaryReasoning is recorded separately because a hop may pin its own level
	// ("ollama:qwen3-coder:30b:high"); without replaying it, the fallback's level
	// would silently persist on the primary's model after returning home.
	primaryReasoning string
	// fallbackProvider/fallbackModel are what the last hop installed. The restore
	// compares the live selection against these so it only reverts its own change.
	fallbackProvider string
	fallbackModel    string
	// resetAt is when the quota period ends; the restore fires at/after it.
	resetAt time.Time
	// switched is true while running on a fallback provider.
	switched bool
}

// snapshotLocked renders the state as its persisted form. Caller holds st.mu.
func (st *quotaFallbackState) snapshotLocked() *session.QuotaFallback {
	if !st.switched {
		return nil
	}
	return &session.QuotaFallback{
		PrimaryProvider:  st.primaryProvider,
		PrimaryModel:     st.primaryModel,
		PrimaryReasoning: st.primaryReasoning,
		FallbackProvider: st.fallbackProvider,
		FallbackModel:    st.fallbackModel,
		ResetAt:          st.resetAt,
	}
}

// clearLocked resets every recovery field. Caller holds st.mu.
//
// Assigns fields individually rather than `*st = quotaFallbackState{}`: the mutex is
// part of the struct and is HELD by the caller, so overwriting the whole value zeroes
// a locked mutex and the deferred Unlock crashes the process.
func (st *quotaFallbackState) clearLocked() {
	st.primaryProvider, st.primaryModel, st.primaryReasoning = "", "", ""
	st.fallbackProvider, st.fallbackModel = "", ""
	st.resetAt = time.Time{}
	st.switched = false
}

// restoreFromLocked reloads persisted state. Caller holds st.mu.
func (st *quotaFallbackState) restoreFromLocked(rec *session.QuotaFallback) {
	if rec == nil {
		st.clearLocked()
		return
	}
	st.primaryProvider = rec.PrimaryProvider
	st.primaryModel = rec.PrimaryModel
	st.primaryReasoning = rec.PrimaryReasoning
	st.fallbackProvider = rec.FallbackProvider
	st.fallbackModel = rec.FallbackModel
	st.resetAt = rec.ResetAt
	st.switched = rec.PrimaryProvider != "" || rec.FallbackProvider != ""
}

// resetQuotaFallback clears the recovery state, e.g. when the session is reset.
func (a *Agent) resetQuotaFallback() {
	if a == nil {
		return
	}
	a.quotaFB.mu.Lock()
	defer a.quotaFB.mu.Unlock()
	a.quotaFB.clearLocked()
}

// QuotaFallbackSnapshot returns the persisted form of an active fallback, or nil.
func (a *Agent) QuotaFallbackSnapshot() *session.QuotaFallback {
	if a == nil {
		return nil
	}
	a.quotaFB.mu.Lock()
	defer a.quotaFB.mu.Unlock()
	return a.quotaFB.snapshotLocked()
}

// RestoreQuotaFallback reloads a persisted fallback after a session reload.
func (a *Agent) RestoreQuotaFallback(rec *session.QuotaFallback) {
	if a == nil {
		return
	}
	a.quotaFB.mu.Lock()
	defer a.quotaFB.mu.Unlock()
	a.quotaFB.restoreFromLocked(rec)
}

// QuotaSnapshot is one reading of the current provider's spend.
type QuotaSnapshot struct {
	Provider string    // provider the numbers belong to
	Spend    float64   // dollars used in the current period
	Budget   float64   // dollars allowed in the period; 0 = provider reports no cap
	ResetAt  time.Time // end of the period; zero = unknown
	// HardLimit is set when the provider itself refused the request with a
	// daily-quota error, which outranks any budget arithmetic.
	HardLimit bool
}

// quotaBreached reports whether spend has reached either configured threshold.
//
// budgetUSD is absolute dollars; budgetPct is a percentage of the provider's own
// reported cap (providerBudget) and is therefore only checkable when the provider
// reports one. Either trigger alone is enough; 0 disables that trigger.
func quotaBreached(spend, providerBudget, budgetUSD, budgetPct float64) bool {
	if budgetUSD > 0 && spend >= budgetUSD {
		return true
	}
	if budgetPct > 0 && providerBudget > 0 {
		if spend/providerBudget*100 >= budgetPct {
			return true
		}
	}
	return false
}

// nextFallbackProvider returns the first usable provider AFTER current in chain.
//
// The walk starts at current's position, so a chain is traversed one hop at a
// time as successive quotas run out. There is deliberately no wrap-around:
// cycling back to a provider we already exhausted would thrash. When current
// isn't in the chain at all (e.g. the user picked something else by hand), the
// walk starts from the beginning.
func nextFallbackProvider(chain []string, current string, usable func(string) bool) string {
	cur := provider.Normalize(current)
	start := 0
	for i, id := range chain {
		if provider.Normalize(id) == cur {
			start = i + 1
			break
		}
	}
	for _, id := range chain[start:] {
		norm := provider.Normalize(id)
		if norm == cur {
			continue // already there; a no-op switch is not a fallback
		}
		if usable == nil || usable(norm) {
			return norm
		}
	}
	return ""
}

// providerUsable reports whether the agent could actually talk to a provider with
// the current credentials, so the chain never hops to a dead backend.
//
// Delegates to llm.ProviderReachable, which reuses the same credential/base-URL
// resolution the streaming path uses rather than re-listing which provider needs
// which key.
func providerUsable(cfg *config.Config, id string) bool {
	return llm.ProviderReachable(cfg, id)
}

// fallbackTargetFor returns the chain entry for a provider, so a hop can honour a
// pinned model and reasoning level.
func fallbackTargetFor(cfg *config.Config, id string) config.FallbackTarget {
	norm := provider.Normalize(id)
	for _, t := range cfg.FallbackChain() {
		if provider.Normalize(t.Provider) == norm {
			return t
		}
	}
	return config.FallbackTarget{Provider: norm}
}

// modelForHop resolves the model a hop should use: the chain's pinned model when
// it has one, else the backend's default. Never empty for a known provider —
// carrying the previous provider's model id across a switch would send an id the
// new backend rejects.
func modelForHop(t config.FallbackTarget) string {
	if m := strings.TrimSpace(t.Model); m != "" {
		return m
	}
	return provider.DefaultModelFor(t.Provider)
}

// MaybeQuotaFallback applies the fallback policy to a fresh quota reading.
//
// Returns a human-readable notice when it changed the provider (the caller
// surfaces it as an Info event), or "" when nothing happened. Safe to call after
// every turn: it's a cheap no-op unless the feature is enabled and a threshold is
// actually crossed.
func (a *Agent) MaybeQuotaFallback(q QuotaSnapshot) string {
	if a == nil || a.cfg == nil || !a.cfg.QuotaFallback {
		return ""
	}
	// Refuse mid-turn. This is called from the web server's background usage poll,
	// while cfg.LLMProvider/Model are being READ by the turn goroutine on every
	// request — switching underneath it is a data race, and would also send half a
	// turn to one backend and half to another. Skipping is free: the poll repeats,
	// and a quota that is over budget now will still be over budget then.
	//
	// The reactive path (quotaFallbackOnError) deliberately bypasses this, because it
	// runs ON the turn goroutine, which owns cfg.
	if a.inTurn.Load() > 0 {
		return ""
	}
	return a.applyQuotaPolicy(q)
}

// applyQuotaPolicy is MaybeQuotaFallback's core. Callers must own cfg: either no turn
// is running, or the caller IS the turn goroutine.
func (a *Agent) applyQuotaPolicy(q QuotaSnapshot) string {
	st := &a.quotaFB
	st.mu.Lock()
	defer st.mu.Unlock()

	// Period rolled over: undo the fallback so the primary provider is used again
	// with a fresh quota. Checked before the breach test so a stale reading can't
	// immediately bounce the session back onto the fallback.
	if msg := st.restoreIfResetLocked(a.cfg); msg != "" {
		return msg
	}

	if !q.HardLimit && !quotaBreached(q.Spend, q.Budget, a.cfg.QuotaBudgetUSD, a.cfg.QuotaBudgetPct) {
		return ""
	}
	// Only act on numbers describing the provider actually in use: a late reply
	// about a provider we already left must not trigger another hop.
	cur := provider.Normalize(a.cfg.LLMProvider)
	if q.Provider != "" && provider.Normalize(q.Provider) != cur {
		return ""
	}

	chain := config.FallbackChainProviders(a.cfg.FallbackChain())
	next := nextFallbackProvider(chain, cur, func(id string) bool {
		return providerUsable(a.cfg, id)
	})
	if next == "" {
		return "" // chain exhausted; stay put and let the provider's own error surface
	}

	target := fallbackTargetFor(a.cfg, next)
	model := modelForHop(target)

	// Remember the pre-fallback selection once per period, so a second hop still
	// restores all the way back to the primary rather than to the previous hop.
	if !st.switched {
		st.primaryProvider = cur
		st.primaryModel = a.cfg.Model
		st.primaryReasoning = a.cfg.ReasoningLevel
	}
	// Record the reset instant: the provider's own timestamp when it gave one,
	// else the configured daily rollover. Without either, the restore can never
	// fire and the session stays on the fallback.
	st.resetAt = q.ResetAt
	if st.resetAt.IsZero() {
		st.resetAt = NextDailyReset(a.cfg.QuotaResetAt, time.Now())
	}

	a.cfg.SetLLMProvider(next)
	a.cfg.SetModel(model)
	if target.Reasoning != "" {
		a.cfg.SetReasoningFor(model, target.Reasoning)
		a.cfg.ReasoningLevel = target.Reasoning
	}
	st.fallbackProvider = next
	st.fallbackModel = model
	st.switched = true

	why := fmt.Sprintf("$%.2f spent", q.Spend)
	if q.HardLimit {
		why = "provider reported its quota exhausted"
	} else if a.cfg.QuotaBudgetUSD > 0 && q.Spend >= a.cfg.QuotaBudgetUSD {
		why = fmt.Sprintf("$%.2f of the $%.2f budget spent", q.Spend, a.cfg.QuotaBudgetUSD)
	} else if q.Budget > 0 {
		why = fmt.Sprintf("%.0f%% of the provider quota used", q.Spend/q.Budget*100)
	}
	note := fmt.Sprintf("Quota fallback: %s — switched to %s (%s).",
		why, provider.Label(next), shortModelLabel(model))
	if !st.resetAt.IsZero() {
		note += fmt.Sprintf(" Reverting to %s after %s.",
			provider.Label(st.primaryProvider), st.resetAt.Local().Format("Mon 15:04 MST"))
	}
	return note
}

// MaybeQuotaRestore returns the primary provider once the quota period has ended,
// WITHOUT needing a quota reading.
//
// This exists because the restore would otherwise be reachable only via
// MaybeQuotaFallback, which the web server calls from fetchQuota — and fetchQuota
// bails when Usage() errors. Usage() errors on every backend without a quota
// endpoint (Ollama, LM Studio, llama.cpp), which is exactly where the chain lands,
// so the restore must not depend on the very provider we fell back FROM being
// reachable.
//
// Cheap and side-effect free unless a restore is actually due, so it is safe to
// call after every turn. Returns a notice to surface, or "".
func (a *Agent) MaybeQuotaRestore() string {
	if a == nil || a.cfg == nil || !a.cfg.QuotaFallback {
		return ""
	}
	// Same reasoning as MaybeQuotaFallback: this runs on the background poll
	// goroutine, so it must not rewrite cfg under a live turn.
	if a.inTurn.Load() > 0 {
		return ""
	}
	st := &a.quotaFB
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.restoreIfResetLocked(a.cfg)
}

// restoreIfResetLocked puts the primary provider back once the quota period has
// ended. Caller must hold st.mu.
func (st *quotaFallbackState) restoreIfResetLocked(cfg *config.Config) string {
	if !st.switched || st.resetAt.IsZero() || time.Now().Before(st.resetAt) {
		return ""
	}
	from := provider.Normalize(cfg.LLMProvider)
	back, model, reasoning := st.primaryProvider, st.primaryModel, st.primaryReasoning
	installedProv, installedModel := st.fallbackProvider, st.fallbackModel
	// Clear the state either way: the period is over, so this fallback is done
	// whether or not we are the ones who get to change the provider back.
	st.clearLocked()

	if back == "" {
		return ""
	}
	// Only revert our OWN change. If the live selection is no longer what the hop
	// installed, the user has since chosen something deliberately — leave it.
	if provider.Normalize(installedProv) != from ||
		(installedModel != "" && cfg.Model != installedModel) {
		return ""
	}
	if from == provider.Normalize(back) && cfg.Model == model {
		return "" // already home
	}

	cfg.SetLLMProvider(back)
	if model != "" {
		cfg.SetModel(model)
	}
	// Replay the primary's reasoning level: a hop may have pinned its own, which
	// would otherwise stick to the primary's model after returning home.
	if reasoning != "" && model != "" {
		cfg.SetReasoningFor(model, reasoning)
		cfg.ReasoningLevel = reasoning
	}
	return fmt.Sprintf("Quota reset: switched back to %s (%s).",
		provider.Label(cfg.LLMProvider), shortModelLabel(cfg.Model))
}

// QuotaFallbackState is the schema currently in force plus whether this session
// has actually hopped off its primary provider, so a front end can show the
// configured chain and the live state without reaching into agent internals.
type QuotaFallbackState struct {
	// Enabled is false when the feature is off OR the chain is empty, since a
	// chain of zero providers makes the toggle inert (MaybeQuotaFallback no-ops).
	Enabled bool
	// Switched reports that we are currently running on a fallback provider.
	Switched bool
	// Chain carries each hop's provider plus its optional pinned model and
	// reasoning level, in switch order.
	Chain     []config.FallbackTarget
	BudgetUSD float64
	BudgetPct float64
	// PrimaryProvider/FallbackProvider are only meaningful while Switched.
	PrimaryProvider  string
	FallbackProvider string
	// ResetAt is when the automatic restore becomes due; zero when not switched or
	// when no reset instant is known.
	ResetAt time.Time
}

// QuotaFallbackInfo reports the fallback schema and live state for the UI.
func (a *Agent) QuotaFallbackInfo() QuotaFallbackState {
	if a == nil || a.cfg == nil {
		return QuotaFallbackState{}
	}
	chain := a.cfg.FallbackChain()
	out := QuotaFallbackState{
		Enabled:   a.cfg.QuotaFallback && len(chain) > 0,
		Chain:     chain,
		BudgetUSD: a.cfg.QuotaBudgetUSD,
		BudgetPct: a.cfg.QuotaBudgetPct,
	}
	a.quotaFB.mu.Lock()
	defer a.quotaFB.mu.Unlock()
	out.Switched = a.quotaFB.switched
	if a.quotaFB.switched {
		out.PrimaryProvider = a.quotaFB.primaryProvider
		out.FallbackProvider = a.quotaFB.fallbackProvider
		out.ResetAt = a.quotaFB.resetAt
	}
	return out
}

// shortModelLabel trims a model id down for a one-line notice, mirroring the web
// status bar's shortModel (internal/web/status.go) so the same model reads
// identically wherever it's shown: strip the backend prefix, then the vendor
// prefix, then the trailing revision.
func shortModelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "default model"
	}
	if i := strings.LastIndex(model, "/"); i >= 0 && i < len(model)-1 {
		model = model[i+1:] // e.g. "openai/gpt-4o"
	}
	for _, p := range []string{"aws:", "gcp:", "azure:"} {
		if rest, ok := strings.CutPrefix(model, p); ok {
			model = rest
			break
		}
	}
	model = strings.TrimPrefix(model, "anthropic.")
	if i := strings.IndexByte(model, ':'); i >= 0 { // trailing ":1" revision
		model = model[:i]
	}
	return model
}

// backendSwitchedEvent snapshots the live selection for the UI after a hop.
func (a *Agent) backendSwitchedEvent() BackendSwitched {
	return BackendSwitched{
		Provider:  provider.Normalize(a.cfg.LLMProvider),
		Model:     a.cfg.Model,
		Reasoning: a.cfg.ReasoningLevel,
	}
}

// quotaFallbackOnError is the REACTIVE trigger: when a request fails because the
// provider's quota is exhausted (llm.ErrQuotaExhausted), advance the fallback chain
// immediately so the turn can continue on a working backend.
//
// The caller (the turn loop in agent.go) retries the SAME round on the new provider
// when this returns a note: a hard quota stop is detected from the response status
// and body before any token streams, so nothing was emitted yet and the round can
// simply be redone on the next backend. That keeps the user's question answered
// instead of surfacing a quota error and waiting for a manual re-send.
//
// Returns a notice to surface, or "" when the policy is off or has nowhere to go —
// the empty case is what bounds the retry, since an exhausted chain stops moving.
func (a *Agent) quotaFallbackOnError(err error) string {
	if err == nil || a == nil || a.cfg == nil || !a.cfg.QuotaFallback {
		return ""
	}
	if !errors.Is(err, llm.ErrQuotaExhausted) {
		return ""
	}
	// applyQuotaPolicy, not MaybeQuotaFallback: we are ON the turn goroutine, so the
	// mid-turn guard there would reject the very case this exists to handle.
	return a.applyQuotaPolicy(QuotaSnapshot{
		Provider:  a.cfg.LLMProvider,
		HardLimit: true,
	})
}

// ParseQuotaResetTime interprets a provider-reported quota reset timestamp.
//
// Providers send either an RFC3339 instant or epoch seconds; anything else yields
// the zero time, which callers treat as "unknown" and fall back to the configured
// daily rollover.
func ParseQuotaResetTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	// Epoch seconds (or milliseconds, which some providers send).
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
		if n > 1e12 { // milliseconds
			return time.UnixMilli(n)
		}
		return time.Unix(n, 0)
	}
	return time.Time{}
}

// NextDailyReset returns the next occurrence of the "HH:MM" local wall-clock time
// after now, which is the fallback reset instant for providers that don't report
// one. An empty or malformed hhmm yields the zero time, disabling the time-based
// restore.
func NextDailyReset(hhmm string, now time.Time) time.Time {
	hhmm = strings.TrimSpace(hhmm)
	if hhmm == "" {
		return time.Time{}
	}
	var t time.Time
	var err error
	for _, layout := range []string{"15:04", "3:04PM", "3:04 PM", "15:04:05"} {
		if t, err = time.Parse(layout, strings.ToUpper(hhmm)); err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// ForceQuotaResetAtForTest overrides the recorded reset instant. Test-only helper
// so tests in other packages can simulate the rollover having passed.
func (a *Agent) ForceQuotaResetAtForTest(at time.Time) {
	a.quotaFB.mu.Lock()
	defer a.quotaFB.mu.Unlock()
	a.quotaFB.resetAt = at
}
