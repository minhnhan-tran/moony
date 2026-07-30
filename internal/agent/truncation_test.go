package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/tools"
)

func truncationAgent(t *testing.T, fake *scriptedStreamer) *Agent {
	t.Helper()
	isolateConfig(t)
	dir := t.TempDir()
	cfg := &config.Config{
		Model:              "anthropic.claude-sonnet-5",
		MaxTokens:          256,
		MaxToolOutputBytes: 1 << 20,
		MaxAgentSteps:      10,
		ApprovalPolicy:     config.ApprovalAuto,
		WorkingDir:         dir,
	}
	return New(cfg, fake, tools.NewRegistry(dir), nil, nil)
}

// TestTruncatedResponseRetries verifies that a response cut off by the output
// length cap before producing a tool call (StopReason == "max_tokens", no
// blocks) is retried with a terse nudge instead of being treated as a
// deliberate, silent stop.
func TestTruncatedResponseRetries(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "max_tokens"}, // thinking ate the whole budget, no tool call
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}},
	}}
	ag := truncationAgent(t, fake)

	var notices []string
	if err := ag.Run(context.Background(), "write the file", func(ev Event) {
		if e, ok := ev.(Info); ok {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 (initial truncation + retry)", fake.calls)
	}
	found := false
	for _, n := range notices {
		if strings.Contains(n, "cut off") && strings.Contains(n, "retrying") {
			found = true
		}
	}
	if !found {
		t.Errorf("notices = %v, want one mentioning the cut-off retry", notices)
	}

	// The retry's outgoing messages must carry the synthetic nudge so the
	// model knows to be terser.
	last := fake.sent[len(fake.sent)-1]
	nudged := false
	for _, m := range last {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "[automatic retry]") {
				nudged = true
			}
		}
	}
	if !nudged {
		t.Errorf("retry request did not contain the truncation nudge message")
	}
}

// TestTruncatedResponseStopsAfterMaxRetries verifies the retry is bounded: if
// the model keeps getting cut off, the turn eventually gives up with a clear
// explanation rather than looping forever.
func TestTruncatedResponseStopsAfterMaxRetries(t *testing.T) {
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "max_tokens"},
		{StopReason: "max_tokens"},
		{StopReason: "max_tokens"},
	}}
	ag := truncationAgent(t, fake)

	var gaveUp bool
	if err := ag.Run(context.Background(), "write the file", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "stopping") {
			gaveUp = true
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if fake.calls != maxTruncationRetries+1 {
		t.Fatalf("calls = %d, want %d (initial + %d retries)", fake.calls, maxTruncationRetries+1, maxTruncationRetries)
	}
	if !gaveUp {
		t.Errorf("expected a give-up notice after exhausting retries")
	}
}

// TestCutOffAnswerContinues verifies that a response cut off by max_tokens that
// already produced real answer TEXT (not just thinking) is CONTINUED so the long
// answer finishes — rather than being nudged to be terse (the no-output path) and
// left truncated.
func TestCutOffAnswerContinues(t *testing.T) {
	longAnswer := strings.Repeat("this is a long answer that got cut off. ", 3) // >= minAnswerTextForContinue
	fake := &scriptedStreamer{turns: []llm.Response{
		{StopReason: "max_tokens", Blocks: []llm.ContentBlock{llm.TextBlock(longAnswer)}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("…and the rest of the answer.")}},
	}}
	ag := truncationAgent(t, fake)

	var notices []string
	if err := ag.Run(context.Background(), "explain in detail", func(ev Event) {
		if e, ok := ev.(Info); ok {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2 (initial cut-off + continuation)", fake.calls)
	}
	sawContinue := false
	for _, n := range notices {
		if strings.Contains(n, "continuing") {
			sawContinue = true
		}
		if strings.Contains(n, "retrying") {
			t.Errorf("cut-off long answer used the terse retry path, want continuation: %q", n)
		}
	}
	if !sawContinue {
		t.Errorf("notices = %v, want one mentioning continuing the answer", notices)
	}
	// The continuation request must carry the continue message, NOT the terse nudge.
	last := fake.sent[len(fake.sent)-1]
	sawContinueMsg, sawTerse := false, false
	for _, m := range last {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "[automatic continuation]") {
				sawContinueMsg = true
			}
			if strings.Contains(b.Text, "[automatic retry]") {
				sawTerse = true
			}
		}
	}
	if !sawContinueMsg || sawTerse {
		t.Errorf("continuation request wrong: continueMsg=%v terseNudge=%v", sawContinueMsg, sawTerse)
	}
}

// TestCutOffAnswerContinuationBounded verifies answer-continuation is bounded: an
// answer that keeps getting cut off stops after maxAnswerContinues rounds instead
// of looping forever.
func TestCutOffAnswerContinuationBounded(t *testing.T) {
	long := strings.Repeat("more answer text that never quite ends. ", 2) // >= minAnswerTextForContinue
	turns := make([]llm.Response, maxAnswerContinues+1)
	for i := range turns {
		turns[i] = llm.Response{StopReason: "max_tokens", Blocks: []llm.ContentBlock{llm.TextBlock(long)}}
	}
	fake := &scriptedStreamer{turns: turns}
	ag := truncationAgent(t, fake)

	var stopped bool
	if err := ag.Run(context.Background(), "explain in detail", func(ev Event) {
		if e, ok := ev.(Info); ok && strings.Contains(e.Text, "stopping") {
			stopped = true
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.calls != maxAnswerContinues+1 {
		t.Fatalf("calls = %d, want %d (initial + %d continuations)", fake.calls, maxAnswerContinues+1, maxAnswerContinues)
	}
	if !stopped {
		t.Errorf("expected a stopping notice after exhausting continuations")
	}
}
