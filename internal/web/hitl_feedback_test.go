package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

func TestApproveWithFeedbackFlow(t *testing.T) {
	fake := &fakeStreamer{turns: []llm.Response{
		// First turn: propose write_file, which requires approval
		{StopReason: "tool_use", Blocks: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{}`)}}},
		// Second turn: end turn
		{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("acknowledged")}},
	}}
	srv, key := newTestServer(t, fake, config.ApprovalPrompt, true)
	h := srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	json.Unmarshal(rec.Body.Bytes(), &created)

	// Post initial prompt to start the turn.
	postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/message", `{"text":"write something"}`)

	// The agent blocks waiting for approval of the tool.
	waitState(t, srv, created.ID, stateApproval)

	// POST deny with specific custom feedback.
	body := `{"decision":"deny", "feedback":"no, use different parameters"}`
	rec = postJSON(t, h, "POST", "/api/sessions/"+created.ID+"/approve", body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("approve status = %d, want 204", rec.Code)
	}

	// Wait for the agent to finish and go back to idle.
	waitState(t, srv, created.ID, stateIdle)

	srv.mu.Lock()
	sess := srv.sessions[created.ID]
	srv.mu.Unlock()

	_, _, hist := sess.subscribe()
	var foundFeedback bool
	for _, e := range hist {
		if e.Type == "tool_end" {
			if strings.Contains(e.Output, "no, use different parameters") {
				foundFeedback = true
			}
		}
	}

	if !foundFeedback {
		t.Error("ToolEnd output did not contain the user's denial feedback")
	}
}
