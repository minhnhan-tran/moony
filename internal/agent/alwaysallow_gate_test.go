package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/tools"
)

// "Always allow" is honored for ordinary tools but deliberately ignored for git
// commit/push and destructive commands, which are re-confirmed every time. The bug
// these tests pin is not the gate itself — it's offering the choice anyway, so the
// user picks "always", sees the prompt return, and reports permissions as broken.

func newAlwaysGateAgent(t *testing.T, wd string) *Agent {
	t.Helper()
	var z int32
	reg := tools.NewRegistry(wd)
	for _, n := range []string{"bash", "write_file", "git_commit"} {
		reg.Register(&recordingTool{name: n, mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	}
	a := &Agent{
		cfg:         &config.Config{ApprovalPolicy: config.ApprovalPrompt, WorkingDir: wd},
		tools:       reg,
		alwaysAllow: map[string]bool{},
		// These gates route to the interactive prompt; without this they take the
		// "no human available" path and deny outright.
		interactive: true,
	}
	a.AttachPermissions(permissions.Load(wd))
	return a
}

// captureApproval answers with d and records the request it saw.
func captureApproval(d Decision, got *ApprovalRequest, asked *bool) func(Event) {
	return func(ev Event) {
		if req, ok := ev.(ApprovalRequest); ok {
			*asked = true
			*got = req
			req.Reply <- d
		}
	}
}

// An ordinary tool: "always allow" must stick across a restart.
func TestAlwaysAllowPersistsForOrdinaryTools(t *testing.T) {
	isolateConfig(t)
	wd := t.TempDir()

	for _, tc := range []struct{ name, tool, input string }{
		{"bash", "bash", `{"command":"go test ./..."}`},
		{"write_file", "write_file", `{"path":"a.txt"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req ApprovalRequest
			asked := false
			a1 := newAlwaysGateAgent(t, wd)
			ok, _ := a1.approveToolCall(context.Background(), captureApproval(ApproveAlways, &req, &asked),
				llm.ContentBlock{ID: "1", Name: tc.tool, Input: json.RawMessage(tc.input)})
			if !ok || !asked {
				t.Fatalf("first call should prompt and be approved (ok=%v asked=%v)", ok, asked)
			}
			if req.AlwaysDisabled {
				t.Error("an ordinary tool should offer 'always allow'")
			}

			// Restart: a brand new agent reloading rules from the same project.
			asked2 := false
			a2 := newAlwaysGateAgent(t, wd)
			ok2, _ := a2.approveToolCall(context.Background(), captureApproval(ApproveAlways, &req, &asked2),
				llm.ContentBlock{ID: "2", Name: tc.tool, Input: json.RawMessage(tc.input)})
			if !ok2 {
				t.Fatal("should still be allowed after restart")
			}
			if asked2 {
				t.Error("'always allow' did not persist: the user is prompted again after restart")
			}
		})
	}
}

// Gates that re-confirm every time must SAY so, so the UI can hide the button.
func TestReconfirmGatesMarkAlwaysDisabled(t *testing.T) {
	isolateConfig(t)
	wd := t.TempDir()

	for _, tc := range []struct{ name, tool, input string }{
		{"git_commit_tool", "git_commit", `{"message":"x"}`},
		{"bash_git_commit", "bash", `{"command":"git commit -m x"}`},
		{"bash_git_push", "bash", `{"command":"git push origin main"}`},
		{"bash_rm_rf", "bash", `{"command":"rm -rf build/"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req ApprovalRequest
			asked := false
			a := newAlwaysGateAgent(t, wd)
			if ok, _ := a.approveToolCall(context.Background(), captureApproval(ApproveAlways, &req, &asked),
				llm.ContentBlock{ID: "1", Name: tc.tool, Input: json.RawMessage(tc.input)}); !ok {
				t.Fatal("approving should let the call run")
			}
			if !asked {
				t.Fatal("this gate should always prompt")
			}
			if !req.AlwaysDisabled {
				t.Error("the request must be marked so the UI hides 'always allow'")
			}
			if req.AlwaysDisabledReason == "" {
				t.Error("a reason should be supplied to explain the re-prompt")
			}

			// And it genuinely re-prompts next time, even after "always".
			asked2 := false
			a2 := newAlwaysGateAgent(t, wd)
			a2.approveToolCall(context.Background(), captureApproval(Approve, &req, &asked2),
				llm.ContentBlock{ID: "2", Name: tc.tool, Input: json.RawMessage(tc.input)})
			if !asked2 {
				t.Error("this gate is supposed to re-confirm every time")
			}
		})
	}
}
