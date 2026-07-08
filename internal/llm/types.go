package llm

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/pysty1995/moony/internal/config"
)

// Role constants for messages.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Content block types in the Anthropic Messages API.
const (
	BlockText             = "text"
	BlockImage            = "image"
	BlockDocument         = "document"
	BlockToolUse          = "tool_use"
	BlockToolResult       = "tool_result"
	BlockThinking         = "thinking"
	BlockRedactedThinking = "redacted_thinking"
)

// Message is a single turn in the conversation.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// cacheControl marks a prompt-caching breakpoint on a block. Everything from
// the start of the prompt up to and including a marked block is cached, so
// subsequent turns with the same prefix are billed at the much cheaper
// cache-read rate. "ephemeral" is the only supported type.
type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// systemBlock is the content-block form of the system prompt, used so a
// cache_control breakpoint can be attached to it (the Anthropic API accepts the
// system field as either a string or an array of these blocks).
type systemBlock struct {
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// ContentBlock is a polymorphic piece of message content. Only the fields
// relevant to its Type are populated when serialized.
type ContentBlock struct {
	Type string `json:"type"`

	// BlockText
	Text string `json:"text,omitempty"`

	// BlockImage / BlockDocument carry their payload in Source. Documents also
	// use Title for a human-readable file name.
	Source *ContentSource `json:"source,omitempty"`
	Title  string         `json:"title,omitempty"`

	// BlockToolUse
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// ThoughtSignature carries Gemini's per-tool-call thought signature on the
	// OpenAI-compatible route (extra_content.google.thought_signature). It is
	// json:"-" on purpose: it must NOT appear on the Anthropic wire (which would
	// reject the unknown field) and it isn't persisted — Gemini only validates
	// signatures within the active, in-memory turn, where this block lives across
	// steps. The OpenAI route reads/writes it explicitly (see openai.go), never
	// via these struct tags.
	ThoughtSignature string `json:"-"`

	// BlockToolResult
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// ContentList, when set on a BlockToolResult, serializes as the `content`
	// ARRAY (text + image blocks) instead of the Content string — for tool
	// results that return images, e.g. a browser screenshot. The Anthropic API
	// accepts tool_result `content` as either a string or an array of blocks.
	ContentList []ContentBlock `json:"-"`

	// BlockThinking / BlockRedactedThinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`

	// CacheControl, when set, makes this block a prompt-caching breakpoint.
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON special-cases a tool_result whose content is a list of blocks
// (text + images): the Anthropic API accepts tool_result `content` as either a
// string or an array, and image results (e.g. a browser screenshot) need the
// array form. Every other block marshals exactly as its struct tags dictate —
// via the alias type, which has no MarshalJSON, so there's no recursion.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if b.Type == BlockToolResult && len(b.ContentList) > 0 {
		return json.Marshal(struct {
			Type         string         `json:"type"`
			ToolUseID    string         `json:"tool_use_id,omitempty"`
			Content      []ContentBlock `json:"content"`
			IsError      bool           `json:"is_error,omitempty"`
			CacheControl *cacheControl  `json:"cache_control,omitempty"`
		}{b.Type, b.ToolUseID, b.ContentList, b.IsError, b.CacheControl})
	}
	type alias ContentBlock
	return json.Marshal(alias(b))
}

// UnmarshalJSON is the inverse of MarshalJSON: it accepts a tool_result whose
// `content` is EITHER a string (the common case → Content) OR an array of blocks
// (image results → ContentList), so a message persisted by MarshalJSON round-trips
// losslessly. Without this, reloading a saved session that contains an image
// tool-result (e.g. a browser screenshot) fails with "cannot unmarshal array into
// Go struct field …content of type string", which makes the whole session
// unopenable. Every other field decodes via the alias type (no UnmarshalJSON, so
// no recursion); the polymorphic `content` is captured raw and dispatched here.
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type alias ContentBlock
	aux := struct {
		*alias
		Content json.RawMessage `json:"content"`
	}{alias: (*alias)(b)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	// Reset the polymorphic fields, then set whichever form `content` carried.
	b.Content = ""
	b.ContentList = nil
	raw := bytes.TrimSpace(aux.Content)
	switch {
	case len(raw) == 0 || string(raw) == "null":
		// no content present
	case raw[0] == '[':
		var list []ContentBlock
		if err := json.Unmarshal(raw, &list); err != nil {
			return fmt.Errorf("tool_result content array: %w", err)
		}
		b.ContentList = list
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("tool_result content string: %w", err)
		}
		b.Content = s
	default:
		return fmt.Errorf("ContentBlock.content: unexpected JSON %s", raw)
	}
	return nil
}

// ContentSource is the base64 payload of an image or document block, in the
// Anthropic Messages API shape.
type ContentSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. image/png, application/pdf
	Data      string `json:"data"`       // base64-encoded bytes
}

// ImageBlock builds an image content block from base64 data.
func ImageBlock(mediaType, dataB64 string) ContentBlock {
	return ContentBlock{
		Type:   BlockImage,
		Source: &ContentSource{Type: "base64", MediaType: mediaType, Data: dataB64},
	}
}

// DocumentBlock builds a document content block (e.g. a PDF) from base64 data,
// labeled with a file name.
func DocumentBlock(mediaType, dataB64, title string) ContentBlock {
	return ContentBlock{
		Type:   BlockDocument,
		Source: &ContentSource{Type: "base64", MediaType: mediaType, Data: dataB64},
		Title:  title,
	}
}

// TextBlock builds a plain text content block.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: BlockText, Text: text}
}

// ToolResultBlock builds a tool_result block referencing a prior tool_use.
func ToolResultBlock(toolUseID, content string, isError bool) ContentBlock {
	return ContentBlock{
		Type:      BlockToolResult,
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   isError,
	}
}

// ToolResultBlocks builds a tool_result whose content is a LIST of blocks (e.g. a
// text note plus one or more images, like a browser screenshot) referencing a
// prior tool_use. It serializes `content` as an array (see MarshalJSON).
func ToolResultBlocks(toolUseID string, blocks []ContentBlock, isError bool) ContentBlock {
	return ContentBlock{
		Type:        BlockToolResult,
		ToolUseID:   toolUseID,
		ContentList: blocks,
		IsError:     isError,
	}
}

// UserText is a convenience constructor for a user message of plain text.
func UserText(text string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{TextBlock(text)}}
}

// Tool is the schema describing a callable tool to the model.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	// CacheControl, when set, makes this tool a prompt-caching breakpoint
	// (caching the tools prefix up to and including it).
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// request is the JSON body sent to the Messages endpoint.
type request struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	System      any       `json:"system,omitempty"` // string, or []systemBlock when caching
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Thinking    *thinking `json:"thinking,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream"`
}

// thinking enables Anthropic's native extended-thinking mode: type "enabled"
// plus a token budget (>=1024, and less than MaxTokens). Temperature must be
// unset (or 1) while thinking is enabled — the request omits it in that case.
type thinking struct {
	Type         string `json:"type"` // "enabled"
	BudgetTokens int    `json:"budget_tokens"`
}

// Usage reports token accounting for a turn. With prompt caching, Anthropic
// reports the cached portions separately from InputTokens; parseStream folds
// them back in so InputTokens reflects the full context size.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// CostUSD prices this usage for model, accounting for prompt-cache discounts.
// Because the parser folds the cached portions into InputTokens on both routes,
// the cache-token fields are subsets of InputTokens; CostUSDFull charges the
// uncached remainder at the base rate and the cached reads/writes at their own
// (cheaper/dearer) rates. Returns 0 for models with no known pricing.
func (u Usage) CostUSD(model string) float64 {
	return config.CostUSDFull(model, u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
}

// Response is the fully-assembled result of a single model turn.
type Response struct {
	// Blocks are the assembled content blocks (text + tool_use).
	Blocks []ContentBlock
	// StopReason is "end_turn", "tool_use", "max_tokens", etc.
	StopReason string
	Usage      Usage
	// RetryUsage is the token usage of any FAILED, retried stream attempts for
	// this call (a mid-stream drop re-sends the whole request, and the provider
	// still bills the input it processed). It is tracked separately from Usage so
	// cost accounting can include the wasted spend while the context-size
	// indicators (which want the final, successful call's input) stay accurate.
	RetryUsage Usage
}

// ToolCalls returns just the tool_use blocks from the response.
func (r *Response) ToolCalls() []ContentBlock {
	var calls []ContentBlock
	for _, b := range r.Blocks {
		if b.Type == BlockToolUse {
			calls = append(calls, b)
		}
	}
	return calls
}

// AssistantMessage converts the response into a Message for history.
func (r *Response) AssistantMessage() Message {
	return Message{Role: RoleAssistant, Content: r.Blocks}
}

// --- Streaming server-sent event payloads ---

type sseEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	ContentBlock *sseBlock       `json:"content_block,omitempty"`
	Delta        *sseDelta       `json:"delta,omitempty"`
	Message      json.RawMessage `json:"message,omitempty"`
	Usage        *Usage          `json:"usage,omitempty"`
	Error        *sseError       `json:"error,omitempty"`
}

type sseBlock struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"` // some gateways send thinking text here
	Data     string `json:"data"`     // redacted_thinking payload
}

type sseDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
	Thinking    string `json:"thinking"`  // thinking_delta
	Signature   string `json:"signature"` // signature_delta
}

type sseError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
