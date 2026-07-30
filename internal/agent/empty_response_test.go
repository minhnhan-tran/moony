package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

// emptyTestAgent builds an agent over a scripted stream.
func emptyTestAgent(t *testing.T, turns []llm.Response) *Agent {
	t.Helper()
	dir := t.TempDir()
	isolateConfig(t)
	return New(&config.Config{WorkingDir: dir, MaxTokens: 256, MaxAgentSteps: 20,
		MaxToolOutputBytes: 1 << 20, ApprovalPolicy: config.ApprovalAuto},
		&scriptedStreamer{turns: turns}, tools.NewRegistry(dir), nil, session.New("m", dir))
}

func collect(t *testing.T, ag *Agent, prompt string) (texts, infos []string) {
	t.Helper()
	if err := ag.Run(context.Background(), prompt, func(ev Event) {
		switch e := ev.(type) {
		case TextDelta:
			texts = append(texts, e.Text)
		case Info:
			infos = append(infos, e.Text)
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return texts, infos
}

// TestEmptyResponseIsRetriedNotSilent is the regression test for a turn that
// "stopped too early" with nothing to show for it.
//
// A response with no tool calls and no visible text, on a NORMAL stop reason, is not
// a deliberate finish — the model returned nothing. The turn used to end right there
// with no error and no explanation, so the transcript showed only incidental chatter
// (verbose-routing note, checkpoint info) and the user was left guessing. The
// max_tokens variants were already handled; a clean stop was not.
func TestEmptyResponseIsRetriedNotSilent(t *testing.T) {
	ag := emptyTestAgent(t, []llm.Response{
		{StopReason: "end_turn"}, // nothing at all
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Phase 1 done.")}},
	})
	texts, infos := collect(t, ag, "go phase 1")

	joinedInfo := strings.Join(infos, "\n")
	if !strings.Contains(joinedInfo, "empty response") {
		t.Errorf("the empty response must be reported to the user, got infos:\n%s", joinedInfo)
	}
	if got := strings.Join(texts, ""); !strings.Contains(got, "Phase 1 done.") {
		t.Errorf("the turn must retry and produce the real answer, got %q", got)
	}
}

// TestEmptyResponseRetryIsBounded: a model stuck returning nothing must not loop
// forever, and the give-up must be explicit rather than silent.
func TestEmptyResponseRetryIsBounded(t *testing.T) {
	var turns []llm.Response
	for i := 0; i < 10; i++ {
		turns = append(turns, llm.Response{StopReason: "end_turn"})
	}
	ag := emptyTestAgent(t, turns)
	_, infos := collect(t, ag, "go phase 1")

	joined := strings.Join(infos, "\n")
	var retries int
	for _, i := range infos {
		if strings.Contains(i, "retrying") {
			retries++
		}
	}
	if retries != maxEmptyRetries {
		t.Errorf("got %d retries, want exactly maxEmptyRetries=%d", retries, maxEmptyRetries)
	}
	if !strings.Contains(joined, "times in a row") {
		t.Errorf("giving up must be stated explicitly, got:\n%s", joined)
	}
}

// TestThinkingOnlyResponseCountsAsEmpty: a turn whose entire output was internal
// reasoning produced nothing the user can see or act on, so it must be treated as
// empty rather than as a finished answer.
func TestThinkingOnlyResponseCountsAsEmpty(t *testing.T) {
	ag := emptyTestAgent(t, []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{
			{Type: llm.BlockThinking, Thinking: "considering the options at length"},
		}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Here it is.")}},
	})
	_, infos := collect(t, ag, "go phase 1")
	if !strings.Contains(strings.Join(infos, "\n"), "empty response") {
		t.Errorf("a thinking-only response must count as empty, got infos:\n%s", strings.Join(infos, "\n"))
	}
}

// TestWhitespaceOnlyResponseCountsAsEmpty: " \n " is not an answer.
func TestWhitespaceOnlyResponseCountsAsEmpty(t *testing.T) {
	ag := emptyTestAgent(t, []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("  \n\t ")}},
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Real answer.")}},
	})
	_, infos := collect(t, ag, "go phase 1")
	if !strings.Contains(strings.Join(infos, "\n"), "empty response") {
		t.Errorf("a whitespace-only response must count as empty, got infos:\n%s", strings.Join(infos, "\n"))
	}
}

// TestRealAnswerIsNotTreatedAsEmpty is the false-positive guard: a normal short
// answer must finish the turn cleanly with no retry and no scary message.
func TestRealAnswerIsNotTreatedAsEmpty(t *testing.T) {
	ag := emptyTestAgent(t, []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("Done.")}},
	})
	texts, infos := collect(t, ag, "go phase 1")
	if strings.Contains(strings.Join(infos, "\n"), "empty response") {
		t.Errorf("a real answer must not be flagged as empty, got infos:\n%s", strings.Join(infos, "\n"))
	}
	if got := strings.Join(texts, ""); got != "Done." {
		t.Errorf("answer = %q, want %q", got, "Done.")
	}
}
