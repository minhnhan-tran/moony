package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/domains"
)

const (
	defaultSearchResults = 8
	maxSearchResults     = 20
	// BraveSearchKeyEnv holds a Brave Search API subscription token; when set,
	// web_search uses Brave instead of the keyless DuckDuckGo HTML endpoint.
	BraveSearchKeyEnv = "MOONY_SEARCH_API_KEY"
)

// searchResult is one ranked hit: a title, an absolute URL the agent can pass
// to web_fetch, and a short snippet.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// webSearchTool queries a web search engine and returns ranked results. Unlike
// web_fetch (which needs a URL already in hand), this lets the agent DISCOVER
// URLs. Backend: Brave Search API when MOONY_SEARCH_API_KEY is set, otherwise
// the keyless DuckDuckGo HTML endpoint.
type webSearchTool struct {
	client *http.Client
	// getenv/httpGet are injectable for tests; nil means use the real ones.
	getenv func(string) string
}

func (t *webSearchTool) Name() string { return "web_search" }

func (t *webSearchTool) Description() string {
	return "Search the web and return ranked results (title, URL, snippet) for a query. Use this to DISCOVER " +
		"pages when you don't already have a URL — e.g. to find a library's current docs, an error's cause, or a " +
		"project's homepage — then pass a promising URL to web_fetch to read it. Returns the top results; raise " +
		"max_results for more. This does not read page contents."
}

func (t *webSearchTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"query": {"type": "string", "description": "The search query."},
		"max_results": {"type": "integer", "description": "Maximum results to return (default 8, max 20)."}
	}, "required": ["query"]}`)
}

func (t *webSearchTool) Mutates() bool { return false }

func (t *webSearchTool) env(key string) string {
	if t.getenv != nil {
		return t.getenv(key)
	}
	return os.Getenv(key)
}

func (t *webSearchTool) httpClient() *http.Client {
	if t.client == nil {
		t.client = &http.Client{}
	}
	return t.client
}

func (t *webSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return "", fmt.Errorf("query is required")
	}
	n := in.MaxResults
	if n <= 0 || n > maxSearchResults {
		n = defaultSearchResults
	}

	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var (
		results []searchResult
		err     error
	)
	if key := strings.TrimSpace(t.env(BraveSearchKeyEnv)); key != "" {
		results, err = t.searchBrave(cctx, q, n, key)
	} else {
		results, err = t.searchDuckDuckGo(cctx, q, n)
	}
	if err != nil {
		return "", err
	}
	return renderSearchResults(q, results), nil
}

func renderSearchResults(query string, results []searchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results for %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d results for %q:\n", len(results), query)
	for i, r := range results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "\n   %s", r.Snippet)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// checkHost enforces the user's domain allow/deny rules for the search backend.
func checkHost(rawURL string) error {
	if host := domains.HostFromURL(rawURL); host != "" {
		if ok, reason := domains.Load().Check(host); !ok {
			return fmt.Errorf("web_search %s — manage domains with /domains or the web UI's Domains menu", reason)
		}
	}
	return nil
}

// --- DuckDuckGo HTML backend (keyless) ---

func (t *webSearchTool) searchDuckDuckGo(ctx context.Context, query string, n int) ([]searchResult, error) {
	endpoint := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	if err := checkHost(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "moony (+web_search)")
	req.Header.Set("Accept", "text/html")
	resp, err := t.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web_search: search engine returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return parseDuckDuckGoHTML(body, n), nil
}

var (
	ddgResultRe  = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?is)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	htmlTagStrip = regexp.MustCompile(`(?s)<[^>]+>`)
)

// parseDuckDuckGoHTML extracts result links (and snippets, positionally) from a
// DuckDuckGo HTML response. Result hrefs are DDG redirect links carrying the
// real target in the uddg query param, which we decode back to an absolute URL.
func parseDuckDuckGoHTML(body []byte, n int) []searchResult {
	s := string(body)
	links := ddgResultRe.FindAllStringSubmatch(s, -1)
	snips := ddgSnippetRe.FindAllStringSubmatch(s, -1)

	out := make([]searchResult, 0, len(links))
	for i, m := range links {
		target := decodeDDGHref(m[1])
		if target == "" {
			continue
		}
		r := searchResult{Title: cleanHTMLText(m[2]), URL: target}
		if i < len(snips) {
			r.Snippet = cleanHTMLText(snips[i][1])
		}
		if r.Title == "" {
			r.Title = target
		}
		out = append(out, r)
		if len(out) >= n {
			break
		}
	}
	return out
}

func decodeDDGHref(href string) string {
	href = html.UnescapeString(strings.TrimSpace(href))
	if href == "" {
		return ""
	}
	// DDG uses protocol-relative redirect links like //duckduckgo.com/l/?uddg=...
	probe := href
	if strings.HasPrefix(probe, "//") {
		probe = "https:" + probe
	}
	if u, err := url.Parse(probe); err == nil {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			return uddg // already percent-decoded by Query()
		}
		if strings.HasPrefix(u.Scheme, "http") {
			return u.String()
		}
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return ""
}

func cleanHTMLText(s string) string {
	s = htmlTagStrip.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// --- Brave Search API backend (keyed) ---

func (t *webSearchTool) searchBrave(ctx context.Context, query string, n int, key string) ([]searchResult, error) {
	endpoint := "https://api.search.brave.com/res/v1/web/search?count=" +
		fmt.Sprint(n) + "&q=" + url.QueryEscape(query)
	if err := checkHost(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	resp, err := t.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search (brave): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web_search (brave): API returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseBraveJSON(body, n)
}

func parseBraveJSON(body []byte, n int) ([]searchResult, error) {
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("web_search (brave): decode response: %w", err)
	}
	out := make([]searchResult, 0, len(payload.Web.Results))
	for _, r := range payload.Web.Results {
		out = append(out, searchResult{
			Title:   cleanHTMLText(r.Title),
			URL:     r.URL,
			Snippet: cleanHTMLText(r.Description),
		})
		if len(out) >= n {
			break
		}
	}
	return out, nil
}
