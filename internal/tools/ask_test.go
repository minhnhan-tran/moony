package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAskUserToolBasics(t *testing.T) {
	tool := &askUserTool{}
	if tool.Name() != "ask_user" {
		t.Fatalf("name = %q", tool.Name())
	}
	if tool.Mutates() {
		t.Fatal("ask_user must be non-mutating")
	}
	if len(tool.Schema()) == 0 || !json.Valid(tool.Schema()) {
		t.Fatal("schema should be valid JSON")
	}
}

func TestAskUserValidation(t *testing.T) {
	tool := &askUserTool{}
	// An asker is present so we only exercise input validation, not the round-trip.
	ctx := WithUserAsker(context.Background(), func(context.Context, AskRequest) ([]string, error) {
		return []string{"unused"}, nil
	})

	cases := map[string]string{
		"empty question":     `{"question":"  ","options":[{"label":"a"}]}`,
		"no options no text": `{"question":"q","options":[]}`,
		"blank labels":       `{"question":"q","options":[{"label":"  "}]}`,
		"duplicate":          `{"question":"q","options":[{"label":"a"},{"label":"a"}]}`,
	}
	for name, in := range cases {
		if _, err := tool.Execute(ctx, json.RawMessage(in)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAskUserTooManyOptions(t *testing.T) {
	tool := &askUserTool{}
	ctx := WithUserAsker(context.Background(), func(context.Context, AskRequest) ([]string, error) {
		return []string{"x"}, nil
	})
	var opts []string
	for i := 0; i < maxAskOptions+1; i++ {
		opts = append(opts, `{"label":"opt`+string(rune('a'+i))+`"}`)
	}
	in := `{"question":"q","options":[` + strings.Join(opts, ",") + `]}`
	if _, err := tool.Execute(ctx, json.RawMessage(in)); err == nil {
		t.Fatal("expected too-many-options error")
	}
}

func TestAskUserNoAsker(t *testing.T) {
	tool := &askUserTool{}
	// No asker in the context (headless/sub-agent/test): the tool reports it's
	// unavailable rather than hanging.
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"q","options":[{"label":"a"}]}`))
	if err == nil {
		t.Fatal("expected an unavailable error without an asker")
	}
}

func TestAskUserRoundTrip(t *testing.T) {
	tool := &askUserTool{}
	var gotReq AskRequest
	ctx := WithUserAsker(context.Background(), func(_ context.Context, req AskRequest) ([]string, error) {
		gotReq = req
		return []string{"Postgres"}, nil
	})

	out, err := tool.Execute(ctx, json.RawMessage(`{
		"question": "Which database?",
		"options": [{"label":"Postgres","description":"relational"},{"label":"SQLite"}]
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotReq.Question != "Which database?" || len(gotReq.Options) != 2 {
		t.Fatalf("asker got wrong request: %+v", gotReq)
	}
	if gotReq.Options[0].Description != "relational" {
		t.Fatalf("option description not passed: %+v", gotReq.Options[0])
	}
	if !strings.Contains(out, "Postgres") {
		t.Fatalf("result should echo the choice, got %q", out)
	}
}

func TestAskUserDismissed(t *testing.T) {
	tool := &askUserTool{}
	ctx := WithUserAsker(context.Background(), func(context.Context, AskRequest) ([]string, error) {
		return nil, nil // user dismissed without choosing
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{"question":"q","options":[{"label":"a"}]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "dismissed") {
		t.Fatalf("dismissal should be reported, got %q", out)
	}
}

func TestAskUserMultiple(t *testing.T) {
	tool := &askUserTool{}
	ctx := WithUserAsker(context.Background(), func(_ context.Context, req AskRequest) ([]string, error) {
		if !req.Multiple {
			t.Error("multiple flag not propagated")
		}
		return []string{"a", "c"}, nil
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{
		"question":"pick some","multiple":true,
		"options":[{"label":"a"},{"label":"b"},{"label":"c"}]
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "a, c") {
		t.Fatalf("multi-select result wrong: %q", out)
	}
}

// A combined answer — chosen options PLUS a typed free-text note — is reported as
// both parts, not with the typed text mislabeled as a selection.
func TestAskUserMultipleWithText(t *testing.T) {
	tool := &askUserTool{}
	ctx := WithUserAsker(context.Background(), func(_ context.Context, _ AskRequest) ([]string, error) {
		return []string{"a", "c", "but prefer none of these"}, nil // 2 options + 1 typed
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{
		"question":"pick some","multiple":true,"allow_text":true,
		"options":[{"label":"a"},{"label":"b"},{"label":"c"}]
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "selected: a, c") {
		t.Errorf("expected chosen options reported as selected, got %q", out)
	}
	if !strings.Contains(out, "answered: but prefer none of these") {
		t.Errorf("expected typed note reported as answered, got %q", out)
	}
}

// allow_text with only options chosen (no typed text) still reports "selected:".
func TestAskUserAllowTextOnlySelection(t *testing.T) {
	tool := &askUserTool{}
	ctx := WithUserAsker(context.Background(), func(_ context.Context, _ AskRequest) ([]string, error) {
		return []string{"a", "b"}, nil
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{
		"question":"pick","multiple":true,"allow_text":true,
		"options":[{"label":"a"},{"label":"b"}]
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "selected: a, b") || strings.Contains(out, "answered:") {
		t.Errorf("options-only answer should be 'selected:' with no 'answered:', got %q", out)
	}
}

// allow_text with no options is valid (a pure free-text prompt), and a typed
// answer not among the offered labels is reported as an answer, not a selection.
func TestAskUserAllowText(t *testing.T) {
	tool := &askUserTool{}
	var gotReq AskRequest
	ctx := WithUserAsker(context.Background(), func(_ context.Context, req AskRequest) ([]string, error) {
		gotReq = req
		return []string{"use bun instead"}, nil
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{"question":"which runtime?","allow_text":true}`))
	if err != nil {
		t.Fatalf("execute (no options + allow_text should be valid): %v", err)
	}
	if !gotReq.AllowText {
		t.Error("allow_text flag not propagated to asker")
	}
	if !strings.Contains(out, "answered: use bun instead") {
		t.Fatalf("typed answer should be reported as an answer, got %q", out)
	}
}

// A typed answer that happens to match an offered label is still reported as a
// selection.
func TestAskUserAllowTextMatchingLabel(t *testing.T) {
	tool := &askUserTool{}
	ctx := WithUserAsker(context.Background(), func(_ context.Context, req AskRequest) ([]string, error) {
		return []string{"Postgres"}, nil
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{"question":"db?","allow_text":true,"options":[{"label":"Postgres"}]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "selected: Postgres") {
		t.Fatalf("matching label should be a selection, got %q", out)
	}
}

// A `questions` array asks each in turn (one asker call per question) and returns
// every answer in one result, keyed by its question — mixing selection, combined
// select+text, and dismissal.
func TestAskUserMultipleQuestions(t *testing.T) {
	tool := &askUserTool{}
	calls := 0
	ctx := WithUserAsker(context.Background(), func(_ context.Context, req AskRequest) ([]string, error) {
		calls++
		switch req.Question {
		case "db?":
			return []string{"Postgres"}, nil
		case "cache?":
			return []string{"redis", "and memcached too"}, nil // select + typed note
		default:
			return nil, nil // dismissed
		}
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{
		"questions":[
			{"question":"db?","options":[{"label":"Postgres"},{"label":"MySQL"}]},
			{"question":"cache?","allow_text":true,"options":[{"label":"redis"}]},
			{"question":"anything else?","allow_text":true}
		]
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 sequential asks, got %d", calls)
	}
	if !strings.Contains(out, `For "db?": The user selected: Postgres`) {
		t.Errorf("q1 answer missing/wrong: %q", out)
	}
	if !strings.Contains(out, "and memcached too") {
		t.Errorf("q2 combined answer missing: %q", out)
	}
	if !strings.Contains(out, `For "anything else?":`) || !strings.Contains(out, "dismissed") {
		t.Errorf("q3 dismissal missing: %q", out)
	}
}

// An invalid sub-question (no options, no allow_text) errors with its index.
func TestAskUserMultipleQuestionsValidation(t *testing.T) {
	tool := &askUserTool{}
	ctx := WithUserAsker(context.Background(), func(_ context.Context, _ AskRequest) ([]string, error) {
		return []string{"x"}, nil
	})
	_, err := tool.Execute(ctx, json.RawMessage(`{
		"questions":[
			{"question":"ok?","options":[{"label":"yes"}]},
			{"question":"bad?"}
		]
	}`))
	if err == nil || !strings.Contains(err.Error(), "question 2") {
		t.Fatalf("expected a 'question 2' validation error, got %v", err)
	}
}

// A batch asker (a grouped-panel surface, e.g. the web tabs UI) answers every
// question in ONE call; the tool returns all answers keyed by question, using the
// same wording as the single-question form — selection, combined select+text, and
// dismissal.
func TestAskUserQuestionsBatch(t *testing.T) {
	tool := &askUserTool{}
	calls := 0
	var gotReqs []AskRequest
	ctx := WithUserBatchAsker(context.Background(), func(_ context.Context, reqs []AskRequest) ([][]string, error) {
		calls++
		gotReqs = reqs
		return [][]string{
			{"Postgres"},               // q1: selection
			{"redis", "and a 60s TTL"}, // q2: chosen option + typed note
			nil,                        // q3: dismissed
		}, nil
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{
		"questions":[
			{"question":"db?","options":[{"label":"Postgres"},{"label":"MySQL"}]},
			{"question":"cache?","allow_text":true,"options":[{"label":"redis"}]},
			{"question":"anything else?","allow_text":true}
		]
	}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected ONE batched ask, got %d", calls)
	}
	if len(gotReqs) != 3 {
		t.Fatalf("batch asker got %d questions, want 3", len(gotReqs))
	}
	if !strings.Contains(out, `For "db?": The user selected: Postgres`) {
		t.Errorf("q1 answer missing/wrong: %q", out)
	}
	if !strings.Contains(out, "and a 60s TTL") || !strings.Contains(out, "answered:") {
		t.Errorf("q2 combined answer missing: %q", out)
	}
	if !strings.Contains(out, `For "anything else?":`) || !strings.Contains(out, "dismissed") {
		t.Errorf("q3 dismissal missing: %q", out)
	}
}

// When both a batch asker and a single asker are wired (as in the web agent), the
// questions[] form uses the batch asker — the single asker is never called.
func TestAskUserQuestionsBatchPreferred(t *testing.T) {
	tool := &askUserTool{}
	single := 0
	ctx := WithUserAsker(context.Background(), func(context.Context, AskRequest) ([]string, error) {
		single++
		return []string{"x"}, nil
	})
	ctx = WithUserBatchAsker(ctx, func(_ context.Context, reqs []AskRequest) ([][]string, error) {
		out := make([][]string, len(reqs))
		for i := range reqs {
			out[i] = []string{"ok"}
		}
		return out, nil
	})
	out, err := tool.Execute(ctx, json.RawMessage(`{"questions":[{"question":"a?","options":[{"label":"ok"}]},{"question":"b?","options":[{"label":"ok"}]}]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if single != 0 {
		t.Errorf("single asker should not be called when a batch asker is present, got %d", single)
	}
	if !strings.Contains(out, `For "a?":`) || !strings.Contains(out, `For "b?":`) {
		t.Errorf("both answers should be present: %q", out)
	}
}

// Batch validation errors identify the offending question by index, before any
// asking happens.
func TestAskUserQuestionsBatchValidation(t *testing.T) {
	tool := &askUserTool{}
	called := false
	ctx := WithUserBatchAsker(context.Background(), func(context.Context, []AskRequest) ([][]string, error) {
		called = true
		return nil, nil
	})
	_, err := tool.Execute(ctx, json.RawMessage(`{"questions":[{"question":"ok?","options":[{"label":"y"}]},{"question":"bad?"}]}`))
	if err == nil || !strings.Contains(err.Error(), "question 2") {
		t.Fatalf("expected a 'question 2' validation error, got %v", err)
	}
	if called {
		t.Error("batch asker should not be called when a question fails validation")
	}
}
