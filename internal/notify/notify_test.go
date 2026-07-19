package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// synchronize makes runAsync execute inline so tests don't need a sleep/poll.
func synchronize(t *testing.T) {
	t.Helper()
	old := runAsync
	runAsync = func(f func()) { f() }
	t.Cleanup(func() { runAsync = old })
}

func TestSendDisabledWithoutURL(t *testing.T) {
	t.Setenv("MN_NOTIFY_WEBHOOK_URL", "")
	called := false
	old := runAsync
	runAsync = func(f func()) { called = true; f() }
	defer func() { runAsync = old }()

	Send(TurnDone, "hi", time.Minute)
	if called {
		t.Error("Send should not even schedule work when no webhook URL is set")
	}
}

func TestSendPostsBothTextAndContentKeys(t *testing.T) {
	synchronize(t)
	var mu sync.Mutex
	var gotBody map[string]string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
	}))
	defer srv.Close()

	t.Setenv("MN_NOTIFY_WEBHOOK_URL", srv.URL)
	Send(Approval, "moony: approval needed for bash", 0)

	mu.Lock()
	defer mu.Unlock()
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotBody["text"] != "moony: approval needed for bash" || gotBody["content"] != "moony: approval needed for bash" {
		t.Errorf("body = %+v, want both text and content set", gotBody)
	}
}

func TestSendFiltersByEnabledEvents(t *testing.T) {
	synchronize(t)
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer srv.Close()

	t.Setenv("MN_NOTIFY_WEBHOOK_URL", srv.URL)
	t.Setenv("MN_NOTIFY_EVENTS", "approval")

	Send(TurnDone, "should be filtered out", time.Hour)
	Send(Approval, "should go through", 0)

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("hits = %d, want 1 (only approval enabled)", hits)
	}
}

func TestSendGatesTurnDoneByMinSeconds(t *testing.T) {
	synchronize(t)
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer srv.Close()

	t.Setenv("MN_NOTIFY_WEBHOOK_URL", srv.URL)
	t.Setenv("MN_NOTIFY_MIN_SECONDS", "30")

	Send(TurnDone, "too quick", 5*time.Second)
	Send(TurnDone, "long enough", 45*time.Second)
	// Approval must ignore the duration gate entirely.
	Send(Approval, "always fires", 0)

	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Errorf("hits = %d, want 2 (one turn_done past the gate + one approval)", hits)
	}
}

func TestSendNeverBlocksOrPanicsOnUnreachableURL(t *testing.T) {
	// Deliberately NOT synchronized: this exercises the real async path with
	// an address nothing is listening on, and must return immediately.
	t.Setenv("MN_NOTIFY_WEBHOOK_URL", "http://127.0.0.1:1/webhook")
	done := make(chan struct{})
	go func() {
		Send(TurnDone, "x", time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked on an unreachable webhook")
	}
}

func TestEnabledEventsDefaultsToBoth(t *testing.T) {
	t.Setenv("MN_NOTIFY_EVENTS", "")
	got := enabledEvents()
	if !got[TurnDone] || !got[Approval] {
		t.Errorf("default enabledEvents = %v, want both on", got)
	}
}

func TestMinSecondsDefaultAndParsing(t *testing.T) {
	t.Setenv("MN_NOTIFY_MIN_SECONDS", "")
	if got := MinSeconds(); got != 20 {
		t.Errorf("default MinSeconds = %v, want 20", got)
	}
	t.Setenv("MN_NOTIFY_MIN_SECONDS", "garbage")
	if got := MinSeconds(); got != 20 {
		t.Errorf("garbage MinSeconds = %v, want fallback 20", got)
	}
	t.Setenv("MN_NOTIFY_MIN_SECONDS", "5")
	if got := MinSeconds(); got != 5 {
		t.Errorf("MinSeconds = %v, want 5", got)
	}
}

func TestWebhookURLTrimsWhitespace(t *testing.T) {
	t.Setenv("MN_NOTIFY_WEBHOOK_URL", "  https://example.com/hook  ")
	if got := WebhookURL(); got != "https://example.com/hook" {
		t.Errorf("WebhookURL = %q", got)
	}
}
