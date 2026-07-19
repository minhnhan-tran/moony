package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTotalInput(t *testing.T) {
	u := Usage{InputTokens: 10, CacheReadInputTokens: 100, CacheCreationInputTokens: 5}
	if got := totalInput(u); got != 115 {
		t.Errorf("totalInput = %d, want 115", got)
	}
}

// TestUsageCostUSD verifies the cache-aware cost method: InputTokens is the full
// prompt and the cache fields are subsets of it, so the uncached remainder bills
// at the base rate and cached reads bill ~10x cheaper. Sonnet is $3/1M input.
func TestUsageCostUSD(t *testing.T) {
	// 1M-token prompt, 900k served from cache → 100k uncached*$3 + 900k*$0.30.
	u := Usage{InputTokens: 1_000_000, OutputTokens: 0, CacheReadInputTokens: 900_000}
	got := u.CostUSD("anthropic.claude-sonnet-4-6")
	if got < 0.569 || got > 0.571 {
		t.Errorf("Usage.CostUSD = %v, want ~0.57 (cache-discounted)", got)
	}
	// An unknown model prices at 0.
	if got := u.CostUSD("not.a.real.model"); got != 0 {
		t.Errorf("unknown model should cost 0, got %v", got)
	}
}

func TestApplyPromptCacheMarksBreakpoints(t *testing.T) {
	t.Setenv("MN_PROMPT_CACHE", "") // enabled

	tools := []Tool{{Name: "a"}, {Name: "b"}}
	msgs := []Message{
		{Role: RoleUser, Content: []ContentBlock{TextBlock("hi")}},
		{Role: RoleAssistant, Content: []ContentBlock{TextBlock("yo"), TextBlock("more")}},
	}
	body := request{System: "you are sqe", Tools: tools, Messages: msgs}
	applyPromptCache(&body)

	// System becomes a cacheable content-block array.
	sb, ok := body.System.([]systemBlock)
	if !ok || len(sb) != 1 || sb[0].Text != "you are sqe" || sb[0].CacheControl == nil {
		t.Fatalf("system not converted to a cacheable block: %#v", body.System)
	}
	// Only the last tool is marked.
	if body.Tools[len(body.Tools)-1].CacheControl == nil {
		t.Error("last tool should be a cache breakpoint")
	}
	if body.Tools[0].CacheControl != nil {
		t.Error("non-last tool should not be marked")
	}
	// Only the last block of the last message is marked.
	last := body.Messages[len(body.Messages)-1].Content
	if last[len(last)-1].CacheControl == nil {
		t.Error("last message's last block should be a cache breakpoint")
	}
	if last[0].CacheControl != nil {
		t.Error("only the final block of the last message should be marked")
	}

	// The caller's slices must be untouched (cache_control must not accumulate
	// in the agent's history or registry across turns).
	if tools[1].CacheControl != nil {
		t.Error("original tools slice was mutated")
	}
	if msgs[1].Content[1].CacheControl != nil {
		t.Error("original message content was mutated")
	}
}

func TestApplyPromptCacheDisabled(t *testing.T) {
	t.Setenv("MN_PROMPT_CACHE", "0")
	body := request{
		System:   "sys",
		Tools:    []Tool{{Name: "a"}},
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{TextBlock("hi")}}},
	}
	applyPromptCache(&body)

	if _, ok := body.System.(string); !ok {
		t.Errorf("system should remain a plain string when caching is disabled, got %T", body.System)
	}
	if body.Tools[0].CacheControl != nil {
		t.Error("no tool should be marked when caching is disabled")
	}
}

// TestPromptCacheJSONShape locks the wire format the gateway/Anthropic receives.
func TestPromptCacheJSONShape(t *testing.T) {
	t.Setenv("MN_PROMPT_CACHE", "")
	body := request{
		Model:     "m",
		MaxTokens: 1,
		System:    "sys",
		Tools:     []Tool{{Name: "t", InputSchema: json.RawMessage(`{}`)}},
		Messages:  []Message{UserText("hi")},
	}
	applyPromptCache(&body)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"system":[{"type":"text"`) {
		t.Errorf("system should serialize as a block array, got:\n%s", s)
	}
	if !strings.Contains(s, `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("expected an ephemeral cache_control breakpoint, got:\n%s", s)
	}
}

// TestApplyPromptCacheSplitsDynamicSuffix verifies that applyPromptCache
// splits the system prompt when a dynamic directive suffix is present so that
// only the static base prompt is cached.
func TestApplyPromptCacheSplitsDynamicSuffix(t *testing.T) {
	t.Setenv("MN_PROMPT_CACHE", "") // enabled
	body := request{
		System:   "you are sqe\n\nULTRATHINK MODE:\n- be thorough",
		Tools:    []Tool{{Name: "a"}},
		Messages: []Message{UserText("hi")},
	}
	applyPromptCache(&body)

	sb, ok := body.System.([]systemBlock)
	if !ok {
		t.Fatalf("expected systemBlock slice, got %T", body.System)
	}
	if len(sb) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(sb))
	}
	if sb[0].Text != "you are sqe" {
		t.Errorf("expected base block 'you are sqe', got %q", sb[0].Text)
	}
	if sb[0].CacheControl == nil {
		t.Error("expected base block to have cache control")
	}
	if sb[1].Text != "\n\nULTRATHINK MODE:\n- be thorough" {
		t.Errorf("expected suffix block, got %q", sb[1].Text)
	}
	if sb[1].CacheControl != nil {
		t.Error("expected suffix block to have no cache control")
	}
}

// TestApplyPromptCacheSplitsContextSuffix verifies the session-context marker is
// peeled off uncached just like the directive markers — this is what keeps a
// refreshed checkpoint (carried in this suffix) from invalidating the cached
// system base.
func TestApplyPromptCacheSplitsContextSuffix(t *testing.T) {
	t.Setenv("MN_PROMPT_CACHE", "") // enabled
	suffix := SystemDirectiveSeparator + ContextDirectiveMarker + "\n\n# Session checkpoint\n\nGoal: x"
	body := request{System: "you are sqe" + suffix, Messages: []Message{UserText("hi")}}
	applyPromptCache(&body)

	sb, ok := body.System.([]systemBlock)
	if !ok || len(sb) != 2 {
		t.Fatalf("expected a 2-block split, got %#v", body.System)
	}
	if sb[0].Text != "you are sqe" || sb[0].CacheControl == nil {
		t.Errorf("base block should be the cached stable prompt, got %q (cc=%v)", sb[0].Text, sb[0].CacheControl)
	}
	if sb[1].Text != suffix || sb[1].CacheControl != nil {
		t.Errorf("context suffix should be uncached, got %q (cc=%v)", sb[1].Text, sb[1].CacheControl)
	}
}

// TestApplyPromptCacheSplitsAtLastMarker verifies the split anchors on the
// directive nearest the END of the prompt. The base prompt here itself mentions a
// marker phrase, and a *different* directive is appended after it — the whole base
// must stay in the cached block (regression guard for the old first-match split,
// which truncated the base into the uncached suffix).
func TestApplyPromptCacheSplitsAtLastMarker(t *testing.T) {
	t.Setenv("MN_PROMPT_CACHE", "") // enabled
	base := "you are sqe" + SystemDirectiveSeparator + UltrathinkDirectiveMarker + " is a phrase we mention inline."
	suffix := SystemDirectiveSeparator + InvestigationDirectiveMarker + ":\n- explore"
	body := request{System: base + suffix, Messages: []Message{UserText("hi")}}
	applyPromptCache(&body)

	sb, ok := body.System.([]systemBlock)
	if !ok || len(sb) != 2 {
		t.Fatalf("expected a 2-block split, got %#v", body.System)
	}
	if sb[0].Text != base {
		t.Errorf("base block was truncated:\n got: %q\nwant: %q", sb[0].Text, base)
	}
	if sb[0].CacheControl == nil {
		t.Error("base block should carry the cache breakpoint")
	}
	if sb[1].Text != suffix {
		t.Errorf("suffix block = %q, want %q", sb[1].Text, suffix)
	}
	if sb[1].CacheControl != nil {
		t.Error("suffix block must stay uncached")
	}
}
