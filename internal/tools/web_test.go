package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// roundTripperFunc adapts a function into an http.RoundTripper so we can stub
// HTTP responses without binding a TCP port (sandboxed test envs forbid bind).
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// isolateDomains points the global config dir at a throwaway temp dir so the
// web_fetch domain check sees no rules (allow all) and these mechanics tests
// don't depend on the developer's real ~/.config (which carries the seeded
// Apple-only default from domains.EnsureDefaults).
func isolateDomains(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func stubResp(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestWebFetchPlainText(t *testing.T) {
	isolateDomains(t)
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://example.com/" {
			t.Fatalf("unexpected URL: %s", r.URL)
		}
		return stubResp(200, "text/plain", "hello world"), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/"})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "hello world") {
		t.Fatalf("body missing:\n%s", got)
	}
	if !strings.Contains(got, "Content-Type: text/plain") {
		t.Fatalf("header missing:\n%s", got)
	}
}

func TestWebFetchStripsHTML(t *testing.T) {
	isolateDomains(t)
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(200, "text/html; charset=utf-8",
			`<html><body><script>secret()</script><p>visible &amp; trimmed</p></body></html>`), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/"})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<p>") || strings.Contains(got, "secret()") {
		t.Fatalf("HTML/script not stripped: %s", got)
	}
	if !strings.Contains(got, "visible & trimmed") {
		t.Fatalf("body text missing or entity not decoded: %s", got)
	}
}

func TestWebFetchTruncates(t *testing.T) {
	isolateDomains(t)
	big := strings.Repeat("x", 600)
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(200, "text/plain", big), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/", "max_bytes": 100})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "(truncated to 100 bytes)") {
		t.Fatalf("expected truncation marker, got:\n%s", got)
	}
}

func TestWebFetchRejectsNonHTTP(t *testing.T) {
	tool := &webFetchTool{}
	in, _ := json.Marshal(map[string]any{"url": "file:///etc/passwd"})
	if _, err := tool.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error for non-http(s) scheme")
	}
}

func TestWebFetchRequiresURL(t *testing.T) {
	tool := &webFetchTool{}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error when url is missing")
	}
}

func TestWebFetchJSONPretty(t *testing.T) {
	isolateDomains(t)
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(200, "application/json", `{"a":1,"b":[2,3]}`), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/api"})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\"a\": 1") || !strings.Contains(got, "\"b\": [") {
		t.Fatalf("JSON not pretty-printed:\n%s", got)
	}
}

func TestWebFetchMarkdownLinkAndTitle(t *testing.T) {
	isolateDomains(t)
	body := `<html><head><title>Doc &amp; Page</title></head><body>` +
		`<h1>Heading</h1><p>see <a href="https://x.com/p">the link</a> now</p></body></html>`
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(200, "text/html", body), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/"})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Title: Doc & Page") {
		t.Fatalf("title not extracted/decoded:\n%s", got)
	}
	if !strings.Contains(got, "[the link](https://x.com/p)") {
		t.Fatalf("anchor not converted to markdown:\n%s", got)
	}
	if !strings.Contains(got, "# Heading") {
		t.Fatalf("heading not converted:\n%s", got)
	}
}

func TestWebFetchTextFormatNoMarkdown(t *testing.T) {
	isolateDomains(t)
	body := `<html><body><p>hi <a href="https://x.com">there</a></p></body></html>`
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(200, "text/html", body), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/", "format": "text"})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "](") {
		t.Fatalf("text format should not emit markdown links:\n%s", got)
	}
	if !strings.Contains(got, "there") {
		t.Fatalf("link text missing:\n%s", got)
	}
}

func TestWebFetchSendsCustomHeaders(t *testing.T) {
	isolateDomains(t)
	var gotHeader string
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotHeader = r.Header.Get("X-Test")
		return stubResp(200, "text/plain", "ok"), nil
	})}}
	in, _ := json.Marshal(map[string]any{
		"url":     "https://example.com/",
		"headers": map[string]string{"X-Test": "secret-value"},
	})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "secret-value" {
		t.Fatalf("custom header not sent, got %q", gotHeader)
	}
}

func TestWebFetchBinaryToFile(t *testing.T) {
	isolateDomains(t)
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(200, "image/png", "PNGDATA"), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/logo.png"})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Saved binary response to:") || !strings.Contains(got, "(7 bytes)") {
		t.Fatalf("binary not saved to file:\n%s", got)
	}
	if strings.Contains(got, "PNGDATA") {
		t.Fatalf("raw binary bytes should not be inlined:\n%s", got)
	}
	path := strings.TrimSpace(strings.SplitN(strings.SplitN(got, "Saved binary response to: ", 2)[1], " (", 2)[0])
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("temp file unreadable: %v", rerr)
	}
	defer os.Remove(path)
	if string(data) != "PNGDATA" {
		t.Fatalf("temp file content mismatch: %q", data)
	}
}

func TestBatchWebFetchConcurrentOrdered(t *testing.T) {
	isolateDomains(t)
	wf := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return stubResp(200, "text/plain", "body-for-"+r.URL.Path), nil
	})}}
	tool := &batchWebFetchTool{wf: wf}
	in, _ := json.Marshal(map[string]any{
		"requests": []map[string]any{
			{"url": "https://example.com/one"},
			{"url": "https://example.com/two"},
			{"url": "file:///etc/passwd"},
		},
	})
	got, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Request 1", "## Request 2", "## Request 3",
		"body-for-/one", "body-for-/two"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in batch output:\n%s", want, got)
		}
	}
	// Order must be preserved despite concurrency.
	if strings.Index(got, "## Request 1") > strings.Index(got, "## Request 2") {
		t.Fatalf("batch results out of order:\n%s", got)
	}
	// The bad URL becomes a per-item error, not a whole-tool failure.
	if !strings.Contains(got, "Error fetching file:///etc/passwd") {
		t.Fatalf("expected per-item error for bad url:\n%s", got)
	}
}

func TestBatchWebFetchRequiresRequests(t *testing.T) {
	tool := &batchWebFetchTool{}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"requests":[]}`)); err == nil {
		t.Fatal("expected error when requests is empty")
	}
}

// writeNetrc points $NETRC at a temp file with the given content so web_fetch's
// netrc auth is hermetic and never touches the developer's real ~/.netrc.
func writeNetrc(t *testing.T, content string) {
	t.Helper()
	path := t.TempDir() + "/netrc"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETRC", path)
}

func TestWebFetchNetrcBearer(t *testing.T) {
	isolateDomains(t)
	writeNetrc(t, "machine example.com login u password NETRCTOK\n")
	var auth string
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		return stubResp(200, "text/plain", "ok"), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/secret"})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer NETRCTOK" {
		t.Fatalf("expected netrc bearer, got %q", auth)
	}
}

func TestWebFetchNetrcCallerHeaderWins(t *testing.T) {
	isolateDomains(t)
	writeNetrc(t, "machine example.com login u password NETRCTOK\n")
	var auth string
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		return stubResp(200, "text/plain", "ok"), nil
	})}}
	in, _ := json.Marshal(map[string]any{
		"url":     "https://example.com/secret",
		"headers": map[string]string{"Authorization": "token CALLER"},
	})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if auth != "token CALLER" {
		t.Fatalf("caller Authorization should win, got %q", auth)
	}
}

func TestWebFetchNetrcNoMatchNoAuth(t *testing.T) {
	isolateDomains(t)
	writeNetrc(t, "machine other.com login u password NETRCTOK\n")
	var auth string
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		return stubResp(200, "text/plain", "ok"), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/"})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Fatalf("expected no auth for unlisted host, got %q", auth)
	}
}

func TestWebFetchNetrcDisabled(t *testing.T) {
	isolateDomains(t)
	writeNetrc(t, "machine example.com login u password NETRCTOK\n")
	t.Setenv("MN_NETRC", "0")
	var auth string
	tool := &webFetchTool{client: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		return stubResp(200, "text/plain", "ok"), nil
	})}}
	in, _ := json.Marshal(map[string]any{"url": "https://example.com/"})
	if _, err := tool.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Fatalf("MN_NETRC=0 should disable netrc auth, got %q", auth)
	}
}

// TestAppleAuthWallHint removed with the apple_fetch/apple_search tools.
