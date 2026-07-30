package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

func TestHealthzReportsVersion(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/healthz status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) || !strings.Contains(string(body), config.Version) {
		t.Errorf("/healthz body missing status/version: %s", body)
	}
}

func TestMetricsCountsRequestsAndSSE(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)
	dir := t.TempDir()
	s := seedSession(t, srv.store, dir, 3)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	scrape := func() string {
		resp, err := http.Get(ts.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("/metrics Content-Type = %q, want text/plain exposition", ct)
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	// Hit a normal endpoint so a request is recorded.
	if _, err := http.Get(ts.URL + "/healthz"); err != nil {
		t.Fatal(err)
	}
	m1 := scrape()
	if !strings.Contains(m1, "moony_web_uptime_seconds") {
		t.Errorf("/metrics missing uptime gauge:\n%s", m1)
	}
	if !strings.Contains(m1, `moony_web_requests_total{`) {
		t.Errorf("/metrics missing per-route request counter:\n%s", m1)
	}
	// The session-id path must collapse to a low-cardinality label, not leak the id.
	if strings.Contains(m1, s.ID) {
		t.Errorf("/metrics leaked a session id into a label (cardinality bug):\n%s", m1)
	}

	// Open an SSE stream: the active gauge should rise while it's open and the
	// total + replay counters should increment.
	c := dialSSE(t, ts.URL, "/api/sessions/"+s.ID+"/events")
	c.drainUntilState(t, 5*time.Second)
	// Give the server a beat to register the subscriber gauge.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(scrape(), "moony_web_sse_streams_active 1") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m2 := scrape()
	if !strings.Contains(m2, "moony_web_sse_streams_active 1") {
		t.Errorf("sse_streams_active should be 1 while a stream is open:\n%s", m2)
	}
	if !strings.Contains(m2, "moony_web_replay_served_total 1") {
		t.Errorf("replay_served_total should be 1 after a capped-replay connect:\n%s", m2)
	}

	c.close()
	// After close, the active gauge should fall back to 0.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(scrape(), "moony_web_sse_streams_active 0") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(scrape(), "moony_web_sse_streams_active 0") {
		t.Errorf("sse_streams_active should return to 0 after the stream closes")
	}
}

func TestMetricsHistoryCounter(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)
	dir := t.TempDir()
	total := defaultReplayTurns + 5
	s := seedSession(t, srv.store, dir, total)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	getHistoryPage(t, ts.URL, s.ID, total-defaultReplayTurns, 3)

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "moony_web_history_served_total 1") {
		t.Errorf("history_served_total should be 1 after a /history fetch:\n%s", body)
	}
}

func TestRouteLabelCollapsesIDs(t *testing.T) {
	got := routeLabel("GET", "/api/sessions/9f3a1c2b7d4e5f60/events")
	if got != "GET /api/sessions/:id/events" {
		t.Errorf("routeLabel = %q, want the session id collapsed to :id", got)
	}
	// A short, human-readable segment is NOT an id.
	if routeLabel("GET", "/api/models") != "GET /api/models" {
		t.Errorf("routeLabel wrongly collapsed a normal path segment")
	}
}
