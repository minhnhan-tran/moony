package llm

// Usage/quota data shapes, populated from the SQE Anthropic gateway's usage API.
// Only that provider exposes this schema; other providers (Ollama, OpenAI, native
// Anthropic, …) have no compatible per-key quota endpoint, so Usage() reports
// "not available" for them and these structs simply render nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/provider"
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

	// Balance is prepaid credit REMAINING, for providers that report that instead of
	// an amount spent (DeepSeek, Moonshot, Z.AI, DeepInfra, Venice — see
	// quota_providers.go). Kept separate from Usage.Spend on purpose: the two mean
	// opposite things, and rendering a remaining balance as "spent" would tell the user
	// the inverse of the truth.
	//
	// BalanceSet distinguishes "$0.00 left" (a real and urgent reading) from "no
	// balance reported", which a bare float cannot express.
	Balance    float64 `json:"-"`
	BalanceSet bool    `json:"-"`
}

// HasBalance reports whether this reading carries a remaining-credit figure.
func (u *UsageResponse) HasBalance() bool { return u != nil && u.BalanceSet }

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

// Usage fetches a quota/usage snapshot for the active provider.
//
// Routing:
//   - sqe-anthropic → the gateway's /api/usage/v1/personal endpoint (below), which
//     returns the full spend + budget + per-model rate schema.
//   - any provider with an adapter in quota_providers.go → that provider's own endpoint,
//     normalized onto the same UsageResponse.
//   - everything else → an error naming the provider, so the caller can degrade to a
//     local cost estimate and TELL the user why there is no real reading. Most providers
//     land here: they either have no such endpoint, or gate it behind an admin/org
//     credential the inference key doesn't carry.
func (c *Client) Usage(ctx context.Context) (*UsageResponse, error) {
	if c == nil || c.cfg == nil {
		return nil, fmt.Errorf("usage/quota stats need a configured client")
	}
	if provider.Normalize(c.cfg.LLMProvider) != provider.SQEAnthropic {
		// providerUsage returns (nil, nil) when the provider has no adapter, keeping
		// "unsupported" distinguishable from "the request failed".
		out, err := c.providerUsage(ctx)
		if err != nil {
			return nil, err
		}
		if out != nil {
			return out, nil
		}
		return nil, fmt.Errorf("provider %q exposes no usage/quota endpoint for an inference key", c.cfg.LLMProvider)
	}
	key := strings.TrimSpace(c.anthropicKey())
	if key == "" {
		return nil, fmt.Errorf("SQE Anthropic API key not set (MN_SQE_ANTHROPIC_API_KEY)")
	}
	url := strings.TrimRight(c.anthropicBaseURL(), "/") + "/api/usage/v1/personal"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("usage endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out UsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode usage response: %w", err)
	}
	return &out, nil
}
