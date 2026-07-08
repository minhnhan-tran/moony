// /btw side-question support: a one-shot LLM call that uses the running
// conversation as read-only context but does NOT mutate the agent's history
// or session. See internal/agent/btw.go for the agent-side implementation.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// btwCmd dispatches an Agent.AskSide call on a goroutine and returns its
// result as a btwDoneMsg. The agent's main message history is left untouched.
func (m Model) btwCmd(q string) tea.Cmd {
	ag := m.agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ans, err := ag.AskSide(ctx, q)
		return btwDoneMsg{question: q, answer: ans, err: err}
	}
}
