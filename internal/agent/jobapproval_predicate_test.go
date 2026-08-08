package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// BackgroundJobNeedsApproval must agree with what gateBackgroundJob actually does. They
// are separate code paths (one predicts, one enforces), so drift between them is the real
// risk: predicting "no approval" for a command the gate then blocks on would reintroduce
// the deadlock this fix removed.
//
// The gate is driven with emit == nil, which makes it fail fast with "no prompt surface"
// instead of blocking — so that error is a reliable signal that it WOULD have prompted.
func TestJobApprovalPredicateMatchesGate(t *testing.T) {
	commands := []string{
		"echo hello",
		"ls -la",
		"npm run dev",
		"./run_local_wss.sh",
		"git push origin main",
		"cat file.txt",
		"sleep 60",
	}
	policies := []string{
		config.ApprovalPrompt, config.ApprovalSafe, config.ApprovalAuto, config.ApprovalDangerSkip,
	}

	for _, pol := range policies {
		for _, cmd := range commands {
			for _, tty := range []bool{false, true} {
				a := jobAgent(t, pol)

				predicted := a.BackgroundJobNeedsApproval(cmd, tty)

				// emit == nil: the gate refuses rather than blocking when it needs a prompt.
				err := a.gateBackgroundJob(context.Background(), nil, cmd, tty)
				wouldPrompt := err != nil && err.Error() == "approval required but no prompt surface available"

				if predicted != wouldPrompt {
					t.Errorf("policy=%s tty=%v cmd=%q: predicate says %v but the gate %v (err=%v)",
						pol, tty, cmd, predicted, map[bool]string{true: "prompts", false: "does not prompt"}[wouldPrompt], err)
				}
			}
		}
	}
}

// A destructive command must be refused outright, never merely "needs approval" — the
// hard refusal is checked before the prompt, and offering it for confirmation would be a
// downgrade.
func TestDestructiveJobStillHardRefused(t *testing.T) {
	a := jobAgent(t, config.ApprovalDangerSkip) // the most permissive policy

	err := a.gateBackgroundJob(context.Background(), func(Event) {}, "rm -rf /", false)
	if err == nil {
		t.Fatal("a destructive command must be refused even under danger-skip")
	}
	if err.Error() == "approval required but no prompt surface available" {
		t.Errorf("destructive command was offered for approval instead of refused: %v", err)
	}
}
