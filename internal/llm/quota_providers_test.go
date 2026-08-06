package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// Parser tests over the documented response shapes.
//
// These endpoints are mostly undocumented or lightly documented, and no live keys are
// available here, so the parsers accept several spellings per field. That leniency is the
// risk: it must never turn an unrecognised payload into a plausible-looking number. Every
// case below therefore checks BOTH that a real shape parses AND that a junk shape errors
// rather than yielding zero.

func TestParseOpenRouterUsage(t *testing.T) {
	t.Run("spend and limit", func(t *testing.T) {
		got, err := parseOpenRouterUsage([]byte(`{"data":{"label":"sk-or-…","usage":12.5,"limit":100,"is_free_tier":false}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Usage.Spend != 12.5 {
			t.Errorf("spend = %v, want 12.5", got.Usage.Spend)
		}
		// OpenRouter is the only provider that gives both numbers, so it is the only one
		// where a percentage (and MN_QUOTA_BUDGET_PCT) is meaningful.
		if got.Quota.Budget.Spend != 100 {
			t.Errorf("budget = %v, want 100", got.Quota.Budget.Spend)
		}
		if got.HasBalance() {
			t.Error("OpenRouter reports spend, not a prepaid balance")
		}
	})

	t.Run("null limit means unlimited, not zero", func(t *testing.T) {
		// Treating null as 0 would render "0% of $0.00" — worse than showing no cap.
		got, err := parseOpenRouterUsage([]byte(`{"data":{"usage":3.25,"limit":null}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Usage.Spend != 3.25 {
			t.Errorf("spend = %v, want 3.25", got.Usage.Spend)
		}
		if got.Quota.Budget.Spend != 0 {
			t.Errorf("budget = %v, want 0 (no cap known)", got.Quota.Budget.Spend)
		}
	})

	t.Run("derives the cap from limit_remaining", func(t *testing.T) {
		got, err := parseOpenRouterUsage([]byte(`{"data":{"usage":10,"limit_remaining":40}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Quota.Budget.Spend != 50 {
			t.Errorf("budget = %v, want 50 (10 spent + 40 left)", got.Quota.Budget.Spend)
		}
	})

	t.Run("flat body without the data wrapper", func(t *testing.T) {
		got, err := parseOpenRouterUsage([]byte(`{"usage":1.5,"limit":10}`))
		if err != nil || got.Usage.Spend != 1.5 {
			t.Fatalf("got %+v, err %v", got, err)
		}
	})

	t.Run("no usage figure is an error, not zero", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"data":{}}`, `{"data":{"label":"x"}}`, `not json`} {
			if _, err := parseOpenRouterUsage([]byte(body)); err == nil {
				t.Errorf("%s: expected an error rather than a $0.00 reading", body)
			}
		}
	})
}

func TestParseDeepSeekUsage(t *testing.T) {
	// DeepSeek quotes its amounts as STRINGS, which a plain float64 decode would drop.
	const real = `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"4.66","granted_balance":"0.00","topped_up_balance":"4.66"}]}`

	t.Run("string amounts and USD selection", func(t *testing.T) {
		got, err := parseDeepSeekUsage([]byte(real))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !got.HasBalance() {
			t.Fatal("expected a balance reading")
		}
		if got.Balance != 4.66 {
			t.Errorf("balance = %v, want 4.66", got.Balance)
		}
		// Must NOT land in Usage.Spend: remaining credit shown as spend is the inverse
		// of the truth.
		if got.Usage.Spend != 0 {
			t.Errorf("Usage.Spend = %v, want 0 — a balance is not spend", got.Usage.Spend)
		}
	})

	t.Run("prefers USD over other currencies", func(t *testing.T) {
		got, err := parseDeepSeekUsage([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"70.0"},{"currency":"USD","total_balance":"10.0"}]}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Balance != 10 {
			t.Errorf("balance = %v, want the USD entry (10)", got.Balance)
		}
	})

	t.Run("falls back to the first currency when there is no USD", func(t *testing.T) {
		got, err := parseDeepSeekUsage([]byte(`{"balance_infos":[{"currency":"CNY","total_balance":"70.0"}]}`))
		if err != nil || got.Balance != 70 {
			t.Fatalf("got %+v err %v, want 70", got, err)
		}
	})

	t.Run("a real zero balance parses (it is urgent, not missing)", func(t *testing.T) {
		got, err := parseDeepSeekUsage([]byte(`{"balance_infos":[{"currency":"USD","total_balance":"0.00"}]}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !got.HasBalance() {
			t.Error("$0.00 remaining must be reported, not treated as no reading")
		}
		if got.Balance != 0 {
			t.Errorf("balance = %v, want 0", got.Balance)
		}
	})

	t.Run("junk errors", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"balance_infos":[]}`, `{"balance_infos":[{"currency":"USD"}]}`, `<html>`} {
			if _, err := parseDeepSeekUsage([]byte(body)); err == nil {
				t.Errorf("%s: expected an error", body)
			}
		}
	})
}

func TestParseMoonshotUsage(t *testing.T) {
	got, err := parseMoonshotUsage([]byte(`{"code":0,"data":{"available_balance":6.98,"voucher_balance":0,"cash_balance":6.98},"status":true}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.HasBalance() || got.Balance != 6.98 {
		t.Errorf("balance = %v (set=%v), want 6.98", got.Balance, got.HasBalance())
	}
	for _, body := range []string{`{}`, `{"data":{}}`, `{"code":401}`} {
		if _, err := parseMoonshotUsage([]byte(body)); err == nil {
			t.Errorf("%s: expected an error", body)
		}
	}
}

// xAI's /v1/api-key describes the KEY, not a balance. The valuable behaviour is
// reporting a blocked key — that is exactly what a user with a missing quota needs told.
func TestParseXAIUsage(t *testing.T) {
	t.Run("valid key with no money figure is an honest error", func(t *testing.T) {
		_, err := parseXAIUsage([]byte(`{"redacted_api_key":"xai-…abc","name":"key","api_key_blocked":false,"api_key_disabled":false,"team_blocked":false}`))
		if err == nil {
			t.Fatal("expected an error: xAI exposes no spend figure, and $0.00 would be a lie")
		}
		if !strings.Contains(err.Error(), "no spend or balance") {
			t.Errorf("unexpected error %v", err)
		}
	})

	t.Run("blocked key is surfaced", func(t *testing.T) {
		_, err := parseXAIUsage([]byte(`{"api_key_blocked":true}`))
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("err = %v, want it to name the key as blocked", err)
		}
	})

	t.Run("disabled key is surfaced", func(t *testing.T) {
		_, err := parseXAIUsage([]byte(`{"api_key_disabled":true}`))
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("err = %v, want it to name the key as disabled", err)
		}
	})

	t.Run("uses a balance when one is present", func(t *testing.T) {
		// Defensive: if xAI adds a figure later, pick it up rather than erroring.
		got, err := parseXAIUsage([]byte(`{"balance":25.5}`))
		if err != nil || !got.HasBalance() || got.Balance != 25.5 {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
}

func TestParseDeepInfraUsage(t *testing.T) {
	t.Run("nested billing balance", func(t *testing.T) {
		got, err := parseDeepInfraUsage([]byte(`{"email":"x@y.z","billing":{"balance":12.34,"limit":50}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !got.HasBalance() || got.Balance != 12.34 {
			t.Errorf("balance = %v", got.Balance)
		}
		if got.Quota.Budget.Spend != 50 {
			t.Errorf("cap = %v, want 50", got.Quota.Budget.Spend)
		}
	})
	t.Run("spend shape", func(t *testing.T) {
		got, err := parseDeepInfraUsage([]byte(`{"quota":{"spend":7,"limit":20}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got.Usage.Spend != 7 || got.Quota.Budget.Spend != 20 {
			t.Errorf("got spend=%v cap=%v, want 7/20", got.Usage.Spend, got.Quota.Budget.Spend)
		}
		if got.HasBalance() {
			t.Error("a spend shape must not be reported as a balance")
		}
	})
	t.Run("junk errors", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"email":"x@y.z"}`} {
			if _, err := parseDeepInfraUsage([]byte(body)); err == nil {
				t.Errorf("%s: expected an error", body)
			}
		}
	})
}

func TestParseVeniceUsage(t *testing.T) {
	t.Run("prefers USD over VCU/DIEM", func(t *testing.T) {
		got, err := parseVeniceUsage([]byte(`{"data":{"balances":{"USD":15.5,"VCU":420.0,"DIEM":1.0}}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		// USD keeps the figure comparable with every other provider and with the local
		// cost estimate; VCU would be an incomparable unit shown as dollars.
		if got.Balance != 15.5 {
			t.Errorf("balance = %v, want the USD figure 15.5", got.Balance)
		}
	})
	t.Run("falls back to VCU when there is no USD", func(t *testing.T) {
		got, err := parseVeniceUsage([]byte(`{"data":{"balances":{"VCU":420.0}}}`))
		if err != nil || got.Balance != 420 {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
	t.Run("junk errors", func(t *testing.T) {
		for _, body := range []string{`{}`, `{"data":{}}`, `{"data":{"balances":{}}}`} {
			if _, err := parseVeniceUsage([]byte(body)); err == nil {
				t.Errorf("%s: expected an error", body)
			}
		}
	})
}

func TestParseGenericBalance(t *testing.T) {
	for _, body := range []string{
		`{"balance":9.99}`,
		`{"data":{"balance":9.99}}`,
		`{"result":{"available_balance":9.99}}`,
		`{"balance":"9.99"}`,
		`{"balance":"$9.99"}`,
	} {
		got, err := parseGenericBalance([]byte(body))
		if err != nil {
			t.Errorf("%s: %v", body, err)
			continue
		}
		if got.Balance != 9.99 {
			t.Errorf("%s: balance = %v, want 9.99", body, got.Balance)
		}
	}
	for _, body := range []string{`{}`, `{"data":{}}`, `{"nope":1}`, `oops`} {
		if _, err := parseGenericBalance([]byte(body)); err == nil {
			t.Errorf("%s: expected an error", body)
		}
	}
}

// Only providers actually verified to answer with an inference key may claim support.
// Claiming more would show a permanent "quota unavailable" error to those users.
func TestProviderQuotaSupported(t *testing.T) {
	supported := []string{
		provider.SQEAnthropic, provider.OpenRouter, provider.DeepSeek,
		provider.Moonshot, provider.XAI, provider.ZAI, provider.DeepInfra, provider.Venice,
	}
	for _, id := range supported {
		if !ProviderQuotaSupported(id) {
			t.Errorf("%s should be supported", id)
		}
	}
	// Probing found no inference-key-readable endpoint for these: either none exists, or
	// it needs an admin/org credential (OpenAI, Anthropic), or the backend is local.
	unsupported := []string{
		provider.OpenAI, provider.Anthropic, provider.Groq, provider.Mistral,
		provider.Together, provider.Fireworks, provider.NVIDIA, provider.HuggingFace,
		provider.Cerebras, provider.Perplexity, provider.Gemini,
		provider.Ollama, provider.LMStudio, provider.LlamaCpp,
	}
	for _, id := range unsupported {
		if ProviderQuotaSupported(id) {
			t.Errorf("%s must NOT claim quota support", id)
		}
	}
	// An unknown id must not inherit the default provider's answer.
	for _, id := range []string{"", "  ", "opneai", "not-a-provider"} {
		if ProviderQuotaSupported(id) {
			t.Errorf("%q must not be reported as supported", id)
		}
	}
}

// End-to-end through providerUsage: a fake server standing in for the provider, so the
// URL, auth header and decode path are all exercised.
func TestProviderUsageEndToEnd(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"usage":7.5,"limit":30}}`))
	}))
	defer srv.Close()

	c := newTestClient(nil)
	c.cfg = &config.Config{
		LLMProvider:       provider.OpenRouter,
		OpenRouterAPIKey:  "sk-or-test",
		OpenRouterBaseURL: srv.URL,
	}
	got, err := c.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if gotPath != "/auth/key" {
		t.Errorf("path = %q, want /auth/key", gotPath)
	}
	// Reusing the inference credential is the whole point: no extra setup for the user.
	if gotAuth != "Bearer sk-or-test" {
		t.Errorf("auth = %q, want the inference key", gotAuth)
	}
	if got.Usage.Spend != 7.5 || got.Quota.Budget.Spend != 30 {
		t.Errorf("got spend=%v cap=%v", got.Usage.Spend, got.Quota.Budget.Spend)
	}
}

// A provider with no adapter must produce an error that NAMES it, so the UI can explain
// why there is no number instead of silently showing an estimate.
func TestUsageUnsupportedProviderNamesIt(t *testing.T) {
	c := newTestClient(nil)
	c.cfg = &config.Config{LLMProvider: provider.Groq, GroqAPIKey: "gsk-x"}
	_, err := c.Usage(context.Background())
	if err == nil {
		t.Fatal("expected an error for a provider with no usage endpoint")
	}
	if !strings.Contains(err.Error(), provider.Groq) {
		t.Errorf("error should name the provider, got %q", err)
	}
}

// A missing key must be reported as such rather than as a failed request, so the user is
// told what to set.
func TestProviderUsageMissingKey(t *testing.T) {
	c := newTestClient(nil)
	c.cfg = &config.Config{LLMProvider: provider.DeepSeek, DeepSeekBaseURL: "https://example.invalid"}
	_, err := c.Usage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("err = %v, want it to mention the missing API key", err)
	}
}

// A non-200 must not be parsed as data, and the message must not leak the key.
func TestProviderUsageHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Incorrect API key provided: sk-or-abcdefghijklmnopqrst"}`))
	}))
	defer srv.Close()

	c := newTestClient(nil)
	c.cfg = &config.Config{LLMProvider: provider.OpenRouter, OpenRouterAPIKey: "sk-or-x", OpenRouterBaseURL: srv.URL}
	_, err := c.Usage(context.Background())
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if strings.Contains(err.Error(), "sk-or-abcdefghijklmnopqrst") {
		t.Errorf("the key leaked into the error: %q", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("expected redaction, got %q", err)
	}
}

// An HTML error page (proxy, captive portal) must not parse as a reading.
func TestProviderUsageHTMLBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>gateway timeout</body></html>"))
	}))
	defer srv.Close()
	c := newTestClient(nil)
	c.cfg = &config.Config{LLMProvider: provider.DeepSeek, DeepSeekAPIKey: "sk-x", DeepSeekBaseURL: srv.URL}
	if _, err := c.Usage(context.Background()); err == nil {
		t.Error("expected an error for a non-JSON body")
	}
}

func TestProvidersWithQuota(t *testing.T) {
	got := ProvidersWithQuota()
	if len(got) < 8 {
		t.Errorf("got %d providers, want at least 8", len(got))
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("duplicate entry %q", id)
		}
		seen[id] = true
		if _, ok := provider.Lookup(id); !ok {
			t.Errorf("%q is not a known provider id", id)
		}
	}
}

// A prepaid provider must surface a BALANCE through the whole client path — not land in
// Usage.Spend, where every display would label remaining credit as money spent.
func TestBalanceProviderEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" {
			t.Errorf("path = %q, want /user/balance", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"4.66"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(nil)
	c.cfg = &config.Config{LLMProvider: provider.DeepSeek, DeepSeekAPIKey: "sk-x", DeepSeekBaseURL: srv.URL}
	got, err := c.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if !got.HasBalance() || got.Balance != 4.66 {
		t.Errorf("balance = %v (set=%v), want 4.66", got.Balance, got.HasBalance())
	}
	if got.Usage.Spend != 0 {
		t.Errorf("Usage.Spend = %v; a remaining balance must not be reported as spend", got.Usage.Spend)
	}
	if got.Quota.Budget.Spend != 0 {
		t.Errorf("budget = %v, want 0 — inventing a cap would fake a percentage", got.Quota.Budget.Spend)
	}
}

// Every adapter's path must be non-empty and rooted, or it would silently GET the base
// URL and try to parse a models list as a balance.
func TestQuotaAdapterPathsAreWellFormed(t *testing.T) {
	for id, ad := range quotaAdapters() {
		if ad.parse == nil {
			t.Errorf("%s: no parser", id)
		}
		if ad.absolute != "" {
			if !strings.HasPrefix(ad.absolute, "https://") {
				t.Errorf("%s: absolute URL %q must be https", id, ad.absolute)
			}
			continue
		}
		if ad.path == "" {
			t.Errorf("%s: empty path would probe the base URL", id)
		}
		if !strings.HasPrefix(ad.path, "/") {
			t.Errorf("%s: path %q must start with /", id, ad.path)
		}
	}
}
