package web

// Project memory is stored in the PROJECT's AGENTS.md, so it is shared by every
// session in that project. But each session's agent caches its assembled system
// prompt, and the memory handlers only rebuilt the prompt of the session that
// made the edit — so a sibling session already open kept running with the old
// rules until it was restarted. These tests pin the fan-out.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// openTwoSessions creates two sessions in the same project and makes both LIVE
// (openSession), which is the situation the fan-out is about: a second tab open on
// the same project while you edit memory in the first.
func openTwoSessions(t *testing.T, srv *Server, key string) (string, string) {
	t.Helper()
	var ids []string
	for i := 0; i < 2; i++ {
		rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/sessions", "")
		var created sessionInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatalf("create session: %v (%s)", err, rec.Body)
		}
		if created.ID == "" {
			t.Fatalf("no session id returned: %s", rec.Body)
		}
		if srv.openSession(created.ID) == nil {
			t.Fatalf("session %s did not go live", created.ID)
		}
		ids = append(ids, created.ID)
	}
	if ids[0] == ids[1] {
		t.Fatal("expected two distinct sessions")
	}
	return ids[0], ids[1]
}

func TestProjectMemoryIsSharedAcrossSessions(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, "never", false)
	a, b := openTwoSessions(t, srv, key)

	// Add a project rule through session A.
	rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+a+"/memory/save",
		`{"section":"Rules","text":"prefer table-driven tests","global":false}`)
	if rec.Code != 200 {
		t.Fatalf("add memory via A: %d %s", rec.Code, rec.Body.String())
	}

	// Session B must see it: same project AGENTS.md, so the same bullets.
	rec = postJSON(t, srv.Handler(), "GET", "/api/sessions/"+b+"/memory", "")
	if rec.Code != 200 {
		t.Fatalf("list memory via B: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "prefer table-driven tests") {
		t.Errorf("a rule added in one session should be visible in its sibling:\n%s", rec.Body.String())
	}
}

// The prompt each sibling agent will send must actually contain the new rule —
// that's the part that was broken, and it isn't visible from the JSON listing.
func TestProjectMemoryEditRefreshesSiblingPrompt(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, "never", false)
	a, b := openTwoSessions(t, srv, key)

	const rule = "always run gofmt before committing"
	rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+a+"/memory/save",
		`{"section":"Rules","text":"`+rule+`","global":false}`)
	if rec.Code != 200 {
		t.Fatalf("add memory: %d %s", rec.Code, rec.Body.String())
	}

	srv.mu.Lock()
	sib := srv.sessions[b]
	srv.mu.Unlock()
	if sib == nil || sib.agent == nil {
		t.Fatal("sibling session should still be live")
	}

	// propagateMemory refreshes prompts on background goroutines; wait for it.
	if !eventually(func() bool {
		return strings.Contains(sib.agent.SystemPrompt(), rule)
	}) {
		t.Errorf("sibling session's system prompt should pick up the new project rule; got:\n%s",
			tailOf(sib.agent.SystemPrompt()))
	}
}

// A GLOBAL edit is shared by every project, so it must fan out to sessions in
// other working dirs too — the project-dir filter must not swallow it.
func TestGlobalMemoryEditRefreshesAllSessions(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, "never", false)
	a, b := openTwoSessions(t, srv, key)

	const rule = "never push to main directly"
	rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/"+a+"/memory/save",
		`{"section":"Rules","text":"`+rule+`","global":true}`)
	if rec.Code != 200 {
		t.Fatalf("add global memory: %d %s", rec.Code, rec.Body.String())
	}

	srv.mu.Lock()
	sib := srv.sessions[b]
	srv.mu.Unlock()
	if sib == nil || sib.agent == nil {
		t.Fatal("sibling should be live")
	}
	if !eventually(func() bool { return strings.Contains(sib.agent.SystemPrompt(), rule) }) {
		t.Errorf("a global rule should reach every live session; got:\n%s", tailOf(sib.agent.SystemPrompt()))
	}
}

// eventually polls pred, since the prompt fan-out is asynchronous.
func eventually(pred func() bool) bool {
	for i := 0; i < 200; i++ {
		if pred() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func tailOf(s string) string {
	if len(s) > 600 {
		return "…" + s[len(s)-600:]
	}
	return s
}
