package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// TestHandleWorkflowModels verifies that GET /api/workflows/models returns a
// valid JSON array. When the live API is unreachable (typical in tests) and the
// active provider is Floodgate, the handler falls back to the curated
// AllKnownModels list, so the array must be non-empty.
func TestHandleWorkflowModels(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	h := srv.Handler()

	rec := postJSON(t, h, "GET", "/api/workflows/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/workflows/models: got %d, want 200\nbody: %s", rec.Code, rec.Body)
	}

	var models []workflowModelOption
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("response is not a valid JSON array: %v\nbody: %s", err, rec.Body)
	}
	if len(models) == 0 {
		t.Errorf("expected at least one known model, got empty list")
	}
	for i, m := range models {
		if m.ID == "" {
			t.Errorf("models[%d].id is empty", i)
		}
	}
}

// TestHandleCreateAgent covers JSON binding, required-field validation, and a
// full creation round-trip for POST /api/workflows/agents.
func TestHandleCreateAgent(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	h := srv.Handler()

	validationCases := []struct {
		label      string
		body       string
		wantStatus int
	}{
		{
			label:      "invalid JSON",
			body:       `{not json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "missing name",
			body:       `{"description":"d","model":"claude-3-5-sonnet","systemPrompt":"s"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "blank name",
			body:       `{"name":"   ","description":"d","model":"claude-3-5-sonnet","systemPrompt":"s"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "missing description",
			body:       `{"name":"Test Handler Agent","model":"claude-3-5-sonnet","systemPrompt":"You are a test."}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "missing systemPrompt",
			body:       `{"name":"Test Handler Agent","description":"for tests","model":"claude-3-5-sonnet"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range validationCases {
		t.Run(tc.label, func(t *testing.T) {
			rec := postJSON(t, h, "POST", "/api/workflows/agents", tc.body)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d\nbody: %s", rec.Code, tc.wantStatus, rec.Body)
			}
		})
	}

	t.Run("valid", func(t *testing.T) {
		const (
			wantName = "web-handler-agent" // normalizeCapName("Web Handler Agent")
			wantDesc = "created by handler test"
		)
		rec := postJSON(t, h, "POST", "/api/workflows/agents",
			`{"name":"Web Handler Agent","description":"created by handler test","model":"claude-3-5-sonnet","systemPrompt":"You are a handler test agent."}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body)
		}
		var resp struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not valid JSON: %v\nbody: %s", err, rec.Body)
		}
		if resp.Name != wantName {
			t.Errorf("name = %q, want %q", resp.Name, wantName)
		}
		if resp.Description != wantDesc {
			t.Errorf("description = %q, want %q", resp.Description, wantDesc)
		}
	})
}

// TestHandleCreateSkill covers JSON binding, required-field validation, and a
// full creation round-trip for POST /api/workflows/skills.
func TestHandleCreateSkill(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, config.ApprovalAuto, false)
	h := srv.Handler()

	validationCases := []struct {
		label      string
		body       string
		wantStatus int
	}{
		{
			label:      "invalid JSON",
			body:       `{not json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "missing name",
			body:       `{"description":"d","body":"b"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "blank name",
			body:       `{"name":"   ","description":"d","body":"b"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "missing description",
			body:       `{"name":"Test Handler Skill","body":"do a thing"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			label:      "missing body",
			body:       `{"name":"Test Handler Skill","description":"d"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range validationCases {
		t.Run(tc.label, func(t *testing.T) {
			rec := postJSON(t, h, "POST", "/api/workflows/skills", tc.body)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d\nbody: %s", rec.Code, tc.wantStatus, rec.Body)
			}
		})
	}

	t.Run("valid", func(t *testing.T) {
		const (
			wantName = "web-handler-skill" // normalizeCapName("Web Handler Skill")
			wantDesc = "created by skill test"
		)
		rec := postJSON(t, h, "POST", "/api/workflows/skills",
			`{"name":"Web Handler Skill","description":"created by skill test","body":"Run the skill test."}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body)
		}
		var resp struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not valid JSON: %v\nbody: %s", err, rec.Body)
		}
		if resp.Name != wantName {
			t.Errorf("name = %q, want %q", resp.Name, wantName)
		}
		if resp.Description != wantDesc {
			t.Errorf("description = %q, want %q", resp.Description, wantDesc)
		}
	})
}
