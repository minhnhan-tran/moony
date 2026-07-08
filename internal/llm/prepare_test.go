package llm

import (
	"context"
	"strings"
	"testing"
)

// TestWithEffortRoundTrip verifies the per-turn reasoning-effort override carried
// on the context: WithEffort sets it, WithEffort("") is a no-op, and effortFrom
// reads it back (this is what lets an in-prompt "ultrathink" escalate one turn).
func TestWithEffortRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := effortFrom(ctx); got != "" {
		t.Errorf("effortFrom(empty ctx) = %q, want \"\"", got)
	}
	if got := effortFrom(WithEffort(ctx, "")); got != "" {
		t.Errorf("WithEffort(ctx, \"\") should be a no-op, got %q", got)
	}
	if got := effortFrom(WithEffort(ctx, "max")); got != "max" {
		t.Errorf("effortFrom after WithEffort(max) = %q, want max", got)
	}
	// withEffort (internal) forces a value, including "off", to neutralize an
	// inherited override (used by StreamMessagesNoThink for auxiliary calls).
	if got := effortFrom(withEffort(WithEffort(ctx, "max"), "off")); got != "off" {
		t.Errorf("withEffort(off) should override an inherited effort, got %q", got)
	}
}

func TestPrepareMessagesStripsThinkingWhenDisabled(t *testing.T) {
	messages := []Message{
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: BlockThinking, Thinking: "hmm", Signature: "sig"},
			{Type: BlockText, Text: "hi"},
			{Type: BlockToolUse, ID: "t1", Name: "read_file"},
		}},
	}

	// Disabled: thinking blocks are stripped.
	out := prepareMessages(messages, false)
	for _, c := range out[0].Content {
		if c.Type == BlockThinking || c.Type == BlockRedactedThinking {
			t.Fatal("thinking blocks should be stripped when thinking is disabled")
		}
	}
	if len(out[0].Content) != 2 {
		t.Fatalf("expected text + tool_use to remain, got %d blocks", len(out[0].Content))
	}

	// Enabled: messages pass through unchanged (signatures preserved).
	out = prepareMessages(messages, true)
	if len(out[0].Content) != 3 {
		t.Fatalf("expected all blocks preserved when enabled, got %d", len(out[0].Content))
	}
}

func TestPrepareMessagesThinkingFallback(t *testing.T) {
	messages := []Message{
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: BlockThinking, Thinking: "deep thoughts", Signature: "sig"},
		}},
	}
	out := prepareMessages(messages, false)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if len(out[0].Content) != 1 {
		t.Fatalf("expected 1 fallback block, got %d", len(out[0].Content))
	}
	if out[0].Content[0].Type != BlockText {
		t.Errorf("expected fallback block to be text, got %q", out[0].Content[0].Type)
	}
	// The fallback MUST be non-empty: Bedrock rejects empty text blocks
	// ("text content blocks must be non-empty").
	if strings.TrimSpace(out[0].Content[0].Text) == "" {
		t.Errorf("fallback text must be non-empty, got %q", out[0].Content[0].Text)
	}
}

// TestPrepareMessagesEmptyContentFallbackWithThinking covers the bug v3.5.26
// missed: a message that is ALREADY content-less (nil or empty Content — e.g. an
// assistant turn finalized with zero blocks, or a null-content message replayed
// from a long persisted history) must still be repaired when thinking is
// ENABLED. The Floodgate Claude path runs with adaptive thinking on, and the old
// prepareMessages returned early (`if thinkingEnabled { return messages }`)
// before the empty-content guard, so such a message reached the API and was
// rejected with "messages.N.content: Field required".
func TestPrepareMessagesEmptyContentFallbackWithThinking(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{TextBlock("hello")}},
		{Role: RoleAssistant, Content: nil},         // finalized with zero blocks
		{Role: RoleUser, Content: []ContentBlock{}}, // empty (non-nil) slice
	}
	out := prepareMessages(messages, true) // thinking ENABLED — the production path
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	for i, m := range out {
		if len(m.Content) == 0 {
			t.Fatalf("message %d still has empty content — would trigger a 400", i)
		}
	}
	// The originally non-empty user message is untouched.
	if out[0].Content[0].Text != "hello" {
		t.Errorf("first message mutated: %q", out[0].Content[0].Text)
	}
	// The two content-less messages got a compliant NON-EMPTY text fallback
	// (an empty text block would be rejected by Bedrock).
	for _, i := range []int{1, 2} {
		if out[i].Content[0].Type != BlockText || strings.TrimSpace(out[i].Content[0].Text) == "" {
			t.Errorf("message %d not repaired with non-empty text: %+v", i, out[i].Content)
		}
	}
}

// TestPrepareMessagesDropsEmptyTextBlocks covers the Bedrock rejection
// "text content blocks must be non-empty": an empty text block (e.g. a streamed
// text block that finalized with no deltas) must be dropped. When it sits beside
// a real block the sibling is kept; when it's the message's only block the
// non-empty placeholder backfills it.
func TestPrepareMessagesDropsEmptyTextBlocks(t *testing.T) {
	messages := []Message{
		// Empty text block alongside a tool_use — drop the empty text, keep the tool_use.
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: BlockText, Text: "   "}, // whitespace-only counts as empty
			{Type: BlockToolUse, ID: "t1", Name: "read_file"},
		}},
		// Only an empty text block — backfill with the non-empty placeholder.
		{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockText, Text: ""}}},
	}
	// Assert on both thinking states — the empty-text drop is unconditional.
	for _, thinking := range []bool{true, false} {
		out := prepareMessages(messages, thinking)
		if len(out[0].Content) != 1 || out[0].Content[0].Type != BlockToolUse {
			t.Fatalf("thinking=%v: expected only the tool_use to survive, got %+v", thinking, out[0].Content)
		}
		if len(out[1].Content) != 1 || out[1].Content[0].Type != BlockText ||
			strings.TrimSpace(out[1].Content[0].Text) == "" {
			t.Fatalf("thinking=%v: empty-only message not backfilled, got %+v", thinking, out[1].Content)
		}
	}
}
