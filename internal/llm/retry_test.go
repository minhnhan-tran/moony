package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// newTestClient returns a Client wired to the given RoundTripper, configured
// for the (sole) Ollama provider.
func newTestClient(rt http.RoundTripper) *Client {
	cfg := &config.Config{
		LLMProvider:   provider.Ollama,
		OllamaBaseURL: "https://ollama.test/v1",
		Model:         "gemma4:31b",
	}
	return &Client{cfg: cfg, http: &http.Client{Transport: rt}}
}

// roundTripperFunc adapts a function into an http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestUsageNotAvailable: Ollama has no account/quota, so Usage always errors —
// callers treat that as "quota unknown" and hide the display.
func TestUsageNotAvailable(t *testing.T) {
	c := newTestClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("Usage should not make any HTTP request for ollama")
		return nil, nil
	}))
	if _, err := c.Usage(context.Background()); err == nil {
		t.Fatal("expected an error from Usage() on the ollama provider")
	}
}

func TestRetryableStatus(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503, 504, 529} {
		if !retryableStatus(code) {
			t.Errorf("status %d should be retryable", code)
		}
	}
	for _, code := range []int{200, 201, 400, 401, 403, 404, 422} {
		if retryableStatus(code) {
			t.Errorf("status %d should not be retryable", code)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("3"); got != 3*time.Second {
		t.Errorf("seconds: got %v, want 3s", got)
	}
	for _, v := range []string{"", "  ", "-1", "garbage"} {
		if got := parseRetryAfter(v); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %v, want 0", v, got)
		}
	}
	// HTTP-date form, a few seconds out.
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 6*time.Second {
		t.Errorf("date form: got %v, want (0, 6s]", got)
	}
}

// fastRetryClient returns a stubbed client with negligible backoff so retry
// tests don't actually sleep.
func fastRetryClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c := newTestClient(rt)
	c.maxRetries = 4
	c.maxStreamRetries = 4
	c.retryBase = time.Millisecond
	c.retryMax = 2 * time.Millisecond
	return c
}

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)), Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

func newReqTo(t *testing.T, c *Client) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, "https://floodgate.test/x", nil)
	}
}

func TestDoWithRetrySucceedsAfterTransientStatuses(t *testing.T) {
	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return resp(503, "overloaded"), nil
		}
		return resp(200, "ok"), nil
	}))
	r, err := c.doWithRetry(context.Background(), newReqTo(t, c))
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	if r.StatusCode != 200 {
		t.Errorf("final status = %d, want 200", r.StatusCode)
	}
	if calls != 3 {
		t.Errorf("attempts = %d, want 3", calls)
	}
}

func TestDoWithRetryRetriesNetworkErrors(t *testing.T) {
	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 2 {
			return nil, errors.New("connection reset")
		}
		return resp(200, "ok"), nil
	}))
	if _, err := c.doWithRetry(context.Background(), newReqTo(t, c)); err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	if calls != 2 {
		t.Errorf("attempts = %d, want 2", calls)
	}
}

func TestDoWithRetryGivesUpAfterMax(t *testing.T) {
	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return resp(503, "still down"), nil
	}))
	c.maxRetries = 2 // 3 attempts total
	_, err := c.doWithRetry(context.Background(), newReqTo(t, c))
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if calls != 3 {
		t.Errorf("attempts = %d, want 3 (1 + 2 retries)", calls)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention the last status: %v", err)
	}
}

func TestDoWithRetryDoesNotRetryClientErrors(t *testing.T) {
	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return resp(400, "bad request"), nil
	}))
	r, err := c.doWithRetry(context.Background(), newReqTo(t, c))
	if err != nil {
		t.Fatalf("a 4xx should be returned, not error: %v", err)
	}
	if r.StatusCode != 400 || calls != 1 {
		t.Errorf("got status %d after %d calls, want 400 after 1", r.StatusCode, calls)
	}
}

func TestDoWithRetryHonorsContextCancel(t *testing.T) {
	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return resp(503, "down"), nil
	}))
	c.retryBase = time.Hour // a real backoff sleep would hang if cancel didn't work
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled up front; the first backoff must abort immediately

	_, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, "https://floodgate.test/x", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("attempts = %d, want 1 (cancel during the first backoff)", calls)
	}
}

// TestStreamMessagesRetriesThenStreams exercises the full Ollama path: a
// transient 503 is retried, then a 200 OpenAI-compatible SSE stream is parsed.
func TestStreamMessagesRetriesThenStreams(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return resp(503, "overloaded"), nil
		}
		return resp(200, sse), nil
	}))

	r, err := c.StreamMessages(context.Background(), "sys", []Message{UserText("hi")}, nil, Handler{})
	if err != nil {
		t.Fatalf("StreamMessages: %v", err)
	}
	if calls != 2 {
		t.Errorf("attempts = %d, want 2", calls)
	}
	if len(r.Blocks) != 1 || r.Blocks[0].Type != BlockText || r.Blocks[0].Text != "hi" {
		t.Errorf("unexpected blocks: %+v", r.Blocks)
	}
}

// resetReader yields its data, then fails the next read with a "connection reset
// by peer" error — simulating a peer resetting the connection mid-stream (after a
// 200 status), which is what doWithRetry can't catch.
type resetReader struct {
	data []byte
	done bool
}

func (r *resetReader) Read(p []byte) (int, error) {
	if !r.done && len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	r.done = true
	return 0, errors.New("read tcp 1.2.3.4:5->6.7.8.9:443: read: connection reset by peer")
}

// TestStreamOpenAIRetriesMidStream proves the OpenAI route now retries a
// transient MID-stream failure (the gap that made connection resets fail
// immediately on that route): the first 200 response's body resets mid-stream,
// OnRetry fires, and the re-sent turn streams cleanly.
func TestStreamOpenAIRetriesMidStream(t *testing.T) {
	clean := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			// 200, but the body resets partway through the stream.
			r := resp(200, "")
			r.Body = io.NopCloser(&resetReader{data: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}]}\n")})
			return r, nil
		}
		return resp(200, clean), nil
	}))

	var retried int
	r, err := c.StreamMessages(context.Background(), "sys", []Message{UserText("hi")}, nil, Handler{
		OnRetry: func(error) { retried++ },
	})
	if err != nil {
		t.Fatalf("StreamMessages (openai): %v", err)
	}
	if calls != 2 {
		t.Errorf("attempts = %d, want 2 (one reset, one clean)", calls)
	}
	if retried != 1 {
		t.Errorf("OnRetry fired %d times, want 1", retried)
	}
	if len(r.Blocks) != 1 || r.Blocks[0].Type != BlockText || r.Blocks[0].Text != "hello" {
		t.Errorf("unexpected blocks after retry: %+v", r.Blocks)
	}
}

// TestStreamRetryUsageCounted covers honest cost accounting (#2): a mid-stream
// drop after the provider has billed input must carry that wasted spend onto the
// returned Response.RetryUsage, while Usage stays the successful call's totals.
func TestStreamRetryUsageCounted(t *testing.T) {
	clean := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":8}}`,
		`data: [DONE]`,
		"",
	}, "\n")
	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			// The provider reported usage (it billed the input), then the stream
			// resets mid-flight.
			r := resp(200, "")
			r.Body = io.NopCloser(&resetReader{data: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"par\"}}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":5}}\n")})
			return r, nil
		}
		return resp(200, clean), nil
	}))

	r, err := c.StreamMessages(context.Background(), "sys", []Message{UserText("hi")}, nil, Handler{})
	if err != nil {
		t.Fatalf("StreamMessages: %v", err)
	}
	if r.Usage.InputTokens != 1200 || r.Usage.OutputTokens != 8 {
		t.Errorf("final Usage = %+v, want the successful call's 1200/8", r.Usage)
	}
	if r.RetryUsage.InputTokens != 1000 || r.RetryUsage.OutputTokens != 5 {
		t.Errorf("RetryUsage = %+v, want the failed attempt's 1000/5", r.RetryUsage)
	}
}

// TestStreamMidStreamRetryCap covers #3: mid-stream re-sends are bounded by
// maxStreamRetries (separately from the request-level maxRetries), so a link
// that resets every time can't re-bill a huge turn indefinitely.
func TestStreamMidStreamRetryCap(t *testing.T) {
	calls := 0
	c := fastRetryClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := resp(200, "")
		r.Body = io.NopCloser(&resetReader{data: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n")})
		return r, nil
	}))
	c.maxStreamRetries = 1 // initial attempt + exactly one mid-stream retry

	if _, err := c.StreamMessages(context.Background(), "sys", []Message{UserText("hi")}, nil, Handler{}); err == nil {
		t.Fatal("expected failure after exhausting mid-stream retries")
	}
	if calls != 2 {
		t.Errorf("attempts = %d, want 2 (initial + 1 mid-stream retry)", calls)
	}
}

func TestRetryableStreamError(t *testing.T) {
	// The exact error the relay hits, plus other transient stream conditions.
	retry := []error{
		fmt.Errorf("reading stream: read tcp 10.106.254.63:60083->100.74.145.88:443: read: operation timed out"),
		fmt.Errorf("reading stream: read tcp 1.2.3.4:5->6.7.8.9:443: read: connection reset by peer"),
		fmt.Errorf("reading stream: unexpected EOF"),
		fmt.Errorf("reading stream: write: broken pipe"),
	}
	for _, e := range retry {
		if !retryableStreamError(e) {
			t.Errorf("retryableStreamError(%v) = false, want true", e)
		}
	}
	// Not retryable: caller cancelled / deadline, or a non-transient error.
	noRetry := []error{
		nil,
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("reading stream: malformed event: bad json"),
	}
	for _, e := range noRetry {
		if retryableStreamError(e) {
			t.Errorf("retryableStreamError(%v) = true, want false", e)
		}
	}
}
