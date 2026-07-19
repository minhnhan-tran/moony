package llm

// This file implements the provider's OpenAI-compatible Chat Completions route as
// an alternative to the native Anthropic Messages API. The agent works in the
// Anthropic-shaped Message/ContentBlock vocabulary throughout; here we translate
// that conversation (and the tool schemas) into an OpenAI chat request, and
// translate the streamed OpenAI response back into the same Response/Blocks the
// Anthropic path produces — so the rest of the agent is unchanged.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Debug capture: when MN_DEBUG_FILE is set, the OpenAI request body and every
// raw SSE chunk are appended to that file. Useful for discovering how a given
// proxy streams extended-thinking content.
var (
	debugOnce sync.Once
	debugFile *os.File
	debugMu   sync.Mutex
)

func dbg(format string, a ...any) {
	debugOnce.Do(func() {
		if p := strings.TrimSpace(os.Getenv("MN_DEBUG_FILE")); p != "" {
			if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				debugFile = f
			} else {
				fmt.Fprintf(os.Stderr, "failed to open MN_DEBUG_FILE: %v\n", err)
			}
		}
	})
	if debugFile != nil {
		debugMu.Lock()
		defer debugMu.Unlock()
		fmt.Fprintf(debugFile, format+"\n", a...)
	}
}

// ---- OpenAI Chat Completions request shapes --------------------------------

type oaiRequest struct {
	Model         string            `json:"model"`
	Messages      []oaiMessage      `json:"messages"`
	Tools         []oaiTool         `json:"tools,omitempty"`
	Stream        bool              `json:"stream"`
	StreamOptions *oaiStreamOptions `json:"stream_options,omitempty"`
	MaxTokens     int               `json:"max_tokens,omitempty"`

	// ReasoningEffort requests extended thinking on OpenAI's reasoning-model
	// family (o1/o3/o4/gpt-5) — every other OpenAI model errors on this field,
	// so callers only set it when isOpenAIReasoningModel(model) is true.
	// OpenRouter accepts the same field name and normalizes it across upstreams.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"` // string, or []oaiContentPart for multimodal user turns
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// oaiContentPart is one element of a message's content array. cache_control is
// the Anthropic-style prompt-cache breakpoint; the provider's OpenAI-compatible
// shim forwards it to Bedrock for "aws:" Claude models (see applyOpenAIPromptCache).
type oaiContentPart struct {
	Type         string           `json:"type"` // "text" | "image_url"
	Text         string           `json:"text,omitempty"`
	ImageURL     *oaiImageURL     `json:"image_url,omitempty"`
	CacheControl *oaiCacheControl `json:"cache_control,omitempty"`
}

// oaiCacheControl marks a content part as a prompt-cache breakpoint. "ephemeral"
// is the only supported type (mirrors the Anthropic route's cacheControl).
type oaiCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type oaiImageURL struct {
	URL string `json:"url"` // a data URI: data:<media_type>;base64,<data>
}

type oaiToolCall struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	Function oaiToolCallFn `json:"function"`
	// ExtraContent carries Gemini's thought signature back to the model on the
	// next turn. Gemini 3 REQUIRES the first tool call of each step in the
	// current turn to echo its thought_signature, or the request fails with a
	// 400 (see https://ai.google.dev/gemini-api/docs/thought-signatures#openai).
	// omitempty so non-Gemini routes never emit it.
	ExtraContent *oaiExtraContent `json:"extra_content,omitempty"`
}

// oaiExtraContent is the OpenAI-compat envelope for provider-specific fields.
// Gemini nests its thought signature under "google".
type oaiExtraContent struct {
	Google *oaiGoogleExtra `json:"google,omitempty"`
}

type oaiGoogleExtra struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

type oaiToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string        `json:"type"`
	Function oaiToolSchema `json:"function"`
}

type oaiToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ---- OpenAI streaming response shapes --------------------------------------

type oaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string             `json:"content"`
			ToolCalls []oaiToolCallDelta `json:"tool_calls"`
			// Extended-thinking text. Different OpenAI-compatible gateways use
			// different field names, so accept the common ones.
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaiUsage `json:"usage"`
}

type oaiToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	// ExtraContent.Google.ThoughtSignature is present on the first tool-call
	// delta for Gemini thinking models; we capture it so it can be echoed back
	// next turn (required, or Gemini 3 returns a 400).
	ExtraContent *oaiExtraContent `json:"extra_content,omitempty"`
}

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// PromptTokensDetails.CachedTokens is the cached portion of PromptTokens
	// (a subset, already counted in PromptTokens) — surfaced so the per-turn
	// footer can show the prompt-cache hit rate.
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// streamOpenAI sends one turn via the OpenAI-compatible endpoint and assembles
// the streamed reply into a Response.
// toOpenAIMessages converts the Anthropic-shaped conversation (plus the system
// prompt) into OpenAI chat messages. Tool results become role:"tool" messages;
// thinking blocks have no OpenAI equivalent and are dropped.
func toOpenAIMessages(system string, messages []Message) []oaiMessage {
	out := make([]oaiMessage, 0, len(messages)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, oaiMessage{Role: "system", Content: system})
	}
	for _, m := range messages {
		switch m.Role {
		case RoleUser:
			var text strings.Builder
			var images []oaiContentPart
			var toolMsgs []oaiMessage
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					text.WriteString(b.Text)
				case BlockImage:
					if b.Source != nil {
						images = append(images, oaiContentPart{
							Type:     "image_url",
							ImageURL: &oaiImageURL{URL: dataURI(b.Source.MediaType, b.Source.Data)},
						})
					}
				case BlockDocument:
					// OpenAI chat can't ingest documents (PDFs); note it so the
					// model knows one was attached. Use /api anthropic to have
					// the model actually read PDFs.
					name := b.Title
					if name == "" {
						name = "document"
					}
					text.WriteString("\n[attached document not viewable on this route: " + name + "]")
				case BlockToolResult:
					toolMsgs = append(toolMsgs, oaiMessage{
						Role:       "tool",
						ToolCallID: b.ToolUseID,
						Content:    b.Content,
					})
				}
			}
			// With images, content must be a parts array (text part first, if any);
			// otherwise keep the simple string form.
			if len(images) > 0 {
				parts := make([]oaiContentPart, 0, len(images)+1)
				if text.Len() > 0 {
					parts = append(parts, oaiContentPart{Type: "text", Text: text.String()})
				}
				parts = append(parts, images...)
				out = append(out, oaiMessage{Role: "user", Content: parts})
			} else if text.Len() > 0 {
				out = append(out, oaiMessage{Role: "user", Content: text.String()})
			}
			out = append(out, toolMsgs...)

		case RoleAssistant:
			var text strings.Builder
			var calls []oaiToolCall
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					text.WriteString(b.Text)
				case BlockToolUse:
					args := strings.TrimSpace(string(b.Input))
					if args == "" {
						args = "{}"
					}
					tc := oaiToolCall{
						ID:       b.ID,
						Type:     "function",
						Function: oaiToolCallFn{Name: b.Name, Arguments: args},
					}
					// Echo Gemini's thought signature back exactly as received;
					// required on the first tool call of each step in the active
					// turn, or Gemini 3 rejects the request (400).
					if b.ThoughtSignature != "" {
						tc.ExtraContent = &oaiExtraContent{Google: &oaiGoogleExtra{ThoughtSignature: b.ThoughtSignature}}
					}
					calls = append(calls, tc)
				}
			}
			content := text.String()
			msg := oaiMessage{Role: "assistant", ToolCalls: calls}
			if content != "" {
				msg.Content = content
			}
			if content != "" || len(msg.ToolCalls) > 0 {
				out = append(out, msg)
			}
		}
	}
	return out
}

// dataURI builds an RFC 2397 data URI from a media type and base64 payload, the
// form OpenAI's image_url expects.
func dataURI(mediaType, dataB64 string) string {
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return "data:" + mediaType + ";base64," + dataB64
}

// toOpenAITools converts tool schemas to the OpenAI function-tool shape.
func toOpenAITools(tools []Tool) []oaiTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]oaiTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, oaiTool{
			Type: "function",
			Function: oaiToolSchema{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}

// openaiTag summarizes a chat stream chunk for the debug log by listing which
// known delta fields are populated (content, reasoning, tool_calls, …) so any
// reasoning field — or its absence — is easy to spot.
func openaiTag(c oaiStreamChunk) string {
	if len(c.Choices) == 0 {
		if c.Usage != nil {
			return "usage"
		}
		return "empty"
	}
	d := c.Choices[0].Delta
	var parts []string
	if d.Content != "" {
		parts = append(parts, "content")
	}
	if d.ReasoningContent != "" {
		parts = append(parts, "reasoning_content")
	}
	if d.Reasoning != "" {
		parts = append(parts, "reasoning")
	}
	if len(d.ToolCalls) > 0 {
		parts = append(parts, "tool_calls")
	}
	if fr := c.Choices[0].FinishReason; fr != "" {
		parts = append(parts, "finish="+fr)
	}
	if len(parts) == 0 {
		parts = append(parts, "other") // unknown field(s); inspect the raw JSON
	}
	return strings.Join(parts, ",")
}

// parseOpenAIStream consumes an OpenAI chat SSE body and assembles a Response in
// the same Anthropic-shaped form (text + tool_use blocks) the agent expects.
func parseOpenAIStream(r io.Reader, h Handler) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	resp := &Response{}
	var text strings.Builder
	var thinking strings.Builder

	type toolAcc struct {
		id, name string
		args     strings.Builder
		sig      string // Gemini thought signature (extra_content.google)
		notified bool
	}
	tools := map[int]*toolAcc{}
	var order []int
	finish := ""

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			dbg("<<< [unparsed] %s", data)
			continue // skip malformed/keep-alive lines defensively
		}
		dbg("<<< [%s] %s", openaiTag(chunk), data)
		if chunk.Usage != nil {
			resp.Usage.InputTokens = chunk.Usage.PromptTokens
			resp.Usage.OutputTokens = chunk.Usage.CompletionTokens
			// PromptTokens already includes the cached subset; record it
			// separately for the cache-hit telemetry in the footer.
			resp.Usage.CacheReadInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			if r := ch.Delta.ReasoningContent + ch.Delta.Reasoning; r != "" {
				thinking.WriteString(r)
				if h.OnThinkingDelta != nil {
					h.OnThinkingDelta(r)
				}
			}
			if ch.Delta.Content != "" {
				text.WriteString(ch.Delta.Content)
				if h.OnTextDelta != nil {
					h.OnTextDelta(ch.Delta.Content)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				acc := tools[tc.Index]
				if acc == nil {
					acc = &toolAcc{}
					tools[tc.Index] = acc
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
					if !acc.notified && h.OnToolUse != nil {
						acc.notified = true
						h.OnToolUse(acc.name)
					}
				}
				// Capture the thought signature (first non-empty wins). Gemini
				// sends it on the tool call's opening delta.
				if tc.ExtraContent != nil && tc.ExtraContent.Google != nil && acc.sig == "" {
					acc.sig = tc.ExtraContent.Google.ThoughtSignature
				}
				acc.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// Return the partial resp (not nil) so its accumulated Usage survives for
		// honest cost accounting in the retry loop.
		return resp, fmt.Errorf("reading stream: %w", err)
	}

	if thinking.Len() > 0 {
		resp.Blocks = append(resp.Blocks, ContentBlock{Type: BlockThinking, Thinking: thinking.String()})
	}
	if text.Len() > 0 {
		resp.Blocks = append(resp.Blocks, ContentBlock{Type: BlockText, Text: text.String()})
	}
	for _, idx := range order {
		acc := tools[idx]
		args := strings.TrimSpace(acc.args.String())
		if args == "" {
			args = "{}"
		}
		resp.Blocks = append(resp.Blocks, ContentBlock{
			Type:             BlockToolUse,
			ID:               acc.id,
			Name:             acc.name,
			Input:            json.RawMessage(args),
			ThoughtSignature: acc.sig,
		})
	}
	resp.StopReason = openAIStopReason(finish)
	return resp, nil
}

// openAIStopReason maps an OpenAI finish_reason to the Anthropic-style stop
// reason the agent uses elsewhere.
func openAIStopReason(finish string) string {
	switch finish {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "stop":
		return "end_turn"
	default:
		return finish
	}
}
