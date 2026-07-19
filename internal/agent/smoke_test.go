package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// TestAgentSmoke exercises the full pipeline against a live hosted provider. It is
// skipped unless MN_SMOKE=1 because it requires provider auth, network
// access, and consumes model quota.
//
//	MN_SMOKE=1 go test ./internal/agent -run TestAgentSmoke -v
func TestAgentSmoke(t *testing.T) {
	if os.Getenv("MN_SMOKE") != "1" {
		t.Skip("set MN_SMOKE=1 to run the live smoke test")
	}

	cfg := config.Load()
	cfg.MaxAgentSteps = 6
	cfg.ApprovalPolicy = config.ApprovalAuto // non-interactive test: auto-run tools
	client := llm.NewClient(cfg)
	registry := tools.Default(cfg.WorkingDir, cfg.MaxToolOutputBytes)
	ag := New(cfg, client, registry, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var (
		text     strings.Builder
		toolSeen []string
	)
	err := ag.Run(ctx, "Use your list_dir tool to list the current directory, then reply with exactly: DONE", func(ev Event) {
		switch e := ev.(type) {
		case TextDelta:
			text.WriteString(e.Text)
		case ToolStart:
			toolSeen = append(toolSeen, e.Name)
			t.Logf("tool start: %s %s", e.Name, e.Input)
		case ToolEnd:
			t.Logf("tool end: %s (err=%v) -> %.80q", e.Name, e.IsError, e.Output)
		case TurnDone:
			t.Logf("turn done: in=%d out=%d", e.Usage.InputTokens, e.Usage.OutputTokens)
		}
	})
	if err != nil {
		t.Fatalf("agent run failed: %v", err)
	}

	final := text.String()
	t.Logf("final assistant text: %q", final)
	if strings.TrimSpace(final) == "" {
		t.Fatal("expected non-empty assistant response")
	}
	if len(toolSeen) == 0 {
		t.Error("expected at least one tool call (list_dir)")
	}
}
