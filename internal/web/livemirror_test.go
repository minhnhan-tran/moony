package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/bridge"
	"github.com/pysty1995/moony/internal/config"
	sessionpkg "github.com/pysty1995/moony/internal/session"
)

// TestServerRoutesActiveSessionToHub verifies the integrated live mirror: after
// AttachLiveMirror, the multi-project server routes the terminal's active
// session id to the bridge hub (the running TUI), while sessions in other
// projects stay store-backed.
func TestServerRoutesActiveSessionToHub(t *testing.T) {
	hub := bridge.New()
	var submitted string
	hub.SetController(
		func(text string) { submitted = text },
		func(agent.Decision) {},
		func() {},
		func(string) {},
	)
	hub.SetActiveSession("live-123")

	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := &config.Config{Model: "m", WorkingDir: dir}
	srv := NewServer(store, dir, cfg, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.AttachLiveMirror(hub, "live-123")
	h := srv.Handler()

	// A message to the active session id is forwarded to the hub (the TUI).
	rec := postJSON(t, h, "POST", "/api/sessions/live-123/message", `{"text":"hello live"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("live message status=%d body=%s", rec.Code, rec.Body.String())
	}
	if submitted != "hello live" {
		t.Fatalf("hub did not receive the live submit, got %q", submitted)
	}

	// An id that isn't the active session and isn't a stored session of this
	// project is NOT routed to the hub (no such stored session → 404).
	submitted = ""
	rec = postJSON(t, h, "POST", "/api/sessions/other/message", `{"text":"nope"}`)
	if rec.Code == http.StatusAccepted || submitted != "" {
		t.Fatalf("unknown id should not hit the hub (status=%d submitted=%q)", rec.Code, submitted)
	}

	// The active session is listed under its project (even though it's unsaved)
	// and flagged live.
	key := projectKey(dir)
	rec = postJSON(t, h, "GET", "/api/projects/"+key+"/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("project sessions status=%d", rec.Code)
	}
	var items []savedSessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, rec.Body.String())
	}
	found := false
	for _, it := range items {
		if it.ID == "live-123" {
			found = true
			if !it.Live {
				t.Errorf("active session should be flagged live: %+v", it)
			}
		}
	}
	if !found {
		t.Fatalf("active session not listed in its project: %s", rec.Body.String())
	}
}

// TestAgentWriteHandlersOnLiveMirror is the regression test for the "404 page not
// found when changing an agent's model from the live terminal" bug: the agents
// WRITE handlers (install/model/toggle/remove) used to require a store-backed
// session via s.lookup, so on the live terminal-mirror session id they 404'd —
// even though the catalog/models READ handlers worked (they resolve via catalogWD,
// which is hub-aware). The write handlers now use catalogWD too, so they succeed on
// the live mirror (the persona .md is a working-dir file; no live web agent needed).
func TestAgentWriteHandlersOnLiveMirror(t *testing.T) {
	t.Setenv("MOONY_IMPORT_EXTERNAL_AGENTS", "0")
	srv, _ := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)

	hub := bridge.New()
	hub.SetController(func(string) {}, func(agent.Decision) {}, func() {}, func(string) {})
	hub.SetActiveSession("live-xyz")
	srv.AttachLiveMirror(hub, "live-xyz")
	h := srv.Handler()

	// Install + pin a model via the LIVE session id — each was a 404 before the fix.
	if rec := postJSON(t, h, "POST", "/api/sessions/live-xyz/agents/install", `{"name":"explore"}`); rec.Code != http.StatusOK {
		t.Fatalf("install on live mirror = %d (want 200 — the 404 bug): %s", rec.Code, rec.Body)
	}
	if rec := postJSON(t, h, "POST", "/api/sessions/live-xyz/agents/model", `{"name":"explore","model":"anthropic.claude-haiku-4-5"}`); rec.Code != http.StatusOK {
		t.Fatalf("set model on live mirror = %d (want 200 — the 404 bug): %s", rec.Code, rec.Body)
	}
	if a, _ := findAgent(agentsCatalog(t, srv, "live-xyz"), "explore"); a.Model != "anthropic.claude-haiku-4-5" {
		t.Errorf("explore model = %q after pinning via the live mirror, want the pinned id", a.Model)
	}
	// Toggle also works (no 404).
	if rec := postJSON(t, h, "POST", "/api/sessions/live-xyz/agents/toggle", `{"name":"explore","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle on live mirror = %d (want 200): %s", rec.Code, rec.Body)
	}
}

// TestServerRunsProjectSessionsInParallel verifies the parallel model: opening
// the SSE event stream for a NON-active saved session no longer switches the
// terminal — every session except the terminal's active one runs as its own
// store-backed agent, so multiple agents can run at once on the same project.
func TestServerRunsProjectSessionsInParallel(t *testing.T) {
	hub := bridge.New()
	var switched string
	hub.SetController(
		func(string) {},
		func(agent.Decision) {},
		func() {},
		func(id string) { switched = id },
	)
	hub.SetActiveSession("live-active")

	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()      // the terminal's project (launchDir)
	otherDir := t.TempDir() // a different project

	// A saved session in the terminal's project — switchable.
	mine := sessionpkg.New("m", dir)
	if err := store.Save(mine); err != nil {
		t.Fatal(err)
	}
	// A saved session in another project — must NOT switch the terminal.
	theirs := sessionpkg.New("m", otherDir)
	if err := store.Save(theirs); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Model: "m", WorkingDir: dir}
	srv := NewServer(store, dir, cfg, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.AttachLiveMirror(hub, "live-active")
	h := srv.Handler()

	// Opening a NON-active session of the terminal's project must NOT switch the
	// terminal anymore — it runs as an independent store-backed agent (parallel).
	openEvents(t, h, "/api/sessions/"+mine.ID+"/events")
	if switched != "" {
		t.Fatalf("opening a project session must not switch the terminal (parallel model): switched=%q", switched)
	}

	// A session in another project also stays store-backed (no switch).
	switched = ""
	openEvents(t, h, "/api/sessions/"+theirs.ID+"/events")
	if switched != "" {
		t.Errorf("an other-project session should not switch the terminal: switched=%q", switched)
	}
}

// TestActiveSwitchEvictsStoreBacked verifies the conflict guard: when the
// terminal switches its live session to one already open store-backed, that
// store-backed instance is dropped so the same session never runs in two places.
func TestActiveSwitchEvictsStoreBacked(t *testing.T) {
	hub := bridge.New()
	hub.SetActiveSession("live-active")
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mine := sessionpkg.New("m", dir)
	if err := store.Save(mine); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "m", WorkingDir: dir}
	srv := NewServer(store, dir, cfg, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.AttachLiveMirror(hub, "live-active")

	// Open `mine` as a store-backed (parallel) session — it isn't the active one.
	if srv.openSession(mine.ID) == nil {
		t.Fatal("openSession(mine) returned nil")
	}
	srv.mu.Lock()
	_, open := srv.sessions[mine.ID]
	srv.mu.Unlock()
	if !open {
		t.Fatal("expected mine to be open store-backed")
	}

	// The terminal switches its live session to `mine` → its store-backed twin
	// must be evicted (via hub.OnActiveChange → evictStoreBacked).
	hub.SetActiveSession(mine.ID)
	srv.mu.Lock()
	_, still := srv.sessions[mine.ID]
	srv.mu.Unlock()
	if still {
		t.Error("store-backed session should be evicted once it becomes the terminal's live session")
	}
}

// openEvents issues a GET against an SSE endpoint and cancels shortly after, so
// the (blocking) handler runs long enough to perform its up-front side effects
// (like hub.Switch) without hanging the test.
func openEvents(t *testing.T, h http.Handler, path string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	go h.ServeHTTP(rec, req)
	// The switch is performed synchronously at the top of handleEvents before it
	// blocks on the subscription channel; a brief wait is enough.
	time.Sleep(50 * time.Millisecond)
}

func TestServerNoLiveMirrorByDefault(t *testing.T) {
	store, _ := sessionpkg.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	srv := NewServer(store, dir, &config.Config{WorkingDir: dir}, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	// Without AttachLiveMirror, an arbitrary id is store-backed (404), not hub.
	rec := postJSON(t, srv.Handler(), "POST", "/api/sessions/whatever/message", `{"text":"x"}`)
	if rec.Code == http.StatusAccepted {
		t.Fatal("no live mirror attached, so nothing should be accepted via the hub")
	}
	if strings.Contains(rec.Body.String(), "panic") {
		t.Fatal("unexpected panic path")
	}
}

// TestDeleteActiveSessionSwitchesToLatest verifies that deleting the terminal's
// currently-active session is allowed: the server switches the terminal to the
// project's next most-recent session, deletes the old one, and returns the id it
// switched to so the browser can follow.
func TestDeleteActiveSessionSwitchesToLatest(t *testing.T) {
	hub := bridge.New()
	var switched string
	hub.SetController(func(string) {}, func(agent.Decision) {}, func() {}, func(id string) { switched = id })

	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	other := sessionpkg.New("m", dir)
	active := sessionpkg.New("m", dir)
	for _, s := range []*sessionpkg.Session{other, active} {
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	hub.SetActiveSession(active.ID)

	srv := NewServer(store, dir, &config.Config{Model: "m", WorkingDir: dir}, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.AttachLiveMirror(hub, active.ID)
	h := srv.Handler()

	rec := postJSON(t, h, "DELETE", "/api/sessions/"+active.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deleting the active session should now succeed (200), got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		SwitchedTo string `json:"switchedTo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SwitchedTo != other.ID {
		t.Errorf("switchedTo = %q, want the other session %q", resp.SwitchedTo, other.ID)
	}
	if switched != other.ID {
		t.Errorf("terminal should have been switched to %q, switchTo got %q", other.ID, switched)
	}
	if _, err := store.Load(active.ID); err == nil {
		t.Error("the active session should have been deleted from the store")
	}
	if _, err := store.Load(other.ID); err != nil {
		t.Errorf("the switched-to session should still exist: %v", err)
	}
}

// TestDeleteActiveLastSessionMintsFresh verifies that deleting the active session
// when it's the only one in the project mints a fresh empty session for the
// terminal to land on, so it's never stranded on a deleted session.
func TestDeleteActiveLastSessionMintsFresh(t *testing.T) {
	hub := bridge.New()
	var switched string
	hub.SetController(func(string) {}, func(agent.Decision) {}, func() {}, func(id string) { switched = id })

	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	active := sessionpkg.New("m", dir)
	if err := store.Save(active); err != nil {
		t.Fatal(err)
	}
	hub.SetActiveSession(active.ID)

	srv := NewServer(store, dir, &config.Config{Model: "m", WorkingDir: dir}, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.AttachLiveMirror(hub, active.ID)
	h := srv.Handler()

	rec := postJSON(t, h, "DELETE", "/api/sessions/"+active.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete should succeed (200), got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		SwitchedTo string `json:"switchedTo"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.SwitchedTo == "" || resp.SwitchedTo == active.ID {
		t.Fatalf("switchedTo should be a fresh session id, got %q", resp.SwitchedTo)
	}
	if switched != resp.SwitchedTo {
		t.Errorf("terminal should switch to the fresh session %q, got %q", resp.SwitchedTo, switched)
	}
	if _, err := store.Load(resp.SwitchedTo); err != nil {
		t.Errorf("the fresh session should have been saved: %v", err)
	}
	if _, err := store.Load(active.ID); err == nil {
		t.Error("the deleted session should be gone")
	}
}

// TestDeleteActiveSessionBusyRejected verifies that the active session can't be
// deleted while the terminal is mid-turn (switching, and thus a safe delete, is
// only possible while idle), and the reason points at the running turn.
func TestDeleteActiveSessionBusyRejected(t *testing.T) {
	hub := bridge.New()
	hub.SetController(func(string) {}, func(agent.Decision) {}, func() {}, func(string) {})
	hub.SetActiveSession("live-1")
	hub.PublishState(bridge.StateBusy)

	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv := NewServer(store, dir, &config.Config{Model: "m", WorkingDir: dir}, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.AttachLiveMirror(hub, "live-1")
	h := srv.Handler()

	rec := postJSON(t, h, "DELETE", "/api/sessions/live-1", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("deleting the active session mid-turn should be 409, got %d", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "running turn") {
		t.Errorf("busy refusal should mention the running turn: %q", rec.Body.String())
	}
}

// The live terminal session is driven through the hub. Even though it IS
// persisted to the store (so s.lookup would happily resurrect a parallel
// store-backed agent for it), the persona handler must short-circuit to the
// shared live config (s.cfg) for the active id — loading the panel without
// spinning up a second agent on the same on-disk session. A blank-marker token
// in s.cfg proves the response came from the shared config, and the factory
// must NOT be invoked for the active id.
func TestPersonaUsesLiveConfigForActiveTerminalSession(t *testing.T) {
	hub := bridge.New()
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	active := sessionpkg.New("m", dir)
	if err := store.Save(active); err != nil { // persisted: store.Load(active.ID) WOULD succeed
		t.Fatal(err)
	}
	hub.SetActiveSession(active.ID)

	// The shared live config carries a recognizable token so we can confirm the
	// panel reflects s.cfg (the config the live terminal agent shares).
	live := &config.Config{Model: "m-LIVE", WorkingDir: dir, OllamaBaseURL: "http://live-host:11434/v1"}
	factoryCalls := 0
	srv := NewServer(store, dir, live, func(string, *sessionpkg.Session) *agent.Agent {
		factoryCalls++ // resurrecting a store-backed agent for the active id would trip this
		return nil
	})
	srv.AttachLiveMirror(hub, active.ID)
	h := srv.Handler()

	rec := postJSON(t, h, "GET", "/api/sessions/"+active.ID+"/persona", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("persona GET for the active terminal session should be 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Fields []personaField `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode persona response: %v (%s)", err, rec.Body)
	}
	var ollamaHost string
	for _, f := range resp.Fields {
		if f.Key == "ollamaHost" {
			if s, ok := f.Value.(string); ok {
				ollamaHost = s // reflects s.cfg.OllamaBaseURL
			}
		}
	}
	if ollamaHost != "http://live-host:11434/v1" {
		t.Errorf("persona should reflect the shared live config (ollama host), fields = %+v", resp.Fields)
	}
	if factoryCalls != 0 {
		t.Errorf("the active terminal session must NOT spawn a store-backed agent, factory called %d time(s)", factoryCalls)
	}
}

// A not-yet-persisted session (no store entry, not the active id) still loads the
// persona panel by falling back to the shared s.cfg rather than 404ing.
func TestPersonaFallsBackToLiveConfigForUnknownSession(t *testing.T) {
	hub := bridge.New()
	hub.SetActiveSession("live-1")
	store, err := sessionpkg.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv := NewServer(store, dir, &config.Config{Model: "m", WorkingDir: dir}, func(string, *sessionpkg.Session) *agent.Agent { return nil })
	srv.AttachLiveMirror(hub, "live-1")
	h := srv.Handler()

	// An id that isn't the active session and isn't in the store: lookup yields
	// nil, so the handler falls back to s.cfg instead of 404ing.
	rec := postJSON(t, h, "GET", "/api/sessions/not-stored/persona", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("persona GET for an unknown session should fall back to 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "\"fields\"") {
		t.Fatalf("persona response should carry fields from the live config, got %q", rec.Body.String())
	}
}
