package llm

import (
	"context"

	"github.com/pysty1995/moony/internal/config"
)

// ModelServableBy reports whether a model id can run on the given backend. Each
// backend has its own model namespace and there's no reliable cross-provider
// mapping, so any non-empty id is considered servable (a bad id just errors at
// call time with the provider's own message).
func ModelServableBy(model, llmProvider string) bool {
	return model != ""
}

// ListModels returns the models available on the active backend: installed tags
// for Ollama, or the account's model catalog for OpenAI/Anthropic/OpenRouter.
// Users can still select any id via `/model <id>`.
func (c *Client) ListModels(ctx context.Context) ([]config.ModelInfo, error) {
	switch {
	case c.usesCopilot():
		return c.listCopilotModels(ctx)
	case c.usesAnthropic():
		return c.listAnthropicModels(ctx)
	case c.usesRemote():
		return c.listRemoteModels(ctx, c.remoteBackend())
	default:
		return c.listOllamaModels(ctx)
	}
}
