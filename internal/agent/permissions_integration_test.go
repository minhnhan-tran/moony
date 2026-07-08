package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/tools"
)

// isolateConfig (defined in agents_md_test.go) points the global config dir at a
// throwaway temp dir so these tests never read the developer's real ~/.config
// (which now carries seeded default rules from EnsureGlobalDefaults).

// newPermTestAgent builds an agent with a write_file/bash recording toolset and
// the prompt approval policy, plus the given permission rules.
func newPermTestAgent(t *testing.T, rules *permissions.Rules) *Agent {
	t.Helper()
	var z int32
	reg := tools.NewRegistry(t.TempDir())
	reg.Register(&recordingTool{name: "write_file", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	reg.Register(&recordingTool{name: "bash", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	a := &Agent{
		cfg:         &config.Config{ApprovalPolicy: config.ApprovalPrompt},
		tools:       reg,
		alwaysAllow: map[string]bool{},
	}
	a.AttachPermissions(rules)
	return a
}

// approveAlwaysEmitter returns an emit func that auto-approves any approval
// request and records whether one was ever made.
func approveAlwaysEmitter(asked *bool) func(Event) {
	return func(ev Event) {
		if req, ok := ev.(ApprovalRequest); ok {
			*asked = true
			req.Reply <- Approve
		}
	}
}

func TestApproveToolCallDenyRuleBlocks(t *testing.T) {
	isolateConfig(t)
	rules := permissions.Load(t.TempDir())
	if err := rules.Add("write_file", true, permissions.Project); err != nil {
		t.Fatal(err)
	}
	a := newPermTestAgent(t, rules)

	asked := false
	ok, _ := a.approveToolCall(context.Background(), approveAlwaysEmitter(&asked),
		llm.ContentBlock{ID: "1", Name: "write_file", Input: []byte(`{}`)})
	if ok {
		t.Fatal("deny rule should block the call")
	}
	if asked {
		t.Fatal("deny rule should not prompt the user")
	}
}

func TestApproveToolCallAllowRuleSkipsPrompt(t *testing.T) {
	isolateConfig(t)
	rules := permissions.Load(t.TempDir())
	if err := rules.Add("write_file", false, permissions.Project); err != nil {
		t.Fatal(err)
	}
	a := newPermTestAgent(t, rules)

	asked := false
	ok, _ := a.approveToolCall(context.Background(), approveAlwaysEmitter(&asked),
		llm.ContentBlock{ID: "1", Name: "write_file", Input: []byte(`{}`)})
	if !ok {
		t.Fatal("allow rule should permit the call")
	}
	if asked {
		t.Fatal("allow rule should skip the approval prompt")
	}
}

func TestApproveToolCallNoRuleStillPrompts(t *testing.T) {
	isolateConfig(t)
	rules := permissions.Load(t.TempDir())
	a := newPermTestAgent(t, rules)

	asked := false
	ok, _ := a.approveToolCall(context.Background(), approveAlwaysEmitter(&asked),
		llm.ContentBlock{ID: "1", Name: "write_file", Input: []byte(`{}`)})
	if !ok {
		t.Fatal("auto-approved call should be permitted")
	}
	if !asked {
		t.Fatal("with no rule, the prompt approval policy should still prompt")
	}
}

func TestApproveToolCallBashDenyPrefix(t *testing.T) {
	isolateConfig(t)
	rules := permissions.Load(t.TempDir())
	if err := rules.Add("bash:rm -rf", true, permissions.Project); err != nil {
		t.Fatal(err)
	}
	a := newPermTestAgent(t, rules)

	// A matching bash command is denied...
	asked := false
	ok, _ := a.approveToolCall(context.Background(), approveAlwaysEmitter(&asked),
		llm.ContentBlock{ID: "1", Name: "bash", Input: []byte(`{"command":"rm -rf /tmp/x"}`)})
	if ok || asked {
		t.Fatalf("matching bash command should be denied without prompt (ok=%v asked=%v)", ok, asked)
	}

	// ...but a non-matching one falls through to the prompt.
	asked = false
	ok, _ = a.approveToolCall(context.Background(), approveAlwaysEmitter(&asked),
		llm.ContentBlock{ID: "2", Name: "bash", Input: []byte(`{"command":"ls -la"}`)})
	if !ok || !asked {
		t.Fatalf("non-matching bash command should prompt (ok=%v asked=%v)", ok, asked)
	}
}
