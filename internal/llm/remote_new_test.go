package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// TestRemoteBackendForNewProviders checks the connection details for the
// providers added on top of the base OpenAI-compatible set.
func TestRemoteBackendForNewProviders(t *testing.T) {
	cases := []struct {
		id        string
		cfg       config.Config
		wantBase  string
		wantKey   string
		wantKeyHd string // "" => Bearer
		wantEnv   string
	}{
		{
			id:       provider.Cerebras,
			cfg:      config.Config{CerebrasAPIKey: "csk", CerebrasBaseURL: "https://api.cerebras.ai/v1"},
			wantBase: "https://api.cerebras.ai/v1",
			wantKey:  "csk",
			wantEnv:  "MOONY_CEREBRAS_API_KEY",
		},
		{
			id:       provider.Perplexity,
			cfg:      config.Config{PerplexityAPIKey: "pk", PerplexityBaseURL: "https://api.perplexity.ai"},
			wantBase: "https://api.perplexity.ai",
			wantKey:  "pk",
			wantEnv:  "MOONY_PERPLEXITY_API_KEY",
		},
		{
			id:       provider.Cohere,
			cfg:      config.Config{CohereAPIKey: "ck", CohereBaseURL: "https://api.cohere.ai/compatibility/v1"},
			wantBase: "https://api.cohere.ai/compatibility/v1",
			wantKey:  "ck",
			wantEnv:  "MOONY_COHERE_API_KEY",
		},
		{
			id:        provider.Azure,
			cfg:       config.Config{AzureAPIKey: "ak", AzureBaseURL: "https://res.openai.azure.com", AzureAPIVersion: "2024-10-21"},
			wantBase:  "https://res.openai.azure.com",
			wantKey:   "ak",
			wantKeyHd: "api-key",
			wantEnv:   "MOONY_AZURE_API_KEY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			cfg := tc.cfg
			c := &Client{cfg: &cfg}
			rb := c.remoteBackendFor(tc.id)
			if rb.baseURL != tc.wantBase {
				t.Errorf("baseURL = %q, want %q", rb.baseURL, tc.wantBase)
			}
			if rb.apiKey != tc.wantKey {
				t.Errorf("apiKey = %q, want %q", rb.apiKey, tc.wantKey)
			}
			if rb.keyHeader != tc.wantKeyHd {
				t.Errorf("keyHeader = %q, want %q", rb.keyHeader, tc.wantKeyHd)
			}
			if rb.keyEnv != tc.wantEnv {
				t.Errorf("keyEnv = %q, want %q", rb.keyEnv, tc.wantEnv)
			}
		})
	}
}

// TestAzureURLs verifies Azure's deployment-scoped paths and api-version query.
func TestAzureURLs(t *testing.T) {
	cfg := config.Config{AzureAPIKey: "ak", AzureBaseURL: "https://res.openai.azure.com", AzureAPIVersion: "2024-10-21"}
	rb := (&Client{cfg: &cfg}).remoteBackendFor(provider.Azure)

	chat := rb.chatURL("my-deploy")
	if !strings.Contains(chat, "/openai/deployments/my-deploy/chat/completions") {
		t.Errorf("chat URL missing deployment path: %s", chat)
	}
	if !strings.Contains(chat, "api-version=2024-10-21") {
		t.Errorf("chat URL missing api-version: %s", chat)
	}
	models := rb.modelsURL()
	if !strings.Contains(models, "/openai/models") || !strings.Contains(models, "api-version=2024-10-21") {
		t.Errorf("models URL wrong: %s", models)
	}
}

// TestNonAzureURLs verifies the default (non-deployment) paths with no query.
func TestNonAzureURLs(t *testing.T) {
	cfg := config.Config{CerebrasAPIKey: "k", CerebrasBaseURL: "https://api.cerebras.ai/v1"}
	rb := (&Client{cfg: &cfg}).remoteBackendFor(provider.Cerebras)
	if got := rb.chatURL("llama-3.3-70b"); got != "https://api.cerebras.ai/v1/chat/completions" {
		t.Errorf("chat URL = %q", got)
	}
	if got := rb.modelsURL(); got != "https://api.cerebras.ai/v1/models" {
		t.Errorf("models URL = %q", got)
	}
}

// TestVertexBaseURL covers the derived and overridden Vertex endpoints.
func TestVertexBaseURL(t *testing.T) {
	got := vertexBaseURL(&config.Config{VertexProject: "proj", VertexLocation: "us-central1"})
	want := "https://us-central1-aiplatform.googleapis.com/v1/projects/proj/locations/us-central1/endpoints/openapi"
	if got != want {
		t.Errorf("regional: got %q, want %q", got, want)
	}
	got = vertexBaseURL(&config.Config{VertexProject: "proj", VertexLocation: "global"})
	if strings.Contains(got, "global-aiplatform") || !strings.Contains(got, "//aiplatform.googleapis.com/") {
		t.Errorf("global host wrong: %q", got)
	}
	if got := vertexBaseURL(&config.Config{VertexBaseURL: "https://custom/v1/"}); got != "https://custom/v1" {
		t.Errorf("override not honored: %q", got)
	}
}

// TestVertexSkipsModelsList: Vertex has no usable GET /models, so the backend
// is flagged to skip it.
func TestVertexSkipsModelsList(t *testing.T) {
	cfg := config.Config{VertexAPIKey: "tok", VertexProject: "p", VertexLocation: "us-central1"}
	rb := (&Client{cfg: &cfg}).remoteBackendFor(provider.Vertex)
	if !rb.skipModelsList {
		t.Error("Vertex backend should set skipModelsList")
	}
}

// TestBedrockBodyShape checks the Bedrock request body drops model/stream and
// injects anthropic_version.
func TestBedrockBodyShape(t *testing.T) {
	body := &request{Model: "claude", MaxTokens: 1024, System: "sys", Stream: true}
	raw, err := bedrockBody(body)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["model"]; ok {
		t.Error(`"model" must be omitted for Bedrock`)
	}
	if _, ok := m["stream"]; ok {
		t.Error(`"stream" must be omitted for Bedrock`)
	}
	var ver string
	if err := json.Unmarshal(m["anthropic_version"], &ver); err != nil || ver != bedrockAnthropicVersion {
		t.Errorf(`anthropic_version = %q (err %v), want %q`, ver, err, bedrockAnthropicVersion)
	}
	if _, ok := m["max_tokens"]; !ok {
		t.Error("max_tokens should be preserved")
	}
}

// TestUsesBedrock routes only the bedrock provider to the native backend.
func TestUsesBedrock(t *testing.T) {
	if !(&Client{cfg: &config.Config{LLMProvider: provider.Bedrock}}).usesBedrock() {
		t.Error("bedrock provider should use the Bedrock backend")
	}
	if (&Client{cfg: &config.Config{LLMProvider: provider.Anthropic}}).usesBedrock() {
		t.Error("anthropic must not use the Bedrock backend")
	}
	// Bedrock must not be misrouted to the remote OpenAI or Anthropic paths.
	if isRemoteProvider(provider.Bedrock) {
		t.Error("bedrock must not be an OpenAI-compatible remote provider")
	}
	if provider.UsesAnthropicWire(provider.Bedrock) {
		t.Error("bedrock must not use the Anthropic HTTP wire route")
	}
}

// TestDeepInfraBackend checks DeepInfra's base URL, key and env mapping.
func TestDeepInfraBackend(t *testing.T) {
	cfg := config.Config{DeepInfraAPIKey: "dk", DeepInfraBaseURL: "https://api.deepinfra.com/v1/openai"}
	rb := (&Client{cfg: &cfg}).remoteBackendFor(provider.DeepInfra)
	if rb.baseURL != "https://api.deepinfra.com/v1/openai" {
		t.Errorf("baseURL = %q", rb.baseURL)
	}
	if rb.apiKey != "dk" {
		t.Errorf("apiKey = %q", rb.apiKey)
	}
	if rb.keyEnv != "MOONY_DEEPINFRA_API_KEY" {
		t.Errorf("keyEnv = %q", rb.keyEnv)
	}
	if rb.noKeyOK {
		t.Error("DeepInfra requires a key; noKeyOK should be false")
	}
	if got := rb.chatURL("meta-llama/Llama-3.3-70B-Instruct"); got != "https://api.deepinfra.com/v1/openai/chat/completions" {
		t.Errorf("chat URL = %q", got)
	}
}

// TestLocalKeylessBackends: LM Studio and llama.cpp are keyless local servers.
func TestLocalKeylessBackends(t *testing.T) {
	cases := []struct {
		id       string
		wantBase string
	}{
		{provider.LMStudio, "http://localhost:1234/v1"},
		{provider.LlamaCpp, "http://localhost:8080/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			c := &Client{cfg: &config.Config{}, http: &http.Client{Transport: errRoundTripper{}}}
			rb := c.remoteBackendFor(tc.id)
			if rb.baseURL != tc.wantBase {
				t.Errorf("baseURL = %q, want %q", rb.baseURL, tc.wantBase)
			}
			if !rb.noKeyOK {
				t.Error("local server should set noKeyOK")
			}
			if rb.apiKey != "" {
				t.Errorf("expected no apiKey, got %q", rb.apiKey)
			}
			if !isRemoteProvider(tc.id) {
				t.Error("should be an OpenAI-compatible remote provider")
			}
			if provider.UsesAnthropicWire(tc.id) {
				t.Error("must not use the Anthropic wire route")
			}
			// A keyless local server must NOT be rejected for a missing key: it
			// should get as far as the (here stubbed) network call instead.
			err := c.checkRemoteAuth(context.Background(), rb)
			if err != nil && strings.Contains(err.Error(), "API key not set") {
				t.Errorf("keyless server rejected for missing key: %v", err)
			}
		})
	}
}

// errRoundTripper fails every request, so auth checks reach the transport
// (proving the key gate passed) without needing a live server.
type errRoundTripper struct{}

func (errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errStubTransport
}

var errStubTransport = errorString("stub transport: no network in test")

type errorString string

func (e errorString) Error() string { return string(e) }

// TestSetAuthKeyless: no Authorization header is sent when there's no key.
func TestSetAuthKeyless(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://localhost:1234/v1/chat/completions", nil)
	remoteBackend{id: provider.LlamaCpp, noKeyOK: true}.setAuth(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("keyless request should not set Authorization, got %q", got)
	}
	// LM Studio with an optional token should still send it.
	req2, _ := http.NewRequest("POST", "http://localhost:1234/v1/chat/completions", nil)
	remoteBackend{id: provider.LMStudio, apiKey: "tok", noKeyOK: true}.setAuth(req2)
	if got := req2.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("LM Studio token not sent, got %q", got)
	}
}
