package web

import (
	"encoding/json"
	"strings"
	"testing"
)

// readProject returns the project with the given key from GET /api/projects.
func readProject(t *testing.T, srv *Server, key string) projectInfo {
	t.Helper()
	rec := postJSON(t, srv.Handler(), "GET", "/api/projects", "")
	if rec.Code != 200 {
		t.Fatalf("list projects: %d", rec.Code)
	}
	var got []projectInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode project list: %v", err)
	}
	for _, p := range got {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("project %q missing from the list", key)
	return projectInfo{}
}

func TestProjectNotesSaveAndList(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, "never", false)

	if p := readProject(t, srv, key); p.Notes != "" {
		t.Fatalf("a fresh project should have no notes, got %q", p.Notes)
	}

	const body = "- [ ] wire the endpoint\n- [x] write the store"
	rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/notes",
		`{"notes":`+jsonString(body)+`}`)
	if rec.Code != 200 {
		t.Fatalf("save notes: %d %s", rec.Code, rec.Body.String())
	}
	if p := readProject(t, srv, key); p.Notes != body {
		t.Errorf("notes should round-trip through the project list:\n want %q\n got  %q", body, p.Notes)
	}
}

// Clearing must delete the entry rather than store a blank string, or the project
// stays badged as "has notes" and @notes keeps being offered for an empty backlog.
func TestProjectNotesClearRemovesEntry(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, "never", false)

	postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/notes", `{"notes":"something"}`)
	if p := readProject(t, srv, key); p.Notes == "" {
		t.Fatal("precondition: notes should have been saved")
	}
	// Whitespace-only counts as cleared.
	rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/notes", `{"notes":"   \n  "}`)
	if rec.Code != 200 {
		t.Fatalf("clear notes: %d", rec.Code)
	}
	if p := readProject(t, srv, key); p.Notes != "" {
		t.Errorf("cleared notes should be absent, got %q", p.Notes)
	}
}

func TestProjectNotesRejectsBadInput(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, "never", false)

	if rec := postJSON(t, srv.Handler(), "POST", "/api/projects/!!not-base64!!/notes", `{"notes":"x"}`); rec.Code != 400 {
		t.Errorf("an undecodable project key should be 400, got %d", rec.Code)
	}
	if rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/notes", `{"notes":`); rec.Code != 400 {
		t.Errorf("malformed JSON should be 400, got %d", rec.Code)
	}
}

// The notes payload is capped so an unbounded body can't be buffered into memory.
func TestProjectNotesTooLargeRejected(t *testing.T) {
	srv, key := newTestServer(t, &fakeStreamer{}, "never", false)

	huge := strings.Repeat("A", maxProjectNotesBytes+1024)
	rec := postJSON(t, srv.Handler(), "POST", "/api/projects/"+key+"/notes",
		`{"notes":`+jsonString(huge)+`}`)
	if rec.Code != 400 {
		t.Errorf("an oversized notes body should be rejected, got %d", rec.Code)
	}
	if p := readProject(t, srv, key); p.Notes != "" {
		t.Error("a rejected oversized save must not be persisted")
	}
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
