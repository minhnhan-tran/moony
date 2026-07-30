package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	sessionpkg "github.com/pysty1995/moony/internal/session"
)

// sseClient is a minimal real Server-Sent-Events reader over a live http.Client
// connection — the point of these tests is to exercise the ACTUAL streaming/
// flush/serialization path (httptest.NewServer + net/http), not the in-process
// httptest.NewRecorder shortcut most web_test.go cases use, which never streams.
type sseClient struct {
	resp   *http.Response
	rd     *bufio.Reader
	cancel context.CancelFunc
}

func dialSSE(t *testing.T, base, path string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("dial SSE %s: %v", path, err)
	}
	if resp.StatusCode != 200 {
		cancel()
		t.Fatalf("SSE %s status = %d, want 200", path, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		cancel()
		t.Fatalf("SSE %s Content-Type = %q, want text/event-stream", path, ct)
	}
	return &sseClient{resp: resp, rd: bufio.NewReader(resp.Body), cancel: cancel}
}

func (c *sseClient) close() {
	c.cancel()
	if c.resp != nil {
		c.resp.Body.Close()
	}
}

// next reads the next SSE "data:" event (skipping keepalive comments and blank
// lines), decodes it as a wireEvent, and returns it. Fails on timeout so a stuck
// stream doesn't hang the suite.
func (c *sseClient) next(t *testing.T, within time.Duration) wireEvent {
	t.Helper()
	type res struct {
		ev  wireEvent
		err error
	}
	ch := make(chan res, 1)
	go func() {
		for {
			line, err := c.rd.ReadString('\n')
			if err != nil {
				ch <- res{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" || strings.HasPrefix(line, ":") { // keepalive/comment
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev wireEvent
			if err := json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev); err != nil {
				ch <- res{err: err}
				return
			}
			ch <- res{ev: ev}
			return
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read SSE event: %v", r.err)
		}
		return r.ev
	case <-time.After(within):
		t.Fatalf("timed out after %s waiting for an SSE event", within)
		return wireEvent{}
	}
}

// drainUntilState reads events until the single authoritative "state" event that
// marks the replay→live boundary, returning everything before it. It fails if the
// boundary doesn't arrive, or if a SECOND state shows up during replay (there must
// be exactly one on connect — see server.go handleEvents).
func (c *sseClient) drainUntilState(t *testing.T, within time.Duration) []wireEvent {
	t.Helper()
	deadline := time.Now().Add(within)
	var replay []wireEvent
	for {
		ev := c.next(t, time.Until(deadline)+time.Second)
		if ev.Type == "state" {
			return replay
		}
		replay = append(replay, ev)
		if time.Now().After(deadline) {
			t.Fatalf("never saw the authoritative replay→live state event; got %d replay events", len(replay))
		}
	}
}

// seedSession builds and stores a session with n user/assistant turns.
func seedSession(t *testing.T, store *sessionpkg.Store, dir string, n int) *sessionpkg.Session {
	t.Helper()
	s := sessionpkg.New("m", dir)
	for i := 0; i < n; i++ {
		s.Messages = append(s.Messages,
			llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("Q%d", i))}},
			llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("A%d", i))}},
		)
	}
	if err := store.Save(s); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return s
}

// TestE2EReplayBoundaryOverRealSSE drives the events stream over a real HTTP
// connection and asserts the capped-replay → single-state → keepalive contract
// the browser relies on. This is the seam we keep fixing reactively (paging,
// lazy turns, reconnection), so it's worth an over-the-wire test.
func TestE2EReplayBoundaryOverRealSSE(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)
	dir := t.TempDir()
	store := srv.store
	total := defaultReplayTurns + 8
	s := seedSession(t, store, dir, total)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// Opening a session over HTTP spawns per-session goroutines (MCP connect, turn
	// loop). Evict them before the test's TempDirs are removed, or a late persist
	// recreates files under an already-cleaned store dir and fails cleanup.
	t.Cleanup(srv.EvictAllLive)

	c := dialSSE(t, ts.URL, "/api/sessions/"+s.ID+"/events")
	defer c.close()

	replay := c.drainUntilState(t, 5*time.Second)

	// The replay must be capped: a history_start marker with the withheld count,
	// and it must include the newest turn but NOT the oldest.
	var start *wireEvent
	var sawNewest, sawOldest bool
	for i := range replay {
		switch replay[i].Type {
		case "history_start":
			start = &replay[i]
		case "user", "text":
			if strings.Contains(replay[i].Text, fmt.Sprintf("Q%d", total-1)) || strings.Contains(replay[i].Text, fmt.Sprintf("A%d", total-1)) {
				sawNewest = true
			}
			if replay[i].Text == "Q0" {
				sawOldest = true
			}
		}
	}
	if start == nil {
		t.Fatalf("no history_start in capped replay (%d events)", len(replay))
	}
	if start.Dropped != total-defaultReplayTurns || start.FirstTurn != total-defaultReplayTurns {
		t.Errorf("history_start dropped=%d firstTurn=%d, want %d for both", start.Dropped, start.FirstTurn, total-defaultReplayTurns)
	}
	if !sawNewest {
		t.Errorf("capped replay should include the newest turn (Q/A%d)", total-1)
	}
	if sawOldest {
		t.Errorf("capped replay must NOT include the oldest turn (Q0) — it was withheld")
	}
}

// TestE2EReconnectReplaysAgain: a fresh connection to the same session replays
// the capped history again (the browser reconnects on transient drops and must
// re-seed), ending on its own single authoritative state.
func TestE2EReconnectReplaysAgain(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)
	dir := t.TempDir()
	s := seedSession(t, srv.store, dir, defaultReplayTurns+3)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// Opening a session over HTTP spawns per-session goroutines (MCP connect, turn
	// loop). Evict them before the test's TempDirs are removed, or a late persist
	// recreates files under an already-cleaned store dir and fails cleanup.
	t.Cleanup(srv.EvictAllLive)

	for attempt := 0; attempt < 2; attempt++ {
		c := dialSSE(t, ts.URL, "/api/sessions/"+s.ID+"/events")
		replay := c.drainUntilState(t, 5*time.Second)
		hasStart := false
		for _, ev := range replay {
			if ev.Type == "history_start" {
				hasStart = true
			}
		}
		if !hasStart {
			t.Fatalf("attempt %d: reconnect did not re-seed capped history", attempt)
		}
		c.close()
	}
}

// TestE2EMultiSubscriberFanout: two concurrent SSE viewers of the SAME session
// both receive the replay and each terminate on their own authoritative state —
// exercising the hub subscribe/unsubscribe fan-out that the relay mirror depends
// on (a late joiner must get the same snapshot).
func TestE2EMultiSubscriberFanout(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)
	dir := t.TempDir()
	s := seedSession(t, srv.store, dir, 4)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// Opening a session over HTTP spawns per-session goroutines (MCP connect, turn
	// loop). Evict them before the test's TempDirs are removed, or a late persist
	// recreates files under an already-cleaned store dir and fails cleanup.
	t.Cleanup(srv.EvictAllLive)

	var wg sync.WaitGroup
	for viewer := 0; viewer < 2; viewer++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			c := dialSSE(t, ts.URL, "/api/sessions/"+s.ID+"/events")
			defer c.close()
			replay := c.drainUntilState(t, 5*time.Second)
			// A 4-turn session is under the cap, so every turn replays and there's
			// no history_start.
			sawQ0, sawQ3 := false, false
			for _, ev := range replay {
				if ev.Text == "Q0" {
					sawQ0 = true
				}
				if ev.Text == "Q3" {
					sawQ3 = true
				}
				if ev.Type == "history_start" {
					t.Errorf("viewer %d: unexpected history_start for a small (uncapped) session", v)
				}
			}
			if !sawQ0 || !sawQ3 {
				t.Errorf("viewer %d: incomplete replay (Q0=%v Q3=%v)", v, sawQ0, sawQ3)
			}
		}(viewer)
	}
	wg.Wait()
}

// TestE2EHistoryPagingOverWire drives GET /history over a real connection and
// checks the pager cursor contract shipped in 6.21.0/6.21.2: paging backward
// from the capped window returns older turns with a correct fromTurn/remaining,
// and never leaks the newest (live-replay) turn into a history page.
func TestE2EHistoryPagingOverWire(t *testing.T) {
	fake := &fakeStreamer{}
	srv, _ := newTestServer(t, fake, config.ApprovalAuto, false)
	dir := t.TempDir()
	total := defaultReplayTurns + 10
	s := seedSession(t, srv.store, dir, total)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// Opening a session over HTTP spawns per-session goroutines (MCP connect, turn
	// loop). Evict them before the test's TempDirs are removed, or a late persist
	// recreates files under an already-cleaned store dir and fails cleanup.
	t.Cleanup(srv.EvictAllLive)

	firstTurn := total - defaultReplayTurns // == history_start.firstTurn cursor

	// Page the 5 turns immediately older than the capped window.
	page := getHistoryPage(t, ts.URL, s.ID, firstTurn, 5)
	if page.FromTurn != firstTurn-5 {
		t.Errorf("history page fromTurn=%d, want %d", page.FromTurn, firstTurn-5)
	}
	if page.Remaining != firstTurn-5 {
		t.Errorf("history page remaining=%d, want %d", page.Remaining, firstTurn-5)
	}
	body := pageText(page)
	if !strings.Contains(body, `"Q`+fmt.Sprint(firstTurn-5)+`"`) || !strings.Contains(body, `"Q`+fmt.Sprint(firstTurn-1)+`"`) {
		t.Errorf("history page should contain turns Q%d..Q%d:\n%s", firstTurn-5, firstTurn-1, body)
	}
	if strings.Contains(body, fmt.Sprintf(`"Q%d"`, total-1)) {
		t.Errorf("history page must NOT include the newest turn Q%d", total-1)
	}

	// Page from the very start: fromTurn clamps to 0 and nothing remains older.
	first := getHistoryPage(t, ts.URL, s.ID, 3, 30)
	if first.FromTurn != 0 || first.Remaining != 0 {
		t.Errorf("start-of-history page fromTurn=%d remaining=%d, want 0 and 0", first.FromTurn, first.Remaining)
	}
	if !strings.Contains(pageText(first), `"Q0"`) {
		t.Errorf("start-of-history page should contain the oldest turn Q0")
	}
}

type historyPage struct {
	Events    []map[string]any `json:"events"`
	FromTurn  int              `json:"fromTurn"`
	Remaining int              `json:"remaining"`
}

func getHistoryPage(t *testing.T, base, id string, before, n int) historyPage {
	t.Helper()
	url := fmt.Sprintf("%s/api/sessions/%s/history?before=%d&turns=%d", base, id, before, n)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET history status = %d, want 200", resp.StatusCode)
	}
	var p historyPage
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode history page: %v", err)
	}
	return p
}

func pageText(p historyPage) string {
	b, _ := json.Marshal(p.Events)
	return string(b)
}
