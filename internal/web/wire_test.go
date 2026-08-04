package web

import (
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/agent"
)

// An agent event kind that toWire doesn't map must degrade to the inert "unknown"
// type, never a text-less "info": the browser builds an info bubble from ev.text, so
// a missing Text renders the literal string "undefined" (and throws on
// ev.text.startsWith, taking the whole event handler down).
func TestCompactionEventCarriesText(t *testing.T) {
	// agent.Compaction is the case that actually shipped broken: compact.go emits it,
	// toWire had no branch for it, so it reached the browser as a text-less "info".
	w := toWire(agent.Compaction{Turns: 3})
	if w.Text == "" {
		t.Error("agent.Compaction produced an event with no Text (renders as \"undefined\")")
	}
	if !strings.Contains(w.Text, "3") {
		t.Errorf("Text = %q, want it to mention the 3 compacted turns", w.Text)
	}
}

// Every remaining unmapped kind must degrade to the inert "unknown" type rather than
// a text-less "info", so a future event added to the agent can't reintroduce the
// "undefined" bubble (or throw on ev.text.startsWith in the browser).
func TestUnmappedAgentEventDegradesToInertType(t *testing.T) {
	w := toWire(nil) // no case matches, exercising the default branch
	if w.Type == "info" {
		t.Error("unmapped agent event became a text-less \"info\" (renders as \"undefined\")")
	}
	if w.Type != "unknown" {
		t.Errorf("Type = %q, want \"unknown\"", w.Type)
	}
}
