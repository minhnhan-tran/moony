// Package provider enumerates the backend model providers the agent can use.
//
// Ollama (local, no key) is the default; the rest are public hosted APIs the
// user opts into with an API key. Every hosted provider speaks either the
// OpenAI-compatible Chat Completions API or (Anthropic) its own native
// Messages API — see internal/llm.
package provider

import (
	"fmt"
	"strings"
)

// Provider describes one selectable backend model provider.
type Provider struct {
	ID    string // stable id used in the /provider command (lower-case)
	Label string // human display name
	Desc  string // one-line description
}

// Provider ids. These are the values the /provider command accepts.
const (
	Ollama      = "ollama"
	OpenAI      = "openai"
	Anthropic   = "anthropic"
	OpenRouter  = "openrouter"
	Gemini      = "gemini"
	DeepSeek    = "deepseek"
	Groq        = "groq"
	XAI         = "xai"
	Mistral     = "mistral"
	Together    = "together"
	Fireworks   = "fireworks"
	Moonshot    = "moonshot"
	ZAI         = "zai"
	DashScope   = "dashscope"
	NVIDIA      = "nvidia"
	HuggingFace = "huggingface"
	Venice      = "venice"
	OmniRoute   = "omniroute"
	Cerebras    = "cerebras"
	Perplexity  = "perplexity"
	Cohere      = "cohere"
	DeepInfra   = "deepinfra"
	Meta        = "meta"

	// LMStudio and LlamaCpp are LOCAL OpenAI-compatible servers (like Ollama):
	// no API key, no cost. LM Studio's server defaults to :1234 and llama.cpp's
	// (llama-server) to :8080. Models are whatever you've loaded, so neither has
	// a fixed default model — pick one with /model (or /models to list).
	LMStudio = "lmstudio"
	LlamaCpp = "llamacpp"

	// Azure is Azure OpenAI Service: OpenAI models on a per-resource host with
	// deployment-based paths, an api-key header (not Bearer), and an
	// api-version query param — see internal/llm/remote_openai.go.
	Azure = "azure"

	// Vertex is Google Vertex AI's OpenAI-compatible endpoint. Auth is a
	// short-lived GCP OAuth access token (e.g. `gcloud auth print-access-token`)
	// and the base URL embeds the project id and region.
	Vertex = "vertex"

	// Bedrock is AWS Bedrock. It has no OpenAI-compatible endpoint: requests are
	// SigV4-signed and streamed over the AWS event-stream protocol via the AWS
	// SDK, reusing the Anthropic wire types — see internal/llm/bedrock.go.
	Bedrock = "bedrock"

	// Copilot is GitHub Copilot, driven through the github/copilot-sdk Go SDK
	// (it spawns and talks to the local `copilot` CLI over JSON-RPC) rather than
	// a plain HTTP API. It runs autonomously with its own built-in tools — see
	// internal/llm/copilot.go.
	Copilot = "copilot"

	// SQEAnthropic is the public Anthropic backend reached through the SQE LAN
	// gateway (a Bedrock-style proxy). Same wire protocol and models as Anthropic;
	// only the default base URL (and, optionally, the API key) differ.
	SQEAnthropic = "sqe-anthropic"

	// Default is the provider used unless the user selects another. Ollama
	// needs no API key and runs locally, so it's the safest default.
	Default = Ollama
)

// Per-provider default model, used when a provider has just been selected and
// the current session model doesn't belong to it.
const (
	OllamaDefaultModel      = "llama3.1:8b"
	OpenAIDefaultModel      = "gpt-4o"
	AnthropicDefaultModel   = "claude-sonnet-4-5"
	OpenRouterDefaultModel  = "openai/gpt-4o-mini"
	GeminiDefaultModel      = "gemini-2.5-pro"
	DeepSeekDefaultModel    = "deepseek-chat"
	GroqDefaultModel        = "llama-3.3-70b-versatile"
	XAIDefaultModel         = "grok-4"
	MistralDefaultModel     = "mistral-large-latest"
	TogetherDefaultModel    = "meta-llama/Llama-3.3-70B-Instruct-Turbo"
	FireworksDefaultModel   = "accounts/fireworks/models/llama-v3p3-70b-instruct"
	MoonshotDefaultModel    = "kimi-k2-0905-preview"
	ZAIDefaultModel         = "glm-4.6"
	DashScopeDefaultModel   = "qwen-plus"
	NVIDIADefaultModel      = "nvidia/llama-3.1-nemotron-70b-instruct"
	HuggingFaceDefaultModel = "meta-llama/Llama-3.3-70B-Instruct"
	VeniceDefaultModel      = "llama-3.3-70b"
	// OmniRouteDefaultModel is OmniRoute's zero-config "smart router" id — it picks
	// a backend automatically, so it's the safest starting model.
	OmniRouteDefaultModel  = "auto"
	CerebrasDefaultModel   = "llama-3.3-70b"
	PerplexityDefaultModel = "sonar-pro"
	CohereDefaultModel     = "command-a-03-2025"
	DeepInfraDefaultModel  = "meta-llama/Llama-3.3-70B-Instruct"
	// MetaDefaultModel is the standard Muse Spark tier. The "-contributor" tier
	// is cheaper but rate-limited by a rolling token window and opts prompts into
	// training, so it is never the default -- pick it explicitly with /model.
	MetaDefaultModel = "muse-spark-1.2"
	// LMStudioDefaultModel and LlamaCppDefaultModel are empty: these local servers
	// serve whatever model you've loaded, so there's no safe universal default —
	// the loaded model (or /model) decides.
	LMStudioDefaultModel = ""
	LlamaCppDefaultModel = ""
	// AzureDefaultModel is empty: Azure addresses models by the *deployment* name
	// you created in your resource, which varies per account, so there's no safe
	// universal default — pick yours with /model.
	AzureDefaultModel  = ""
	VertexDefaultModel = "google/gemini-2.5-pro"
	// BedrockDefaultModel is a cross-region Claude inference-profile id, which is
	// what on-demand Bedrock invocation expects.
	BedrockDefaultModel = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	// CopilotDefaultModel is empty so the Copilot CLI uses its own configured
	// default model; users can still pick a specific one with /model.
	CopilotDefaultModel = ""
)

// list is the fixed registry, in the order shown by /provider.
var list = []Provider{
	{ID: Ollama, Label: "Ollama", Desc: "local (or remote) Ollama server — no API key, no cost"},
	{ID: OpenAI, Label: "OpenAI", Desc: "api.openai.com — needs MN_OPENAI_API_KEY"},
	{ID: Anthropic, Label: "Anthropic", Desc: "api.anthropic.com (Claude) — needs MN_ANTHROPIC_API_KEY"},
	{ID: SQEAnthropic, Label: "SQE Anthropic", Desc: "Claude via the SQE LAN gateway (default http://192.168.33.158:8787) — needs MN_SQE_ANTHROPIC_API_KEY (falls back to MN_ANTHROPIC_API_KEY)"},
	{ID: OpenRouter, Label: "OpenRouter", Desc: "openrouter.ai — one key, hundreds of models — needs MN_OPENROUTER_API_KEY"},
	{ID: Gemini, Label: "Gemini", Desc: "Google's OpenAI-compatible endpoint — needs MN_GEMINI_API_KEY"},
	{ID: DeepSeek, Label: "DeepSeek", Desc: "api.deepseek.com — strong, cheap coding models — needs MN_DEEPSEEK_API_KEY"},
	{ID: Groq, Label: "Groq", Desc: "api.groq.com — very fast inference for open models — needs MN_GROQ_API_KEY"},
	{ID: XAI, Label: "xAI", Desc: "api.x.ai (Grok) — needs MN_XAI_API_KEY"},
	{ID: Mistral, Label: "Mistral", Desc: "api.mistral.ai — needs MN_MISTRAL_API_KEY"},
	{ID: Together, Label: "Together.ai", Desc: "api.together.xyz — hosted open models — needs MN_TOGETHER_API_KEY"},
	{ID: Fireworks, Label: "Fireworks.ai", Desc: "api.fireworks.ai — hosted open models — needs MN_FIREWORKS_API_KEY"},
	{ID: Moonshot, Label: "Moonshot", Desc: "api.moonshot.ai (Kimi) — needs MN_MOONSHOT_API_KEY"},
	{ID: ZAI, Label: "Z.AI", Desc: "api.z.ai (Zhipu GLM) — needs MN_ZAI_API_KEY"},
	{ID: DashScope, Label: "DashScope", Desc: "Alibaba DashScope (Qwen), OpenAI-compatible — needs MN_DASHSCOPE_API_KEY"},
	{ID: NVIDIA, Label: "NVIDIA NIM", Desc: "integrate.api.nvidia.com — hosted open models — needs MN_NVIDIA_API_KEY"},
	{ID: HuggingFace, Label: "Hugging Face", Desc: "router.huggingface.co inference router — needs MN_HUGGINGFACE_API_KEY"},
	{ID: Venice, Label: "Venice", Desc: "api.venice.ai — privacy-focused open models — needs MN_VENICE_API_KEY"},
	{ID: OmniRoute, Label: "OmniRoute", Desc: "local OmniRoute gateway (default http://localhost:20128/v1) — needs MN_OMNIROUTE_API_KEY; use prefix/model ids or auto"},
	{ID: Cerebras, Label: "Cerebras", Desc: "api.cerebras.ai — extremely fast inference for open models — needs MN_CEREBRAS_API_KEY"},
	{ID: Perplexity, Label: "Perplexity", Desc: "api.perplexity.ai (Sonar) — web-grounded answers with citations — needs MN_PERPLEXITY_API_KEY"},
	{ID: Cohere, Label: "Cohere", Desc: "api.cohere.ai (Command, OpenAI-compatible) — strong RAG/tool-use — needs MN_COHERE_API_KEY"},
	{ID: DeepInfra, Label: "DeepInfra", Desc: "api.deepinfra.com — large catalog of hosted open models, cheap — needs MN_DEEPINFRA_API_KEY"},
	{ID: Meta, Label: "Meta Model API", Desc: "api.meta.ai — Meta's Muse Spark coding models (OpenAI-compatible) — needs MN_META_API_KEY"},
	{ID: LMStudio, Label: "LM Studio", Desc: "local LM Studio server (default http://localhost:1234/v1) — no API key; serves the model you've loaded"},
	{ID: LlamaCpp, Label: "llama.cpp", Desc: "local llama.cpp server / llama-server (default http://localhost:8080/v1) — no API key; serves the model you've loaded"},
	{ID: Azure, Label: "Azure OpenAI", Desc: "Azure OpenAI Service — OpenAI models on your resource — needs MN_AZURE_API_KEY + MN_AZURE_BASE_URL (your endpoint); /model = deployment name"},
	{ID: Vertex, Label: "Vertex AI", Desc: "Google Vertex AI (OpenAI-compatible) — needs MN_VERTEX_API_KEY (a GCP access token) + MN_VERTEX_PROJECT/MN_VERTEX_LOCATION"},
	{ID: Bedrock, Label: "AWS Bedrock", Desc: "AWS Bedrock (Claude et al., SigV4-signed) — needs AWS credentials (MN_AWS_* / standard AWS env) + MN_AWS_REGION"},
	{ID: Copilot, Label: "GitHub Copilot", Desc: "GitHub Copilot via the copilot CLI (autonomous, its own tools) — needs the `copilot` CLI installed and a GitHub token (MN_COPILOT_TOKEN / GH_TOKEN)"},
}

// List returns a copy of the provider registry, in display order.
func List() []Provider {
	out := make([]Provider, len(list))
	copy(out, list)
	return out
}

// Lookup finds a provider by id (case-insensitive, trimmed).
func Lookup(id string) (Provider, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Label returns the display name for a provider id, or the id itself if
// unknown.
func Label(id string) string {
	if p, ok := Lookup(id); ok {
		return p.Label
	}
	return id
}

// Normalize returns a known provider id for s, defaulting to Default when s is
// empty or unrecognized.
func Normalize(s string) string {
	if p, ok := Lookup(s); ok {
		return p.ID
	}
	return Default
}

// UsesAnthropicWire reports whether a provider speaks Anthropic's native
// Messages API. Both the public Anthropic backend and SQE Anthropic (the same
// backend reached through the SQE LAN gateway) use it — everything else speaks
// the OpenAI-compatible Chat Completions API.
func UsesAnthropicWire(id string) bool {
	switch Normalize(id) {
	case Anthropic, SQEAnthropic:
		return true
	}
	return false
}

// UsesCopilotSDK reports whether a provider is driven through the GitHub
// Copilot SDK/CLI rather than a direct HTTP API. Such providers run
// autonomously (their own agent loop + tools), so the agent treats a turn as a
// live SDK session instead of a stateless request. See internal/llm/copilot.go.
func UsesCopilotSDK(id string) bool {
	return Normalize(id) == Copilot
}

// UsesBedrockRuntime reports whether a provider is served through AWS Bedrock,
// which is SigV4-signed and streamed over the AWS event-stream protocol via the
// AWS SDK (reusing the Anthropic wire types) rather than a plain HTTP API — see
// internal/llm/bedrock.go.
func UsesBedrockRuntime(id string) bool {
	return Normalize(id) == Bedrock
}

// defaultModels maps each provider id to its starting model. Kept as a map
// (rather than a switch) so adding a provider only means adding one entry here
// and one to list above.
var defaultModels = map[string]string{
	Ollama:       OllamaDefaultModel,
	OpenAI:       OpenAIDefaultModel,
	Anthropic:    AnthropicDefaultModel,
	SQEAnthropic: AnthropicDefaultModel, // same Claude models as public Anthropic
	OpenRouter:   OpenRouterDefaultModel,
	Gemini:       GeminiDefaultModel,
	DeepSeek:     DeepSeekDefaultModel,
	Groq:         GroqDefaultModel,
	XAI:          XAIDefaultModel,
	Mistral:      MistralDefaultModel,
	Together:     TogetherDefaultModel,
	Fireworks:    FireworksDefaultModel,
	Moonshot:     MoonshotDefaultModel,
	ZAI:          ZAIDefaultModel,
	DashScope:    DashScopeDefaultModel,
	NVIDIA:       NVIDIADefaultModel,
	HuggingFace:  HuggingFaceDefaultModel,
	Venice:       VeniceDefaultModel,
	OmniRoute:    OmniRouteDefaultModel,
	Cerebras:     CerebrasDefaultModel,
	Perplexity:   PerplexityDefaultModel,
	Cohere:       CohereDefaultModel,
	DeepInfra:    DeepInfraDefaultModel,
	Meta:         MetaDefaultModel,
	LMStudio:     LMStudioDefaultModel,
	LlamaCpp:     LlamaCppDefaultModel,
	Azure:        AzureDefaultModel,
	Vertex:       VertexDefaultModel,
	Bedrock:      BedrockDefaultModel,
	Copilot:      CopilotDefaultModel,
}

// DefaultModelFor returns the model a provider starts with when freshly
// selected (and the session model doesn't already belong to it).
func DefaultModelFor(id string) string {
	if m, ok := defaultModels[Normalize(id)]; ok {
		return m
	}
	return OllamaDefaultModel
}

// ListText renders the registry as a numbered, human-readable list for the
// /provider command, marking the active one.
func ListText(active string) string {
	active = Normalize(active)
	var b strings.Builder
	b.WriteString("Model providers:\n")
	for _, p := range list {
		marker := "  "
		if p.ID == active {
			marker = "* "
		}
		fmt.Fprintf(&b, "%s%-10s %s\n", marker, p.ID, p.Desc)
	}
	b.WriteString("\n(* = active. Select with /provider <name>; ollama is the default.)")
	return b.String()
}
