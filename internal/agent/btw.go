// Side-question ("by the way") support: a one-shot LLM call that uses the
// current conversation as read-only context but does NOT mutate the agent's
// message history or session. Inspired by Claude Code's /btw — lets the user
// ask a quick question without polluting the main thread.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// AskSide answers a brief side-question using the current conversation as
// context. The agent's persistent state (a.messages, session DB) is left
// untouched: the question and answer never become part of the main thread.
//
// Implementation mirrors summarize() — it uses the cheap aux model with
// extended thinking off, and runs without tools so it can't make changes.
func (a *Agent) AskSide(ctx context.Context, question string) (string, error) {
	q := strings.TrimSpace(question)
	if q == "" {
		return "", fmt.Errorf("empty question")
	}

	const sys = "You are answering a quick SIDE QUESTION from the user. " +
		"The prior conversation between the user and a coding agent is provided " +
		"only as background context — DO NOT continue, plan, or act on it. " +
		"Answer ONLY the side question, briefly and conversationally. " +
		"You have no tools; if you cannot answer from context, say so."

	// Snapshot the history so concurrent edits (there shouldn't be any —
	// turns are serialized — but be defensive) can't race with the slice we
	// hand to the LLM client.
	prior := append([]llm.Message(nil), a.messages...)
	msgs := append(prior, llm.UserText("[side question] "+q))

	resp, err := a.streamAux(ctx, sys, msgs)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, b := range resp.Blocks {
		if b.Type == llm.BlockText {
			out.WriteString(b.Text)
		}
	}
	answer := strings.TrimSpace(out.String())
	if answer == "" {
		return "", fmt.Errorf("empty answer")
	}
	return answer, nil
}
