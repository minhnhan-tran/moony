package llm

// Native Ollama backend (POST <host>/api/chat). moony talks to Ollama's native
// chat API rather than its OpenAI-compatible /v1 endpoint because only the
// native API honors runtime options — notably options.num_ctx, which moony must
// set: its system prompt + tool schemas run several thousand tokens and overflow
// the common default context (4096), which truncates the prompt, drops the
// model's thinking, and can halt generation after a token or two. The native API
// also reliably honors `think` and `keep_alive`.
//
// The wire differs from OpenAI's: the request carries options/think/keep_alive,
// tool-call arguments are JSON OBJECTS (not strings), and the response is a
// stream of newline-delimited JSON objects (not SSE). This file holds the native
// request/response shapes, the message converter, and the NDJSON stream parser.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ollamaNativeReq is the POST /api/chat body. Tools use the same
// {type,function{name,description,parameters}} schema as OpenAI, so toOpenAITools
// is reused.
type ollamaNativeReq struct {
	Model     string            `json:"model"`
	Messages  []ollamaNativeMsg `json:"messages"`
	Tools     []oaiTool         `json:"tools,omitempty"`
	Stream    bool              `json:"stream"`
	Think     *bool             `json:"think,omitempty"`
	KeepAlive string            `json:"keep_alive,omitempty"`
	Options   *ollamaOptions    `json:"options,omitempty"`
}

type ollamaNativeMsg struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Images    []string         `json:"images,omitempty"` // base64 payloads (no data: prefix)
	ToolCalls []ollamaNativeTC `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"` // role:"tool" — names the tool this result answers
}

type ollamaNativeTC struct {
	Function ollamaNativeTCFn `json:"function"`
}

// ollamaNativeTCFn carries arguments as a raw JSON OBJECT (Ollama's native form),
// unlike OpenAI's arguments-as-string.
type ollamaNativeTCFn struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// toOllamaMessages converts the Anthropic-shaped conversation (+ system prompt)
// into native Ollama chat messages. Mirrors toOpenAIMessages: tool results become
// role:"tool" messages, images ride as base64 in `images`, tool calls carry their
// arguments as JSON objects, and thinking blocks from history are dropped.
func toOllamaMessages(system string, messages []Message) []ollamaNativeMsg {
	out := make([]ollamaNativeMsg, 0, len(messages)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, ollamaNativeMsg{Role: "system", Content: system})
	}
	for _, m := range messages {
		switch m.Role {
		case RoleUser:
			var text strings.Builder
			var images []string
			var toolMsgs []ollamaNativeMsg
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					text.WriteString(b.Text)
				case BlockImage:
					if b.Source != nil && b.Source.Data != "" {
						images = append(images, b.Source.Data)
					}
				case BlockDocument:
					name := b.Title
					if name == "" {
						name = "document"
					}
					text.WriteString("\n[attached document not viewable on this route: " + name + "]")
				case BlockToolResult:
					toolMsgs = append(toolMsgs, ollamaNativeMsg{Role: "tool", Content: b.Content})
				}
			}
			if text.Len() > 0 || len(images) > 0 {
				out = append(out, ollamaNativeMsg{Role: "user", Content: text.String(), Images: images})
			}
			out = append(out, toolMsgs...)

		case RoleAssistant:
			var text strings.Builder
			var calls []ollamaNativeTC
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					text.WriteString(b.Text)
				case BlockToolUse:
					args := json.RawMessage(strings.TrimSpace(string(b.Input)))
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					calls = append(calls, ollamaNativeTC{Function: ollamaNativeTCFn{Name: b.Name, Arguments: args}})
				}
			}
			if text.Len() > 0 || len(calls) > 0 {
				out = append(out, ollamaNativeMsg{Role: "assistant", Content: text.String(), ToolCalls: calls})
			}
		}
	}
	return out
}

// ollamaNativeChunk is one newline-delimited object from the /api/chat stream.
// Content/thinking arrive as deltas; tool calls typically arrive whole (args as
// an object). The final object has done=true with the token counts.
type ollamaNativeChunk struct {
	Message struct {
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []ollamaNativeTC `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"` // input tokens
	EvalCount       int    `json:"eval_count"`        // output tokens
	Error           string `json:"error"`
}

// parseOllamaNativeStream assembles the NDJSON /api/chat stream into a Response,
// firing the Handler's delta callbacks as content/thinking/tool-calls arrive.
// Signature matches parseOpenAIStream so it slots into streamWithRetry.
func parseOllamaNativeStream(r io.Reader, h Handler) (*Response, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	resp := &Response{}
	var text, thinking strings.Builder
	var toolBlocks []ContentBlock
	toolIdx := 0

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ch ollamaNativeChunk
		if err := json.Unmarshal(line, &ch); err != nil {
			continue // skip a malformed line rather than abort the turn
		}
		if ch.Error != "" {
			return nil, fmt.Errorf("ollama: %s", ch.Error)
		}
		if ch.Message.Thinking != "" {
			thinking.WriteString(ch.Message.Thinking)
			if h.OnThinkingDelta != nil {
				h.OnThinkingDelta(ch.Message.Thinking)
			}
		}
		if ch.Message.Content != "" {
			text.WriteString(ch.Message.Content)
			if h.OnTextDelta != nil {
				h.OnTextDelta(ch.Message.Content)
			}
		}
		for _, tc := range ch.Message.ToolCalls {
			args := strings.TrimSpace(string(tc.Function.Arguments))
			if args == "" {
				args = "{}"
			}
			if h.OnToolUse != nil && tc.Function.Name != "" {
				h.OnToolUse(tc.Function.Name)
			}
			toolBlocks = append(toolBlocks, ContentBlock{
				Type:  BlockToolUse,
				ID:    "call_" + strconv.Itoa(toolIdx),
				Name:  tc.Function.Name,
				Input: json.RawMessage(args),
			})
			toolIdx++
		}
		if ch.Done {
			if ch.PromptEvalCount > 0 {
				resp.Usage.InputTokens = ch.PromptEvalCount
			}
			if ch.EvalCount > 0 {
				resp.Usage.OutputTokens = ch.EvalCount
			}
			resp.StopReason = ollamaStopReason(ch.DoneReason, len(toolBlocks) > 0)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	// Finalize in the same order as the OpenAI parser: thinking, then text, then
	// tool calls.
	if thinking.Len() > 0 {
		resp.Blocks = append(resp.Blocks, ContentBlock{Type: BlockThinking, Thinking: thinking.String()})
	}
	if text.Len() > 0 {
		resp.Blocks = append(resp.Blocks, ContentBlock{Type: BlockText, Text: text.String()})
	}
	resp.Blocks = append(resp.Blocks, toolBlocks...)
	if resp.StopReason == "" {
		resp.StopReason = "end_turn"
	}
	return resp, nil
}

// ollamaStopReason maps Ollama's done_reason to the Anthropic-style stop reason
// moony uses. A turn with tool calls is always "tool_use".
func ollamaStopReason(reason string, hasTools bool) string {
	if hasTools {
		return "tool_use"
	}
	if reason == "length" {
		return "max_tokens"
	}
	return "end_turn"
}
