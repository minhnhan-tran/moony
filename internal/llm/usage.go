package llm

// Usage/quota data shapes. Historically these were populated from a hosted
// gateway's usage API. Ollama is a local server with no account, quota, rate
// limit, or billing, so Usage() now reports "not available" and these structs
// remain only so the TUI/web status surfaces that consume them keep compiling
// (they simply render nothing when quota is unknown).

import (
	"context"
	"encoding/json"
	"fmt"
)

// UsageResponse is the parsed shape of the /usage endpoints. Only the fields
// we display or use to drive pricing are included; everything else is dropped
// silently so a future server-side schema addition won't break us.
type UsageResponse struct {
	// Identity (one of these is set depending on which endpoint is hit).
	DSID        json.Number `json:"dsid,omitempty"`
	ProjectID   string      `json:"project_id,omitempty"`
	ProjectName string      `json:"project_name,omitempty"`

	Quota              UsageQuota `json:"quota"`
	Usage              UsageData  `json:"usage"`
	LifetimeBatchSpend float64    `json:"lifetime_batch_spend,omitempty"`
}

// UsageQuota mirrors the `quota` key.
type UsageQuota struct {
	Budget UsageBudget                `json:"budget"`
	Rates  UsageRates                 `json:"rates"`
	Models map[string]UsageModelQuota `json:"models"`
}

// UsageBudget describes the spend budget for the current period.
type UsageBudget struct {
	Spend        float64 `json:"spend"`
	ResetTime    string  `json:"reset_time"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

// UsageRates is the rate-limit pair in force for the current period.
type UsageRates struct {
	Calls        int    `json:"calls"`
	CallsPeriod  string `json:"calls_period"`
	Tokens       int    `json:"tokens"`
	TokensPeriod string `json:"tokens_period"`
}

// UsageModelQuota is the per-model entry under quota.models.
type UsageModelQuota struct {
	Budget  UsageBudget  `json:"budget"`
	Rates   UsageRates   `json:"rates"`
	Pricing UsagePricing `json:"pricing"`
}

// UsagePricing is per-token pricing as returned by the provider (USD per token).
type UsagePricing struct {
	InputTokenCost      float64 `json:"input_token_cost"`
	OutputTokenCost     float64 `json:"output_token_cost"`
	CacheWriteTokenCost float64 `json:"cache_write_token_cost"`
	CacheReadTokenCost  float64 `json:"cache_read_token_cost"`
	InputImageCost      float64 `json:"input_image_cost,omitempty"`
	OutputImageCost     float64 `json:"output_image_cost,omitempty"`
}

// UsageData mirrors the `usage` key (totals + per-model breakdown).
type UsageData struct {
	Calls            int                       `json:"calls"`
	Spend            float64                   `json:"spend"`
	InputTokens      int                       `json:"input_tokens"`
	OutputTokens     int                       `json:"output_tokens"`
	CacheWriteTokens int                       `json:"cache_write_tokens"`
	CacheReadTokens  int                       `json:"cache_read_tokens"`
	Models           map[string]UsageModelData `json:"models"`
}

// UsageModelData is the per-model entry under usage.models.
type UsageModelData struct {
	Calls            int          `json:"calls"`
	Spend            float64      `json:"spend"`
	InputTokens      int          `json:"input_tokens"`
	OutputTokens     int          `json:"output_tokens"`
	CacheWriteTokens int          `json:"cache_write_tokens"`
	CacheReadTokens  int          `json:"cache_read_tokens"`
	Pricing          UsagePricing `json:"pricing"`
}

// IsProject reports whether this response describes project-scoped usage.
func (r *UsageResponse) IsProject() bool { return r.ProjectID != "" }

// Usage reports current quota/spend. Ollama has no account or quota, so this
// always returns an error; callers treat that as "quota unknown" and hide the
// quota display.
func (c *Client) Usage(ctx context.Context) (*UsageResponse, error) {
	return nil, fmt.Errorf("usage/quota stats are not available for the ollama provider")
}
