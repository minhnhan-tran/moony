package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

func TestUsesOllama(t *testing.T) {
	if (&Client{cfg: &config.Config{LLMProvider: provider.Ollama}}).usesOllama() != true {
		t.Error("usesOllama should be true for the ollama provider")
	}
	// Ollama is now the only/default provider, so an empty provider normalizes
	// to ollama too.
	if (&Client{cfg: &config.Config{}}).usesOllama() != true {
		t.Error("usesOllama should be true for the default (empty) provider")
	}
}

func TestResolveOllamaModel(t *testing.T) {
	c := &Client{cfg: &config.Config{Model: "gemma4:31b"}}
	if got := c.resolveOllamaModel(context.Background()); got != "gemma4:31b" {
		t.Errorf("resolveOllamaModel = %q, want the session model", got)
	}
	// A per-call override wins.
	ctx := WithModel(context.Background(), "llama3:8b")
	if got := c.resolveOllamaModel(ctx); got != "llama3:8b" {
		t.Errorf("per-call override should win, got %q", got)
	}
	// A per-call override carrying a Floodgate id is ignored (Ollama would 404);
	// fall back to the session model. Guards against a fast-model pick leaking a
	// Floodgate id onto the Ollama backend.
	for _, fg := range []string{"anthropic.claude-sonnet-4-6", "gcp:gemini-3.5-flash", "aws:anthropic.claude"} {
		if got := c.resolveOllamaModel(WithModel(context.Background(), fg)); got != "gemma4:31b" {
			t.Errorf("resolveOllamaModel(override=%q) = %q, want fallback to session model", fg, got)
		}
	}
}

func TestOllamaBaseURL(t *testing.T) {
	if got := (&Client{cfg: &config.Config{}}).ollamaBaseURL(); got != "http://localhost:11434/v1" {
		t.Errorf("default ollamaBaseURL = %q", got)
	}
	c := &Client{cfg: &config.Config{OllamaBaseURL: "http://host:29248/v1/"}}
	if got := c.ollamaBaseURL(); got != "http://host:29248/v1" {
		t.Errorf("ollamaBaseURL should trim the trailing slash, got %q", got)
	}
}

// TestOllamaListModels stands up a fake Ollama /api/tags and checks the parse.
func TestOllamaListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"models":[{"name":"gemma4:31b","model":"gemma4:31b"},{"name":"llama3:8b","model":"llama3:8b"}]}`)
	}))
	defer srv.Close()

	c := &Client{cfg: &config.Config{LLMProvider: provider.Ollama, OllamaBaseURL: srv.URL + "/v1"}, http: srv.Client()}
	models, err := c.listOllamaModels(context.Background())
	if err != nil {
		t.Fatalf("listOllamaModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "gemma4:31b" || models[1].ID != "llama3:8b" {
		t.Fatalf("unexpected models: %+v", models)
	}
	// checkOllamaAuth reaches the same endpoint and should pass.
	if err := c.checkOllamaAuth(context.Background()); err != nil {
		t.Errorf("checkOllamaAuth should succeed against a reachable server: %v", err)
	}
}

func TestOllamaListModelsEmptyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[]}`)
	}))
	defer srv.Close()
	c := &Client{cfg: &config.Config{OllamaBaseURL: srv.URL + "/v1"}, http: srv.Client()}
	if _, err := c.listOllamaModels(context.Background()); err == nil {
		t.Error("an empty model list should be an error (nothing installed)")
	}
}

// TestStreamOllamaEndToEnd drives a full turn against a fake Ollama OpenAI-compat
// endpoint that streams text + a tool call, verifying the request shape and the
// assembled Response (the OpenAI machinery is reused, so this also covers wiring).
func TestStreamOllamaEndToEnd(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		// Stream: a text delta, then a tool call, then finish + usage.
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hi"}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":5}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := &Client{
		cfg:  &config.Config{LLMProvider: provider.Ollama, OllamaBaseURL: srv.URL + "/v1", Model: "gemma4:31b", MaxTokens: 256},
		http: srv.Client(),
	}
	var text strings.Builder
	resp, err := c.StreamMessages(context.Background(), "be terse",
		[]Message{{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "hello"}}}},
		[]Tool{{Name: "read_file", Description: "read a file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
		Handler{OnTextDelta: func(s string) { text.WriteString(s) }},
	)
	if err != nil {
		t.Fatalf("StreamMessages(ollama): %v", err)
	}
	// Request used the configured model and carried the tool.
	if gotBody["model"] != "gemma4:31b" {
		t.Errorf("request model = %v, want gemma4:31b", gotBody["model"])
	}
	if _, ok := gotBody["tools"]; !ok {
		t.Error("request should carry the tools array")
	}
	// Response: text + a tool_use block, stop reason mapped to tool_use.
	if text.String() != "Hi" {
		t.Errorf("streamed text = %q, want Hi", text.String())
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("stop reason = %q, want tool_use", resp.StopReason)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "read_file" || calls[0].ID != "call_1" {
		t.Fatalf("unexpected tool calls: %+v", calls)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v, want in:12 out:5", resp.Usage)
	}
}

func TestNormalizeOllamaBaseURL(t *testing.T) {
	cases := map[string]string{
		"http://host:29248":     "http://host:29248/v1",
		"http://host:29248/":    "http://host:29248/v1",
		"http://host:29248/v1":  "http://host:29248/v1",
		"http://host:29248/v1/": "http://host:29248/v1",
		"host:11434":            "http://host:11434/v1",
		"https://h/api/v1":      "https://h/api/v1",
		"":                      "",
	}
	for in, want := range cases {
		if got := config.NormalizeOllamaBaseURL(in); got != want {
			t.Errorf("NormalizeOllamaBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStreamOllamaSendsKeepAlive verifies the request carries Ollama's keep_alive
// extension alongside the OpenAI fields (keeps a large model resident between
// turns instead of reloading it each time), and always sets think:true so a
// thinking-capable model's reasoning is shown — independent of the reasoning level.
func TestStreamOllamaSendsKeepAlive(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	send := func() {
		gotBody = nil
		if _, err := c0(srv).StreamMessages(context.Background(), "", []Message{{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "hi"}}}}, nil, Handler{}); err != nil {
			t.Fatalf("StreamMessages: %v", err)
		}
	}
	// think:true is always sent (thinking display isn't tied to the reasoning level).
	send()
	if gotBody["keep_alive"] != "30m" {
		t.Errorf("request keep_alive = %v, want 30m", gotBody["keep_alive"])
	}
	if gotBody["think"] != true {
		t.Errorf("request think = %v, want true (always request thinking)", gotBody["think"])
	}
}

// c0 builds a fresh Ollama client pointed at srv (helper for the keep-alive test).
func c0(srv *httptest.Server) *Client {
	return &Client{cfg: &config.Config{LLMProvider: provider.Ollama, OllamaBaseURL: srv.URL + "/v1", Model: "gemma4:31b", MaxTokens: 64}, http: srv.Client()}
}

// TestStreamOllamaThinkingDeltas confirms reasoning deltas from the OpenAI-compat
// stream are surfaced via OnThinkingDelta and collected into a thinking block.
func TestStreamOllamaThinkingDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"reasoning":"Let me think. "}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"reasoning":"2+2 is 4."}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"4"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &Client{cfg: &config.Config{LLMProvider: provider.Ollama, OllamaBaseURL: srv.URL + "/v1", Model: "gemma4:31b", MaxTokens: 64, ReasoningLevel: "auto"}, http: srv.Client()}
	var think, text strings.Builder
	resp, err := c.StreamMessages(context.Background(), "", []Message{{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "2+2?"}}}}, nil,
		Handler{OnThinkingDelta: func(s string) { think.WriteString(s) }, OnTextDelta: func(s string) { text.WriteString(s) }})
	if err != nil {
		t.Fatalf("StreamMessages: %v", err)
	}
	if think.String() != "Let me think. 2+2 is 4." {
		t.Errorf("thinking = %q, want the joined reasoning deltas", think.String())
	}
	if text.String() != "4" {
		t.Errorf("text = %q, want 4", text.String())
	}
	// The assembled response carries a thinking block before the text.
	var sawThinking bool
	for _, b := range resp.Blocks {
		if b.Type == BlockThinking && b.Thinking != "" {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Errorf("response should include a thinking block: %+v", resp.Blocks)
	}
}

// TestCheckOllamaAuthWarns verifies the connectivity check flags a model that's
// loaded on CPU (size_vram=0 → unusably slow) or with a context window too small
// for the agent's prompt+tools (→ truncation drops thinking). A healthy GPU model
// with a roomy context passes.
func TestCheckOllamaAuthWarns(t *testing.T) {
	// CPU-loaded model → CPU warning.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"gemma4:31b","model":"gemma4:31b"}]}`)
		case "/api/ps":
			io.WriteString(w, `{"models":[{"name":"gemma4:31b","size":63538967616,"size_vram":0}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{cfg: &config.Config{OllamaBaseURL: srv.URL + "/v1"}, http: srv.Client()}
	if err := c.checkOllamaAuth(context.Background()); err == nil || !strings.Contains(err.Error(), "CPU") {
		t.Errorf("expected a CPU/no-GPU warning, got: %v", err)
	}

	// GPU model but tiny context window → context warning (this is the gemma case).
	srvCtx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"gemma4:latest","model":"gemma4:latest"}]}`)
		case "/api/ps":
			io.WriteString(w, `{"models":[{"name":"gemma4:latest","size":8000000000,"size_vram":10000000000,"context_length":8192}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvCtx.Close()
	cCtx := &Client{cfg: &config.Config{OllamaBaseURL: srvCtx.URL + "/v1"}, http: srvCtx.Client()}
	if err := cCtx.checkOllamaAuth(context.Background()); err == nil || !strings.Contains(err.Error(), "context window") {
		t.Errorf("expected a small-context warning, got: %v", err)
	}

	// A GPU-loaded model with a roomy context should not warn.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"llama3:8b","model":"llama3:8b"}]}`)
		case "/api/ps":
			io.WriteString(w, `{"models":[{"name":"llama3:8b","size":8000000000,"size_vram":8000000000,"context_length":32768}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv2.Close()
	c2 := &Client{cfg: &config.Config{OllamaBaseURL: srv2.URL + "/v1"}, http: srv2.Client()}
	if err := c2.checkOllamaAuth(context.Background()); err != nil {
		t.Errorf("a healthy GPU model with a roomy context should pass, got: %v", err)
	}
}

// TestPreTurnNotice verifies the pre-turn "model is loading" notice: it fires
// when the selected Ollama model isn't resident, stays quiet when it is, and is
// inert off the Ollama backend or when the server can't be reached.
func TestPreTurnNotice(t *testing.T) {
	// Server whose /api/ps reports gemma4:31b resident (loaded).
	loaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			io.WriteString(w, `{"models":[{"name":"gemma4:31b","model":"gemma4:31b","size":1,"size_vram":1}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer loaded.Close()

	// Resident model → no notice.
	c := &Client{cfg: &config.Config{LLMProvider: provider.Ollama, OllamaBaseURL: loaded.URL + "/v1", Model: "gemma4:31b"}, http: loaded.Client()}
	if got := c.PreTurnNotice(context.Background()); got != "" {
		t.Errorf("resident model should produce no notice, got %q", got)
	}
	// A different (not-resident) model → loading notice.
	c.cfg.Model = "qwen3.5:122b"
	if got := c.PreTurnNotice(context.Background()); !strings.Contains(got, "Loading") || !strings.Contains(got, "qwen3.5:122b") {
		t.Errorf("not-resident model should warn it's loading, got %q", got)
	}

	// Empty /api/ps (nothing loaded) → notice for any model.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[]}`)
	}))
	defer empty.Close()
	c2 := &Client{cfg: &config.Config{LLMProvider: provider.Ollama, OllamaBaseURL: empty.URL + "/v1", Model: "gemma4:31b"}, http: empty.Client()}
	if got := c2.PreTurnNotice(context.Background()); !strings.Contains(got, "Loading") {
		t.Errorf("nothing loaded should warn, got %q", got)
	}

	// Unreachable server → no notice (don't guess).
	cDown := &Client{cfg: &config.Config{LLMProvider: provider.Ollama, OllamaBaseURL: "http://127.0.0.1:1/v1", Model: "gemma4:31b"}, http: &http.Client{}}
	if got := cDown.PreTurnNotice(context.Background()); got != "" {
		t.Errorf("unreachable server should produce no notice, got %q", got)
	}
}

// TestOllamaModelResident covers the loaded-model matching, including the untagged
// case (a "gemma4" request matching a resident "gemma4:latest").
func TestOllamaModelResident(t *testing.T) {
	m := ollamaPSModel{Name: "gemma4:latest", Model: "gemma4:latest"}
	cases := []struct {
		want string
		ok   bool
	}{
		{"gemma4:latest", true},
		{"gemma4", true}, // untagged matches the tagged resident
		{"gemma4:31b", false},
		{"qwen3.5:122b", false},
	}
	for _, c := range cases {
		if got := ollamaModelResident(m, c.want); got != c.ok {
			t.Errorf("ollamaModelResident(%q) = %v, want %v", c.want, got, c.ok)
		}
	}
}

// TestModelServableBy: with Ollama as the sole provider, any non-empty model id
// is considered servable (the cross-provider compatibility check is retired).
func TestModelServableBy(t *testing.T) {
	cases := []struct {
		model, prov string
		ok          bool
	}{
		{"gemma4:31b", provider.Ollama, true},
		{"qwen3.5:122b", "", true},
		{"", provider.Ollama, false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := ModelServableBy(c.model, c.prov); got != c.ok {
			t.Errorf("ModelServableBy(%q, %q) = %v, want %v", c.model, c.prov, got, c.ok)
		}
	}
}
