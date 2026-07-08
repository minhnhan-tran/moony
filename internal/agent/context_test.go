package agent

import (
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func TestContextBreakdownSegments(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	cfg := &config.Config{Model: "anthropic.claude-sonnet-4-6", WorkingDir: dir}
	reg := tools.Default(dir, 30000)
	ag := New(cfg, nil, reg, nil, nil)

	bd := ag.ContextBreakdown()

	// A fresh agent always has a non-empty system prompt and tool schemas.
	labels := map[string]int{}
	for _, s := range bd.Segments {
		labels[s.Label] = s.Tokens
	}
	if labels["System"] <= 0 {
		t.Errorf("expected a positive System segment, got %+v", bd.Segments)
	}
	if labels["Built-in tools"] <= 0 {
		t.Errorf("expected a positive Built-in tools segment, got %+v", bd.Segments)
	}
	// No history yet → no History segment.
	if _, ok := labels["History"]; ok {
		t.Errorf("fresh agent should have no History segment, got %+v", bd.Segments)
	}
	// Total must equal the sum of the segments.
	sum := 0
	for _, s := range bd.Segments {
		sum += s.Tokens
	}
	if sum != bd.Total {
		t.Errorf("Total %d != sum of segments %d", bd.Total, sum)
	}
}

func TestContextBreakdownCountsHistory(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	cfg := &config.Config{Model: "m", WorkingDir: dir}
	ag := New(cfg, nil, tools.NewRegistry(dir), nil, nil)

	before := ag.ContextBreakdown()
	if hasSegment(before.Segments, "History") {
		t.Fatalf("expected no History segment before any messages, got %+v", before.Segments)
	}

	// Add a sizeable user turn and confirm a History segment appears and Total grows.
	big := make([]byte, 4000)
	for i := range big {
		big[i] = 'x'
	}
	ag.messages = []llm.Message{llm.UserText(string(big))}

	after := ag.ContextBreakdown()
	if !hasSegment(after.Segments, "History") {
		t.Fatalf("expected a History segment after a message, got %+v", after.Segments)
	}
	if after.Total <= before.Total {
		t.Errorf("Total should grow with history: before=%d after=%d", before.Total, after.Total)
	}
	// ~4000 bytes / 4 ≈ 1000 tokens for the history payload (the text dominates).
	if got := segTokens(after.Segments, "History"); got < 900 {
		t.Errorf("History segment too small: got %d, want ~1000", got)
	}
}

func TestContextBreakdownAgentsMDSegment(t *testing.T) {
	isolateConfig(t)
	root := fakeRepoTree(t) // writes AGENTS.md at root and sub
	dir := root
	cfg := &config.Config{Model: "m", WorkingDir: dir}
	ag := New(cfg, nil, tools.NewRegistry(dir), nil, nil)

	bd := ag.ContextBreakdown()
	if !hasSegment(bd.Segments, "Memory files") {
		t.Fatalf("expected a Memory files segment when an AGENTS.md exists, got %+v", bd.Segments)
	}
	// The base System segment must not double-count the AGENTS.md bytes: the
	// system prompt should be split cleanly into base + memory files.
	sys := segTokens(bd.Segments, "System")
	agents := segTokens(bd.Segments, "Memory files")
	if sys <= 0 || agents <= 0 {
		t.Errorf("expected positive System and Memory files segments, got system=%d agents=%d", sys, agents)
	}
}

// TestContextBreakdownToolCategories verifies the tool schemas are split into
// Built-in tools, MCP tools, Subagents (task), and Skills (skill) segments.
func TestContextBreakdownToolCategories(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	cfg := &config.Config{Model: "m", WorkingDir: dir}
	reg := tools.NewRegistry(dir)
	reg.Register(&recordingTool{name: "read_file"})
	reg.Register(&recordingTool{name: "mcp.github.list_prs"})
	reg.Register(&recordingTool{name: "task"})
	reg.Register(&recordingTool{name: "skill"})
	ag := New(cfg, nil, reg, nil, nil)

	bd := ag.ContextBreakdown()
	for _, label := range []string{"Built-in tools", "MCP tools", "Subagents", "Skills"} {
		if !hasSegment(bd.Segments, label) {
			t.Errorf("expected a %q segment, got %+v", label, bd.Segments)
		}
	}
	// Categories must itemize their contributors for the meter detail.
	for _, seg := range bd.Segments {
		switch seg.Label {
		case "Built-in tools":
			if !hasItem(seg.Items, "read_file") {
				t.Errorf("Built-in tools should itemize read_file, got %+v", seg.Items)
			}
		case "MCP tools":
			if !hasItem(seg.Items, "github") { // grouped per server: mcp.github.* → "github"
				t.Errorf("MCP tools should itemize the github server, got %+v", seg.Items)
			}
		}
	}
}

func hasItem(items []ContextSegment, label string) bool {
	for _, it := range items {
		if it.Label == label {
			return true
		}
	}
	return false
}

func hasSegment(segs []ContextSegment, label string) bool {
	for _, s := range segs {
		if s.Label == label {
			return true
		}
	}
	return false
}

func segTokens(segs []ContextSegment, label string) int {
	for _, s := range segs {
		if s.Label == label {
			return s.Tokens
		}
	}
	return 0
}
