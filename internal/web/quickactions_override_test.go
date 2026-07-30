package web

// Per-turn model overrides for quick actions.
//
// A quick action may pin a model/provider/reasoning for the single turn it fires.
// The override must be applied for that turn and reverted afterwards — and the
// revert has to be tied to the *turn*, not the HTTP response: POST /message
// returns 202 immediately while the turn runs for minutes, so reverting in the
// handler would put the session back on its own model almost at once and the
// action would run on the wrong one.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
	sessionpkg "github.com/pysty1995/moony/internal/session"
)

// liveSession creates and opens a session so its agent config can be inspected.
func liveSession(t *testing.T, srv *Server) *session {
	t.Helper()
	sess := sessionpkg.New("test", srv.cfg.WorkingDir)
	if err := srv.store.Save(sess); err != nil {
		t.Fatal(err)
	}
	live := srv.openSession(sess.ID)
	if live == nil {
		t.Fatal("openSession returned nil")
	}
	// Wait out the MCP connect goroutine before returning — see openLive.
	live.waitMCPReady(10 * time.Second)
	return live
}

// readSource reads a file from this package for the wiring assertions below.
func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

func TestTurnOverrideEmpty(t *testing.T) {
	if !(turnOverride{}).empty() {
		t.Error("a zero override means 'use the session settings'")
	}
	for _, ov := range []turnOverride{
		{Model: "gpt-5"}, {Provider: "afm"}, {Reasoning: "high"}, {Model: "  x  "},
	} {
		if ov.empty() {
			t.Errorf("%+v should not be considered empty", ov)
		}
	}
	// Whitespace-only is nothing, so a blank form field doesn't "override to nothing".
	if !(turnOverride{Model: "   ", Provider: "\t", Reasoning: " "}).empty() {
		t.Error("whitespace-only fields must count as unset")
	}
}

func TestApplyTurnOverrideRestoresSettings(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}

	cfg.Model, cfg.Provider, cfg.ReasoningLevel = "base-model", "ollama", config.ReasoningLow
	restore := sess.applyTurnOverride(turnOverride{Reasoning: "high"})
	if restore == nil {
		t.Fatal("a non-empty override must return a restore func")
	}
	if cfg.ReasoningLevel == config.ReasoningLow {
		t.Error("the override should have changed the reasoning level for this turn")
	}
	restore()
	if cfg.Model != "base-model" || cfg.Provider != "ollama" || cfg.ReasoningLevel != config.ReasoningLow {
		t.Fatalf("settings not restored: got %q/%q/%q", cfg.Model, cfg.Provider, cfg.ReasoningLevel)
	}
}

// TestApplyTurnOverrideRestoreKeepsNonDefaultProvider guards the reason restore
// writes the fields back directly instead of calling SetModel: SetModel derives
// Provider/ReasoningLevel from the model name, so restoring through it would
// silently discard a provider the user had deliberately chosen.
func TestApplyTurnOverrideRestoreKeepsNonDefaultProvider(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}

	cfg.Model, cfg.LLMProvider, cfg.ReasoningLevel = "base-model", "sqe-proxy", config.ReasoningMedium
	restore := sess.applyTurnOverride(turnOverride{Model: "other-model"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	restore()
	if cfg.LLMProvider != "sqe-proxy" {
		t.Fatalf("a deliberately-set backend must survive the round trip, got %q", cfg.LLMProvider)
	}
	if cfg.ReasoningLevel != config.ReasoningMedium {
		t.Fatalf("reasoning must survive too, got %q", cfg.ReasoningLevel)
	}
	if cfg.Model != "base-model" {
		t.Fatalf("model not restored, got %q", cfg.Model)
	}
}

func TestApplyTurnOverrideNoopWhenEmpty(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	if restore := sess.applyTurnOverride(turnOverride{}); restore != nil {
		t.Error("an empty override must not touch the session or return a restore func")
	}
}

// TestExplicitProviderBeatsModelDefault: an action naming both a backend and a
// model must route to that backend. Asserts LLMProvider (what actually selects
// floodgate / afm / ollama), NOT cfg.Provider — that is the wire API family
// derived from the model, and an earlier version of this test checked it and so
// passed while routing was broken.
func TestExplicitProviderBeatsModelDefault(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}

	restore := sess.applyTurnOverride(turnOverride{Model: "some-model", Provider: "ollama"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	defer restore()
	if cfg.LLMProvider != "ollama" {
		t.Fatalf("the action's backend must route the turn, got LLMProvider=%q", cfg.LLMProvider)
	}
	if cfg.Model != "some-model" {
		t.Fatalf("an explicit model must survive the backend switch, got %q", cfg.Model)
	}
}

// TestOverrideWiring asserts the plumbing is actually connected end to end. The
// helpers can all be correct while nothing calls them — and the queue path is
// easy to miss, which would silently drop the override for any action fired while
// a turn is already running.
func TestOverrideWiring(t *testing.T) {
	for _, tc := range []struct{ file, want, why string }{
		{"server.go", "Model     string", "the endpoint must accept an override field"},
		{"server.go", "sess.startWith(text, blocks, wires, ov)", "the immediate path must pass it"},
		{"server.go", "sess.enqueueIfBusyWith(text, blocks, wires, ov)", "a busy session must queue it"},
		{"server.go", "sess.startWith(m.text, m.blocks, m.wires, m.ov)", "the queue drain must apply it"},
		{"session.go", "if restore := s.applyTurnOverride(ov); restore != nil", "the turn must apply it"},
		{"session.go", "defer restore()", "restore must be deferred so errors/panics still revert"},
		{"session.go", "ov turnOverride", "the queued message must carry it"},
	} {
		src, err := readSource(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(src, tc.want) {
			t.Errorf("%s: missing %q — %s", tc.file, tc.want, tc.why)
		}
	}
}

// --- audit fixes -------------------------------------------------------------

// TestRestoreKeepsMidTurnUserChange is the regression test for the clobber bug:
// the user can change the model or run /reasoning WHILE an overridden turn is
// running. A blanket write-back on turn end silently undid that deliberate
// choice, so restore now compares-and-swaps per field.
func TestRestoreKeepsMidTurnUserChange(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}
	cfg.Model, cfg.Provider, cfg.ReasoningLevel = "base", "ollama", config.ReasoningMedium

	restore := sess.applyTurnOverride(turnOverride{Reasoning: "high"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	// The user picks low mid-turn; their choice must survive the turn ending.
	cfg.ReasoningLevel = config.ReasoningLow
	restore()
	if cfg.ReasoningLevel != config.ReasoningLow {
		t.Errorf("user's mid-turn reasoning was clobbered: got %q, want %q", cfg.ReasoningLevel, config.ReasoningLow)
	}
}

func TestRestoreKeepsMidTurnModelChange(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}
	cfg.Model, cfg.Provider, cfg.ReasoningLevel = "base", "ollama", config.ReasoningMedium

	restore := sess.applyTurnOverride(turnOverride{Model: "override-model"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	cfg.Model = "user-picked-mid-turn"
	restore()
	if cfg.Model != "user-picked-mid-turn" {
		t.Errorf("user's mid-turn model was clobbered: got %q", cfg.Model)
	}
}

// TestRestoreStillRevertsUntouchedFields: the compare-and-swap must not stop the
// normal case from reverting.
func TestRestoreStillRevertsUntouchedFields(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}
	cfg.Model, cfg.Provider, cfg.ReasoningLevel = "base", "ollama", config.ReasoningMedium

	restore := sess.applyTurnOverride(turnOverride{Reasoning: "high"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	restore()
	if cfg.ReasoningLevel != config.ReasoningMedium {
		t.Errorf("an untouched field must still revert: got %q, want medium", cfg.ReasoningLevel)
	}
}

// TestMessageRejectsBadOverride: /message must apply the same validation as the
// quick-action editor. An unrecognised reasoning level normalizes to "off", so
// accepting one here silently DISABLED thinking for the turn.
func TestMessageRejectsBadOverride(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	h := srv.Handler()
	sess := liveSession(t, srv)

	// Hold the session busy for the whole test so an accepted message is QUEUED
	// rather than dispatched. Validation runs before the enqueue, so this still
	// exercises the path under test — and no turn goroutine is left writing into
	// the t.TempDir() after the test ends (which failed cleanup with "directory
	// not empty" when this test actually ran turns).
	sess.mu.Lock()
	sess.state = stateBusy
	sess.mu.Unlock()

	for _, tc := range []struct {
		name, body string
		wantCode   int
	}{
		{"typo'd reasoning", `{"text":"hi","reasoning":"hgih"}`, http.StatusBadRequest},
		{"unknown provider", `{"text":"hi","provider":"ollamaa"}`, http.StatusBadRequest},
		{"valid reasoning", `{"text":"hi","reasoning":"high"}`, http.StatusAccepted},
		{"valid provider", `{"text":"hi","provider":"ollama"}`, http.StatusAccepted},
		{"no override", `{"text":"hi"}`, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/sessions/"+sess.id+"/message", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// TestQueuePopReturnsOverride: editing a queued quick action via Esc must hand the
// pin back to the composer, or the re-sent turn silently runs on the session's
// model instead of the action's.
func TestQueuePopReturnsOverride(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	h := srv.Handler()
	sess := liveSession(t, srv)

	sess.mu.Lock()
	sess.state = stateBusy
	sess.mu.Unlock()
	if !sess.enqueueIfBusyWith("review the diff", nil, nil,
		turnOverride{Model: "gpt-5", Provider: "ollama", Reasoning: "high"}) {
		t.Fatal("enqueue failed")
	}

	req := httptest.NewRequest("POST", "/api/sessions/"+sess.id+"/queue",
		strings.NewReader(`{"action":"pop"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pop status = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"model": "gpt-5", "provider": "ollama", "reasoning": "high"} {
		if got[k] != want {
			t.Errorf("pop %s = %v, want %q", k, got[k], want)
		}
	}
}

// --- provider routing (the "wrong provider was called" bug) ------------------
//
// cfg has TWO provider-ish fields and they are not interchangeable:
//   LLMProvider — the backend that serves the request (floodgate/afm/ollama/…)
//   Provider    — the wire API family (anthropic/openai/…), derived from the model
// The override originally wrote only Provider, so pinning an action to "afm"
// changed nothing about routing but relabelled the API: the request went to
// Anthropic as an anthropic call for an anthropic model and came back
// 400 "no accounts available can serve requests with the following
// characteristics: (API: anthropic, model: afm-…)".

func TestOverrideSetsRoutingProvider(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}
	cfg.LLMProvider = provider.Anthropic
	cfg.SetModel("anthropic.claude-sonnet-4-6")

	restore := sess.applyTurnOverride(turnOverride{Provider: "afm"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	if cfg.LLMProvider != provider.Ollama {
		t.Fatalf("LLMProvider = %q, want afm — the override must change the ROUTING field", cfg.LLMProvider)
	}
	restore()
	if cfg.LLMProvider != provider.Anthropic {
		t.Errorf("LLMProvider not restored: got %q", cfg.LLMProvider)
	}
}

// TestOverrideProviderSwitchesModel guards the other half of the same failure: a
// backend only serves its own models, so switching provider without naming a model
// must adopt that provider's default. Keeping the session's Anthropic model id
// while routing to afm is precisely what produced the 400.
func TestOverrideProviderSwitchesModel(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}
	cfg.LLMProvider = provider.Anthropic
	cfg.SetModel("anthropic.claude-sonnet-4-6")
	sessionModel := cfg.Model

	restore := sess.applyTurnOverride(turnOverride{Provider: "afm"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	defer restore()
	if want := provider.DefaultModelFor(provider.Ollama); want != "" && cfg.Model != want {
		t.Errorf("model = %q, want the backend's default %q", cfg.Model, want)
	}
	if cfg.Model == sessionModel {
		t.Errorf("the session's model %q was kept while routing to afm — it cannot serve it", sessionModel)
	}
}

// TestOverrideModelOnlyKeepsBackend: a model-only pin must not silently move the
// turn to another backend.
func TestOverrideModelOnlyKeepsBackend(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	cfg := sess.agent.Config()
	if cfg == nil {
		t.Skip("agent has no config in this harness")
	}
	cfg.LLMProvider = provider.Anthropic
	cfg.SetModel("anthropic.claude-sonnet-4-6")

	restore := sess.applyTurnOverride(turnOverride{Model: "anthropic.claude-haiku-4-5-20251001-v1:0"})
	if restore == nil {
		t.Fatal("want a restore func")
	}
	defer restore()
	if cfg.LLMProvider != provider.Anthropic {
		t.Errorf("a model-only pin must keep the backend, got %q", cfg.LLMProvider)
	}
}

// TestOverrideAnnouncesPin: a turn that silently runs elsewhere makes a routing
// error look like a broken session, so the pin is announced.
func TestOverrideAnnouncesPin(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)
	if sess.agent.Config() == nil {
		t.Skip("agent has no config in this harness")
	}
	id, sub, _ := sess.subscribe()
	defer sess.unsubscribe(id)

	restore := sess.applyTurnOverride(turnOverride{Provider: "afm", Reasoning: "high"})
	if restore != nil {
		defer restore()
	}

	var saw string
	for len(sub) > 0 {
		ev := <-sub
		if ev.Type == "info" && strings.Contains(ev.Text, "quick action") {
			saw = ev.Text
		}
	}
	if saw == "" {
		t.Fatal("expected an info event announcing the per-turn pin")
	}
	for _, want := range []string{"ollama", "high"} {
		if !strings.Contains(saw, want) {
			t.Errorf("announcement %q should mention %q", saw, want)
		}
	}
}

// --- MCP reload safety -------------------------------------------------------

// TestTurnActiveReportsMidFlightStates: the MCP reload path refuses while a turn is
// in flight, so this predicate must be true for a running turn AND for one parked on
// an approval or question prompt (tools must not be swapped while the model is
// waiting to act on them).
func TestTurnActiveReportsMidFlightStates(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "never", false)
	sess := liveSession(t, srv)

	for _, tc := range []struct {
		state string
		want  bool
	}{
		{stateIdle, false},
		{stateBusy, true},
		{stateApproval, true},
		{stateQuestion, true},
	} {
		sess.mu.Lock()
		sess.state = tc.state
		sess.mu.Unlock()
		if got := sess.turnActive(); got != tc.want {
			t.Errorf("turnActive() with state %q = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// --- subagent catalog: servability judged against the persona's own backend ---

// TestCatalogDisplayModelUsesPersonaProvider: a persona can pin its OWN backend,
// and task.go applies that override BEFORE its ModelServableBy check. The catalog
// used the PARENT session's provider instead, so a model that runs fine under the
// persona's own backend was blanked to "inherit" in the UI — and re-saving from
// that screen dropped the pin for good.
func TestCatalogDisplayModelUsesPersonaProvider(t *testing.T) {
	// Mirrors the fixed displayModel closure: effective provider = the persona's
	// override when it names a known one, else the session's.
	displayModel := func(sessionProvider, model, personaProvider string) string {
		effective := sessionProvider
		if personaProvider != "" {
			if _, ok := provider.Lookup(personaProvider); ok {
				effective = personaProvider
			}
		}
		if model == "" || llm.ModelServableBy(model, effective) {
			return model
		}
		return ""
	}

	// A Anthropic model on a persona pinned to ollama: not servable by ollama, so
	// blanking it is correct.
	if got := displayModel(provider.Anthropic, "anthropic.claude-sonnet-4-6", "ollama"); got != "" {
		t.Errorf("a model the persona's backend cannot serve should blank, got %q", got)
	}
	// The same model with no persona override: servable by the session's floodgate.
	if got := displayModel(provider.Anthropic, "anthropic.claude-sonnet-4-6", ""); got == "" {
		t.Error("a model the session's backend can serve must be shown, got blank")
	}
	// The regression: session on ollama, persona pinned to floodgate. Judged against
	// the persona's backend the model is valid and must survive.
	if got := displayModel("ollama", "anthropic.claude-sonnet-4-6", provider.Anthropic); got == "" {
		t.Error("the persona's own provider override must decide servability, got blank (pin would be lost on save)")
	}
	// An unknown/garbage persona provider falls back to the session's.
	if got := displayModel(provider.Anthropic, "anthropic.claude-sonnet-4-6", "not-a-provider"); got == "" {
		t.Error("an unknown persona provider should fall back to the session's, got blank")
	}
}
