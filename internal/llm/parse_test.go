package llm

import (
	"strings"
	"testing"
)

// feed runs an SSE script through parseStream, collecting thinking deltas.
func feedAnthropic(t *testing.T, script string) (*Response, string) {
	t.Helper()
	var think strings.Builder
	resp, err := parseStream(strings.NewReader(script), Handler{
		OnThinkingDelta: func(s string) { think.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("parseStream: %v", err)
	}
	return resp, think.String()
}

func TestParseStreamThinkingDelta(t *testing.T) {
	script := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me reason."}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"42"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		"",
	}, "\n")

	_, think := feedAnthropic(t, script)
	if think != "Let me reason." {
		t.Errorf("thinking deltas = %q", think)
	}
}

func TestParseStreamInlineThinking(t *testing.T) {
	// Thinking text delivered on the start event rather than via deltas.
	script := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"inline reasoning"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		"",
	}, "\n")

	_, think := feedAnthropic(t, script)
	if think != "inline reasoning" {
		t.Errorf("expected inline thinking captured, got %q", think)
	}
}

func TestParseStreamHiddenThinkingIndicator(t *testing.T) {
	// A thinking block with only a signature (Floodgate redacts the text) should
	// surface a visible marker.
	script := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		"",
	}, "\n")

	_, think := feedAnthropic(t, script)
	if !strings.Contains(think, "hidden") {
		t.Errorf("expected a hidden-reasoning marker, got %q", think)
	}
}

func TestParseStreamPreservesCacheBreakdown(t *testing.T) {
	// message_start reports fresh + cached input separately; InputTokens must
	// fold them into the full context size while the cache split is preserved
	// for the footer's hit-rate display.
	script := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":1800,"cache_creation_input_tokens":50,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}`,
		"",
	}, "\n")

	resp, _ := feedAnthropic(t, script)
	if resp.Usage.InputTokens != 2050 {
		t.Errorf("InputTokens = %d, want 2050 (fresh+read+creation)", resp.Usage.InputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 1800 {
		t.Errorf("CacheReadInputTokens = %d, want 1800", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.CacheCreationInputTokens != 50 {
		t.Errorf("CacheCreationInputTokens = %d, want 50", resp.Usage.CacheCreationInputTokens)
	}
	if resp.Usage.OutputTokens != 12 {
		t.Errorf("OutputTokens = %d, want 12", resp.Usage.OutputTokens)
	}
}
