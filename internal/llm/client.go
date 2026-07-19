package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// Client talks to the Ollama-compatible API.
type Client struct {
	cfg  *config.Config
	http *http.Client

	// Retry policy for transient request failures (network errors, 429, 5xx)
	// on the streaming turn. maxRetries is the number of retries after the
	// first attempt; retryBase is the initial backoff (doubled per retry) and
	// retryMax caps a single wait (and any Retry-After).
	maxRetries int
	retryBase  time.Duration
	retryMax   time.Duration

	// maxStreamRetries bounds re-sends after a MID-stream drop, separately from
	// maxRetries. A mid-stream retry re-submits the whole (possibly large) turn,
	// so the provider re-bills the input each time — kept lower than the
	// request/status-level retries to limit wasted spend on a flaky link.
	maxStreamRetries int
}

// NewClient builds an Ollama client from config.
func NewClient(cfg *config.Config) *Client {
	httpClient := &http.Client{Timeout: 10 * time.Minute}
	return &Client{
		cfg:              cfg,
		http:             httpClient,
		maxRetries:       4,
		retryBase:        500 * time.Millisecond,
		retryMax:         8 * time.Second,
		maxStreamRetries: streamRetriesFromEnv(2),
	}
}

// streamRetriesFromEnv returns the mid-stream re-send cap, honoring
// MOONY_STREAM_RETRIES (a non-negative integer; 0 disables mid-stream retries).
// Invalid or unset values fall back to def.
func streamRetriesFromEnv(def int) int {
	if v := strings.TrimSpace(os.Getenv("MOONY_STREAM_RETRIES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// CheckAuth verifies that Ollama is reachable.
func (c *Client) CheckAuth(ctx context.Context) error {
	switch {
	case c.usesCopilot():
		return c.checkCopilotAuth(ctx)
	case c.usesBedrock():
		return c.checkBedrockAuth(ctx)
	case c.usesAnthropic():
		return c.checkAnthropicAuth(ctx)
	case c.usesRemote():
		return c.checkRemoteAuth(ctx, c.remoteBackend())
	default:
		return c.checkOllamaAuth(ctx)
	}
}

// Handler receives streaming events as a turn is generated. Any field may be
// nil. Callbacks are invoked synchronously from the streaming goroutine.
type Handler struct {
	// OnTextDelta fires for each chunk of assistant text.
	OnTextDelta func(string)
	// OnThinkingDelta fires for each chunk of extended-thinking text.
	OnThinkingDelta func(string)
	// OnToolUse fires once per tool call, when its name first becomes known.
	OnToolUse func(name string)
	// OnRetry fires when a transient mid-stream failure (e.g. a read timeout) is
	// about to be retried from scratch. Handlers should discard any partial output
	// already emitted this turn (the retry re-streams the whole response).
	OnRetry func(err error)
}

// StreamMessages sends one turn and streams the result, assembling text and
// tool_use blocks into the returned Response. It dispatches to the OpenAI-
// compatible route when the configured provider is "openai", otherwise it uses
// the native Anthropic Messages API.
//
// Tool names are translated to a gateway-safe form for the request (the gateway's
// Bedrock models reject names outside [a-zA-Z0-9_-], such as the dotted
// "mcp.<server>.<tool>") and mapped back on the response, so the agent keeps
// StreamMessages streams a chat completion turn to Ollama, dispatching on tool names.
func (c *Client) StreamMessages(ctx context.Context, system string, messages []Message, tools []Tool, h Handler) (*Response, error) {
	// Honor per-call model and/or provider overrides on a private clone, so the
	// override applies to THIS call only and never mutates the client the web
	// server shares across sessions. A model override comes from each session's
	// /model picker; a provider override lets the Fast / Self-review tiers route a
	// single call to a different provider — and since routing keys off
	// cfg.LLMProvider, setting it on the copy re-selects that provider's base URL
	// and API key. When neither differs from the client's config, no clone is made
	// and behavior is byte-identical.
	mOverride := modelFrom(ctx)
	pOverride := providerFrom(ctx)
	if c.cfg != nil && ((mOverride != "" && mOverride != c.cfg.Model) ||
		(pOverride != "" && provider.Normalize(pOverride) != provider.Normalize(c.cfg.LLMProvider))) {
		cc := *c
		cfgCopy := *c.cfg
		if mOverride != "" {
			cfgCopy.Model = mOverride
		}
		if pOverride != "" {
			cfgCopy.LLMProvider = provider.Normalize(pOverride)
			// Keep the derived wire field coherent with the new route. It's unread
			// in this package today (routing uses LLMProvider), but correct for any
			// future reader of the cloned config.
			if provider.UsesAnthropicWire(cfgCopy.LLMProvider) {
				cfgCopy.Provider = config.ProviderAnthropic
			} else {
				cfgCopy.Provider = config.ProviderOpenAI
			}
		}
		cc.cfg = &cfgCopy
		c = &cc
	}
	// Tool names are translated to a wire-safe form and restored on the response
	// (some backends reject custom function names from the agent).
	wireTools, wireMsgs, toOrig := prepareToolNames(tools, messages)
	wrapped := wrapToolUseHandler(h, toOrig)
	var (
		resp *Response
		err  error
	)
	switch {
	case c.usesCopilot():
		resp, err = c.streamCopilot(ctx, system, wireMsgs, wireTools, wrapped)
	case c.usesBedrock():
		resp, err = c.streamBedrock(ctx, system, wireMsgs, wireTools, wrapped)
	case c.usesAnthropic():
		resp, err = c.streamAnthropic(ctx, system, wireMsgs, wireTools, wrapped)
	case c.usesRemote():
		resp, err = c.streamRemote(ctx, c.remoteBackend(), system, wireMsgs, wireTools, wrapped)
	default:
		resp, err = c.streamOllama(ctx, system, wireMsgs, wireTools, wrapped)
	}
	if err != nil {
		return nil, err
	}
	restoreToolNames(resp, toOrig)
	return resp, nil
}

// StreamMessagesNoThink is StreamMessages with reasoning disabled. For Ollama,
// it simply calls StreamMessages (there is no reasoning mode to turn off).
func (c *Client) StreamMessagesNoThink(ctx context.Context, system string, messages []Message, tools []Tool, h Handler) (*Response, error) {
	// Force reasoning off for this turn so the hosted backends don't spend a
	// thinking budget on a cheap auxiliary call.
	return c.StreamMessages(WithEffort(ctx, config.ReasoningOff), system, messages, tools, h)
}

// effortKey carries a per-call reasoning-effort override on the context, used by
// the agent to escalate just one turn (e.g. an in-prompt "ultrathink" keyword)
// above the session's configured level.
type effortKey struct{}

// WithEffort returns ctx carrying a reasoning-effort override for the next model
// call(s). An empty level is ignored (ctx unchanged), so callers can wrap
// unconditionally.
func WithEffort(ctx context.Context, level string) context.Context {
	if level == "" {
		return ctx
	}
	return context.WithValue(ctx, effortKey{}, level)
}

// withEffort is the unconditional form (used internally to force a level, e.g.
// "off").
func withEffort(ctx context.Context, level string) context.Context {
	return context.WithValue(ctx, effortKey{}, level)
}

// effortFrom returns the per-call effort override on ctx, or "" if none.
func effortFrom(ctx context.Context) string {
	if v, ok := ctx.Value(effortKey{}).(string); ok {
		return v
	}
	return ""
}

// EffortFrom exposes the per-call effort override carried on ctx (or "" if none),
// for callers/tests that need to observe the effort routed to a given model call.
func EffortFrom(ctx context.Context) string { return effortFrom(ctx) }

// modelKey carries a per-call model override on the context. The web server
// shares one *Client across every session, but each session holds its own config
// copy whose model the /model picker mutates; the agent passes its session's
// model here so the shared client honors it for this call instead of its own
// configured model. Without it, switching models in the web UI had no effect.
type modelKey struct{}

// WithModel returns ctx carrying a model override for the next model call(s). An
// empty id is ignored (ctx unchanged), so callers can wrap unconditionally.
func WithModel(ctx context.Context, model string) context.Context {
	if model == "" {
		return ctx
	}
	return context.WithValue(ctx, modelKey{}, model)
}

// modelFrom returns the per-call model override on ctx, or "" if none.
func modelFrom(ctx context.Context) string {
	if v, ok := ctx.Value(modelKey{}).(string); ok {
		return v
	}
	return ""
}

// ModelFrom exposes the per-call model override carried on ctx (the model a turn
// will actually use after WithModel), for callers/tests that need to know it.
func ModelFrom(ctx context.Context) string { return modelFrom(ctx) }

// providerKey carries a per-call provider override on the context. The Fast and
// Self-review tiers can route a single call to a different provider than the
// session (see config.Fast/AuxModelRoute); because llm routing keys off
// cfg.LLMProvider, StreamMessages applies this by cloning the config with the new
// provider so the right base URL + API key are selected for that call only.
type providerKey struct{}

// WithProvider returns ctx carrying a provider override for the next model
// call(s). An empty id is ignored (ctx unchanged), so callers can wrap
// unconditionally.
func WithProvider(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, providerKey{}, id)
}

// providerFrom returns the per-call provider override on ctx, or "" if none.
func providerFrom(ctx context.Context) string {
	if v, ok := ctx.Value(providerKey{}).(string); ok {
		return v
	}
	return ""
}

// ProviderFrom exposes the per-call provider override carried on ctx (or "" if
// none), for callers/tests that need to observe the route a call will take.
func ProviderFrom(ctx context.Context) string { return providerFrom(ctx) }

// streamMessages performs the actual request/stream for one turn, dispatching by
// route. Callers go through StreamMessages, which handles tool-name translation.
// streamWithRetry issues the streaming request and decodes its SSE body, sharing
// one retry policy across request attempts. newReq builds a fresh request per
// attempt, parse decodes the response stream, and h is the event handler. Stops
// after maxStreamRetries, on a cancelled context, or for a non-retryable failure.
// The spend of any failed attempts is carried onto the returned Response's RetryUsage
// so cost accounting stays honest.
func (c *Client) streamWithRetry(ctx context.Context, newReq func() (*http.Request, error), label string, parse func(io.Reader, Handler) (*Response, error), h Handler) (*Response, error) {
	backoff := c.retryBase
	var wasted Usage // tokens billed by failed attempts we re-sent past
	for attempt := 0; ; attempt++ {
		resp, err := c.doWithRetry(ctx, newReq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
			resp.Body.Close()
			return nil, fmt.Errorf("%s returned %s: %s", label, resp.Status, strings.TrimSpace(string(msg)))
		}
		out, perr := parse(resp.Body, h)
		resp.Body.Close()
		if perr == nil {
			// Attach the spend of any failed attempts so cost accounting is honest
			// (the context-size indicators still use out.Usage, the final call).
			out.RetryUsage = wasted
			return out, nil
		}
		if attempt >= c.maxStreamRetries || ctx.Err() != nil || !retryableStreamError(perr) {
			return nil, perr
		}
		// This attempt failed mid-stream; the provider still billed the input it
		// processed (out is the partial Response). Carry that forward as waste.
		if out != nil {
			wasted.InputTokens += out.Usage.InputTokens
			wasted.OutputTokens += out.Usage.OutputTokens
		}
		if h.OnRetry != nil {
			h.OnRetry(perr) // tell the handler to drop partial output before we re-stream
		}
		select {
		case <-time.After(jitter(backoff)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if backoff < c.retryMax {
			backoff *= 2
		}
	}
}

// retryableStreamError reports whether a failure while reading the response
// stream is a transient network condition worth re-sending the turn for (read
// timeouts, resets, premature EOF). A cancelled/expired context is never
// retryable — the caller asked to stop.
func retryableStreamError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"operation timed out", "i/o timeout", "connection reset", "broken pipe", "unexpected eof", "connection closed", "use of closed", "tls", "read: eof"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// doWithRetry sends the request built by newReq, retrying transient failures
// (network errors, 429, and 5xx/529) with exponential backoff + jitter. newReq
// is called once per attempt so the body can be re-read and the auth token
// refreshed. It returns the first response whose status is not transiently
// retryable — which may still be a non-2xx the caller must inspect — or the
// last error after exhausting retries. A cancelled context aborts immediately.
//
// Retries cover only the request and initial status; once a 2xx body starts
// streaming the caller consumes it directly (no mid-stream retry, which would
// duplicate already-emitted output).
func (c *Client) doWithRetry(ctx context.Context, newReq func() (*http.Request, error)) (*http.Response, error) {
	var (
		lastErr    error
		backoff    = c.retryBase
		retryAfter time.Duration
	)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			wait := retryAfter
			if wait <= 0 {
				wait = jitter(backoff)
				if backoff < c.retryMax {
					backoff *= 2
				}
			}
			if c.retryMax > 0 && wait > c.retryMax {
				wait = c.retryMax
			}
			retryAfter = 0
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			}
		}

		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("request failed: %w", err)
		case !retryableStatus(resp.StatusCode):
			return resp, nil
		default:
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			bodyStr := strings.TrimSpace(string(body))
			lastErr = fmt.Errorf("ollama returned %s: %s", resp.Status, bodyStr)
		}

		if attempt >= c.maxRetries {
			return nil, fmt.Errorf("after %d attempt(s): %w", attempt+1, lastErr)
		}
	}
}

// retryableStatus reports whether an HTTP status warrants a retry: rate limits,
// request/gateway timeouts, and transient server/overload errors.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
		529:                            // Anthropic "overloaded"
		return true
	}
	return false
}

// parseRetryAfter interprets a Retry-After header (delay-seconds or HTTP date),
// returning 0 when absent or unparsable.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// jitter returns a randomized backoff in [d/2, d) to avoid synchronized retries.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// Per-turn system-prompt directives.
//
// A caller may append a process-volatile directive to the stable base system
// prompt for a single turn (e.g. ultrathink, investigation steering). It MUST be
// appended as  base + SystemDirectiveSeparator + <directive>,  where <directive>
// begins with one of the markers below. applyPromptCache splits the system
// prompt at that boundary so only the stable base gets the cache breakpoint — the
// volatile suffix is sent uncached and never invalidates the cached prefix.
//
// These are the single source of truth for that boundary: the agent package
// builds its directive constants from them (and joins with the separator), so the
// split here and the directives there can never silently drift apart.
const (
	SystemDirectiveSeparator     = "\n\n"
	UltrathinkDirectiveMarker    = "ULTRATHINK MODE"
	InvestigationDirectiveMarker = "INVESTIGATION MODE"
	// ContextDirectiveMarker leads the per-turn session-context suffix (the
	// distilled checkpoint, plan, and plan-mode steering) when no ultrathink/
	// investigation directive is present, so applyPromptCache peels that volatile
	// context off uncached too — carrying it here instead of as a front-of-history
	// message keeps the cached message prefix from being invalidated when the
	// checkpoint refreshes.
	ContextDirectiveMarker = "SESSION CONTEXT"
)

// systemDirectiveMarkers are the full appended-directive boundaries (separator +
// header) applyPromptCache splits on. Internal to the cache logic. The agent
// appends at most ONE marker per turn (a directive leads, with the context
// preface riding under it; or the context preface leads with the context marker),
// so split-at-last unambiguously anchors on the start of the appended suffix.
var systemDirectiveMarkers = []string{
	SystemDirectiveSeparator + UltrathinkDirectiveMarker,
	SystemDirectiveSeparator + InvestigationDirectiveMarker,
	SystemDirectiveSeparator + ContextDirectiveMarker,
}

// splitSystemDirective separates the stable base system prompt from any per-turn
// directive appended to it (see systemDirectiveMarkers). Because a directive is
// always APPENDED, it splits at the marker nearest the END of the prompt — this
// stays correct even if the base prompt happens to mention a marker phrase
// earlier (e.g. via AGENTS.md). Returns (s, "") when no directive is present.
func splitSystemDirective(s string) (base, suffix string) {
	bestIdx := -1
	for _, marker := range systemDirectiveMarkers {
		if idx := strings.LastIndex(s, marker); idx > bestIdx {
			bestIdx = idx
		}
	}
	if bestIdx > 0 { // >0: keep a non-empty cacheable base
		return s[:bestIdx], s[bestIdx:]
	}
	return s, ""
}

// applyPromptCache marks prompt-caching breakpoints on the request so Anthropic
// can reuse the cached prefix across the many steps of an agentic turn (cached
// input is billed ~10x cheaper). It marks the stable prefix — the tools block
// and the system prompt — plus the end of the conversation so the growing
// history is cached up to the latest turn. It operates on copies so it never
// mutates the caller's system/tools/history (cache_control must not accumulate
// there, and the API caps breakpoints at 4). No-op when disabled via
// MOONY_PROMPT_CACHE. Anthropic-route only; the OpenAI route caches automatically.
func applyPromptCache(body *request) {
	if !promptCacheEnabled() {
		return
	}
	ep := &cacheControl{Type: "ephemeral"}

	// Tools prefix (stable for the whole session): mark the last tool.
	if n := len(body.Tools); n > 0 {
		tools := make([]Tool, n)
		copy(tools, body.Tools)
		tools[n-1].CacheControl = ep
		body.Tools = tools
	}

	// System prompt: send it as a cacheable content-block array. If a volatile
	// per-turn directive was appended (ultrathink/investigation), split it off so
	// only the stable base carries the cache breakpoint — the suffix stays
	// uncached and never invalidates the cached prefix. See splitSystemDirective.
	if s, ok := body.System.(string); ok && s != "" {
		base, suffix := splitSystemDirective(s)
		if suffix != "" {
			body.System = []systemBlock{
				{Type: "text", Text: base, CacheControl: ep},
				{Type: "text", Text: suffix}, // uncached dynamic suffix
			}
		} else {
			body.System = []systemBlock{{Type: "text", Text: s, CacheControl: ep}}
		}
	}

	// Conversation prefix: mark the last message so history up to this turn is
	// cached and reused next turn (longest-prefix match). Also use the 4th (and
	// final) Anthropic breakpoint to anchor the older stable half of the
	// conversation independently — in a long tool loop the "older turns" prefix
	// stays constant while the latest exchange keeps growing, so this second
	// anchor survives across retries and reduces cache misses on deep histories.
	if n := len(body.Messages); n > 0 {
		msgs := make([]Message, n)
		copy(msgs, body.Messages)

		// 4th breakpoint: mid-conversation anchor (only when history is deep
		// enough that the two marks are meaningfully separated — ≥8 messages).
		if n >= 8 {
			mid := n / 2
			if c := msgs[mid].Content; len(c) > 0 {
				cc2 := make([]ContentBlock, len(c))
				copy(cc2, c)
				cc2[len(cc2)-1].CacheControl = ep
				msgs[mid].Content = cc2
			}
		}

		// 3rd breakpoint: latest message (newest prefix anchor).
		if c := msgs[n-1].Content; len(c) > 0 {
			cc := make([]ContentBlock, len(c))
			copy(cc, c)
			cc[len(cc)-1].CacheControl = ep
			msgs[n-1].Content = cc
		}
		body.Messages = msgs
	}
}

// promptCacheEnabled reports whether Anthropic prompt caching is on (default).
// Disable with MOONY_PROMPT_CACHE=0 (or off/false/no).
func promptCacheEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MOONY_PROMPT_CACHE"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// blockBuilder accumulates a single content block as deltas arrive.
type blockBuilder struct {
	typ       string
	text      strings.Builder
	id        string
	name      string
	jsonBuf   strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	data      string
	notified  bool
}

// emptyContentPlaceholder repairs a message that would otherwise carry no
// content. It must be NON-EMPTY and non-whitespace because the upstream backends
// disagree on the failure mode: Vertex AI rejects a null/missing content array
// ("messages.N.content: Field required") while Bedrock rejects an empty text
// block ("text content blocks must be non-empty"). A single non-empty marker
// satisfies both.
const emptyContentPlaceholder = "(no content)"

// prepareMessages returns the messages to send. When extended thinking is
// disabled the provider rejects replayed thinking blocks, so they are stripped;
// when enabled they are preserved intact (their signatures are required for
// assistant turns that contain tool_use).
//
// Regardless of thinking, it enforces two content-shape invariants the upstream
// Anthropic backends validate, and which can be violated by a stream that
// finalized a text block with no deltas, an assistant turn with zero blocks (an
// empty/refused completion), or such a message replayed from a long persisted
// history: (1) no empty text blocks (Bedrock: "text content blocks must be
// non-empty") — dropped; (2) no message with an empty/null content array
// (Vertex: "content: Field required") — backfilled with a non-empty placeholder.
// Repairing here at the send boundary both prevents new violations and fixes
// already-persisted bad messages that are replayed on every turn.
func prepareMessages(messages []Message, thinkingEnabled bool) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		kept := make([]ContentBlock, 0, len(m.Content))
		for _, c := range m.Content {
			switch {
			case !thinkingEnabled && (c.Type == BlockThinking || c.Type == BlockRedactedThinking):
				// Thinking blocks can't be replayed when thinking is disabled.
				continue
			case c.Type == BlockText && strings.TrimSpace(c.Text) == "":
				// An empty text block is rejected by some backends and carries no
				// information; drop it (a tool_use/image sibling, if present, still
				// keeps the message non-empty).
				continue
			}
			kept = append(kept, c)
		}
		// If nothing survived (a thinking-only turn stripped above, an
		// empty/refused completion, or a content-less message from history), fall
		// back to a compliant non-empty text block so we never send null or
		// empty-text content.
		if len(kept) == 0 {
			kept = append(kept, TextBlock(emptyContentPlaceholder))
		}
		out = append(out, Message{Role: m.Role, Content: kept})
	}
	return out
}

// anthropicTag summarizes an SSE event for the debug log: the event type, plus
// the block type (for content_block_start) or delta type (for
// content_block_delta) so reasoning/thinking events are easy to spot.
func anthropicTag(ev sseEvent) string {
	switch ev.Type {
	case "content_block_start":
		if ev.ContentBlock != nil {
			return fmt.Sprintf("content_block_start/%s#%d", ev.ContentBlock.Type, ev.Index)
		}
	case "content_block_delta":
		if ev.Delta != nil {
			return fmt.Sprintf("content_block_delta/%s#%d", ev.Delta.Type, ev.Index)
		}
	}
	return ev.Type
}

// parseStream consumes an SSE body and assembles the final Response.
func parseStream(r io.Reader, h Handler) (*Response, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	resp := &Response{}
	builders := map[int]*blockBuilder{}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			// Skip malformed keep-alive or partial lines defensively.
			dbg("<<< [unparsed] %s", data)
			continue
		}
		dbg("<<< [%s] %s", anthropicTag(ev), data)

		switch ev.Type {
		case "message_start":
			// Anthropic reports input token counts (including the cached
			// portions, which it lists separately) on the opening message.
			// Fold them together so InputTokens reflects the full context size.
			if len(ev.Message) > 0 {
				var ms struct {
					Usage Usage `json:"usage"`
				}
				if json.Unmarshal(ev.Message, &ms) == nil {
					if in := totalInput(ms.Usage); in > 0 {
						resp.Usage.InputTokens = in
					}
					if ms.Usage.OutputTokens > 0 {
						resp.Usage.OutputTokens = ms.Usage.OutputTokens
					}
					// Keep the cache split (folded into InputTokens above) for the
					// per-turn footer's cache-hit telemetry.
					if ms.Usage.CacheReadInputTokens > 0 {
						resp.Usage.CacheReadInputTokens = ms.Usage.CacheReadInputTokens
					}
					if ms.Usage.CacheCreationInputTokens > 0 {
						resp.Usage.CacheCreationInputTokens = ms.Usage.CacheCreationInputTokens
					}
				}
			}

		case "content_block_start":
			b := &blockBuilder{}
			if ev.ContentBlock != nil {
				b.typ = ev.ContentBlock.Type
				b.id = ev.ContentBlock.ID
				b.name = ev.ContentBlock.Name
				b.text.WriteString(ev.ContentBlock.Text)
				b.data = ev.ContentBlock.Data
				// Some gateways deliver the thinking text inline on the start
				// event instead of via thinking_delta; capture and surface it.
				if ev.ContentBlock.Thinking != "" {
					b.thinking.WriteString(ev.ContentBlock.Thinking)
					if h.OnThinkingDelta != nil {
						h.OnThinkingDelta(ev.ContentBlock.Thinking)
					}
				}
			}
			builders[ev.Index] = b
			if b.typ == BlockToolUse && b.name != "" && h.OnToolUse != nil {
				b.notified = true
				h.OnToolUse(b.name)
			}

		case "content_block_delta":
			b := builders[ev.Index]
			if b == nil || ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.text.WriteString(ev.Delta.Text)
				if h.OnTextDelta != nil && ev.Delta.Text != "" {
					h.OnTextDelta(ev.Delta.Text)
				}
			case "thinking_delta":
				b.thinking.WriteString(ev.Delta.Thinking)
				if h.OnThinkingDelta != nil && ev.Delta.Thinking != "" {
					h.OnThinkingDelta(ev.Delta.Thinking)
				}
			case "signature_delta":
				b.signature.WriteString(ev.Delta.Signature)
			case "input_json_delta":
				b.jsonBuf.WriteString(ev.Delta.PartialJSON)
			}

		case "content_block_stop":
			b := builders[ev.Index]
			if b == nil {
				continue
			}
			// The model reasoned but the provider redacted the text (thinking block
			// with a signature but no thinking_delta). Surface a marker so the
			// user can see that reasoning happened.
			if (b.typ == BlockThinking || b.typ == BlockRedactedThinking) &&
				b.thinking.Len() == 0 && h.OnThinkingDelta != nil {
				h.OnThinkingDelta("(reasoning hidden by the provider)")
			}
			resp.Blocks = append(resp.Blocks, finalizeBlock(b))

		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				resp.StopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				resp.Usage.OutputTokens = ev.Usage.OutputTokens
				if in := totalInput(*ev.Usage); in > 0 {
					resp.Usage.InputTokens = in
				}
			}

		case "message_stop":
			// terminal; loop will end on EOF

		case "error":
			if ev.Error != nil {
				return nil, fmt.Errorf("stream error (%s): %s", ev.Error.Type, ev.Error.Message)
			}
			return nil, fmt.Errorf("unknown stream error")
		}
	}
	if err := scanner.Err(); err != nil {
		// Return the partial resp (not nil) so its accumulated Usage — the input
		// tokens the provider already billed before the drop — survives for
		// honest cost accounting in the retry loop.
		return resp, fmt.Errorf("reading stream: %w", err)
	}

	// Blocks may arrive out of index order across builders; reorder defensively
	// is unnecessary here because Anthropic emits stop events in index order.
	return resp, nil
}

// finalizeBlock converts an accumulated builder into a content block.
func finalizeBlock(b *blockBuilder) ContentBlock {
	switch b.typ {
	case BlockToolUse:
		input := strings.TrimSpace(b.jsonBuf.String())
		if input == "" {
			input = "{}"
		}
		return ContentBlock{
			Type:  BlockToolUse,
			ID:    b.id,
			Name:  b.name,
			Input: json.RawMessage(input),
		}
	case BlockThinking:
		return ContentBlock{
			Type:      BlockThinking,
			Thinking:  b.thinking.String(),
			Signature: b.signature.String(),
		}
	case BlockRedactedThinking:
		return ContentBlock{Type: BlockRedactedThinking, Data: b.data}
	default:
		return ContentBlock{Type: BlockText, Text: b.text.String()}
	}
}

// totalInput is the full input-token size of a turn: fresh tokens plus the
// cached portions Anthropic reports separately when prompt caching is active.
func totalInput(u Usage) int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}
