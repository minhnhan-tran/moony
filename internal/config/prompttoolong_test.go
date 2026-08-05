package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPromptTooLong(t *testing.T) {
	t.Run("anthropic form yields both counts", func(t *testing.T) {
		err := errors.New("ValidationException: prompt is too long: 203297 tokens > 200000 maximum")
		used, max, ok := PromptTooLong(err)
		if !ok {
			t.Fatal("expected a match")
		}
		if used != 203297 || max != 200000 {
			t.Errorf("got used=%d max=%d, want 203297/200000", used, max)
		}
	})

	// The ">" arrives literal, JSON-escaped, or HTML-escaped depending on how many
	// layers wrapped the error; all must parse or the counts are silently lost.
	t.Run("escaped comparison operators", func(t *testing.T) {
		for _, body := range []string{
			`prompt is too long: 203297 tokens > 200000 maximum`,
			`prompt is too long: 203297 tokens \u003e 200000 maximum`,
			`prompt is too long: 203297 tokens &gt; 200000 maximum`,
		} {
			used, max, ok := PromptTooLong(errors.New(body))
			if !ok || used != 203297 || max != 200000 {
				t.Errorf("%q: got %d/%d ok=%v, want 203297/200000 true", body, used, max, ok)
			}
		}
	})

	// Other providers don't quantify it. Detection must still succeed, since knowing
	// "it didn't fit" is what triggers the shrink-and-retry.
	t.Run("unquantified variants are still detected", func(t *testing.T) {
		for _, body := range []string{
			`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens"}}`,
			"Please reduce the length of the messages",
			"input is too long",
			"too many tokens in the request",
		} {
			if _, _, ok := PromptTooLong(errors.New(body)); !ok {
				t.Errorf("expected a match for %q", body)
			}
		}
	})

	// Compaction cannot help these, and retrying after compacting would waste a
	// summarizer call and hide the real problem.
	t.Run("unrelated errors never match", func(t *testing.T) {
		for _, body := range []string{
			"", "connection reset by peer",
			"rate limit reached for requests",
			`{"error":{"code":"insufficient_quota"}}`,
			"401 unauthorized",
			"the file is too long", // superficially similar, not a context error
		} {
			if _, _, ok := PromptTooLong(errors.New(body)); ok {
				t.Errorf("must NOT match %q", body)
			}
		}
		if _, _, ok := PromptTooLong(nil); ok {
			t.Error("nil must not match")
		}
	})

	t.Run("wrapped errors are detected", func(t *testing.T) {
		inner := errors.New("prompt is too long: 300 tokens > 200 maximum")
		if _, _, ok := PromptTooLong(fmt.Errorf("stream failed: %w", inner)); !ok {
			t.Error("must see through wrapping")
		}
	})
}

func TestPromptTooLongNote(t *testing.T) {
	err := errors.New("prompt is too long: 203297 tokens > 200000 maximum")

	t.Run("names the model, the numbers, and what to do", func(t *testing.T) {
		note := PromptTooLongNote(err, "claude-sonnet-4")
		if note == "" {
			t.Fatal("expected a note")
		}
		for _, want := range []string{"203K", "200K", "/compact"} {
			if !strings.Contains(note, want) {
				t.Errorf("note is missing %q: %s", want, note)
			}
		}
		// The user's first fear is a corrupted session; a refused request never
		// reached the model, so say so.
		if !strings.Contains(note, "intact") {
			t.Errorf("note should reassure that nothing was sent: %s", note)
		}
	})

	t.Run("unrelated errors produce no note", func(t *testing.T) {
		if note := PromptTooLongNote(errors.New("connection reset"), "m"); note != "" {
			t.Errorf("expected no note, got %q", note)
		}
	})

	t.Run("works without counts", func(t *testing.T) {
		note := PromptTooLongNote(errors.New("context_length_exceeded"), "gpt-4o")
		if note == "" || !strings.Contains(note, "context window") {
			t.Errorf("expected a generic overflow note, got %q", note)
		}
	})

	// A quick action pinning a smaller-window model is the cause; telling the user to
	// /compact would send them to fix a session that is actually fine.
	t.Run("blames the quick-action pin when that is the cause", func(t *testing.T) {
		small := errors.New("prompt is too long: 40000 tokens > 32000 maximum")
		note := PromptTooLongNoteFor(small, "small-window-model", "claude-sonnet-4")
		if !strings.Contains(note, "pinned") {
			t.Errorf("expected the note to name the pin as the cause: %s", note)
		}
		if strings.Contains(note, "/compact") {
			t.Errorf("must not advise /compact when the session model fits: %s", note)
		}
	})

	t.Run("no pin blame when the session model is the one that overflowed", func(t *testing.T) {
		note := PromptTooLongNoteFor(err, "claude-sonnet-4", "claude-sonnet-4")
		if strings.Contains(note, "pinned") {
			t.Errorf("same model, so nothing was pinned: %s", note)
		}
	})
}

func TestFormatTokenCount(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"}, {-5, "0"}, {999, "999"},
		{1000, "1K"}, {203297, "203K"}, {200000, "200K"},
		{1000000, "1M"}, {1500000, "1.5M"}, {2000000, "2M"},
	} {
		if got := FormatTokenCount(tc.in); got != tc.want {
			t.Errorf("FormatTokenCount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The suggestion list must be stable: an unstable order makes the same error read
// differently on each occurrence.
func TestLargerContextModelsIsStable(t *testing.T) {
	first := largerContextModels("claude-sonnet-4", 100000)
	for i := 0; i < 5; i++ {
		if got := largerContextModels("claude-sonnet-4", 100000); strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("unstable ordering: %v vs %v", got, first)
		}
	}
	if len(first) > 3 {
		t.Errorf("suggestions must be capped at 3, got %d", len(first))
	}
	// Every suggestion must actually be roomier than the rejected limit.
	for _, name := range first {
		if name == "" {
			t.Error("empty suggestion name")
		}
	}
}

func TestTurnErrorNoteForOverflow(t *testing.T) {
	err := errors.New("prompt is too long: 203297 tokens > 200000 maximum")
	// The model-blind entry point must keep working for existing callers.
	if got := TurnErrorNote(err); got == "" {
		t.Error("TurnErrorNote must still produce something")
	}
	withModel := TurnErrorNoteFor(err, "claude-sonnet-4")
	if !strings.Contains(withModel, "203K") {
		t.Errorf("expected the overflow guidance, got %q", withModel)
	}
	// Unrecognized errors must pass through untouched.
	raw := errors.New("some novel failure")
	if got := TurnErrorNoteFor(raw, "m"); got != raw.Error() {
		t.Errorf("got %q, want the raw text", got)
	}
	if got := TurnErrorNoteFor(nil, "m"); got != "" {
		t.Errorf("nil must yield an empty note, got %q", got)
	}
}
