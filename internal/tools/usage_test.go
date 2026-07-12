package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/session"
)

func TestUsageToolAggregation(t *testing.T) {
	turns := []session.TurnMeta{
		{Time: time.Now(), Model: "gpt-x", Calls: 2, InTokens: 1000, OutTokens: 200, CacheReadTokens: 400, CostUSD: 0.01},
		{Time: time.Now(), Model: "gpt-x", Calls: 1, InTokens: 500, OutTokens: 100, CacheReadTokens: 0, CostUSD: 0.005},
		{Time: time.Now(), Model: "claude-y", Calls: 3, InTokens: 2000, OutTokens: 800, CacheReadTokens: 100, CostUSD: 0.05},
	}
	tool := &usageTool{provider: func() []session.TurnMeta { return turns }}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	// Totals: 3 turns, 6 calls, cost 0.065.
	if !strings.Contains(out, "3 turns, 6 calls") {
		t.Errorf("missing totals line: %q", out)
	}
	if !strings.Contains(out, "$0.0650") {
		t.Errorf("wrong total cost: %q", out)
	}
	// Per-model breakdown, ordered by cost: claude-y ($0.05) before gpt-x.
	iClaude := strings.Index(out, "claude-y")
	iGPT := strings.Index(out, "gpt-x")
	if iClaude < 0 || iGPT < 0 || iClaude > iGPT {
		t.Errorf("per-model order wrong: %q", out)
	}

	byTurn, err := tool.Execute(context.Background(), json.RawMessage(`{"group_by":"turn"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(byTurn, "by turn") || !strings.Contains(byTurn, "  1.") {
		t.Errorf("by-turn render missing: %q", byTurn)
	}
}

func TestUsageToolNoData(t *testing.T) {
	tool := &usageTool{provider: func() []session.TurnMeta { return nil }}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "No usage recorded") {
		t.Errorf("no-data usage = %q (err %v)", out, err)
	}

	nilProv := &usageTool{}
	if out, _ := nilProv.Execute(context.Background(), json.RawMessage(`{}`)); !strings.Contains(out, "No usage recorded") {
		t.Errorf("nil provider = %q", out)
	}
}

func TestHumanInt(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := humanInt(in); got != want {
			t.Errorf("humanInt(%d) = %q, want %q", in, got, want)
		}
	}
}
