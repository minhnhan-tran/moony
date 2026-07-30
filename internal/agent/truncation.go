package agent

import (
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// maxTruncationRetries bounds how many times, per turn, a response that was
// cut off by the model's output-length cap (StopReason == "max_tokens") with
// no tool call is retried before the turn gives up.
//
// Without this, a response that spends its whole output budget on
// "thinking" (common for adaptive-thinking models at high reasoning effort,
// which share one token budget between thinking and the answer/tool call —
// see buildThinking in internal/llm/client.go) and never reaches the tool
// call is indistinguishable from a deliberate end_turn. The turn loop would
// stop silently, and the user typing "continue" would just replay the exact
// same doomed round forever.
const maxTruncationRetries = 2

// truncationNudgeMessage builds the synthetic user turn that asks the model
// to retry, tersely, after its previous response was cut off by the output
// length limit before it produced a tool call (or otherwise finished).
func truncationNudgeMessage(attempt, max int) llm.Message {
	return llm.UserText(fmt.Sprintf(
		"[automatic retry] Your previous response was cut off by the output length "+
			"limit before you finished — no tool call was made. Keep any explanation "+
			"brief and get straight to the action (e.g. call the tool) so it fits "+
			"within the limit this time. (retry %d/%d)", attempt, max))
}

// minAnswerTextForContinue is the minimum amount of visible answer text (in
// bytes) a cut-off response must contain for the turn to CONTINUE it (finish a
// long answer) rather than treat the cut-off as the reasoning-ate-the-whole-
// budget-before-a-tool case handled by truncationNudgeMessage.
const minAnswerTextForContinue = 40

// maxAnswerContinues bounds how many times a cut-off answer is auto-continued,
// so an unusually long reply finishes without looping unboundedly.
const maxAnswerContinues = 4

// answerBlockLen returns the total length of the visible answer text (text
// blocks only) across a response's content blocks. It tells a cut-off LONG
// ANSWER (lots of output text) from a cut-off that produced no output because
// reasoning consumed the whole budget before any action.
func answerBlockLen(blocks []llm.ContentBlock) int {
	n := 0
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			n += len(strings.TrimSpace(b.Text))
		}
	}
	return n
}

// answerContinueMessage builds the synthetic user turn that asks the model to
// finish a long answer cut off mid-output by the length limit.
func answerContinueMessage(attempt, max int) llm.Message {
	return llm.UserText(fmt.Sprintf(
		"[automatic continuation] Your previous message was cut off by the output "+
			"length limit mid-answer. Continue EXACTLY where you left off — pick up "+
			"mid-sentence if needed — without repeating anything you already wrote and "+
			"without any preamble. (continuation %d/%d)", attempt, max))
}
