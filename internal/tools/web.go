package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/domains"
	"github.com/pysty1995/moony/internal/netrc"
)

// Defaults and caps shared by web_fetch and batch_web_fetch.
const (
	defaultMaxBytes = 256 * 1024 // text body cap fed to the model
	maxMaxBytes     = 1 << 20    // hard ceiling for max_bytes
	maxBinaryBytes  = 25 << 20   // binary downloads streamed to a temp file
	defaultTimeout  = 30 * time.Second
	maxTimeout      = 2 * time.Minute
)

// fetchParams is one fetch request. It is shared by web_fetch (the whole input)
// and batch_web_fetch (one element of its "requests" array), so both tools
// accept exactly the same per-URL options.
type fetchParams struct {
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	MaxBytes   flexInt           `json:"max_bytes"`
	TimeoutSec flexInt           `json:"timeout_sec"`
	Format     string            `json:"format"` // markdown (default) | html | text | json
}

// webFetchTool fetches a URL and returns its body, routed by content type:
// JSON is pretty-printed, HTML is reduced to readable markdown/text, other
// textual types are returned as-is, and binary responses are streamed to a temp
// file (so raw bytes never flood the model context). It is non-mutating (no auth
// beyond caller-supplied headers, idempotent GET) so it needs no user approval.
type webFetchTool struct {
	r      *Registry
	client *http.Client
}

func (t *webFetchTool) Name() string { return "web_fetch" }

func (t *webFetchTool) Description() string {
	return "Fetch a URL via HTTP(S) GET and return readable content. The body is " +
		"routed by content type: JSON is pretty-printed; HTML is reduced to " +
		"markdown (or text/raw HTML via 'format'); plain text, XML, and " +
		"JavaScript are returned as-is; binary responses are streamed to a temp " +
		"file and reported by path. Use it to read docs, issues, raw/gist files, " +
		"or API responses. Pass 'headers' for custom request headers, 'format' " +
		"(markdown|html|text|json), and 'max_bytes' to cap the body. Does NOT run " +
		"JavaScript — use the browser tool for JS-heavy pages. To fetch several " +
		"URLs at once, use batch_web_fetch. NOTE: web_fetch is unauthenticated, so " +
		"AppleConnect-gated internal Apple URLs (github.pie.apple.com and other " +
		"*.apple.com behind SSO) return a SAML login page, not content — use " +
		"apple_fetch for those (enable the \"apple-fetch\" toolset if it's dormant)."
}

func (t *webFetchTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"url":         {"type": "string", "description": "Absolute http or https URL."},
			"format":      {"type": "string", "enum": ["markdown","html","text","json"], "description": "Output shape for HTML pages (default markdown). 'json' forces JSON pretty-printing. Ignored for already-plain content."},
			"headers":     {"type": "object", "additionalProperties": {"type": "string"}, "description": "Extra HTTP request headers to send."},
			"max_bytes":   {"type": "integer", "description": "Cap response body bytes; default 262144 (256 KB)."},
			"timeout_sec": {"type": "integer", "description": "Max seconds before giving up; default 30."}
		},
		"required": ["url"]
	}`)
}

func (t *webFetchTool) Mutates() bool { return false }

func (t *webFetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in fetchParams
	if err := decode(input, &in); err != nil {
		return "", err
	}
	return t.do(ctx, in)
}

// httpClient returns the shared client, creating a default one on first use.
// No client-level timeout is set; per-request deadlines come from the context
// so batch_web_fetch can run many fetches with independent timeouts on one
// client. Tests inject a client with a stub transport.
func (t *webFetchTool) httpClient() *http.Client {
	if t.client == nil {
		t.client = &http.Client{}
	}
	return t.client
}

// do performs a single fetch and renders it (metadata header + body, or a
// saved-file note for binary). Errors are returned so the caller can decide
// whether to fail (web_fetch) or record a per-item error (batch_web_fetch).
func (t *webFetchTool) do(ctx context.Context, p fetchParams) (string, error) {
	u := strings.TrimSpace(p.URL)
	if u == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", fmt.Errorf("only http(s) URLs are supported")
	}
	// Enforce the user's persisted domain allow/deny rules (see internal/domains).
	// Loaded per call so edits from /domains or the web UI apply live.
	if host := domains.HostFromURL(u); host != "" {
		if ok, reason := domains.Load().Check(host); !ok {
			return "", fmt.Errorf("web_fetch %s — manage domains with /domains or the web UI's Domains menu", reason)
		}
	}

	maxBytes := int(p.MaxBytes)
	if maxBytes <= 0 || maxBytes > maxMaxBytes {
		maxBytes = defaultMaxBytes
	}
	timeout := time.Duration(int(p.TimeoutSec)) * time.Second
	if timeout <= 0 || timeout > maxTimeout {
		timeout = defaultTimeout
	}
	format := strings.ToLower(strings.TrimSpace(p.Format))

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "moony (+web_fetch)")
	if format == "json" {
		req.Header.Set("Accept", "application/json, */*;q=0.5")
	} else {
		req.Header.Set("Accept", "text/html,text/plain,application/json,application/xml;q=0.9,*/*;q=0.5")
	}
	for k, v := range p.Headers {
		if k = strings.TrimSpace(k); k != "" {
			req.Header.Set(k, v)
		}
	}
	// Add a Bearer token from ~/.netrc when the host has an entry and the caller
	// didn't already supply an Authorization header. Go strips Authorization on
	// cross-host redirects, so a token can't leak to a different domain.
	applyNetrcAuth(req, u)

	resp, err := t.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %v", u, err)
	}
	defer resp.Body.Close()

	res := fetchResult{
		url:         u,
		status:      resp.Status,
		contentType: resp.Header.Get("Content-Type"),
		maxBytes:    maxBytes,
	}
	if resp.Request != nil && resp.Request.URL != nil {
		if final := resp.Request.URL.String(); final != u {
			res.finalURL = final
		}
	}

	ct := res.contentType
	// Binary / non-textual (and not an attachment we can read as text) → stream
	// to a temp file instead of dumping raw bytes into the model context.
	if ct != "" && !isTextualType(ct) && format != "json" {
		path, size, derr := t.saveBinary(res.finalOrURL(), ct, resp.Header.Get("Content-Disposition"), resp.Body)
		if derr != nil {
			return "", fmt.Errorf("download %s: %v", u, derr)
		}
		res.filePath = path
		res.fileSize = size
		return res.render(), nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if len(body) > maxBytes {
		res.truncated = true
		body = body[:maxBytes]
	}

	res.content, res.title = routeBody(ct, format, body)
	res.wordCount = len(strings.Fields(res.content))
	return res.render(), nil
}

// fetchResult is the rendered-output model for one fetch.
type fetchResult struct {
	url         string
	finalURL    string // set only when different from url
	status      string
	contentType string
	title       string
	content     string
	wordCount   int
	truncated   bool
	maxBytes    int
	// Binary responses:
	filePath string
	fileSize int64
}

func (r fetchResult) finalOrURL() string {
	if r.finalURL != "" {
		return r.finalURL
	}
	return r.url
}

// appleAuthWallHint returns a corrective note when an internal Apple URL responds
// with an AppleConnect/SAML login wall instead of real content. web_fetch is
// unauthenticated, so AppleConnect-gated *.apple.com hosts (e.g.
// github.pie.apple.com) bounce it to the IdMS/SAML login page; apple_fetch signs
// in via AppleConnect and gets the real content. Returns "" when this doesn't
// look like an Apple auth wall (so a public *.apple.com page is unaffected).
func (r fetchResult) appleAuthWallHint() string {
	u, err := url.Parse(r.url)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "apple.com" && !strings.HasSuffix(host, ".apple.com") {
		return ""
	}
	hay := strings.ToLower(r.finalURL + "\n" + r.title + "\n")
	if n := len(r.content); n > 0 {
		if n > 2000 {
			n = 2000
		}
		hay += strings.ToLower(r.content[:n])
	}
	wall := false
	for _, sig := range []string{
		"idmsac.apple.com", "idmswebauth", "samlrequest", "samllogin",
		"single sign-on", "/login?return_to", "identity provider in order to authenticate",
	} {
		if strings.Contains(hay, sig) {
			wall = true
			break
		}
	}
	if !wall {
		return ""
	}
	return "NOTE: this is an authenticated Apple URL and web_fetch is NOT signed in — it " +
		"received the AppleConnect/SAML login page, not the real content. Use apple_fetch " +
		"instead, which authenticates with AppleConnect. apple_fetch is in the \"apple-fetch\" " +
		"toolset; if it isn't active yet, call enable_toolset with \"apple-fetch\" first, then " +
		"apple_fetch on this URL.\n\n"
}

// render builds the metadata header followed by the body (or a saved-file note).
func (r fetchResult) render() string {
	var h strings.Builder
	// Lead with a corrective hint when an internal Apple URL came back as an
	// AppleConnect/SAML login wall — web_fetch isn't signed in, so the body is the
	// login page, not the content. apple_fetch (AppleConnect-authenticated) is the
	// right tool; surface it here since apple_fetch's own "prefer me" steering is
	// invisible while the apple-fetch toolset is dormant.
	h.WriteString(r.appleAuthWallHint())
	fmt.Fprintf(&h, "GET %s -> %s\n", r.url, r.status)
	if r.finalURL != "" {
		fmt.Fprintf(&h, "Final URL: %s\n", r.finalURL)
	}
	if r.title != "" {
		fmt.Fprintf(&h, "Title: %s\n", r.title)
	}
	if r.contentType != "" {
		fmt.Fprintf(&h, "Content-Type: %s\n", r.contentType)
	}
	if r.filePath != "" {
		fmt.Fprintf(&h, "Saved binary response to: %s (%d bytes)\n", r.filePath, r.fileSize)
		return strings.TrimRight(h.String(), "\n")
	}
	if r.wordCount > 0 {
		fmt.Fprintf(&h, "Word count: %d\n", r.wordCount)
	}
	if r.truncated {
		fmt.Fprintf(&h, "(truncated to %d bytes)\n", r.maxBytes)
	}
	h.WriteString("\n")
	return h.String() + r.content
}

// routeBody turns a textual body into content (and an optional title) according
// to its content type and the requested format.
func routeBody(contentType, format string, body []byte) (content, title string) {
	switch {
	case format == "json" || isJSONType(contentType):
		if pretty, ok := prettyJSON(body); ok {
			return pretty, ""
		}
		return strings.TrimSpace(string(body)), ""
	case isHTMLType(contentType) || (contentType == "" && looksHTML(body)):
		s := string(body)
		title = extractTitle(s)
		switch format {
		case "html":
			return strings.TrimSpace(s), title
		case "text":
			return stripHTML(s), title
		default: // markdown
			return htmlToMarkdown(s), title
		}
	default:
		// text/plain, text/markdown, XML/SVG, JavaScript, and other textual
		// types: return as-is (normalize line endings, trim).
		return strings.TrimSpace(strings.ReplaceAll(string(body), "\r\n", "\n")), ""
	}
}

// saveBinary streams a non-text response body to a uniquely-named temp file
// under <tempdir>/sqe-web-fetch and returns the path and size. The download is
// capped at maxBinaryBytes.
func (t *webFetchTool) saveBinary(finalURL, contentType, contentDisposition string, body io.Reader) (string, int64, error) {
	dir := path.Join(os.TempDir(), "sqe-web-fetch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	base, ext := tempName(finalURL, contentType, contentDisposition)
	f, err := os.CreateTemp(dir, base+"-*"+ext)
	if err != nil {
		return "", 0, err
	}
	n, cerr := io.Copy(f, io.LimitReader(body, maxBinaryBytes))
	closeErr := f.Close()
	if cerr != nil {
		os.Remove(f.Name())
		return "", 0, cerr
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	return f.Name(), n, nil
}

// tempName derives a sanitized base filename and extension for a downloaded
// binary, preferring the Content-Disposition filename, then the URL path, with
// the extension backfilled from the content type when missing.
func tempName(finalURL, contentType, contentDisposition string) (base, ext string) {
	name := cdFilename(contentDisposition)
	if name == "" {
		if u, err := url.Parse(finalURL); err == nil {
			name = path.Base(u.Path)
		}
	}
	name = sanitizeFileName(name)
	ext = path.Ext(name)
	base = strings.TrimSuffix(name, ext)
	if base == "" {
		base = "download"
	}
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
			ext = exts[0]
		} else {
			ext = ".dat"
		}
	}
	return base, ext
}

var (
	reCDFilename     = regexp.MustCompile(`(?i)filename\*?=(?:UTF-8''|")?([^";]+)`)
	reUnsafeFileChar = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

func cdFilename(cd string) string {
	m := reCDFilename.FindStringSubmatch(cd)
	if len(m) < 2 {
		return ""
	}
	return path.Base(strings.Trim(strings.TrimSpace(m[1]), `"`))
}

func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return ""
	}
	name = reUnsafeFileChar.ReplaceAllString(name, "_")
	return strings.Trim(name, "_.")
}

// prettyJSON indent-formats a JSON document. Returns ok=false when the body is
// not valid JSON (callers then fall back to the raw text).
func prettyJSON(body []byte) (string, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", "  "); err != nil {
		return "", false
	}
	return buf.String(), true
}

// --- content-type predicates ------------------------------------------------

// normalizeCT lowercases a Content-Type and drops parameters (e.g. "; charset").
func normalizeCT(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func isJSONType(ct string) bool {
	n := normalizeCT(ct)
	return n == "application/json" || n == "text/json" || n == "application/x-ndjson" || strings.HasSuffix(n, "+json")
}

func isXMLType(ct string) bool {
	n := normalizeCT(ct)
	return n == "application/xml" || n == "text/xml" || n == "image/svg+xml" || strings.HasSuffix(n, "+xml")
}

func isJSType(ct string) bool {
	switch normalizeCT(ct) {
	case "application/javascript", "application/x-javascript", "text/javascript", "application/ecmascript":
		return true
	}
	return false
}

func isHTMLType(ct string) bool {
	n := normalizeCT(ct)
	return n == "text/html" || n == "application/xhtml+xml"
}

// isTextualType reports whether a content type can be read as text (and thus
// returned inline) rather than streamed to a file as binary.
func isTextualType(ct string) bool {
	n := normalizeCT(ct)
	if strings.HasPrefix(n, "text/") || isJSONType(n) || isXMLType(n) || isJSType(n) || isHTMLType(n) {
		return true
	}
	switch n {
	case "application/x-sh", "application/yaml", "application/x-yaml",
		"application/toml", "application/csv", "application/graphql":
		return true
	}
	return false
}

func looksHTML(body []byte) bool {
	low := strings.ToLower(string(body[:min(len(body), 1024)]))
	return strings.Contains(low, "<html") || strings.Contains(low, "<!doctype html") || strings.Contains(low, "<body")
}

// --- HTML reduction ---------------------------------------------------------

var (
	// htmlDropRe removes elements whose content is not readable prose.
	htmlDropRe  = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg|head)\b[^>]*>.*?</\s*(script|style|noscript|template|svg|head)\s*>`)
	htmlTitleRe = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	// htmlAnchorRe captures an <a href=...>text</a> (double, single, or unquoted href).
	htmlAnchorRe       = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s">]+))[^>]*>(.*?)</a>`)
	htmlHeadingOpenRe  = regexp.MustCompile(`(?is)<h([1-6])\b[^>]*>`)
	htmlHeadingCloseRe = regexp.MustCompile(`(?is)</h[1-6]\s*>`)
	htmlListItemRe     = regexp.MustCompile(`(?is)<li\b[^>]*>`)
	htmlBreakRe        = regexp.MustCompile(`(?is)<\s*(?:br|hr)\s*/?\s*>|</\s*(?:p|div|section|article|ul|ol|li|tr|table|h[1-6])\s*>`)
	htmlTagRe          = regexp.MustCompile(`(?is)<[^>]+>`)
	htmlBlankLinesRe   = regexp.MustCompile(`\n{3,}`)
	htmlTrailingWSRe   = regexp.MustCompile(`[ \t]+\n`)
	htmlInlineSpaceRe  = regexp.MustCompile(`[ \t]{2,}`)
)

// htmlToMarkdown produces a roughly-readable markdown rendering of an HTML page:
// links become [text](href), headings get #-prefixes, list items get "- ", and
// block boundaries become newlines. It is a pragmatic reducer, not a faithful
// renderer.
func htmlToMarkdown(s string) string {
	s = htmlDropRe.ReplaceAllString(s, "")
	s = htmlAnchorRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := htmlAnchorRe.FindStringSubmatch(m)
		href := strings.TrimSpace(firstNonEmpty(sub[1], sub[2], sub[3]))
		text := strings.TrimSpace(html.UnescapeString(stripTags(sub[4])))
		switch {
		case text == "":
			return href
		case href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:"):
			return text
		default:
			return "[" + text + "](" + href + ")"
		}
	})
	s = htmlHeadingOpenRe.ReplaceAllStringFunc(s, func(m string) string {
		lvl, _ := strconv.Atoi(htmlHeadingOpenRe.FindStringSubmatch(m)[1])
		return "\n\n" + strings.Repeat("#", lvl) + " "
	})
	s = htmlHeadingCloseRe.ReplaceAllString(s, "\n\n")
	s = htmlListItemRe.ReplaceAllString(s, "\n- ")
	s = htmlBreakRe.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return tidyText(s)
}

// stripHTML produces a plain-text rendering (the "text" format): tags removed,
// entities decoded, blank lines collapsed.
func stripHTML(s string) string {
	s = htmlDropRe.ReplaceAllString(s, "")
	s = htmlBreakRe.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return tidyText(s)
}

// extractTitle returns the decoded <title> text, if any.
func extractTitle(s string) string {
	m := htmlTitleRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(stripTags(m[1])))
}

func stripTags(s string) string { return htmlTagRe.ReplaceAllString(s, "") }

func tidyText(s string) string {
	s = htmlInlineSpaceRe.ReplaceAllString(s, " ")
	s = htmlTrailingWSRe.ReplaceAllString(s, "\n")
	s = htmlBlankLinesRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// netrcEnabled reports whether ~/.netrc Bearer auth is active. On by default;
// set SQE_NETRC=0/off/false/no to disable.
func netrcEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_NETRC"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// applyNetrcAuth sets an "Authorization: Bearer <token>" header on req from the
// matching ~/.netrc machine entry, unless disabled, already set by the caller,
// or no entry exists for the host. Failures to read/parse netrc are silent (the
// request just proceeds unauthenticated).
func applyNetrcAuth(req *http.Request, rawURL string) {
	if !netrcEnabled() || req.Header.Get("Authorization") != "" {
		return
	}
	host := domains.HostFromURL(rawURL)
	if host == "" {
		return
	}
	f, err := netrc.Load()
	if err != nil {
		return
	}
	if b := f.Bearer(host); b != "" {
		req.Header.Set("Authorization", b)
	}
}
