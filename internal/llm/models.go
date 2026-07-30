package llm

import (
	"context"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// ModelServableBy reports whether a model id can plausibly run on the given
// backend. Model ids are provider-namespaced with no reliable cross-provider
// mapping, and moony supports many OpenAI-compatible backends whose ids are
// arbitrary (Azure deployment names, OpenRouter "vendor/model" slugs, self-hosted
// tags), so this is deliberately a NEGATIVE check: it only rejects a pin that
// clearly belongs to a DIFFERENT well-known family. Anything unrecognized is
// allowed and simply errors at call time with the provider's own message.
//
// Used to blank stale cross-provider persona pins in the UI (agents_market.go)
// and in subConfig, so a wrong pin degrades to "inherit" rather than to a
// guaranteed API error.
func ModelServableBy(model, llmProvider string) bool {
	if model == "" {
		return false
	}
	if strings.TrimSpace(llmProvider) == "" {
		return true // backend unknown (inherit) — nothing to contradict
	}
	m := strings.ToLower(model)
	fam := modelFamily(m)
	if fam == "" {
		return true // unrecognized id — don't second-guess it
	}
	switch provider.Normalize(llmProvider) {
	case provider.Ollama, provider.LMStudio:
		// Local runtimes serve open-weight tags only, never hosted proprietary models.
		return fam == "open"
	case provider.Anthropic, provider.SQEAnthropic:
		return fam == "claude"
	case provider.OpenAI:
		return fam == "gpt"
	case provider.Gemini:
		return fam == "gemini" || fam == "open" // Gemini API also serves Gemma
	default:
		// Aggregators/gateways (OpenRouter, Groq, Together, …) can serve many
		// families; don't guess.
		return true
	}
}

// modelFamily classifies a model id into a well-known family, or "" when it
// doesn't clearly belong to one.
func modelFamily(m string) string {
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt") || strings.HasPrefix(m, "chatgpt") ||
		strings.HasPrefix(m, "o1-") || strings.HasPrefix(m, "o3-") || strings.HasPrefix(m, "o4-"):
		return "gpt"
	case strings.Contains(m, "gemini"):
		return "gemini"
	case strings.Contains(m, "llama") || strings.Contains(m, "gemma") ||
		strings.Contains(m, "qwen") || strings.Contains(m, "mistral") ||
		strings.Contains(m, "deepseek") || strings.Contains(m, "phi"):
		return "open"
	}
	return ""
}

// ListModels returns the models available on the active backend: installed tags
// for Ollama, or the account's model catalog for OpenAI/Anthropic/OpenRouter.
// Users can still select any id via `/model <id>`.
func (c *Client) ListModels(ctx context.Context) ([]config.ModelInfo, error) {
	switch {
	case c.usesCopilot():
		return c.listCopilotModels(ctx)
	case c.usesBedrock():
		return c.listBedrockModels(ctx)
	case c.usesAnthropic():
		return c.listAnthropicModels(ctx)
	case c.usesRemote():
		return c.listRemoteModels(ctx, c.remoteBackend())
	default:
		return c.listOllamaModels(ctx)
	}
}
