package workflow

// LLM-judge helper shared by the Guardrails node and Evals. It makes a single
// tool-less model call on the cheap "fast" model, asks for a strict JSON verdict,
// and parses it. Kept here (not in the agent package) so guardrails/evals can be
// unit-tested with a fake llm.Streamer and no agent loop.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// responseText concatenates the text blocks of an llm.Response.
func responseText(r *llm.Response) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range r.Blocks {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// extractJSON pulls the first balanced {...} object out of s — models often wrap
// JSON in prose or ```json fences despite instructions. Returns "" if none.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// judge runs one tool-less fast-model call with the given system + user prompt
// and unmarshals the model's JSON reply into out. Returns the raw text and any
// error (model error, or no parseable JSON when out != nil).
func judge(ctx context.Context, client llm.Streamer, base *config.Config, system, user string, out any) (string, error) {
	if client == nil {
		return "", errors.New("no llm client")
	}
	// The judge runs on the Fast tier, which may route to a different provider
	// and reasoning level than the session (Fast model provider/reasoning).
	m, prov, _ := config.FastModelRoute(base.Model, base.LLMProvider)
	ctx = llm.WithModel(ctx, m)
	if prov != "" {
		ctx = llm.WithProvider(ctx, prov)
	}
	if fr := config.FastReasoning(); fr != "" {
		ctx = llm.WithEffort(ctx, fr)
	}
	resp, err := client.StreamMessages(ctx, system, []llm.Message{llm.UserText(user)}, nil, llm.Handler{})
	if err != nil {
		return "", err
	}
	text := responseText(resp)
	if out != nil {
		js := extractJSON(text)
		if js == "" {
			return text, errors.New("judge returned no JSON")
		}
		if err := json.Unmarshal([]byte(js), out); err != nil {
			return text, err
		}
	}
	return text, nil
}
