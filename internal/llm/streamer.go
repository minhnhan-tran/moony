package llm

import (
	"context"

	"github.com/pysty1995/moony/internal/config"
)

// Streamer is the agent's view of the LLM client: it streams a single model
// turn and proxies the model-list and usage lookups behind /models and /usage.
// *Client is the production implementation; tests substitute a fake to drive the
// agent's Run loop deterministically without touching the network.
type Streamer interface {
	StreamMessages(ctx context.Context, system string, messages []Message, tools []Tool, h Handler) (*Response, error)
	ListModels(ctx context.Context) ([]config.ModelInfo, error)
	Usage(ctx context.Context) (*UsageResponse, error)
	CheckAuth(ctx context.Context) error
}

// Compile-time assurance that the concrete client satisfies the interface.
var _ Streamer = (*Client)(nil)
