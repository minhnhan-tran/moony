package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// runtimeSnapshot reports the connector's current provider/model/reasoning plus
// the available providers and model-specific reasoning levels.
func TestRuntimeSnapshot(t *testing.T) {
	s := &Server{cfg: &config.Config{Model: "anthropic.claude-opus-4-8", LLMProvider: "anthropic"}}
	rt := s.runtimeSnapshot()
	if rt.Model != "anthropic.claude-opus-4-8" {
		t.Errorf("model = %q", rt.Model)
	}
	if len(rt.Providers) == 0 {
		t.Error("expected at least one provider option")
	}
	if len(rt.ReasoningLevels) == 0 {
		t.Error("expected reasoning levels for the model")
	}
}

// POST /runtime applies a reasoning change and persists; the response reflects it.
func TestSetRuntimeReasoning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // redirect Save() off the real config
	s := &Server{cfg: &config.Config{Model: "anthropic.claude-opus-4-8", LLMProvider: "anthropic"}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/workflows/runtime", strings.NewReader(`{"reasoning":"low"}`))
	s.handleSetWorkflowRuntime(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := s.cfg.GetReasoningFor(s.cfg.Model); got != "low" {
		t.Errorf("reasoning = %q, want low", got)
	}
	if !strings.Contains(rec.Body.String(), `"reasoning":"low"`) {
		t.Errorf("response did not reflect the new reasoning: %s", rec.Body.String())
	}
}

// POST /runtime applies a model change.
func TestSetRuntimeModel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := &Server{cfg: &config.Config{Model: "anthropic.claude-opus-4-8", LLMProvider: "anthropic"}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/workflows/runtime", strings.NewReader(`{"model":"anthropic.claude-sonnet-4-6"}`))
	s.handleSetWorkflowRuntime(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if s.cfg.Model != "anthropic.claude-sonnet-4-6" {
		t.Errorf("model = %q, want sonnet", s.cfg.Model)
	}
}

// Bad input is rejected before any persistence.
func TestSetRuntimeRejectsBadInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := &Server{cfg: &config.Config{Model: "anthropic.claude-opus-4-8", LLMProvider: "anthropic"}}

	for _, tc := range []struct {
		name, body string
	}{
		{"invalid json", `{not json`},
		{"unknown provider", `{"provider":"nope-not-real"}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/workflows/runtime", strings.NewReader(tc.body))
		s.handleSetWorkflowRuntime(rec, req)
		if rec.Code == 200 {
			t.Errorf("%s: expected a non-200, got 200", tc.name)
		}
	}
}
