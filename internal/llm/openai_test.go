package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToOpenAIMessages(t *testing.T) {
	messages := []Message{
		UserText("add a flag"),
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: BlockThinking, Thinking: "reason"}, // dropped
			{Type: BlockText, Text: "on it"},
			{Type: BlockToolUse, ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)},
		}},
		{Role: RoleUser, Content: []ContentBlock{
			{Type: BlockToolResult, ToolUseID: "call_1", Content: "package main"},
		}},
	}

	out := toOpenAIMessages("be terse", messages)

	if out[0].Role != "system" || out[0].Content != "be terse" {
		t.Fatalf("first message should be the system prompt, got %+v", out[0])
	}
	if out[1].Role != "user" || out[1].Content != "add a flag" {
		t.Fatalf("expected user message, got %+v", out[1])
	}
	// assistant: text + one tool call, thinking dropped.
	asst := out[2]
	if asst.Role != "assistant" || asst.Content != "on it" || len(asst.ToolCalls) != 1 {
		t.Fatalf("unexpected assistant message: %+v", asst)
	}
	if asst.ToolCalls[0].ID != "call_1" || asst.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("unexpected tool call: %+v", asst.ToolCalls[0])
	}
	// tool result becomes a role:"tool" message.
	tool := out[3]
	if tool.Role != "tool" || tool.ToolCallID != "call_1" || tool.Content != "package main" {
		t.Fatalf("unexpected tool message: %+v", tool)
	}
}

func TestParseOpenAIStream(t *testing.T) {
	// A minimal chat.completions SSE: text deltas, then a tool call, then usage.
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":" world"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"bash"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var text strings.Builder
	var toolNames []string
	h := Handler{
		OnTextDelta: func(s string) { text.WriteString(s) },
		OnToolUse:   func(name string) { toolNames = append(toolNames, name) },
	}
	resp, err := parseOpenAIStream(strings.NewReader(stream), h)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if text.String() != "Hello world" {
		t.Errorf("text deltas = %q", text.String())
	}
	if len(toolNames) != 1 || toolNames[0] != "bash" {
		t.Errorf("OnToolUse names = %v", toolNames)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop reason = %q, want tool_use", resp.StopReason)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}

	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].ID != "call_9" || calls[0].Name != "bash" {
		t.Errorf("tool call = %+v", calls[0])
	}
	if got := strings.TrimSpace(string(calls[0].Input)); got != `{"command":"ls"}` {
		t.Errorf("tool args = %q", got)
	}

	// The assembled text block should also be present.
	var gotText string
	for _, b := range resp.Blocks {
		if b.Type == BlockText {
			gotText = b.Text
		}
	}
	if gotText != "Hello world" {
		t.Errorf("text block = %q", gotText)
	}
}

func TestParseOpenAIStreamCachedTokens(t *testing.T) {
	// prompt_tokens_details.cached_tokens is the cached subset of prompt_tokens;
	// it must land in CacheReadInputTokens while InputTokens stays the full
	// prompt_tokens count.
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":2000,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":1900}}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAIStream(strings.NewReader(stream), Handler{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Usage.InputTokens != 2000 || resp.Usage.OutputTokens != 9 {
		t.Errorf("usage = %+v, want in=2000 out=9", resp.Usage)
	}
	if resp.Usage.CacheReadInputTokens != 1900 {
		t.Errorf("CacheReadInputTokens = %d, want 1900", resp.Usage.CacheReadInputTokens)
	}
}

func TestParseOpenAIStreamReasoning(t *testing.T) {
	// Reasoning arrives as reasoning_content deltas before the visible answer.
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"Let me "}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"think."}}]}`,
		`data: {"choices":[{"delta":{"content":"Answer."}}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var think, text strings.Builder
	h := Handler{
		OnThinkingDelta: func(s string) { think.WriteString(s) },
		OnTextDelta:     func(s string) { text.WriteString(s) },
	}
	resp, err := parseOpenAIStream(strings.NewReader(stream), h)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if think.String() != "Let me think." {
		t.Errorf("thinking deltas = %q", think.String())
	}
	if text.String() != "Answer." {
		t.Errorf("text deltas = %q", text.String())
	}
	// A thinking block should precede the text block in the assembled response.
	if len(resp.Blocks) < 2 || resp.Blocks[0].Type != BlockThinking || resp.Blocks[0].Thinking != "Let me think." {
		t.Fatalf("expected leading thinking block, got %+v", resp.Blocks)
	}
}

// TestParseOpenAIStreamThoughtSignature verifies that Gemini's per-tool-call
// thought signature (extra_content.google.thought_signature) is captured off the
// stream and attached to the tool_use block, so it can be echoed back next turn.
func TestParseOpenAIStreamThoughtSignature(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"fc1","type":"function","function":{"name":"check_flight"},"extra_content":{"google":{"thought_signature":"SIG_A"}}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"flight\":\"AA100\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := parseOpenAIStream(strings.NewReader(stream), Handler{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].ThoughtSignature != "SIG_A" {
		t.Errorf("thought signature = %q, want SIG_A", calls[0].ThoughtSignature)
	}
}

// TestToOpenAIMessagesEchoesThoughtSignature verifies the round-trip: a tool_use
// block carrying a thought signature is re-serialized with
// extra_content.google.thought_signature (required for Gemini 3 multi-step tool
// calls), while a tool_use without one omits extra_content entirely.
func TestToOpenAIMessagesEchoesThoughtSignature(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{TextBlock("check AA100")}},
		{Role: RoleAssistant, Content: []ContentBlock{
			{Type: BlockToolUse, ID: "fc1", Name: "check_flight", Input: json.RawMessage(`{"flight":"AA100"}`), ThoughtSignature: "SIG_A"},
			{Type: BlockToolUse, ID: "fc2", Name: "book_taxi", Input: json.RawMessage(`{"time":"10 AM"}`)}, // parallel, no signature
		}},
	}

	out := toOpenAIMessages("", messages)
	// out[0] = user, out[1] = assistant.
	asst := out[len(out)-1]
	if len(asst.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %+v", asst.ToolCalls)
	}
	first := asst.ToolCalls[0]
	if first.ExtraContent == nil || first.ExtraContent.Google == nil || first.ExtraContent.Google.ThoughtSignature != "SIG_A" {
		t.Errorf("first tool call should echo SIG_A, got %+v", first.ExtraContent)
	}
	if asst.ToolCalls[1].ExtraContent != nil {
		t.Errorf("a tool call without a signature must omit extra_content, got %+v", asst.ToolCalls[1].ExtraContent)
	}

	// And it must serialize to the documented wire shape.
	if raw, _ := json.Marshal(first); !strings.Contains(string(raw), `"extra_content":{"google":{"thought_signature":"SIG_A"}}`) {
		t.Errorf("wire shape mismatch: %s", raw)
	}
}

// TestContentBlockThoughtSignatureNotOnWire guards the invariant that the
// signature never leaks onto the Anthropic wire (or session JSON) via struct
// tags — it must be json:"-" and round-tripped only through the OpenAI structs.
func TestContentBlockThoughtSignatureNotOnWire(t *testing.T) {
	b := ContentBlock{Type: BlockToolUse, ID: "fc1", Name: "x", Input: json.RawMessage(`{}`), ThoughtSignature: "SECRET_SIG"}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "SECRET_SIG") || strings.Contains(string(raw), "thought_signature") {
		t.Errorf("thought signature must not appear in ContentBlock JSON (Anthropic wire / session), got %s", raw)
	}
}
