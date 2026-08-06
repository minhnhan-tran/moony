package llm

// Per-provider quota adapters.
//
// Most providers expose nothing a client can read: quota lives in a dashboard, or behind
// an admin/org credential the inference key doesn't carry (OpenAI's
// /v1/organization/costs and Anthropic's usage_report both reject a normal key). For
// those, Usage() reports "not available" and the UI falls back to a local estimate.
//
// A handful DO answer with the same key used for inference. Each speaks its own shape,
// so every adapter below normalizes onto the one UsageResponse the TUI and web status
// already render — no new display code, no per-provider branches in the UI.
//
// Two kinds of answer, and the difference matters:
//
//   - SPEND-vs-LIMIT (OpenRouter): both numbers, so a percentage is meaningful and
//     MN_QUOTA_BUDGET_PCT works.
//   - BALANCE-ONLY (DeepSeek, Moonshot, xAI, Z.AI, DeepInfra, Venice): remaining credit
//     with no cap. We record spend and leave Budget.Spend at 0, which the UI reads as
//     "no cap known" and renders as an absolute figure rather than inventing a
//     denominator. Faking a percentage here would be worse than showing none.
//
// Field names are parsed leniently (several accepted spellings per value) because these
// endpoints are mostly undocumented or lightly documented, and a provider renaming a key
// should degrade to "no reading" rather than a wrong number.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/provider"
)

// quotaAdapter fetches and normalizes one provider's quota endpoint.
type quotaAdapter struct {
	// path is appended to the provider's base URL (which already ends in /v1 or
	// equivalent), unless absolute is set.
	path string
	// absolute overrides path entirely when the endpoint does not live under the
	// inference base URL.
	absolute string
	// parse maps the raw body onto a UsageResponse.
	parse func([]byte) (*UsageResponse, error)
}

// quotaAdapters lists every provider whose quota is readable with the inference key.
//
// Absent providers are a deliberate statement, not an oversight: probing found either no
// endpoint at all (Groq, Mistral, Together, Fireworks, NVIDIA, Hugging Face, Cerebras,
// Perplexity, Gemini), an endpoint needing a different credential (OpenAI, Anthropic,
// Azure, Vertex, Bedrock), or no billing concept (Ollama, LM Studio, llama.cpp).
func quotaAdapters() map[string]quotaAdapter {
	return map[string]quotaAdapter{
		provider.OpenRouter: {path: "/auth/key", parse: parseOpenRouterUsage},
		provider.DeepSeek:   {path: "/user/balance", parse: parseDeepSeekUsage},
		provider.Moonshot:   {path: "/users/me/balance", parse: parseMoonshotUsage},
		provider.XAI:        {path: "/api-key", parse: parseXAIUsage},
		provider.ZAI:        {path: "/account/balance", parse: parseGenericBalance},
		provider.DeepInfra:  {path: "/me", parse: parseDeepInfraUsage},
		provider.Venice:     {path: "/api_keys/rate_limits", parse: parseVeniceUsage},
	}
}

// ProviderQuotaSupported reports whether a provider can serve a quota reading with the
// inference key. Lets callers explain *why* there is no number instead of just omitting
// it.
func ProviderQuotaSupported(id string) bool {
	norm := provider.Normalize(strings.TrimSpace(id))
	if norm == provider.SQEAnthropic {
		return true // handled by the dedicated gateway path in usage.go
	}
	if _, ok := provider.Lookup(strings.TrimSpace(id)); !ok {
		return false // unknown id: Normalize would resolve it to the default provider
	}
	_, ok := quotaAdapters()[norm]
	return ok
}

// ProvidersWithQuota lists the provider ids that can report quota, for docs and the
// Doctor panel.
func ProvidersWithQuota() []string {
	out := []string{provider.SQEAnthropic}
	for id := range quotaAdapters() {
		out = append(out, id)
	}
	return out
}

// providerUsage fetches quota for any provider with an adapter. Returns (nil, nil) when
// the provider has no adapter, so the caller can distinguish "unsupported" from "failed".
func (c *Client) providerUsage(ctx context.Context) (*UsageResponse, error) {
	if c == nil || c.cfg == nil {
		return nil, fmt.Errorf("no configuration")
	}
	id := provider.Normalize(c.cfg.LLMProvider)
	ad, ok := quotaAdapters()[id]
	if !ok {
		return nil, nil
	}
	rb := c.remoteBackendFor(id)
	if strings.TrimSpace(rb.baseURL) == "" {
		return nil, fmt.Errorf("no base URL configured for %s", id)
	}
	if rb.apiKey == "" && !rb.noKeyOK {
		return nil, fmt.Errorf("quota lookup needs an API key — set %s", rb.keyEnv)
	}

	url := ad.absolute
	if url == "" {
		url = strings.TrimSuffix(rb.baseURL, "/") + ad.path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Same auth the inference calls use, so a working key needs no extra setup.
	if rb.keyHeader != "" {
		req.Header.Set(rb.keyHeader, rb.apiKey)
	} else if rb.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+rb.apiKey)
	}
	for k, v := range rb.extra {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Cap the read: a proxy or captive portal can answer with an unbounded HTML page,
	// and this runs on a background poll where a stall is invisible.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quota endpoint returned %s: %s",
			resp.Status, truncateForError(strings.TrimSpace(string(body))))
	}
	out, err := ad.parse(body)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("quota endpoint returned no usable figures")
	}
	return out, nil
}

func truncateForError(s string) string {
	s = redactSecrets(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// ─── parsers ────────────────────────────────────────────────────────────────
//
// Each takes the raw body and returns a UsageResponse, or an error when the payload
// carries no figure we can use. Returning an error (rather than a zero reading) matters:
// a zero would render as "$0.00 spent", which looks like a real answer.

// numberish pulls a float from any of the given keys, accepting both JSON numbers and
// numeric strings — several of these APIs quote their amounts ("total_balance": "4.66").
func numberish(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			return t, true
		case json.Number:
			if f, err := t.Float64(); err == nil {
				return f, true
			}
		case string:
			var n json.Number = json.Number(strings.TrimSpace(strings.TrimPrefix(t, "$")))
			if f, err := n.Float64(); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

// nested returns m[key] as a map, for payloads that wrap their figures one level down.
func nested(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if sub, ok := m[k].(map[string]any); ok {
			return sub
		}
	}
	return nil
}

func decodeMap(body []byte) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode quota response: %w", err)
	}
	return m, nil
}

// balanceReading builds a reading for a provider that reports REMAINING credit.
//
// Goes in Balance, not Usage.Spend: they mean opposite things, and a remaining balance
// shown as "spent" tells the user the inverse of the truth. BalanceSet is what lets the
// UI distinguish "$0.00 left" — real, and urgent — from "nothing reported".
func balanceReading(remaining float64) *UsageResponse {
	return &UsageResponse{Balance: remaining, BalanceSet: true}
}

// spendOnly builds a spend-only reading: amount spent recorded, no cap.
//
// Budget.Spend stays 0 on purpose — the UI treats 0 as "no cap known" and shows an
// absolute figure instead of a percentage against a number we'd have invented.
func spendOnly(spend float64) *UsageResponse {
	out := &UsageResponse{}
	out.Usage.Spend = spend
	return out
}

// parseOpenRouterUsage reads GET /api/v1/auth/key.
//
// The only provider probed that returns BOTH spend and limit, so it is also the only one
// where a percentage (and MN_QUOTA_BUDGET_PCT) is meaningful. A null `limit` means an
// unlimited key: we keep the spend and leave the cap unset rather than treating null as
// zero, which would read as "0% of $0".
func parseOpenRouterUsage(body []byte) (*UsageResponse, error) {
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	// Figures live under "data" on the documented response; accept a flat body too.
	d := nested(m, "data")
	if d == nil {
		d = m
	}
	spend, ok := numberish(d, "usage")
	if !ok {
		return nil, fmt.Errorf("no usage figure in OpenRouter response")
	}
	out := spendOnly(spend)
	if limit, ok := numberish(d, "limit"); ok && limit > 0 {
		out.Quota.Budget.Spend = limit
	} else if rem, ok := numberish(d, "limit_remaining"); ok && rem > 0 {
		// Derive the cap when only the remainder is given.
		out.Quota.Budget.Spend = spend + rem
	}
	return out, nil
}

// parseDeepSeekUsage reads GET /user/balance.
//
// Returns a list of per-currency balances; take the USD entry, falling back to the first
// one so a CNY-only account still shows something.
func parseDeepSeekUsage(body []byte) (*UsageResponse, error) {
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	infos, _ := m["balance_infos"].([]any)
	var chosen map[string]any
	for _, it := range infos {
		bi, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if chosen == nil {
			chosen = bi
		}
		if cur, _ := bi["currency"].(string); strings.EqualFold(cur, "USD") {
			chosen = bi
			break
		}
	}
	if chosen == nil {
		// Some deployments answer flat.
		if bal, ok := numberish(m, "total_balance", "balance"); ok {
			return balanceReading(bal), nil
		}
		return nil, fmt.Errorf("no balance in DeepSeek response")
	}
	bal, ok := numberish(chosen, "total_balance", "topped_up_balance", "granted_balance")
	if !ok {
		return nil, fmt.Errorf("no balance figure in DeepSeek response")
	}
	return balanceReading(bal), nil
}

// parseMoonshotUsage reads GET /v1/users/me/balance.
func parseMoonshotUsage(body []byte) (*UsageResponse, error) {
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	d := nested(m, "data")
	if d == nil {
		d = m
	}
	bal, ok := numberish(d, "available_balance", "cash_balance", "voucher_balance", "balance")
	if !ok {
		return nil, fmt.Errorf("no balance in Moonshot response")
	}
	return balanceReading(bal), nil
}

// parseXAIUsage reads GET /v1/api-key.
//
// This endpoint describes the KEY (name, permissions, blocked flags) rather than a
// balance, so there is usually no money figure at all. When there isn't one we still
// surface the key's disabled state, because "your key is blocked" is exactly what a user
// staring at a missing quota needs to know.
func parseXAIUsage(body []byte) (*UsageResponse, error) {
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	if bal, ok := numberish(m, "balance", "credits", "remaining_credits"); ok {
		return balanceReading(bal), nil
	}
	blocked := boolish(m, "api_key_blocked", "blocked") || boolish(m, "team_blocked")
	disabled := boolish(m, "api_key_disabled", "disabled")
	if blocked || disabled {
		return nil, fmt.Errorf("xAI reports this API key is %s",
			map[bool]string{true: "blocked", false: "disabled"}[blocked])
	}
	// Key is valid but xAI exposes no spend figure — say so rather than showing $0.
	return nil, fmt.Errorf("xAI exposes no spend or balance figure for this key")
}

// parseDeepInfraUsage reads GET /v1/me.
func parseDeepInfraUsage(body []byte) (*UsageResponse, error) {
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	// Documented shapes nest the money under "billing" or "quota".
	for _, sub := range []map[string]any{nested(m, "billing", "quota"), m} {
		if sub == nil {
			continue
		}
		if bal, ok := numberish(sub, "balance", "credits", "remaining", "available_credits"); ok {
			out := balanceReading(bal)
			if cap, ok := numberish(sub, "limit", "max_spend", "quota"); ok && cap > 0 {
				out.Quota.Budget.Spend = cap
			}
			return out, nil
		}
		if spent, ok := numberish(sub, "spend", "usage", "total_spend"); ok {
			out := spendOnly(spent)
			if cap, ok := numberish(sub, "limit", "max_spend", "quota"); ok && cap > 0 {
				out.Quota.Budget.Spend = cap
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("no balance or spend figure in DeepInfra response")
}

// parseVeniceUsage reads GET /api/v1/api_keys/rate_limits.
//
// Venice denominates in its own units (VCU/DIEM) plus USD; prefer USD so the figure is
// comparable to every other provider and to the local cost estimate.
func parseVeniceUsage(body []byte) (*UsageResponse, error) {
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	d := nested(m, "data")
	if d == nil {
		d = m
	}
	if bal, ok := numberish(d, "accessPermitted_usd", "usd", "balance_usd"); ok {
		return balanceReading(bal), nil
	}
	if bals := nested(d, "balances"); bals != nil {
		if bal, ok := numberish(bals, "USD", "usd"); ok {
			return balanceReading(bal), nil
		}
		if bal, ok := numberish(bals, "VCU", "vcu", "DIEM", "diem"); ok {
			return balanceReading(bal), nil
		}
	}
	if bal, ok := numberish(d, "balance", "credits"); ok {
		return balanceReading(bal), nil
	}
	return nil, fmt.Errorf("no balance figure in Venice response")
}

// parseGenericBalance handles the plain {"balance": N} / {"data":{"balance": N}} shape
// (Z.AI and anything else that follows it).
func parseGenericBalance(body []byte) (*UsageResponse, error) {
	m, err := decodeMap(body)
	if err != nil {
		return nil, err
	}
	for _, sub := range []map[string]any{nested(m, "data", "result"), m} {
		if sub == nil {
			continue
		}
		if bal, ok := numberish(sub, "balance", "available_balance", "total_balance", "credits", "remaining"); ok {
			out := balanceReading(bal)
			if cap, ok := numberish(sub, "limit", "total", "quota"); ok && cap > 0 {
				out.Quota.Budget.Spend = cap
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("no balance figure in response")
}

func boolish(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if b, ok := m[k].(bool); ok && b {
			return true
		}
	}
	return false
}
