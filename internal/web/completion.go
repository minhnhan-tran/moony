package web

// Copilot-style inline code completion endpoint for the VS Code extension (and
// any other editor client). Given the text on either side of the cursor it does
// one cheap, tool-less, fill-in-the-middle LLM call on the FAST model and
// returns the raw text to insert at the cursor. It is deliberately stateless
// (no chat session) and honors request-context cancellation so that a keystroke
// which supersedes an in-flight request aborts the old call.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// completionMaxTokens caps the output of a single inline completion. Ghost-text
// suggestions are short (a line or a small block); a low cap keeps latency and
// cost down and stops the model from writing an essay into the buffer.
const completionMaxTokens = 256

// completionMaxContextBytes bounds how much prefix/suffix we forward even if the
// client sends more, so a huge file can't blow up the prompt.
const completionMaxContextBytes = 8000

const completionSystem = `You are a code autocompletion engine, like GitHub Copilot. You will be given the code BEFORE the cursor and the code AFTER the cursor, and you must predict the code that goes exactly at the cursor position to fill the gap.

Rules:
- Output ONLY the raw code that should be inserted at the cursor. No explanations, no commentary, no markdown code fences.
- Continue naturally from the prefix and join cleanly into the suffix. Do NOT repeat code that already appears in the prefix or the suffix.
- Match the surrounding code's language, style, indentation, and conventions.
- Prefer a short, high-confidence completion (finish the current line or a small block). If nothing sensible should be inserted, output nothing.`

// handleComplete: POST /api/complete
//
//	{ "language": "go", "prefix": "...", "suffix": "...", "path": "main.go" }
//	-> { "completion": "..." }
func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Language string `json:"language"`
		Prefix   string `json:"prefix"`
		Suffix   string `json:"suffix"`
		Path     string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	prefix := tailBytes(body.Prefix, completionMaxContextBytes)
	suffix := headBytes(body.Suffix, completionMaxContextBytes)
	if strings.TrimSpace(prefix) == "" && strings.TrimSpace(suffix) == "" {
		writeJSON(w, map[string]string{"completion": ""})
		return
	}

	prompt := buildFIMPrompt(body.Language, body.Path, prefix, suffix)

	// Route to the fast model with a small token cap on a per-call copy of the
	// config (shallow copy is fine — we only tweak a scalar), so the endpoint is
	// side-effect free and concurrency-safe.
	cfg := *s.cfg
	cfg.MaxTokens = completionMaxTokens
	client := llm.NewClient(&cfg)

	ctx := r.Context()
	model, prov := s.cfg.FastModelResolved()
	ctx = llm.WithModel(ctx, model)
	ctx = llm.WithProvider(ctx, prov)

	resp, err := client.StreamMessagesNoThink(ctx, completionSystem, []llm.Message{llm.UserText(prompt)}, nil, llm.Handler{})
	if err != nil {
		// A cancelled request (superseded keystroke) is normal; don't 502 it.
		if ctx.Err() != nil {
			writeJSON(w, map[string]string{"completion": ""})
			return
		}
		http.Error(w, "completion failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"completion": cleanCompletion(llmText(resp))})
}

// buildFIMPrompt frames the prefix/suffix as a fill-in-the-middle request.
func buildFIMPrompt(language, path, prefix, suffix string) string {
	var b strings.Builder
	if language != "" {
		b.WriteString("Language: " + language + "\n")
	}
	if path != "" {
		b.WriteString("File: " + path + "\n")
	}
	b.WriteString("\nCode before the cursor:\n")
	b.WriteString(prefix)
	b.WriteString("\n<CURSOR>\n")
	b.WriteString("Code after the cursor:\n")
	b.WriteString(suffix)
	b.WriteString("\n\nReturn only the text to insert at <CURSOR>.")
	return b.String()
}

// cleanCompletion strips markdown fences the model may add despite instructions
// and trims trailing whitespace, while preserving leading whitespace/newlines
// that are meaningful for insertion at the cursor.
func cleanCompletion(s string) string {
	if trimmed := strings.TrimSpace(s); strings.HasPrefix(trimmed, "```") {
		// Fenced block: drop the opening fence line (``` or ```lang) and the
		// closing fence, keeping the inner code (with its own indentation).
		s = trimmed
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		} else {
			s = strings.TrimPrefix(s, "```")
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	// Preserve leading whitespace (indentation is meaningful at the cursor);
	// strip only trailing whitespace.
	return strings.TrimRight(s, " \t\r\n")
}

// tailBytes returns the last n bytes of s (the part nearest the cursor).
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// headBytes returns the first n bytes of s (the part nearest the cursor).
func headBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
