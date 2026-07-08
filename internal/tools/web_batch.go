package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// defaultBatchConcurrency bounds how many URLs batch_web_fetch retrieves at once;
// maxBatchConcurrency is the hard ceiling.
const (
	defaultBatchConcurrency = 4
	maxBatchConcurrency     = 8
)

// batchWebFetchTool fetches several URLs concurrently, reusing web_fetch's
// per-URL logic and content-type routing. Each request accepts the same options
// as web_fetch. It is non-mutating (idempotent GETs).
type batchWebFetchTool struct {
	r  *Registry
	wf *webFetchTool
}

// fetcher returns the shared web_fetch worker (so both tools share one HTTP
// client), creating a standalone one if this tool was registered on its own.
func (b *batchWebFetchTool) fetcher() *webFetchTool {
	if b.wf == nil {
		b.wf = &webFetchTool{r: b.r}
	}
	return b.wf
}

func (b *batchWebFetchTool) Name() string { return "batch_web_fetch" }

func (b *batchWebFetchTool) Description() string {
	return "Fetch multiple URLs concurrently (bounded parallelism) and return " +
		"their contents in order. Each item in 'requests' takes the same options " +
		"as web_fetch (url, format, headers, max_bytes, timeout_sec). Prefer this " +
		"over many sequential web_fetch calls when you need several pages at once " +
		"(docs, search results, a set of raw files). One slow or failing URL does " +
		"not block the others; per-item errors are reported inline. Does NOT run " +
		"JavaScript."
}

func (b *batchWebFetchTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"requests": {
				"type": "array",
				"minItems": 1,
				"description": "Fetch requests. Each item accepts the same fields as web_fetch.",
				"items": {
					"type": "object",
					"properties": {
						"url":         {"type": "string", "description": "Absolute http or https URL."},
						"format":      {"type": "string", "enum": ["markdown","html","text","json"], "description": "Output shape (default markdown)."},
						"headers":     {"type": "object", "additionalProperties": {"type": "string"}, "description": "Extra HTTP request headers."},
						"max_bytes":   {"type": "integer", "description": "Cap response body bytes; default 262144."},
						"timeout_sec": {"type": "integer", "description": "Max seconds per request; default 30."}
					},
					"required": ["url"]
				}
			},
			"max_concurrency": {"type": "integer", "description": "Parallel fetches; default 4, max 8."}
		},
		"required": ["requests"]
	}`)
}

func (b *batchWebFetchTool) Mutates() bool { return false }

func (b *batchWebFetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Requests       []fetchParams `json:"requests"`
		MaxConcurrency flexInt       `json:"max_concurrency"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if len(in.Requests) == 0 {
		return "", fmt.Errorf("requests is required and must contain at least one item")
	}

	conc := int(in.MaxConcurrency)
	if conc <= 0 {
		conc = defaultBatchConcurrency
	}
	if conc > maxBatchConcurrency {
		conc = maxBatchConcurrency
	}
	if conc > len(in.Requests) {
		conc = len(in.Requests)
	}

	results := make([]string, len(in.Requests))
	jobs := make(chan int)
	var wg sync.WaitGroup
	wf := b.fetcher()

	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out, err := wf.do(ctx, in.Requests[i])
				if err != nil {
					results[i] = fmt.Sprintf("Error fetching %s: %v", urlOf(in.Requests[i]), err)
					continue
				}
				results[i] = out
			}
		}()
	}
	for i := range in.Requests {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var out strings.Builder
	for i, r := range results {
		if i > 0 {
			out.WriteString("\n\n---\n\n")
		}
		fmt.Fprintf(&out, "## Request %d\n\n%s", i+1, r)
	}
	return out.String(), nil
}

func urlOf(p fetchParams) string {
	if u := strings.TrimSpace(p.URL); u != "" {
		return u
	}
	return "(missing url)"
}
