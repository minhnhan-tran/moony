package agent

import (
	"context"
	"testing"

	"github.com/pysty1995/moony/internal/tools"
)

// TestRequestQuestionRoundTrip checks requestQuestion emits a QuestionRequest
// carrying the options and blocks until the host replies on its channel.
func TestRequestQuestionRoundTrip(t *testing.T) {
	a := &Agent{}

	gotReq := make(chan QuestionRequest, 1)
	emit := func(ev Event) {
		if q, ok := ev.(QuestionRequest); ok {
			gotReq <- q
		}
	}

	result := make(chan []string, 1)
	go func() {
		ans, err := a.requestQuestion(context.Background(), emit, tools.AskRequest{
			Question: "Which?",
			Options:  []tools.AskOption{{Label: "A", Description: "first"}, {Label: "B"}},
			Multiple: true,
		})
		if err != nil {
			t.Errorf("requestQuestion: %v", err)
		}
		result <- ans
	}()

	req := <-gotReq
	if req.Question != "Which?" || len(req.Options) != 2 || !req.Multiple {
		t.Fatalf("emitted request wrong: %+v", req)
	}
	if req.Options[0].Label != "A" || req.Options[0].Description != "first" {
		t.Fatalf("options not mapped: %+v", req.Options)
	}
	req.Reply <- []string{"A", "B"}

	ans := <-result
	if len(ans) != 2 || ans[0] != "A" || ans[1] != "B" {
		t.Fatalf("answer not returned: %v", ans)
	}
}

// TestRequestQuestionCancel checks a cancelled context unblocks the ask with an
// error instead of hanging.
func TestRequestQuestionCancel(t *testing.T) {
	a := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.requestQuestion(ctx, func(Event) {}, tools.AskRequest{
		Question: "q", Options: []tools.AskOption{{Label: "a"}},
	})
	if err == nil {
		t.Fatal("expected an error on a cancelled context")
	}
}

// TestAskUserNotParallelSafe ensures ask_user runs on the serial path so two
// questions can never prompt at once.
func TestAskUserNotParallelSafe(t *testing.T) {
	a := &Agent{tools: tools.NewRegistry(t.TempDir())}
	if a.parallelSafe("ask_user") {
		t.Fatal("ask_user must not be parallel-safe")
	}
}
