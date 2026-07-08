package tui

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
)

func TestFormatUsagePersonal(t *testing.T) {
	r := &llm.UsageResponse{
		DSID: "123456",
		Quota: llm.UsageQuota{
			Budget: llm.UsageBudget{Spend: 100, ResetTime: "2025-12-05T00:00:00Z"},
			Rates:  llm.UsageRates{Calls: 40, CallsPeriod: "4m", Tokens: 1_000_000_000, TokensPeriod: "4m"},
		},
		Usage: llm.UsageData{
			Calls:        7,
			Spend:        12.34,
			InputTokens:  500,
			OutputTokens: 300,
			Models: map[string]llm.UsageModelData{
				"aws:anthropic.claude-sonnet-4-6":              {Calls: 5, Spend: 10, InputTokens: 400, OutputTokens: 250},
				"aws:anthropic.claude-haiku-4-5-20251001-v1:0": {Calls: 2, Spend: 2.34, InputTokens: 100, OutputTokens: 50},
			},
		},
	}
	got := formatUsage(r)
	for _, want := range []string{
		"Account: dsid 123456",
		"Budget: $12.34 / $100",
		"resets 2025-12-05T00:00:00Z",
		"Rates: 40 calls / 4m",
		"Per model:",
		"claude-sonnet-4-6",
		"claude-haiku",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Sonnet should appear before Haiku since spend is higher.
	if strings.Index(got, "claude-sonnet-4-6") > strings.Index(got, "claude-haiku") {
		t.Error("expected per-model rows sorted by spend descending")
	}
}

func TestFormatUsageProject(t *testing.T) {
	r := &llm.UsageResponse{
		ProjectID:   "abc",
		ProjectName: "MyProj",
		Usage:       llm.UsageData{Spend: 5, Calls: 3},
	}
	got := formatUsage(r)
	if !strings.Contains(got, "Project: MyProj (abc)") {
		t.Errorf("expected project header, got:\n%s", got)
	}
	// No budget cap → fall back to "Spend (24h)" line.
	if !strings.Contains(got, "Spend (24h): $5") {
		t.Errorf("expected spend line, got:\n%s", got)
	}
}

func TestFormatUsageNil(t *testing.T) {
	if got := formatUsage(nil); got != "No usage data." {
		t.Errorf("expected nil-safe message, got %q", got)
	}
}

func TestFormatQuotaShort(t *testing.T) {
	if got := formatQuotaShort(false, 12, 100); got != "" {
		t.Errorf("not-fetched should produce empty string, got %q", got)
	}
	if got := formatQuotaShort(true, 12.34, 100); got != "$12.34/$100" {
		t.Errorf("with budget = %q, want $12.34/$100", got)
	}
	if got := formatQuotaShort(true, 5, 0); got != "$5.00 24h" {
		t.Errorf("no budget = %q, want $5.00 24h", got)
	}
	if got := formatQuotaShort(true, 0, 100); got != "$0.0000/$100" {
		t.Errorf("zero spend = %q", got)
	}
}

func TestApplyUsageUpdatesModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)

	resp := &llm.UsageResponse{
		DSID:  "42",
		Quota: llm.UsageQuota{Budget: llm.UsageBudget{Spend: 100, ResetTime: "2025-12-05T00:00:00Z"}},
		Usage: llm.UsageData{Spend: 12.34},
	}
	m.applyUsage(resp)

	if !m.quotaFetched {
		t.Error("quotaFetched should flip to true")
	}
	if m.quotaSpend != 12.34 || m.quotaBudget != 100 {
		t.Errorf("spend/budget = %v / %v", m.quotaSpend, m.quotaBudget)
	}
	if m.quotaProject != "" {
		t.Error("personal response should leave quotaProject empty")
	}

	// Status bar shows "$12.34/$100" once a quota is known.
	view := m.statusView()
	if !strings.Contains(view, "$12.34/$100") {
		t.Errorf("status bar missing quota; got:\n%s", view)
	}
}

func TestApplyUsageProjectFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := sized(t, newTestModel(t), 100, 30)
	resp := &llm.UsageResponse{
		ProjectID:   "p-1",
		ProjectName: "MyProj",
		Usage:       llm.UsageData{Spend: 5},
	}
	m.applyUsage(resp)
	if m.quotaProject != "MyProj" {
		t.Errorf("project name = %q", m.quotaProject)
	}
	// No budget, so the bar shows "$X 24h".
	if !strings.Contains(m.statusView(), "$5.00 24h") {
		t.Errorf("status bar missing 24h spend; got:\n%s", m.statusView())
	}
}
