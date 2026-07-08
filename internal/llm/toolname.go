package llm

// Tool-name sanitization for the wire. Floodgate (and the Bedrock models behind
// it) require tool names to match ^[a-zA-Z0-9_-]{1,64}$, but the agent names MCP
// tools "mcp.<server>.<tool>" (dots) to signal provenance. Rather than change
// that internal convention (used by the registry, /doctor, the UI, and prompts),
// we translate names to a wire-safe form only when building the request, and map
// the streamed response's tool_use names back — so the rest of the agent keeps
// working in its dotted vocabulary.

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
)

const maxToolNameLen = 64

// invalidToolNameChar matches any character the model gateway forbids in a tool
// name (anything outside [a-zA-Z0-9_-]).
var invalidToolNameChar = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// wireToolName converts an internal tool name into one the gateway accepts.
// Already-valid names (e.g. the built-in read_file/bash) pass through unchanged
// so their identity stays stable; a name that must be altered (e.g. the dotted
// "mcp.browser-agent.run_task") has its invalid characters replaced with "_" and
// a short suffix derived from the original appended — making the result
// deterministic per original, collision-resistant, and stable across turns.
func wireToolName(name string) string {
	if name != "" && len(name) <= maxToolNameLen && !invalidToolNameChar.MatchString(name) {
		return name
	}
	cleaned := invalidToolNameChar.ReplaceAllString(name, "_")
	sum := sha1.Sum([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:])[:6]
	if len(cleaned) > maxToolNameLen-len(suffix) {
		cleaned = cleaned[:maxToolNameLen-len(suffix)]
	}
	return cleaned + suffix
}

// CleanToolName returns the wire name WITHOUT the collision-suffix — the form a
// model is likely to emit if it sanitizes a dotted name itself (e.g. underscoring
// "mcp.codebase-memory.search_graph" it read in the prompt into
// "mcp_codebase-memory_search_graph"). Returns "" when the name is already
// wire-safe (so there's no suffix-less variant to alias). It's exported so the
// agent can render this same dispatch-safe form in the system prompt's tool
// listing — keeping what the model reads consistent with what addSuffixlessAliases
// will accept, instead of showing the dotted internal name the model can't call.
func CleanToolName(name string) string {
	if name == "" || (len(name) <= maxToolNameLen && !invalidToolNameChar.MatchString(name)) {
		return ""
	}
	c := invalidToolNameChar.ReplaceAllString(name, "_")
	if len(c) > maxToolNameLen {
		c = c[:maxToolNameLen]
	}
	return c
}

// prepareToolNames rewrites tool names to their wire-safe form on both the tool
// definitions and any tool_use blocks in the history (so the assistant's prior
// calls still match the renamed definitions), returning the rewritten copies and
// a wire->original map so the response can be translated back. Inputs are never
// mutated; messages are cloned lazily (only those carrying a tool_use). It's a
// no-op when no tools are present (e.g. the title/summary calls pass none).
func prepareToolNames(tools []Tool, messages []Message) ([]Tool, []Message, map[string]string) {
	if len(tools) == 0 {
		return tools, messages, nil
	}
	toOrig := make(map[string]string, len(tools))
	wireTools := make([]Tool, len(tools))
	for i, t := range tools {
		w := wireToolName(t.Name)
		toOrig[w] = t.Name
		t.Name = w
		wireTools[i] = t
	}

	wireMsgs := messages
	copied := false
	for i, m := range messages {
		if !hasToolUse(m) {
			continue
		}
		if !copied {
			wireMsgs = append([]Message(nil), messages...)
			copied = true
		}
		blocks := append([]ContentBlock(nil), m.Content...)
		for j, b := range blocks {
			if b.Type == BlockToolUse {
				w := wireToolName(b.Name)
				if _, ok := toOrig[w]; !ok {
					toOrig[w] = b.Name
				}
				blocks[j].Name = w
			}
		}
		wireMsgs[i] = Message{Role: m.Role, Content: blocks}
	}
	addSuffixlessAliases(toOrig)
	return wireTools, wireMsgs, toOrig
}

// addSuffixlessAliases makes a model's suffix-less call resolve: it maps the
// sanitized-but-unhashed form of each tool name back to the original, so a model
// that emits "mcp_codebase-memory_search_graph" (no hash — e.g. it underscored a
// dotted name from the prompt itself) still dispatches to
// "mcp.codebase-memory.search_graph". Added only when the form is unambiguous
// (exactly one tool produces it) and doesn't shadow a real wire name, so it can
// never mis-route.
func addSuffixlessAliases(toOrig map[string]string) {
	byClean := map[string]map[string]bool{}
	for _, orig := range toOrig {
		c := CleanToolName(orig)
		if c == "" {
			continue
		}
		if byClean[c] == nil {
			byClean[c] = map[string]bool{}
		}
		byClean[c][orig] = true
	}
	for c, set := range byClean {
		if len(set) != 1 {
			continue // ambiguous — two tools sanitize to the same suffix-less form
		}
		if _, isWire := toOrig[c]; isWire {
			continue // already a real wire name; don't shadow it
		}
		for orig := range set {
			toOrig[c] = orig
		}
	}
}

func hasToolUse(m Message) bool {
	for _, b := range m.Content {
		if b.Type == BlockToolUse {
			return true
		}
	}
	return false
}

// restoreToolNames maps a response's tool_use names from the wire form back to
// the original internal names the agent dispatches on.
func restoreToolNames(resp *Response, toOrig map[string]string) {
	if resp == nil || len(toOrig) == 0 {
		return
	}
	for i, b := range resp.Blocks {
		if b.Type == BlockToolUse {
			if orig, ok := toOrig[b.Name]; ok {
				resp.Blocks[i].Name = orig
			}
		}
	}
}

// wrapToolUseHandler translates the wire tool name back to the original in the
// OnToolUse streaming callback, so any consumer sees the agent's name. It's a
// no-op when the caller didn't set OnToolUse.
func wrapToolUseHandler(h Handler, toOrig map[string]string) Handler {
	if h.OnToolUse == nil || len(toOrig) == 0 {
		return h
	}
	inner := h.OnToolUse
	h.OnToolUse = func(name string) {
		if orig, ok := toOrig[name]; ok {
			inner(orig)
		} else {
			inner(name)
		}
	}
	return h
}
