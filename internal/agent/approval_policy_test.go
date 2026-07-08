package agent

import (
	"encoding/json"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

func newApprovalTestAgent(t *testing.T, policy string) *Agent {
	t.Helper()
	var z int32
	reg := tools.NewRegistry(t.TempDir())
	reg.Register(&recordingTool{name: "read_file", mutates: false, active: &z, maxSeen: &z, soloFail: &z})
	reg.Register(&recordingTool{name: "write_file", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	reg.Register(&recordingTool{name: "bash", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	reg.Register(&recordingTool{name: "task", mutates: true, active: &z, maxSeen: &z, soloFail: &z})
	reg.Register(&recordingTool{name: "todo_write", mutates: false, active: &z, maxSeen: &z, soloFail: &z})
	return &Agent{
		cfg:         &config.Config{ApprovalPolicy: policy},
		tools:       reg,
		alwaysAllow: map[string]bool{},
	}
}

func TestNeedsApprovalPolicies(t *testing.T) {
	bashSafe := json.RawMessage(`{"command":"ls -la"}`)
	bashUnsafe := json.RawMessage(`{"command":"rm -rf build"}`)
	none := json.RawMessage(`{}`)

	type call struct {
		name  string
		input json.RawMessage
	}
	cases := []struct {
		policy string
		call   call
		want   bool
	}{
		// prompt: every mutating tool prompts; reads never do.
		{config.ApprovalPrompt, call{"read_file", none}, false},
		{config.ApprovalPrompt, call{"todo_write", none}, false},
		{config.ApprovalPrompt, call{"write_file", none}, true},
		{config.ApprovalPrompt, call{"bash", bashSafe}, true},
		{config.ApprovalPrompt, call{"bash", bashUnsafe}, true},
		{config.ApprovalPrompt, call{"task", none}, true},

		// safe: read-only bash auto-runs; writes/task and unsafe bash still prompt.
		{config.ApprovalSafe, call{"read_file", none}, false},
		{config.ApprovalSafe, call{"bash", bashSafe}, false},
		{config.ApprovalSafe, call{"bash", bashUnsafe}, true},
		{config.ApprovalSafe, call{"write_file", none}, true},
		{config.ApprovalSafe, call{"task", none}, true},

		// auto: nothing prompts.
		{config.ApprovalAuto, call{"write_file", none}, false},
		{config.ApprovalAuto, call{"bash", bashUnsafe}, false},
		{config.ApprovalAuto, call{"task", none}, false},

		// empty policy normalizes to prompt.
		{"", call{"write_file", none}, true},
	}

	for _, tc := range cases {
		a := newApprovalTestAgent(t, tc.policy)
		if got := a.needsApproval(tc.call.name, tc.call.input); got != tc.want {
			t.Errorf("policy=%q tool=%q → needsApproval=%v, want %v", tc.policy, tc.call.name, got, tc.want)
		}
	}
}

func TestBashCommandField(t *testing.T) {
	if got := bashCommandField(json.RawMessage(`{"command":"go test ./..."}`)); got != "go test ./..." {
		t.Errorf("bashCommandField = %q", got)
	}
	if got := bashCommandField(json.RawMessage(`not json`)); got != "" {
		t.Errorf("bad input should yield empty, got %q", got)
	}
}
