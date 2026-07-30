package web

// Cross-session transcript search. Each machine searches only its OWN local
// session store; the web UI fans out to the other machines connected to the
// same relay through the /l/<host>/ proxy and merges the results client-side
// (the relay keeps no merged session index — it's a pure per-connector proxy).

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// searchMatch is one hit: enough to render a result row and open the session
// (client-side attaches the host it fanned out to, so results are navigable
// cross-machine via focusLaptop + selectSession).
type searchMatch struct {
	SessionID  string `json:"sessionId"`
	ProjectKey string `json:"projectKey"`
	Dir        string `json:"dir"`
	Title      string `json:"title"`
	UpdatedAt  int64  `json:"updatedAt"`
	Role       string `json:"role"`    // role of the message the snippet is from ("title" for a title match)
	Snippet    string `json:"snippet"` // matched text with a little surrounding context
}

const (
	searchMinQueryLen  = 2   // ignore 0/1-char queries (too noisy / too slow)
	searchMaxResults   = 200 // hard cap on total matches returned
	searchPerSession   = 3   // at most this many snippets per session
	searchSnippetChars = 160 // approx snippet width, centered on the match
)

// handleSearch does a case-insensitive substring search across every saved
// session's title and message text on THIS machine, newest-first (Store.List
// already sorts by UpdatedAt desc). Results are capped so a broad query can't
// decrypt-and-scan unboundedly.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := []searchMatch{}
	if len([]rune(q)) < searchMinQueryLen {
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	sessions, err := s.store.List()
	if err != nil {
		http.Error(w, `{"error":"could not list sessions"}`, http.StatusInternalServerError)
		return
	}
	needle := strings.ToLower(q)
	for _, sess := range sessions {
		if len(out) >= searchMaxResults {
			break
		}
		key := projectKey(sess.WorkingDir)
		hits := 0
		add := func(role, snippet string) {
			out = append(out, searchMatch{
				SessionID: sess.ID, ProjectKey: key, Dir: sess.WorkingDir,
				Title: sess.Title, UpdatedAt: sess.UpdatedAt.Unix(),
				Role: role, Snippet: snippet,
			})
			hits++
		}
		if strings.Contains(strings.ToLower(sess.Title), needle) {
			add("title", sess.Title)
		}
		for _, m := range sess.Messages {
			if hits >= searchPerSession || len(out) >= searchMaxResults {
				break
			}
			text := messageSearchText(m)
			if idx := strings.Index(strings.ToLower(text), needle); idx >= 0 {
				add(m.Role, searchSnippet(text, idx, len(q)))
			}
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// messageSearchText flattens a message's searchable text: assistant/user prose,
// thinking, and tool-result content (tool inputs are skipped — they're mostly
// JSON args, noisy to match on).
func messageSearchText(m llm.Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		switch {
		case blk.Text != "":
			b.WriteString(blk.Text)
			b.WriteByte('\n')
		case blk.Thinking != "":
			b.WriteString(blk.Thinking)
			b.WriteByte('\n')
		case blk.Content != "":
			b.WriteString(blk.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// searchSnippet returns ~searchSnippetChars of text centered on the match at
// idx, with collapsed whitespace and leading/trailing ellipses when clipped.
func searchSnippet(text string, idx, matchLen int) string {
	half := (searchSnippetChars - matchLen) / 2
	if half < 0 {
		half = 0
	}
	start := idx - half
	if start < 0 {
		start = 0
	}
	end := idx + matchLen + half
	if end > len(text) {
		end = len(text)
	}
	snippet := strings.Join(strings.Fields(text[start:end]), " ")
	snippet = strings.ToValidUTF8(snippet, "") // byte-slicing may clip a rune at an edge
	if start > 0 {
		snippet = "… " + snippet
	}
	if end < len(text) {
		snippet += " …"
	}
	return snippet
}
