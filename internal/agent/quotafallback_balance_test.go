package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/provider"
)

// Balance-only providers (DeepSeek, Moonshot, Z.AI, DeepInfra, Venice) report REMAINING
// credit, which leaves Spend and Budget at 0. Before balanceBreached existed, every test
// in quotaBreached was therefore skipped and these providers could never trigger a
// fallback at all — a user whose credit ran dry just got failing turns.
func TestBalanceBreached(t *testing.T) {
	tests := []struct {
		name      string
		balance   float64
		budgetUSD float64
		want      bool
	}{
		// Nothing left to spend: the next request fails regardless of configuration,
		// so this breaches even with no threshold set.
		{"exhausted credit breaches with no threshold", 0, 0, true},
		{"negative balance breaches", -1.25, 0, true},
		{"healthy balance with no threshold does not breach", 25, 0, false},

		// MN_QUOTA_BUDGET_USD acts as a RESERVE FLOOR here ("hop while this much is
		// still left"), the opposite sense to the spend case, because the underlying
		// number is the opposite quantity.
		{"balance under the reserve floor breaches", 1.50, 5, true},
		{"balance exactly at the floor breaches", 5, 5, true},
		{"balance above the floor does not breach", 7.50, 5, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := balanceBreached(tc.balance, tc.budgetUSD); got != tc.want {
				t.Errorf("balanceBreached(%v, %v) = %v, want %v",
					tc.balance, tc.budgetUSD, got, tc.want)
			}
		})
	}
}

// A percentage needs a cap to divide by, and these providers report none. Inventing a
// denominator would fire fallbacks at a threshold the user never chose, so PCT must have
// no effect on a balance reading.
func TestBalanceIgnoresPercentThreshold(t *testing.T) {
	// quotaBreached is the only place PCT is honoured; with a balance-only reading its
	// inputs are 0/0, so no percentage can ever apply.
	if quotaBreached(0, 0, 0, 50) {
		t.Error("a percentage threshold must not fire on a 0/0 balance-only reading")
	}
	// And a healthy balance with only PCT configured must stay quiet.
	if balanceBreached(20, 0) {
		t.Error("a healthy balance must not breach when only PCT is configured")
	}
}

// Spend-based providers must be completely unaffected by the balance path.
func TestSpendPathUnchangedByBalanceLogic(t *testing.T) {
	// Under both thresholds: no breach.
	if quotaBreached(10, 100, 50, 90) {
		t.Error("spend 10 of 100 should not breach a $50 / 90% policy")
	}
	// Over the USD cap.
	if !quotaBreached(60, 100, 50, 0) {
		t.Error("spend 60 should breach a $50 cap")
	}
	// Over the percentage.
	if !quotaBreached(95, 100, 0, 90) {
		t.Error("spend 95 of 100 should breach a 90% policy")
	}
	// No cap reported: a percentage cannot apply, and must not divide by zero.
	if quotaBreached(95, 0, 0, 90) {
		t.Error("a percentage must not fire when the provider reports no cap")
	}
}

// BalanceSet is what separates a real "$0.00 left" from "no balance reported". If the
// policy ignored the flag and just read Balance, EVERY spend-based provider would look
// like exhausted credit (Balance defaults to 0) and fall back immediately.
func TestZeroBalanceOnlyCountsWhenReported(t *testing.T) {
	spendOnlySnapshot := QuotaSnapshot{Provider: "openrouter", Spend: 1, Budget: 100}
	if spendOnlySnapshot.BalanceSet {
		t.Fatal("a spend-only snapshot must not claim a balance reading")
	}
	// The guard in applyQuotaPolicy is `q.BalanceSet && balanceBreached(...)`; assert the
	// bare arithmetic would otherwise have misfired, which is why the flag is required.
	if !balanceBreached(spendOnlySnapshot.Balance, 0) {
		t.Fatal("precondition: an unset balance of 0 does look exhausted in isolation")
	}
}

// End-to-end: a prepaid provider with exhausted credit must actually hop. This is the
// user-visible bug the balance path fixes — before it, DeepSeek at $0.00 kept trying the
// dead backend and every turn failed.
func TestMaybeQuotaFallbackFiresOnExhaustedBalance(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.DeepSeek
	cfg.DeepSeekAPIKey = "sk-deepseek"
	cfg.ProviderFallbacks = []string{provider.DeepSeek, provider.Ollama}
	a := agentForQuota(t, cfg)

	// No Spend, no Budget — exactly what a balance-only reading looks like.
	msg := a.MaybeQuotaFallback(QuotaSnapshot{
		Provider: provider.DeepSeek, Balance: 0, BalanceSet: true,
	})
	if msg == "" {
		t.Fatal("exhausted prepaid credit must trigger a fallback")
	}
	if cfg.LLMProvider != provider.Ollama {
		t.Errorf("provider = %q, want %q", cfg.LLMProvider, provider.Ollama)
	}
}

// A healthy balance must NOT hop: a spurious switch would move the user off their chosen
// model for no reason.
func TestMaybeQuotaFallbackQuietOnHealthyBalance(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.DeepSeek
	cfg.DeepSeekAPIKey = "sk-deepseek"
	cfg.ProviderFallbacks = []string{provider.DeepSeek, provider.Ollama}
	a := agentForQuota(t, cfg)

	if msg := a.MaybeQuotaFallback(QuotaSnapshot{
		Provider: provider.DeepSeek, Balance: 25, BalanceSet: true,
	}); msg != "" {
		t.Fatalf("healthy credit must not hop, got %q", msg)
	}
	if cfg.LLMProvider != provider.DeepSeek {
		t.Errorf("provider = %q, want it unchanged", cfg.LLMProvider)
	}
}

// A spend-based provider must not be dragged into the balance path by its zero-valued
// Balance field: BalanceSet is false, so nothing should fire.
func TestMaybeQuotaFallbackIgnoresUnsetBalance(t *testing.T) {
	cfg := usableCfg(t)
	cfg.QuotaFallback = true
	cfg.LLMProvider = provider.Anthropic
	cfg.QuotaBudgetUSD = 40
	cfg.ProviderFallbacks = []string{provider.Anthropic, provider.Ollama}
	a := agentForQuota(t, cfg)

	// Well under the $40 cap, and Balance is the zero value with BalanceSet false.
	if msg := a.MaybeQuotaFallback(QuotaSnapshot{
		Provider: provider.Anthropic, Spend: 5, Budget: 100,
	}); msg != "" {
		t.Fatalf("an unset balance must not be read as exhausted credit, got %q", msg)
	}
}
