package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// TestApprovalSmoke verifies the approval gate and diff preview end-to-end: the
// model is asked to create a file, the agent must request approval (with a
// non-empty diff), and after approval the file is written.
//
//	MN_SMOKE=1 go test ./internal/agent -run TestApprovalSmoke -v
func TestApprovalSmoke(t *testing.T) {
	if os.Getenv("MN_SMOKE") != "1" {
		t.Skip("set MN_SMOKE=1 to run the live approval smoke test")
	}

	dir := t.TempDir()
	cfg := config.Load()
	cfg.WorkingDir = dir
	cfg.MaxAgentSteps = 6
	cfg.ApprovalPolicy = config.ApprovalPrompt

	client := llm.NewClient(cfg)
	registry := tools.Default(dir, cfg.MaxToolOutputBytes)
	ag := New(cfg, client, registry, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var (
		approvedTool string
		sawDiff      bool
	)
	prompt := "Create a file named hello.txt containing exactly the text: hello world"
	err := ag.Run(ctx, prompt, func(ev Event) {
		switch e := ev.(type) {
		case ApprovalRequest:
			approvedTool = e.Tool
			if strings.Contains(e.Diff, "hello world") {
				sawDiff = true
			}
			t.Logf("approval requested for %s; summary=%q diff=\n%s", e.Tool, e.Summary, e.Diff)
			e.Reply <- Approve
		case ToolEnd:
			t.Logf("tool end: %s err=%v -> %q", e.Name, e.IsError, e.Output)
		}
	})
	if err != nil {
		t.Fatalf("agent run failed: %v", err)
	}

	if approvedTool != "write_file" {
		t.Errorf("expected approval request for write_file, got %q", approvedTool)
	}
	if !sawDiff {
		t.Error("expected the approval diff to include the new file content")
	}

	got, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatalf("expected hello.txt to be created: %v", err)
	}
	if !strings.Contains(string(got), "hello world") {
		t.Errorf("file content = %q, want it to contain 'hello world'", string(got))
	}
}
