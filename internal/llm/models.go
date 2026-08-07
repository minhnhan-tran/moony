package llm

import (
	"context"
	"fmt"
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
	case provider.Meta:
		return fam == "muse" || fam == "open" // Model API serves Muse Spark, and Meta's own Llamas
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
	case strings.HasPrefix(m, "muse-"):
		// Meta's Muse Spark coding models: hosted only (Meta Model API, OpenRouter),
		// no open weights, so local runtimes must not claim to serve them.
		return "muse"
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

// knownModelName maps a model id to its curated display name, falling back to a
// humanized form of the id so the result is never blank.
func knownModelName(id string) string {
	for _, list := range [][]config.ModelInfo{config.AllKnownModels(), config.KnownImageModels, config.KnownVideoModels} {
		for _, m := range list {
			if m.ID == id && m.Name != "" {
				return m.Name
			}
		}
	}
	// Never return blank: an empty label renders as an empty picker option.
	return humanizeModelID(id)
}

// ListImageModels fetches image-output-capable models (e.g. Gemini "Flash
// Image" variants) from the active OpenAI-compatible backend, for the
// generate_image model picker. Falls back to the curated
// config.KnownImageModels list when the live fetch fails or turns up none.
func (c *Client) ListImageModels(ctx context.Context) ([]config.ModelInfo, error) {
	models, err := c.listRemoteModels(ctx, c.remoteBackend())
	var out []config.ModelInfo
	for _, m := range models {
		if isImageGenModel(m.ID) {
			if m.Name == "" {
				// Catalogs commonly omit a display name; fill in the curated one.
				m.Name = knownModelName(m.ID)
			}
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		if err == nil {
			err = fmt.Errorf("no image models returned")
		}
		out = append([]config.ModelInfo(nil), config.KnownImageModels...)
	}
	return out, err
}

// isImageGenModel reports whether a model id looks like an image-generation
// (not text-to-speech) model — the image markers isNonChatModel excludes from
// the chat picker, minus TTS.
func isImageGenModel(id string) bool {
	x := strings.ToLower(id)
	for _, marker := range []string{"-tts", "tts-", "text-to-speech"} {
		if strings.Contains(x, marker) {
			return false
		}
	}
	for _, marker := range []string{"image-generation", "-image", "image-", "imagen"} {
		if strings.Contains(x, marker) {
			return true
		}
	}
	return false
}

// isNonChatModel reports whether a model id is a non-chat model that shouldn't
// appear in the model picker: video-generation models (e.g. Veo, any family) or
// non-chat Gemini variants (text-to-speech, image generation).
func isNonChatModel(id string) bool {
	x := strings.ToLower(id)
	// Video-generation models are never chat models — filter them regardless of
	// family/provider prefix.
	for _, marker := range []string{"veo", "video-generation", "-video", "video-"} {
		if strings.Contains(x, marker) {
			return true
		}
	}
	if !strings.Contains(x, "gemini") {
		return false
	}
	for _, marker := range []string{"-tts", "tts-", "text-to-speech", "image-generation", "-image", "image-", "imagen"} {
		if strings.Contains(x, marker) {
			return true
		}
	}
	return false
}

// humanizeModelID turns a bare id like "gemini-3.1-flash-lite-preview" into a
// readable label like "Gemini 3.1 Flash Lite Preview": strips a "provider:"
// prefix, splits on '-', and capitalizes alphabetic tokens.
func humanizeModelID(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	parts := strings.Split(id, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		if r[0] >= 'a' && r[0] <= 'z' {
			r[0] -= 'a' - 'A'
			parts[i] = string(r)
		}
	}
	return strings.Join(parts, " ")
}
