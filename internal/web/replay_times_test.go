package web

import (
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/llm"
	sessionpkg "github.com/pysty1995/moony/internal/session"
)

// A replayed question must be dated from its turn's START, and a question whose turn
// cannot be dated at all must say so rather than be left to the client's clock.
//
// session.TurnMeta.Time is stamped at turn COMPLETION (recordTurnMeta runs after the
// answer lands), so reusing it for the question that OPENED the turn dates the question
// to the answer's finish time — late by the whole turn duration and identical to the turn
// footer right below it, making a long exchange replay as if it happened in one instant.
func TestReplayStampsQuestionsAtTurnStart(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("first")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("answer one")}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("second")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("answer two")}},
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	metas := []sessionpkg.TurnMeta{
		{Time: base.Add(20 * time.Minute), DurationMS: 20 * 60 * 1000, MsgCount: 2},
		{Time: base.Add(35 * time.Minute), DurationMS: 60 * 1000, MsgCount: 4},
	}

	got := replayMessages(msgs, metas, nil, nil, nil)

	var users []wireEvent
	for _, e := range got {
		if e.Type == "user" {
			users = append(users, e)
		}
	}
	if len(users) != 2 {
		t.Fatalf("want 2 user events, got %d", len(users))
	}

	for i, tc := range metas {
		asked, err := time.Parse(time.RFC3339, users[i].Time)
		if err != nil {
			t.Fatalf("user %d time %q unparseable: %v", i, users[i].Time, err)
		}
		want := tc.Time.Add(-time.Duration(tc.DurationMS) * time.Millisecond)
		if !asked.Equal(want) {
			t.Errorf("user %d asked at %v, want turn start %v (end %v - %dms)", i, asked, want, tc.Time, tc.DurationMS)
		}
		// The question must precede its own answer — the invariant the bug violated.
		if !asked.Before(tc.Time) {
			t.Errorf("user %d asked at %v, not before its answer at %v", i, asked, tc.Time)
		}
	}

	// Turns must not overlap: turn 2's question comes after turn 1's answer.
	first, _ := time.Parse(time.RFC3339, users[0].Time)
	second, _ := time.Parse(time.RFC3339, users[1].Time)
	if !second.After(metas[0].Time) {
		t.Errorf("turn 2 asked at %v, before turn 1 finished at %v", second, metas[0].Time)
	}
	if !first.Before(second) {
		t.Errorf("questions out of order: %v then %v", first, second)
	}
}

// A turn with no persisted TurnMeta cannot be dated: an interrupted turn returns from the
// step loop before recordTurnMeta runs, so its user message persists with no metadata.
// Such a question must be flagged Replay with an EMPTY Time so the client renders no clock,
// instead of falling back to the page-load instant and dating old history to "now".
//
// The datable turn is asserted alongside it so the flag can't be handed out indiscriminately.
func TestReplayFlagsUndatableQuestions(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("finished question")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("answered")}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("interrupted question")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("partial")}},
	}
	end := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	// Only the FIRST turn completed, so only it has a TurnMeta (MsgCount 2).
	metas := []sessionpkg.TurnMeta{{Time: end, DurationMS: 90 * 1000, MsgCount: 2}}

	var users []wireEvent
	for _, e := range replayMessages(msgs, metas, nil, nil, nil) {
		if e.Type == "user" {
			users = append(users, e)
		}
	}
	if len(users) != 2 {
		t.Fatalf("want 2 user events, got %d", len(users))
	}

	// Every replayed question is flagged, so the client never mistakes one for a live send.
	for i, u := range users {
		if !u.Replay {
			t.Errorf("user %d not flagged Replay; the client cannot tell replayed history from a live send", i)
		}
	}
	if users[0].Time == "" {
		t.Error("the completed turn's question lost its time — the flag must not suppress a KNOWN time")
	}
	if users[1].Time != "" {
		t.Errorf("the interrupted turn's question was stamped %q; it has no TurnMeta, so its time is unknowable and must be empty", users[1].Time)
	}
}

// A record with no duration (older sessions, or a skewed clock) must fall back to the turn's
// recorded time rather than subtracting a bogus value — a question must never be stamped
// AFTER the answer it opened. A zero duration alone can't prove the guard works (Add(0) is
// indistinguishable from skipping the subtraction), so a NEGATIVE duration is tested too.
func TestReplayQuestionNeverAfterItsAnswer(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("q")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("a")}},
	}
	end := time.Now().Add(-time.Hour).Truncate(time.Second)

	for _, dur := range []int64{0, -5000} {
		metas := []sessionpkg.TurnMeta{{Time: end, DurationMS: dur, MsgCount: 2}}
		var u wireEvent
		for _, e := range replayMessages(msgs, metas, nil, nil, nil) {
			if e.Type == "user" {
				u = e
			}
		}
		asked, err := time.Parse(time.RFC3339, u.Time)
		if err != nil {
			t.Fatalf("duration %d: time %q unparseable: %v", dur, u.Time, err)
		}
		if asked.After(end) {
			t.Errorf("duration %d: question stamped %v, AFTER its answer at %v", dur, asked, end)
		}
	}
}
