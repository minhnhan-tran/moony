package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/session"
)

// usageTool reports token and cost usage for the current session, aggregated
// from the per-turn metadata the agent already records. It gives the model (and
// the user) visibility into spend to inform routing/model choices, without any
// new accounting plumbing.
type usageTool struct {
	provider func() []session.TurnMeta
}

// RegisterUsage adds the usage tool, backed by provider (typically the live
// session's TurnMetas). A nil provider (or one returning no turns) makes the
// tool report that no usage has been recorded yet.
func (r *Registry) RegisterUsage(provider func() []session.TurnMeta) {
	r.Register(&usageTool{provider: provider})
}

func (t *usageTool) Name() string { return "usage" }

func (t *usageTool) Description() string {
	return "Report this session's token and cost usage so far: total input/output/cached tokens, estimated " +
		"total cost in USD, number of turns and model calls, and a per-model breakdown. Set group_by to \"turn\" " +
		"for a chronological per-turn table instead. Use it to see how much a session is costing and which model " +
		"is driving spend."
}

func (t *usageTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"group_by": {"type": "string", "enum": ["model", "turn"], "description": "Breakdown dimension (default \"model\")."}
	}}`)
}

func (t *usageTool) Mutates() bool { return false }

func (t *usageTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		GroupBy string `json:"group_by"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	var turns []session.TurnMeta
	if t.provider != nil {
		turns = t.provider()
	}
	if len(turns) == 0 {
		return "No usage recorded yet this session (no completed turns, or session persistence is disabled).", nil
	}
	if strings.TrimSpace(in.GroupBy) == "turn" {
		return renderUsageByTurn(turns), nil
	}
	return renderUsageByModel(turns), nil
}

type usageTotals struct {
	turns     int
	calls     int
	in        int
	out       int
	cacheRead int
	cost      float64
}

func (u *usageTotals) add(t session.TurnMeta) {
	u.turns++
	u.calls += t.Calls
	u.in += t.InTokens
	u.out += t.OutTokens
	u.cacheRead += t.CacheReadTokens
	u.cost += t.CostUSD
}

func (u usageTotals) line() string {
	return fmt.Sprintf("%d turns, %d calls · in %s (cached %s), out %s · $%.4f",
		u.turns, u.calls, humanInt(u.in), humanInt(u.cacheRead), humanInt(u.out), u.cost)
}

func renderUsageByModel(turns []session.TurnMeta) string {
	var total usageTotals
	byModel := map[string]*usageTotals{}
	var order []string
	for _, t := range turns {
		total.add(t)
		m := t.Model
		if m == "" {
			m = "(unknown)"
		}
		u := byModel[m]
		if u == nil {
			u = &usageTotals{}
			byModel[m] = u
			order = append(order, m)
		}
		u.add(t)
	}
	sort.SliceStable(order, func(i, j int) bool { return byModel[order[i]].cost > byModel[order[j]].cost })

	var b strings.Builder
	b.WriteString("Session usage\n")
	fmt.Fprintf(&b, "Total: %s\n", total.line())
	if total.in+total.out > 0 {
		fmt.Fprintf(&b, "Cache hit rate: %.0f%% of input tokens\n", 100*float64(total.cacheRead)/float64(max(total.in, 1)))
	}
	b.WriteString("\nPer model:\n")
	for _, m := range order {
		fmt.Fprintf(&b, "- %s: %s\n", m, byModel[m].line())
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderUsageByTurn(turns []session.TurnMeta) string {
	var total usageTotals
	var b strings.Builder
	b.WriteString("Session usage by turn\n")
	for i, t := range turns {
		total.add(t)
		model := t.Model
		if model == "" {
			model = "(unknown)"
		}
		fmt.Fprintf(&b, "%3d. %s · %s · in %s out %s · $%.4f\n",
			i+1, t.Time.Format("15:04:05"), model, humanInt(t.InTokens), humanInt(t.OutTokens), t.CostUSD)
	}
	fmt.Fprintf(&b, "\nTotal: %s", total.line())
	return b.String()
}

// humanInt formats a token count with thousands separators (e.g. 12,345).
func humanInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
