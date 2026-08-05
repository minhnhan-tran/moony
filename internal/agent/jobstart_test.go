package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// jobAgent builds an agent with a real registry so StartBackgroundJobApproved can
// actually reach the job store.
func jobAgent(t *testing.T, policy string) *Agent {
	t.Helper()
	dir := t.TempDir()
	return &Agent{
		cfg:   &config.Config{ApprovalPolicy: policy, WorkingDir: dir},
		tools: tools.NewRegistry(dir),
	}
}

// approver returns an emit func that answers every approval request with d, plus a
// counter of how many prompts were raised.
func approver(d Decision) (func(Event), *int) {
	n := 0
	return func(e Event) {
		if req, ok := e.(ApprovalRequest); ok {
			n++
			req.Reply <- d
		}
	}, &n
}

// The panel must not become an unapproved remote shell: a session is reachable by other
// members on a hosted relay, so a click is not proof the workspace owner intended it.
func TestStartBackgroundJobRequiresApproval(t *testing.T) {
	a := jobAgent(t, config.ApprovalPrompt)
	emit, prompts := approver(Approve)

	id, err := a.StartBackgroundJobApproved(context.Background(), emit, "echo hello", false)
	if err != nil {
		t.Fatalf("StartBackgroundJobApproved: %v", err)
	}
	if id == "" {
		t.Fatal("expected a job id")
	}
	if *prompts != 1 {
		t.Errorf("prompts = %d, want exactly 1", *prompts)
	}
}

// A declined prompt must not start anything.
func TestStartBackgroundJobDenied(t *testing.T) {
	a := jobAgent(t, config.ApprovalPrompt)
	emit, _ := approver(Deny)

	id, err := a.StartBackgroundJobApproved(context.Background(), emit, "echo hello", false)
	if !errors.Is(err, ErrJobDenied) {
		t.Fatalf("err = %v, want ErrJobDenied", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty on denial", id)
	}
	// And nothing may have been queued.
	if jobs := a.tools.Jobs().Jobs(); len(jobs) != 0 {
		t.Errorf("a denied job must not exist, got %d", len(jobs))
	}
}

// Destructive commands are refused OUTRIGHT, not offered for confirmation: a bare
// command typed into a box carries none of the conversational context that could
// justify it, so there is nothing to weigh and the safe default is to refuse.
func TestStartBackgroundJobRefusesDestructive(t *testing.T) {
	// Only the kinds the classifier actually recognises. A fork bomb is NOT on this
	// list — neither this codebase nor upstream classifies one — but it is still
	// approval-gated (see TestUnclassifiedDangerousCommandStillPrompts), which is the
	// protection that matters for an unattended click.
	for _, cmd := range []string{
		"rm -rf /",
		"rm -rf /*",
		"git reset --hard HEAD~5",
		"terraform destroy -auto-approve",
	} {
		a := jobAgent(t, config.ApprovalPrompt)
		// Approve everything — the point is that approval is never even reached.
		emit, prompts := approver(ApproveAlways)
		_, err := a.StartBackgroundJobApproved(context.Background(), emit, cmd, false)
		if err == nil {
			t.Errorf("%q: expected an outright refusal", cmd)
			continue
		}
		if errors.Is(err, ErrJobDenied) {
			t.Errorf("%q: must be refused, not merely denied by prompt", cmd)
		}
		if !strings.Contains(err.Error(), "destructive") {
			t.Errorf("%q: unexpected error %v", cmd, err)
		}
		if *prompts != 0 {
			t.Errorf("%q: raised %d prompts; a destructive command must never be offered for confirmation", cmd, *prompts)
		}
	}
}

// Even danger-skip must not run a destructive command through this route: the refusal
// sits ahead of any policy consultation.
func TestDangerSkipStillRefusesDestructive(t *testing.T) {
	a := jobAgent(t, config.ApprovalDangerSkip)
	_, err := a.StartBackgroundJobApproved(context.Background(), func(Event) {}, "rm -rf /", false)
	if err == nil || !strings.Contains(err.Error(), "destructive") {
		t.Errorf("err = %v, want a destructive refusal even under danger-skip", err)
	}
}

// The `safe` policy must still confirm a background job.
//
// needsApproval's safe branch only inspects the command text, so it would wave through
// `ls` as read-only — but detaching changes the risk: the process outlives the turn and
// keeps running after the session ends, which that classifier never reasoned about.
func TestSafePolicyStillPromptsForBackgroundJob(t *testing.T) {
	a := jobAgent(t, config.ApprovalSafe)
	emit, prompts := approver(Approve)

	if _, err := a.StartBackgroundJobApproved(context.Background(), emit, "ls", false); err != nil {
		t.Fatalf("StartBackgroundJobApproved: %v", err)
	}
	if *prompts != 1 {
		t.Errorf("prompts = %d, want 1: a read-only-looking command still detaches", *prompts)
	}
}

// A hard-approval command must not slip through unconfirmed under `auto`.
//
// The tool path applies the hard gate BEFORE consulting policy, so checking only
// needsApproval here would let `git push` run unprompted via this route — exactly the
// bypass the hard gate exists to prevent.
func TestAutoPolicyStillPromptsForHardApprovalCommand(t *testing.T) {
	a := jobAgent(t, config.ApprovalAuto)
	emit, prompts := approver(Approve)

	if _, err := a.StartBackgroundJobApproved(context.Background(), emit, "git push origin main", false); err != nil {
		t.Fatalf("StartBackgroundJobApproved: %v", err)
	}
	if *prompts != 1 {
		t.Errorf("prompts = %d, want 1: hard-approval commands bypass the policy shortcut", *prompts)
	}
}

// An ordinary command under `auto` should not prompt, or the policy would be pointless.
func TestAutoPolicySkipsPromptForOrdinaryCommand(t *testing.T) {
	a := jobAgent(t, config.ApprovalAuto)
	emit, prompts := approver(Deny) // would fail the start if consulted

	if _, err := a.StartBackgroundJobApproved(context.Background(), emit, "echo hi", false); err != nil {
		t.Fatalf("StartBackgroundJobApproved: %v", err)
	}
	if *prompts != 0 {
		t.Errorf("prompts = %d, want 0 under auto for an ordinary command", *prompts)
	}
}

// With no surface to prompt on, refuse rather than run unapproved.
func TestStartBackgroundJobWithoutPromptSurface(t *testing.T) {
	a := jobAgent(t, config.ApprovalPrompt)
	_, err := a.StartBackgroundJobApproved(context.Background(), nil, "echo hi", false)
	if err == nil {
		t.Fatal("expected a refusal when there is nowhere to ask")
	}
	if strings.Contains(err.Error(), "started") {
		t.Errorf("unexpected error %v", err)
	}
}

func TestStartBackgroundJobValidation(t *testing.T) {
	a := jobAgent(t, config.ApprovalAuto)
	for _, cmd := range []string{"", "   ", "\t\n"} {
		if _, err := a.StartBackgroundJobApproved(context.Background(), func(Event) {}, cmd, false); err == nil {
			t.Errorf("%q: expected a validation error", cmd)
		}
	}
	// A registry-less agent must report an error, not panic.
	bare := &Agent{cfg: &config.Config{ApprovalPolicy: config.ApprovalAuto}}
	if _, err := bare.StartBackgroundJobApproved(context.Background(), func(Event) {}, "echo hi", false); err == nil {
		t.Error("expected an error with no tool registry")
	}
}

// A UI-started job must be confined exactly like a model-issued one. The registry
// resolves the sandbox and roots itself, so a web handler cannot widen a job's
// authority by passing its own.
func TestStartBackgroundJobUsesRegistrySandbox(t *testing.T) {
	src := readAgentSource(t, "jobstart.go")
	if strings.Contains(src, "sandboxConfig()") || strings.Contains(src, "allRoots()") {
		t.Error("the agent must not resolve sandbox/roots itself; the registry owns them")
	}
	if !strings.Contains(src, "a.tools.StartBackgroundJob(command, tty)") {
		t.Error("start must go through the registry so it inherits the registry's confinement")
	}
	// The gate must run before the start, or the checks are decorative.
	iGate := strings.Index(src, "a.gateBackgroundJob(ctx, emit, command, tty)")
	iStart := strings.Index(src, "a.tools.StartBackgroundJob(command, tty)")
	if iGate < 0 || iStart < 0 || iGate > iStart {
		t.Error("gateBackgroundJob must run before the job is started")
	}
}

func TestApprovalPolicyName(t *testing.T) {
	if got := jobAgent(t, config.ApprovalAuto).ApprovalPolicyName(); got != config.ApprovalAuto {
		t.Errorf("got %q, want %q", got, config.ApprovalAuto)
	}
	// A nil agent must report the SAFEST default, not the most permissive: a caller
	// deciding whether to warn the user must never be told "this won't prompt" wrongly.
	var nilAgent *Agent
	if got := nilAgent.ApprovalPolicyName(); got != config.ApprovalPrompt {
		t.Errorf("nil agent = %q, want the conservative %q", got, config.ApprovalPrompt)
	}
}

// A dangerous command the classifier does not recognise (e.g. a fork bomb) must still
// be gated by the approval prompt. The classifier is a hard REFUSAL list, not the only
// line of defence — anything unrecognised falls through to "ask the user".
func TestUnclassifiedDangerousCommandStillPrompts(t *testing.T) {
	a := jobAgent(t, config.ApprovalPrompt)
	emit, prompts := approver(Deny)

	_, err := a.StartBackgroundJobApproved(context.Background(), emit, ":(){ :|:& };:", false)
	if !errors.Is(err, ErrJobDenied) {
		t.Fatalf("err = %v, want the prompt to gate it and the denial to stop it", err)
	}
	if *prompts != 1 {
		t.Errorf("prompts = %d, want 1: an unclassified command must still be confirmed", *prompts)
	}
}
