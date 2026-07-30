package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/llm"
	sessionpkg "github.com/pysty1995/moony/internal/session"
)

func TestHandleSearch(t *testing.T) {
	srv, _ := newTestServer(t, &fakeStreamer{}, "auto", false)

	save := func(id, title string, msgs []llm.Message) {
		t.Helper()
		if err := srv.store.Save(&sessionpkg.Session{ID: id, Title: title, WorkingDir: "/tmp/proj", Messages: msgs}); err != nil {
			t.Fatal(err)
		}
	}
	save("s1", "Fix login bug", []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "the widget throws a nullpointer on submit"}}},
		{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "patched the handler"}}},
	})
	save("s2", "Unrelated task", []llm.Message{
		{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "add a dark theme"}}},
	})

	do := func(q string) []searchMatch {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleSearch(rec, httptest.NewRequest("GET", "/api/search?q="+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var got []searchMatch
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, rec.Body.String())
		}
		return got
	}

	// Body match, case-insensitive.
	got := do("NULLPOINTER")
	if len(got) != 1 || got[0].SessionID != "s1" {
		t.Fatalf("body search = %+v, want one hit on s1", got)
	}
	if got[0].ProjectKey != projectKey("/tmp/proj") {
		t.Errorf("projectKey = %q, want %q", got[0].ProjectKey, projectKey("/tmp/proj"))
	}
	if got[0].Snippet == "" || got[0].Role != "user" {
		t.Errorf("snippet/role = %q/%q, want non-empty user snippet", got[0].Snippet, got[0].Role)
	}

	// Title match yields a "title" role hit.
	if got := do("login"); len(got) != 1 || got[0].Role != "title" {
		t.Errorf("title search = %+v, want one title hit", got)
	}

	// Match in the other session only.
	if got := do("dark"); len(got) != 1 || got[0].SessionID != "s2" {
		t.Errorf("dark search = %+v, want one hit on s2", got)
	}

	// No match, and too-short queries, return an empty (non-null) array.
	for _, q := range []string{"zzzznope", "a", ""} {
		if got := do(q); len(got) != 0 {
			t.Errorf("query %q = %+v, want empty", q, got)
		}
	}
}

func TestSearchSnippet(t *testing.T) {
	long := ""
	for i := 0; i < 20; i++ {
		long += "the quick brown fox jumps over the lazy dog. "
	}
	long += "UNIQUEWORD "
	for i := 0; i < 20; i++ {
		long += "and keeps running past the fence. "
	}
	idx := strings.Index(long, "UNIQUEWORD")
	s := searchSnippet(long, idx, len("UNIQUEWORD"))
	if !strings.Contains(s, "UNIQUEWORD") {
		t.Fatalf("snippet should contain the match, got %q", s)
	}
	if len(s) > searchSnippetChars+16 { // window + ellipses/rounding slack
		t.Errorf("snippet too long (%d): %q", len(s), s)
	}
	// Clipped on both ends → both ellipses.
	if s[:len("…")] != "…" {
		t.Errorf("expected leading ellipsis, got %q", s)
	}
}
