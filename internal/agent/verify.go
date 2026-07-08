package agent

// verify.go implements the automatic verification loop: after a turn that
// edited files, run a user-configured command (a build or test) and, if it
// fails, feed the failure back into the conversation so the agent self-corrects
// before the turn is declared done. It's the smallest path to "the agent's
// output is trustworthy" — the model no longer just claims it's finished, it has
// to leave the project in a state where the verify command passes (or hit a
// bounded retry cap, after which it stops for human review).
//
// Wiring: maybeVerify is called at the natural end of a turn (no more tool
// calls) in RunWith, mirroring the goal-judge nudge — on failure it appends a
// synthetic user message and the loop continues; on success (or when disabled,
// no edits, or retries exhausted) it returns false and the turn finishes.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

// defaultVerifyTimeout bounds a verification run when the config doesn't set one.
const defaultVerifyTimeout = 120 * time.Second

// verifyResult is the outcome of one verification command run.
type verifyResult struct {
	ok     bool
	output string
}

// runVerify runs the configured verification command in the working directory
// with a timeout, capturing combined stdout+stderr. ok is true on exit 0. It's a
// no-op success when no command is configured.
func (a *Agent) runVerify(ctx context.Context) verifyResult {
	cmdline := strings.TrimSpace(a.cfg.VerifyCommand)
	if cmdline == "" {
		return verifyResult{ok: true}
	}
	timeout := defaultVerifyTimeout
	if a.cfg.VerifyTimeoutSec > 0 {
		timeout = time.Duration(a.cfg.VerifyTimeoutSec) * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "sh", "-c", cmdline)
	cmd.Dir = a.cfg.WorkingDir
	cmd.Env = tools.ScrubbedEnv() // keep auth secrets out of the verify command (parity with bash)
	out, err := cmd.CombinedOutput()
	res := verifyResult{ok: err == nil, output: string(out)}
	if cctx.Err() == context.DeadlineExceeded {
		res.ok = false
		res.output = strings.TrimSpace(res.output) + fmt.Sprintf("\n[verification timed out after %s]", timeout)
	}
	return res
}

// maybeVerify runs the post-turn verification step. It returns true when the
// caller should CONTINUE the turn loop (a failure was fed back for the model to
// fix), or false when the turn should finish (verification disabled, nothing was
// edited this turn, it passed, or the retry cap is reached). attempts is the
// per-turn fix counter, incremented on each fed-back failure.
func (a *Agent) maybeVerify(ctx context.Context, emit func(Event), attempts *int) bool {
	if strings.TrimSpace(a.cfg.VerifyCommand) == "" {
		return false
	}
	// Only verify when this turn actually changed code, so plain Q&A turns don't
	// pay for a build/test run.
	if a.snapshots == nil || !a.snapshots.mutatedThisTurn() {
		return false
	}
	maxRetries := a.cfg.VerifyMaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	emit(Info{Text: "Verifying changes: " + a.cfg.VerifyCommand})
	res := a.runVerify(ctx)
	if res.ok {
		emit(Info{Text: "Verification passed."})
		return false
	}
	if *attempts >= maxRetries {
		emit(Info{Text: fmt.Sprintf("Verification still failing after %d fix attempt(s) — stopping so you can review. (Run the command yourself: %s)", *attempts, a.cfg.VerifyCommand)})
		return false
	}
	*attempts++
	a.turnFriction.Add(1) // a failing verification is friction: the turn hasn't converged
	emit(Info{Text: fmt.Sprintf("Verification failed (fix attempt %d/%d) — feeding the errors back.", *attempts, maxRetries)})
	a.messages = append(a.messages, verifyFeedbackMessage(a.cfg.VerifyCommand, capOutput(res.output, a.cfg.MaxToolOutputBytes)))
	a.persist()
	return true
}

// verifyFeedbackMessage builds the synthetic user turn that reports a failing
// verification back to the model so it fixes the problem before finishing.
func verifyFeedbackMessage(cmd, output string) llm.Message {
	return llm.UserText(fmt.Sprintf(
		"[automatic verification] After your changes, the verification command failed:\n\n"+
			"$ %s\n\n```\n%s\n```\n\n"+
			"Fix the underlying problem (don't just silence it), then finish. "+
			"Do not ask the user to confirm; only stop once it passes.",
		cmd, strings.TrimSpace(output)))
}
