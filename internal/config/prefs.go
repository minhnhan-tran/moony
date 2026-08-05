package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// savedPrefs is the persisted subset of user preferences, stored as JSON at
// ~/.config/moony/config.json.
//
// ReasoningLevel is the legacy single-value field kept for backward
// compatibility; ReasoningByModel maps a model id to its remembered level so
// e.g. an Opus model can stay on "high" while a Gemini model stays on
// "medium". On load, the active model's entry in ReasoningByModel takes
// precedence over the legacy ReasoningLevel. (The endpoint is no longer
// persisted — it is derived from the saved Model on load.)
type savedPrefs struct {
	Model            string            `json:"model,omitempty"`
	LLMProvider      string            `json:"llm_provider,omitempty"`
	ReasoningLevel   string            `json:"reasoning_level,omitempty"`
	ReasoningByModel map[string]string `json:"reasoning_by_model,omitempty"`
	ApprovalPolicy   string            `json:"approval_policy,omitempty"`
	SandboxMode      string            `json:"sandbox_mode,omitempty"`
	// RelayAddr is a pointer for the same nil-vs-set distinction as the persona
	// overrides below: nil (never configured) keeps the built-in default
	// (defaultRelayAddr, on); a non-nil value is an explicit user choice — an
	// address to connect to, or "" to disable (set via /relay off). Without the
	// pointer, "" (disabled) and "unset" (use default) would be indistinguishable
	// now that the default is non-empty.
	RelayAddr        *string `json:"relay_addr,omitempty"`
	ContextWindow    int     `json:"context_window,omitempty"`
	VerifyCommand    string  `json:"verify_command,omitempty"`
	VerifyMaxRetries int     `json:"verify_max_retries,omitempty"`
	// Persona-explicit overrides: pointer types so an explicit false/empty is
	// distinguishable from "never set" (nil). Applied AFTER env vars in Load()
	// so a user's persona choice survives deployment env changes across restarts.
	// AutoPlan and SelfReview used to be plain bools set BEFORE env vars were
	// read, so e.g. a deployment's MN_SELF_REVIEW=0 silently clobbered a saved
	// "on" choice on every restart; they're pointers now for the same
	// nil-vs-false distinction as CheapReads/FastModel/AuxModel below.
	AutoPlan      *bool   `json:"auto_plan,omitempty"`
	SelfReview    *bool   `json:"self_review,omitempty"`
	CheapReads    *bool   `json:"cheap_reads,omitempty"`
	AdaptiveModel *bool   `json:"adaptive_model,omitempty"`
	FastModel     *string `json:"fast_model,omitempty"`
	AuxModel      *string `json:"aux_model,omitempty"`
	// Per-tier provider + reasoning for the Fast and Self-review (aux) models.
	// Empty/nil = inherit (see config.Fast/AuxProvider, Fast/AuxReasoning).
	FastProvider  *string `json:"fast_provider,omitempty"`
	FastReasoning *string `json:"fast_reasoning,omitempty"`
	AuxProvider   *string `json:"aux_provider,omitempty"`
	AuxReasoning  *string `json:"aux_reasoning,omitempty"`
	// Quota-based provider fallback. Budgets are pointers so an explicit 0
	// ("disable this trigger") is distinguishable from "never set", and the chain
	// is a *[]string so an explicitly emptied chain is distinguishable from nil.
	// ProviderFallbacks holds full "provider[:model[:reasoning]]" specs.
	QuotaFallback     *bool     `json:"quota_fallback,omitempty"`
	QuotaResetAt      *string   `json:"quota_reset_at,omitempty"`
	QuotaBudgetUSD    *float64  `json:"quota_budget_usd,omitempty"`
	QuotaBudgetPct    *float64  `json:"quota_budget_pct,omitempty"`
	ProviderFallbacks *[]string `json:"provider_fallbacks,omitempty"`
}

// prefsPath returns the path to the persisted preferences file.
func prefsPath() (string, error) {
	return GlobalConfigPath("config.json")
}

// loadPrefs reads saved preferences, returning zero values on any error so the
// caller can fall back to defaults.
func loadPrefs() savedPrefs {
	path, err := prefsPath()
	if err != nil {
		return savedPrefs{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return savedPrefs{}
	}
	var sp savedPrefs
	_ = json.Unmarshal(data, &sp)
	return sp
}

// Save persists the user-tunable preferences (model, reasoning memory, and
// approval policy) to disk so they apply on the next launch. It writes
// atomically via a temp file. The endpoint is not stored — it is derived from
// the model on load.
func (c *Config) Save() error {
	path, err := prefsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(savedPrefs{
		Model:             c.Model,
		LLMProvider:       c.LLMProvider,
		ReasoningLevel:    c.ReasoningLevel,
		ReasoningByModel:  c.ReasoningByModel,
		ApprovalPolicy:    c.ApprovalPolicy,
		SandboxMode:       c.SandboxMode,
		RelayAddr:         &c.RelayAddr, // non-nil: persists the explicit choice (addr, or "" for off)
		ContextWindow:     c.ContextWindow,
		VerifyCommand:     c.VerifyCommand,
		VerifyMaxRetries:  c.VerifyMaxRetries,
		AutoPlan:          c.AutoPlanPersona,
		SelfReview:        c.SelfReviewPersona,
		CheapReads:        c.CheapReadsPersona,
		AdaptiveModel:     c.AdaptiveModelPersona,
		FastModel:         c.FastModelPersona,
		AuxModel:          c.AuxModelPersona,
		FastProvider:      c.FastProviderPersona,
		FastReasoning:     c.FastReasoningPersona,
		AuxProvider:       c.AuxProviderPersona,
		AuxReasoning:      c.AuxReasoningPersona,
		QuotaFallback:     c.QuotaFallbackPersona,
		QuotaResetAt:      c.QuotaResetAtPersona,
		QuotaBudgetUSD:    c.QuotaBudgetUSDPersona,
		QuotaBudgetPct:    c.QuotaBudgetPctPersona,
		ProviderFallbacks: c.ProviderFallbacksPersona,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
