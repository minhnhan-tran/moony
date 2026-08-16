package web

import (
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/llm"
)

// The live "user" event must carry the send time. A turn has no persisted TurnMeta until it
// completes (recordTurnMeta runs at the end), so a browser that connects or RELOADS mid-turn
// has no other source for when the question was asked. Without this stamp the client falls
// back to the page-load instant and the turn footer reports time since RELOAD rather than
// turn elapsed — the "09:18 · 95ms · 40 calls" symptom, where 95ms was the age of the page.
func TestLiveUserEventCarriesSendTime(t *testing.T) {
	sess := newSession("id", "t", nil)

	before := time.Now().Add(-time.Second)
	// startWith stamps and publishes the user event before dispatching to the agent, so the
	// nil agent here is never reached (the goroutine blocks on MCP readiness / returns).
	if !sess.startWith("a question", []llm.ContentBlock{llm.TextBlock("a question")}, nil, turnOverride{}) {
		t.Fatal("startWith refused an idle session")
	}
	after := time.Now().Add(time.Second)

	_, _, hist := sess.subscribe()
	var user *wireEvent
	for i := range hist {
		if hist[i].Type == "user" {
			user = &hist[i]
			break
		}
	}
	if user == nil {
		t.Fatal("no user event in history")
	}
	if user.Time == "" {
		t.Fatal("live user event has no Time; a client reloading mid-turn cannot date the question and will use the page-load time")
	}
	sent, err := time.Parse(time.RFC3339, user.Time)
	if err != nil {
		t.Fatalf("user event Time %q is not RFC3339: %v", user.Time, err)
	}
	if sent.Before(before) || sent.After(after) {
		t.Errorf("user event stamped %v, want ~now (between %v and %v)", sent, before, after)
	}
	// A live send must NOT be flagged as replay — that flag means "rebuilt from history",
	// and the client uses it to decide whether it may fall back to its own clock.
	if user.Replay {
		t.Error("live user event marked Replay; only events rebuilt from persisted history may be")
	}
}
