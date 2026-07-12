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
	OmniRouteDefaultModel = "auto"
)

// list is the fixed registry, in the order shown by /provider.
var list = []Provider{
	{ID: Ollama, Label: "Ollama", Desc: "local (or remote) Ollama server — no API key, no cost"},
	{ID: OpenAI, Label: "OpenAI", Desc: "api.openai.com — needs MOONY_OPENAI_API_KEY"},
	{ID: Anthropic, Label: "Anthropic", Desc: "api.anthropic.com (Claude) — needs MOONY_ANTHROPIC_API_KEY"},
	{ID: SQEAnthropic, Label: "SQE Anthropic", Desc: "Claude via the SQE LAN gateway (default http://192.168.33.158:8787) — needs MOONY_SQE_ANTHROPIC_API_KEY (falls back to MOONY_ANTHROPIC_API_KEY)"},
	{ID: OpenRouter, Label: "OpenRouter", Desc: "openrouter.ai — one key, hundreds of models — needs MOONY_OPENROUTER_API_KEY"},
	{ID: Gemini, Label: "Gemini", Desc: "Google's OpenAI-compatible endpoint — needs MOONY_GEMINI_API_KEY"},
	{ID: DeepSeek, Label: "DeepSeek", Desc: "api.deepseek.com — strong, cheap coding models — needs MOONY_DEEPSEEK_API_KEY"},
	{ID: Groq, Label: "Groq", Desc: "api.groq.com — very fast inference for open models — needs MOONY_GROQ_API_KEY"},
	{ID: XAI, Label: "xAI", Desc: "api.x.ai (Grok) — needs MOONY_XAI_API_KEY"},
	{ID: Mistral, Label: "Mistral", Desc: "api.mistral.ai — needs MOONY_MISTRAL_API_KEY"},
	{ID: Together, Label: "Together.ai", Desc: "api.together.xyz — hosted open models — needs MOONY_TOGETHER_API_KEY"},
	{ID: Fireworks, Label: "Fireworks.ai", Desc: "api.fireworks.ai — hosted open models — needs MOONY_FIREWORKS_API_KEY"},
	{ID: Moonshot, Label: "Moonshot", Desc: "api.moonshot.ai (Kimi) — needs MOONY_MOONSHOT_API_KEY"},
	{ID: ZAI, Label: "Z.AI", Desc: "api.z.ai (Zhipu GLM) — needs MOONY_ZAI_API_KEY"},
	{ID: DashScope, Label: "DashScope", Desc: "Alibaba DashScope (Qwen), OpenAI-compatible — needs MOONY_DASHSCOPE_API_KEY"},
	{ID: NVIDIA, Label: "NVIDIA NIM", Desc: "integrate.api.nvidia.com — hosted open models — needs MOONY_NVIDIA_API_KEY"},
	{ID: HuggingFace, Label: "Hugging Face", Desc: "router.huggingface.co inference router — needs MOONY_HUGGINGFACE_API_KEY"},
	{ID: Venice, Label: "Venice", Desc: "api.venice.ai — privacy-focused open models — needs MOONY_VENICE_API_KEY"},
	{ID: OmniRoute, Label: "OmniRoute", Desc: "local OmniRoute gateway (default http://localhost:20128/v1) — needs MOONY_OMNIROUTE_API_KEY; use prefix/model ids or auto"},
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
