package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/config"
)

// User-initiated background jobs (the web panel's "New job" button).
//
// The model starts background jobs through the bash tool, which is wrapped in the
// approval system. A UI-started job must pass the SAME gates, or the panel becomes an
// unapproved remote shell: a session is reachable by other members on a hosted relay,
// so "someone clicked it" is not by itself proof the workspace owner intended it.
//
// This mirrors the tool path deliberately rather than reimplementing it:
//   - same destructive-command classifier, so `rm -rf /` is refused outright
//   - same policy check (needsApproval), so `auto`/`danger-skip` still don't prompt
//   - same approval event, so the existing web/TUI approval UI handles it unchanged
//   - same registry sandbox + roots, so a UI job is confined exactly like a model one

// ErrJobDenied is returned when the user declines the approval prompt.
var ErrJobDenied = fmt.Errorf("background job denied")

// StartBackgroundJobApproved validates and starts a user-requested background job,
// returning its id.
//
// emit delivers the approval request to whichever surface is driving (the web session,
// the TUI); it blocks until the user answers or ctx is cancelled.
func (a *Agent) StartBackgroundJobApproved(ctx context.Context, emit func(Event), command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if a == nil || a.tools == nil {
		return "", fmt.Errorf("no tool registry available")
	}
	if err := a.gateBackgroundJob(ctx, emit, command); err != nil {
		return "", err
	}
	id := a.tools.StartBackgroundJob(command)
	if id == "" {
		return "", fmt.Errorf("failed to start background job")
	}
	return id, nil
}

// ApprovalPolicyName reports the agent's normalized approval policy, so a surface can
// tell the user up front whether starting a job will prompt.
func (a *Agent) ApprovalPolicyName() string {
	if a == nil || a.cfg == nil {
		return config.ApprovalPrompt
	}
	return config.NormalizeApprovalPolicy(a.cfg.ApprovalPolicy)
}

// gateBackgroundJob applies every check a UI-started background job must pass before
// anything runs: the destructive-command refusal, the approval policy, and the prompt.
func (a *Agent) gateBackgroundJob(ctx context.Context, emit func(Event), command string) error {
	// Shape the command as a bash tool call so the existing classifiers see exactly
	// what they see for a model-issued call — no second, subtly different parser.
	input, err := json.Marshal(map[string]any{"command": command, "background": true})
	if err != nil {
		return err
	}

	// Hard refusal, checked BEFORE the approval prompt: destructive commands are
	// blocked outright rather than offered for confirmation. The model path can weigh a
	// destructive request against what the user asked for in conversation; a bare
	// command typed into a box carries no such context, so there is nothing to justify
	// it and the safe default is to refuse.
	if kind, bad := destructiveCommand("bash", input); bad {
		return fmt.Errorf("refusing to start a destructive command as a background job (%s); run it explicitly in a turn if you really mean it", kind)
	}

	// Same policy the bash tool obeys, with two additions:
	//
	// `safe` must NOT skip the prompt. needsApproval's safe branch only inspects the
	// "command" field, so it can't see that we're backgrounding — it would wave through
	// `ls` as read-only. Detaching changes the risk: the process outlives the turn and
	// keeps running after the session ends, which the read-only classifier never
	// reasoned about. So under `safe` we always confirm.
	//
	// hardApproval is checked separately because needsApproval doesn't cover it: the
	// tool path applies the hard gate before consulting policy, so without this a
	// `git push` under `auto` would slip through unconfirmed via this route — exactly
	// the bypass that gate exists to prevent.
	safePolicy := a.ApprovalPolicyName() == config.ApprovalSafe
	if a.needsApproval("bash", input) || safePolicy || hardApproval("bash", input) {
		if emit == nil {
			// No surface to ask on: refuse rather than silently running unapproved.
			return fmt.Errorf("approval required but no prompt surface available")
		}
		summary := "Start background job: " + command
		switch a.requestApproval(ctx, emit, "bash", summary, "") {
		case Approve, ApproveAlways:
			// proceed
		default:
			return ErrJobDenied
		}
	}
	return nil
}
