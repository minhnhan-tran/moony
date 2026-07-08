package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// infoTexts returns the text of every "info" event in the session's history.
func infoTexts(s *session) []string {
	_, _, hist := s.subscribe()
	var out []string
	for _, e := range hist {
		if e.Type == "info" {
			out = append(out, e.Text)
		}
	}
	return out
}

func anyContains(texts []string, sub string) bool {
	for _, t := range texts {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}

// TestSessionAskSideGuards covers the web /btw guard paths that don't reach the
// model: a blank question prints usage, and a second /btw while one is running is
// refused — neither touches the agent (so a nil agent is safe here).
func TestSessionAskSideGuards(t *testing.T) {
	sess := newSession("id", "t", nil)

	// Blank question → usage line, no goroutine spawned (agent untouched).
	sess.askSide("")
	if !anyContains(infoTexts(sess), "ask a quick side question") {
		t.Errorf("blank /btw should print usage, got %v", infoTexts(sess))
	}
	if sess.btwBusy {
		t.Error("blank /btw must not mark the session btw-busy")
	}

	// One-at-a-time: with a side-question already in flight, another is refused
	// without dispatching (so the nil agent is never called).
	sess.btwBusy = true
	sess.askSide("a real question")
	if !anyContains(infoTexts(sess), "already running") {
		t.Errorf("second /btw should report already-running, got %v", infoTexts(sess))
	}
}

// TestBtwInterceptedBeforeQueueHTTP verifies /btw is handled concurrently in the
// web-native path: sent while a turn is busy it returns 202 and is NOT added to
// the message queue (it would be, like any slash command, without the intercept).
func TestBtwInterceptedBeforeQueueHTTP(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeStreamer{
		turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("done")}}},
		gate:  gate,
	}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}

	// Start a turn — it blocks on the gate, so the session stays busy.
	if r := postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"go"}`); r.Code != http.StatusAccepted {
		t.Fatalf("first message code = %d want 202", r.Code)
	}
	waitState(t, srv, created.ID, stateBusy)

	// A blank /btw while busy: accepted, prints usage, and does NOT enqueue
	// (proving it's intercepted before the busy-queue gate). Blank keeps it off
	// the model so the shared fake streamer isn't pulled into the side call.
	if r := postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"/btw "}`); r.Code != http.StatusAccepted {
		t.Fatalf("busy /btw code = %d want 202", r.Code)
	}
	sess := srv.sessions[created.ID]
	if got := sess.queuedList(); len(got) != 0 {
		t.Errorf("/btw must not be queued while busy, queue = %v", got)
	}
	if !anyContains(infoTexts(sess), "ask a quick side question") {
		t.Errorf("busy /btw should print usage, got %v", infoTexts(sess))
	}

	// Release the turn and let the session settle idle.
	gate <- struct{}{}
	waitState(t, srv, created.ID, stateIdle)
}
