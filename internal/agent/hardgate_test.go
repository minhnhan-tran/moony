package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func TestBashRunsGitCommitOrPush(t *testing.T) {
	yes := []string{
		"git push",
		"git push origin main",
		"git commit -m 'x'",
		"git -C /repo commit -am y",
		"git -c user.name=z push",
		"git add -A && git commit -m x",
		"cd repo && git push --force",
	}
	for _, c := range yes {
		if !bashRunsGitCommitOrPush(c) {
			t.Errorf("expected hard-gate match for %q", c)
		}
	}
	no := []string{
		"git status",
		"git log --oneline",
		"git diff",
		"git config commit.gpgsign true",
		"go test ./...",
		"ls -la",
		"grep commit file.go",
	}
	for _, c := range no {
		if bashRunsGitCommitOrPush(c) {
			t.Errorf("did NOT expect hard-gate match for %q", c)
		}
	}
}

func TestHardApprovalClassifier(t *testing.T) {
	if !hardApproval("git_commit", json.RawMessage(`{}`)) {
		t.Error("git_commit must be hard-gated")
	}
	if !hardApproval("bash", json.RawMessage(`{"command":"git push"}`)) {
		t.Error("bash git push must be hard-gated")
	}
	if hardApproval("bash", json.RawMessage(`{"command":"git status"}`)) {
		t.Error("bash git status must NOT be hard-gated")
	}
	if hardApproval("write_file", json.RawMessage(`{}`)) {
		t.Error("write_file is not a hard-gated action")
	}
}

// hardGateAgent builds an Agent with git_commit + bash registered, the given
// interactivity, and the auto policy (to prove the gate overrides the policy).
func hardGateAgent(t *testing.T, interactive bool) *Agent {
	t.Helper()
	var z int32
	reg := tools.NewRegistry(t.TempDir())
	reg.Register(&recordingTool{name: "git_commit", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	reg.Register(&recordingTool{name: "bash", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	return &Agent{
		cfg:         &config.Config{ApprovalPolicy: config.ApprovalAuto}, // auto: would normally skip prompts
		tools:       reg,
		alwaysAllow: map[string]bool{"git_commit": true}, // even an always-allow must not bypass
		interactive: interactive,
	}
}

// approveWith runs approveToolCall and returns (proceed, prompted), replying to
// any ApprovalRequest with the given decision.
func approveWith(a *Agent, call llm.ContentBlock, decision Decision) (proceed, prompted bool) {
	emit := func(ev Event) {
		if ar, ok := ev.(ApprovalRequest); ok {
			prompted = true
			ar.Reply <- decision
		}
	}
	proceed, _ = a.approveToolCall(context.Background(), emit, call)
	return proceed, prompted
}

func TestHardGatePromptsEvenUnderAuto(t *testing.T) {
	a := hardGateAgent(t, true)
	call := llm.ContentBlock{ID: "1", Name: "git_commit", Input: json.RawMessage(`{"message":"x"}`)}
	proceed, prompted := approveWith(a, call, Approve)
	if !prompted {
		t.Error("git_commit must prompt even under the auto policy and an always-allow rule")
	}
	if !proceed {
		t.Error("an approved git_commit should proceed")
	}
}

func TestHardGateDeniesWhenDenied(t *testing.T) {
	a := hardGateAgent(t, true)
	call := llm.ContentBlock{ID: "1", Name: "bash", Input: json.RawMessage(`{"command":"git push origin main"}`)}
	proceed, prompted := approveWith(a, call, Deny)
	if !prompted {
		t.Error("git push must prompt")
	}
	if proceed {
		t.Error("a denied git push must not proceed")
	}
}

func TestHardGateDeniesWhenNonInteractive(t *testing.T) {
	a := hardGateAgent(t, false) // headless / sub-agent: no approver
	call := llm.ContentBlock{ID: "1", Name: "git_commit", Input: json.RawMessage(`{"message":"x"}`)}
	proceed, prompted := approveWith(a, call, Approve)
	if prompted {
		t.Error("non-interactive context must NOT emit an approval prompt (no approver)")
	}
	if proceed {
		t.Error("git_commit must be DENIED, not auto-run, without an interactive approver")
	}
}

// TestDangerSkipBypassesHardGate confirms danger-skip-permission auto-approves
// git commit/push (the hard gate) without prompting.
func TestDangerSkipBypassesHardGate(t *testing.T) {
	a := hardGateAgent(t, true)
	a.cfg.ApprovalPolicy = config.ApprovalDangerSkip
	call := llm.ContentBlock{ID: "1", Name: "git_commit", Input: json.RawMessage(`{"message":"x"}`)}
	proceed, prompted := approveWith(a, call, Deny)
	if prompted {
		t.Error("danger-skip-permission must NOT prompt for git_commit")
	}
	if !proceed {
		t.Error("danger-skip-permission must auto-approve git_commit")
	}
	push := llm.ContentBlock{ID: "2", Name: "bash", Input: json.RawMessage(`{"command":"git push origin main"}`)}
	proceed, prompted = approveWith(a, push, Deny)
	if prompted || !proceed {
		t.Errorf("danger-skip-permission must auto-approve git push (proceed=%v prompted=%v)", proceed, prompted)
	}
}
