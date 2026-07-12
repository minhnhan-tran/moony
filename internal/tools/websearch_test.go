package tools

import (
	"strings"
	"testing"
)

func TestParseDuckDuckGoHTML(t *testing.T) {
	// Minimal fixture mirroring DuckDuckGo's HTML result markup: redirect hrefs
	// carrying the real target in the uddg param, plus positional snippets.
	body := `
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F&rut=x">The Go <b>Docs</b></a>
  <a class="result__snippet" href="...">Official documentation for the Go programming &amp; language.</a>
</div>
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fpkg.go.dev%2F">pkg.go.dev</a>
  <a class="result__snippet">Find packages.</a>
</div>`

	got := parseDuckDuckGoHTML([]byte(body), 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(got), got)
	}
	if got[0].URL != "https://go.dev/doc/" {
		t.Errorf("result 0 URL = %q, want decoded target", got[0].URL)
	}
	if got[0].Title != "The Go Docs" {
		t.Errorf("result 0 Title = %q, want tags stripped", got[0].Title)
	}
	if got[0].Snippet != "Official documentation for the Go programming & language." {
		t.Errorf("result 0 Snippet = %q, want entities decoded", got[0].Snippet)
	}
	if got[1].URL != "https://pkg.go.dev/" {
		t.Errorf("result 1 URL = %q", got[1].URL)
	}

	if lim := parseDuckDuckGoHTML([]byte(body), 1); len(lim) != 1 {
		t.Errorf("limit not honored: got %d results", len(lim))
	}
}

func TestRenderSearchResultsEmpty(t *testing.T) {
	if out := renderSearchResults("xyz", nil); !strings.Contains(out, "No results") {
		t.Errorf("empty render = %q", out)
	}
}

func TestParseBraveJSON(t *testing.T) {
	body := `{"web":{"results":[
		{"title":"Go","url":"https://go.dev","description":"The Go <b>language</b>"},
		{"title":"Rust","url":"https://rust-lang.org","description":"Rust"}
	]}}`
	got, err := parseBraveJSON([]byte(body), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].URL != "https://go.dev" || got[0].Snippet != "The Go language" {
		t.Fatalf("unexpected brave parse: %+v", got)
	}
}
