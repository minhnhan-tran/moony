package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func quota429Resp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusTooManyRequests, Status: "429 Too Many Requests",
		Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{},
	}
}

// A transient (per-minute) rate-limit 429 is NOT the daily cap, so it still
// retries up to maxRetries and yields the "after N attempt(s)" error.
func TestTransient429StillRetries(t *testing.T) {
	var calls int
	c := newTestClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return quota429Resp("rate limited, slow down"), nil
	}))
	c.maxRetries, c.retryBase, c.retryMax = 4, time.Millisecond, time.Millisecond

	newReq := func() (*http.Request, error) { return http.NewRequest(http.MethodPost, "https://relay.test/x", nil) }
	_, err := c.doWithRetry(context.Background(), newReq)

	if calls != c.maxRetries+1 {
		t.Errorf("transient 429 made %d request(s), want %d (retried)", calls, c.maxRetries+1)
	}
	if err == nil || !strings.Contains(err.Error(), "attempt(s)") {
		t.Errorf("transient 429 error = %v, want the 'after N attempt(s)' form", err)
	}
}
