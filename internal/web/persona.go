package web

// Persona panel — view/edit the connected terminal's runtime env/config from the
// web UI's user menu. GET returns the current values (secrets masked to a last-4
// hint — the full token never leaves the server); POST applies edits to the live
// config (immediate effect, since the LLM client reads these per-request via its
// shared *config.Config pointer). Most edits are runtime-only and revert to the
// deployed env on restart; the exceptions are the savedPrefs subset (auto-plan,
// fast model, …) written to config.json and the provider credentials (API keys +
// base-URL overrides) written to the auto-loaded ~/.config/moony/.env, both of
// which persist. Mirrors the /provider + /model pattern of mutating the session
// config and s.cfg together.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
)

// tokenEnvFieldPrefix namespaces the dynamically-discovered MCP/tool credential
// fields in the Persona panel so their keys don't collide with the typed fields.
const tokenEnvFieldPrefix = "envToken:"

// tokenVarRe matches env-var names that look like a personal credential, so the
// panel only surfaces secrets and not arbitrary config vars (NODE_ENV, etc.).
var tokenVarRe = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|API[_-]?KEY|(^|_)KEY$|(^|_)PAT$)`)

func looksLikeTokenVar(name string) bool { return tokenVarRe.MatchString(name) }

// personaField describes one editable knob for the panel UI.
type personaField struct {
	Key     string                `json:"key"`
	Label   string                `json:"label"`
	Type    string                `json:"type"`            // "text" | "secret" | "bool" | "select"
	Value   any                   `json:"value,omitempty"` // text string / bool; omitted for secrets
	Set     bool                  `json:"set,omitempty"`   // secret: whether a value is currently present
	Hint    string                `json:"hint,omitempty"`  // secret: last-4 chars, for recognition only
	Desc    string                `json:"desc,omitempty"`
	Options []personaSelectOption `json:"options,omitempty"` // select: the choices

	Group       string `json:"group,omitempty"`       // provider label; fields sharing a group render as one card
	Placeholder string `json:"placeholder,omitempty"` // text: shown greyed when empty (e.g. the default base URL)
}

// personaSelectOption is one entry in a "select" field's option list.
type personaSelectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// personaEdit is the POST body — pointers so an absent field is left unchanged,
// and an empty secret string means "keep the existing token".
type personaEdit struct {
	OllamaHost    *string `json:"ollamaHost"`
	AutoDelegate  *bool   `json:"autoDelegate"`
	AutoPlan      *bool   `json:"autoPlan"`
	CheapReads    *bool   `json:"cheapReads"`
	SelfReview    *bool   `json:"selfReview"`
	AdaptiveModel *bool   `json:"adaptiveModel"`
	MaxAgentSteps *string `json:"maxAgentSteps"`
	AutoContinue  *string `json:"autoContinue"`
	SqeFastModel  *string `json:"sqeFastModel"`
	SqeAuxModel   *string `json:"sqeAuxModel"`
	// Session-history encryption at rest (read by session.NewStore from the env).
	// These are runtime env overrides and take effect on the NEXT restart, since
	// the store resolves its key once at open.
	SessionEncrypt    *string `json:"sqeSessionEncrypt"`
	SessionKey        *string `json:"sqeSessionKey"`
	SessionKeyCmd     *string `json:"sqeSessionKeyCmd"`
	SessionKeyFile    *string `json:"sqeSessionKeyFile"`
	SessionPassphrase *string `json:"sqeSessionPassphrase"`
	// Provider API keys (MOONY_*_API_KEY) + the optional Ollama bearer token.
	// Secrets: an empty string means "keep the existing key". JSON tags match the
	// providerCreds table below (the single source of truth wiring these to their
	// env var and config field). Applied live and re-read per request via s.cfg.
	OpenAIAPIKey       *string `json:"openaiApiKey"`
	AnthropicAPIKey    *string `json:"anthropicApiKey"`
	SQEAnthropicAPIKey *string `json:"sqeAnthropicApiKey"`
	OpenRouterAPIKey   *string `json:"openrouterApiKey"`
	GeminiAPIKey       *string `json:"geminiApiKey"`
	DeepSeekAPIKey     *string `json:"deepseekApiKey"`
	GroqAPIKey         *string `json:"groqApiKey"`
	XAIAPIKey          *string `json:"xaiApiKey"`
	MistralAPIKey      *string `json:"mistralApiKey"`
	TogetherAPIKey     *string `json:"togetherApiKey"`
	FireworksAPIKey    *string `json:"fireworksApiKey"`
	MoonshotAPIKey     *string `json:"moonshotApiKey"`
	ZAIAPIKey          *string `json:"zaiApiKey"`
	DashScopeAPIKey    *string `json:"dashscopeApiKey"`
	NvidiaAPIKey       *string `json:"nvidiaApiKey"`
	HuggingFaceAPIKey  *string `json:"huggingfaceApiKey"`
	VeniceAPIKey       *string `json:"veniceApiKey"`
	OmniRouteAPIKey    *string `json:"omnirouteApiKey"`
	OllamaToken        *string `json:"ollamaToken"`
	// Provider base-URL overrides (MOONY_*_BASE_URL). Plain text: empty clears the
	// override so the built-in default is used. Ollama's host is the top-level
	// "ollamaHost" field, so it has no entry here.
	OpenAIBaseURL       *string `json:"openaiBaseUrl"`
	AnthropicBaseURL    *string `json:"anthropicBaseUrl"`
	SQEAnthropicBaseURL *string `json:"sqeAnthropicBaseUrl"`
	OpenRouterBaseURL   *string `json:"openrouterBaseUrl"`
	GeminiBaseURL       *string `json:"geminiBaseUrl"`
	DeepSeekBaseURL     *string `json:"deepseekBaseUrl"`
	GroqBaseURL         *string `json:"groqBaseUrl"`
	XAIBaseURL          *string `json:"xaiBaseUrl"`
	MistralBaseURL      *string `json:"mistralBaseUrl"`
	TogetherBaseURL     *string `json:"togetherBaseUrl"`
	FireworksBaseURL    *string `json:"fireworksBaseUrl"`
	MoonshotBaseURL     *string `json:"moonshotBaseUrl"`
	ZAIBaseURL          *string `json:"zaiBaseUrl"`
	DashScopeBaseURL    *string `json:"dashscopeBaseUrl"`
	NvidiaBaseURL       *string `json:"nvidiaBaseUrl"`
	HuggingFaceBaseURL  *string `json:"huggingfaceBaseUrl"`
	VeniceBaseURL       *string `json:"veniceBaseUrl"`
	OmniRouteBaseURL    *string `json:"omnirouteBaseUrl"`
}

// providerCred ties one provider's Persona fields (API key + optional base-URL
// override) to their env vars and the config struct fields they feed. One row
// per provider keeps personaFields/applyPersonaEdit/personaEditedFields in sync
// — adding a provider means adding personaEdit fields, config fields, and one
// row here. baseEnv == "" means the provider has no editable base URL (Ollama,
// whose host is the top-level "ollamaHost" field).
type providerCred struct {
	key      string                       // API-key Persona field key (== personaEdit JSON tag)
	label    string                       // API-key display label
	provider string                       // short provider name for the Persona card header (e.g. "OpenAI")
	env      string                       // MOONY_*_API_KEY (or MOONY_OLLAMA_TOKEN) env var
	desc     string                       // API-key help text
	cfg      func(*config.Config) *string // pointer to the live API-key config field
	edit     func(personaEdit) *string    // posted API-key value (nil = field absent = unchanged)

	baseKey     string                       // base-URL Persona field key ("" = no base-URL field)
	baseLabel   string                       // base-URL display label
	baseEnv     string                       // MOONY_*_BASE_URL env var
	baseDesc    string                       // base-URL help text
	baseCfg     func(*config.Config) *string // pointer to the live base-URL config field
	baseEdit    func(personaEdit) *string    // posted base-URL value (nil = unchanged)
	baseDefault string                       // default endpoint when unset (shown as the input placeholder)
}

// providerCreds is the credential registry for the Persona panel, in display
// order (matching the /provider list). Keys mirror config.Load's MOONY_*_API_KEY
// / MOONY_*_BASE_URL reads; the bare-name key fallbacks (OPENAI_API_KEY, …) are
// deployment env only and not editable here.
var providerCreds = []providerCred{
	{
		key: "openaiApiKey", label: "OpenAI API key", provider: "OpenAI", env: "MOONY_OPENAI_API_KEY",
		desc: "MOONY_OPENAI_API_KEY: authenticates api.openai.com. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.OpenAIAPIKey }, edit: func(e personaEdit) *string { return e.OpenAIAPIKey },
		baseKey: "openaiBaseUrl", baseLabel: "OpenAI base URL", baseEnv: "MOONY_OPENAI_BASE_URL",
		baseDesc: "MOONY_OPENAI_BASE_URL: override the OpenAI-compatible endpoint (default https://api.openai.com/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.OpenAIBaseURL }, baseEdit: func(e personaEdit) *string { return e.OpenAIBaseURL },
		baseDefault: "https://api.openai.com/v1",
	},
	{
		key: "anthropicApiKey", label: "Anthropic API key", provider: "Anthropic", env: "MOONY_ANTHROPIC_API_KEY",
		desc: "MOONY_ANTHROPIC_API_KEY: authenticates api.anthropic.com (Claude). Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.AnthropicAPIKey }, edit: func(e personaEdit) *string { return e.AnthropicAPIKey },
		baseKey: "anthropicBaseUrl", baseLabel: "Anthropic base URL", baseEnv: "MOONY_ANTHROPIC_BASE_URL",
		baseDesc: "MOONY_ANTHROPIC_BASE_URL: override the native Anthropic Messages API base (default https://api.anthropic.com). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.AnthropicBaseURL }, baseEdit: func(e personaEdit) *string { return e.AnthropicBaseURL },
		baseDefault: "https://api.anthropic.com",
	},
	{
		key: "sqeAnthropicApiKey", label: "SQE Anthropic API key", provider: "SQE Anthropic", env: "MOONY_SQE_ANTHROPIC_API_KEY",
		desc: "MOONY_SQE_ANTHROPIC_API_KEY: authenticates the SQE Anthropic gateway (falls back to the Anthropic key if unset). Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.SQEAnthropicAPIKey }, edit: func(e personaEdit) *string { return e.SQEAnthropicAPIKey },
		baseKey: "sqeAnthropicBaseUrl", baseLabel: "SQE Anthropic base URL", baseEnv: "MOONY_SQE_ANTHROPIC_BASE_URL",
		baseDesc: "MOONY_SQE_ANTHROPIC_BASE_URL: the SQE LAN gateway base (default http://192.168.33.158:8787). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.SQEAnthropicBaseURL }, baseEdit: func(e personaEdit) *string { return e.SQEAnthropicBaseURL },
		baseDefault: "http://192.168.33.158:8787",
	},
	{
		key: "openrouterApiKey", label: "OpenRouter API key", provider: "OpenRouter", env: "MOONY_OPENROUTER_API_KEY",
		desc: "MOONY_OPENROUTER_API_KEY: authenticates openrouter.ai. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.OpenRouterAPIKey }, edit: func(e personaEdit) *string { return e.OpenRouterAPIKey },
		baseKey: "openrouterBaseUrl", baseLabel: "OpenRouter base URL", baseEnv: "MOONY_OPENROUTER_BASE_URL",
		baseDesc: "MOONY_OPENROUTER_BASE_URL: override the OpenRouter base (default https://openrouter.ai/api/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.OpenRouterBaseURL }, baseEdit: func(e personaEdit) *string { return e.OpenRouterBaseURL },
		baseDefault: "https://openrouter.ai/api/v1",
	},
	{
		key: "geminiApiKey", label: "Gemini API key", provider: "Gemini", env: "MOONY_GEMINI_API_KEY",
		desc: "MOONY_GEMINI_API_KEY: authenticates Google's OpenAI-compatible endpoint. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.GeminiAPIKey }, edit: func(e personaEdit) *string { return e.GeminiAPIKey },
		baseKey: "geminiBaseUrl", baseLabel: "Gemini base URL", baseEnv: "MOONY_GEMINI_BASE_URL",
		baseDesc: "MOONY_GEMINI_BASE_URL: override the Gemini base (default https://generativelanguage.googleapis.com/v1beta/openai). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.GeminiBaseURL }, baseEdit: func(e personaEdit) *string { return e.GeminiBaseURL },
		baseDefault: "https://generativelanguage.googleapis.com/v1beta/openai",
	},
	{
		key: "deepseekApiKey", label: "DeepSeek API key", provider: "DeepSeek", env: "MOONY_DEEPSEEK_API_KEY",
		desc: "MOONY_DEEPSEEK_API_KEY: authenticates api.deepseek.com. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.DeepSeekAPIKey }, edit: func(e personaEdit) *string { return e.DeepSeekAPIKey },
		baseKey: "deepseekBaseUrl", baseLabel: "DeepSeek base URL", baseEnv: "MOONY_DEEPSEEK_BASE_URL",
		baseDesc: "MOONY_DEEPSEEK_BASE_URL: override the DeepSeek base (default https://api.deepseek.com). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.DeepSeekBaseURL }, baseEdit: func(e personaEdit) *string { return e.DeepSeekBaseURL },
		baseDefault: "https://api.deepseek.com",
	},
	{
		key: "groqApiKey", label: "Groq API key", provider: "Groq", env: "MOONY_GROQ_API_KEY",
		desc: "MOONY_GROQ_API_KEY: authenticates api.groq.com. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.GroqAPIKey }, edit: func(e personaEdit) *string { return e.GroqAPIKey },
		baseKey: "groqBaseUrl", baseLabel: "Groq base URL", baseEnv: "MOONY_GROQ_BASE_URL",
		baseDesc: "MOONY_GROQ_BASE_URL: override the Groq base (default https://api.groq.com/openai/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.GroqBaseURL }, baseEdit: func(e personaEdit) *string { return e.GroqBaseURL },
		baseDefault: "https://api.groq.com/openai/v1",
	},
	{
		key: "xaiApiKey", label: "xAI API key", provider: "xAI", env: "MOONY_XAI_API_KEY",
		desc: "MOONY_XAI_API_KEY: authenticates api.x.ai (Grok). Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.XAIAPIKey }, edit: func(e personaEdit) *string { return e.XAIAPIKey },
		baseKey: "xaiBaseUrl", baseLabel: "xAI base URL", baseEnv: "MOONY_XAI_BASE_URL",
		baseDesc: "MOONY_XAI_BASE_URL: override the xAI base (default https://api.x.ai/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.XAIBaseURL }, baseEdit: func(e personaEdit) *string { return e.XAIBaseURL },
		baseDefault: "https://api.x.ai/v1",
	},
	{
		key: "mistralApiKey", label: "Mistral API key", provider: "Mistral", env: "MOONY_MISTRAL_API_KEY",
		desc: "MOONY_MISTRAL_API_KEY: authenticates api.mistral.ai. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.MistralAPIKey }, edit: func(e personaEdit) *string { return e.MistralAPIKey },
		baseKey: "mistralBaseUrl", baseLabel: "Mistral base URL", baseEnv: "MOONY_MISTRAL_BASE_URL",
		baseDesc: "MOONY_MISTRAL_BASE_URL: override the Mistral base (default https://api.mistral.ai/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.MistralBaseURL }, baseEdit: func(e personaEdit) *string { return e.MistralBaseURL },
		baseDefault: "https://api.mistral.ai/v1",
	},
	{
		key: "togetherApiKey", label: "Together.ai API key", provider: "Together.ai", env: "MOONY_TOGETHER_API_KEY",
		desc: "MOONY_TOGETHER_API_KEY: authenticates api.together.xyz. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.TogetherAPIKey }, edit: func(e personaEdit) *string { return e.TogetherAPIKey },
		baseKey: "togetherBaseUrl", baseLabel: "Together.ai base URL", baseEnv: "MOONY_TOGETHER_BASE_URL",
		baseDesc: "MOONY_TOGETHER_BASE_URL: override the Together.ai base (default https://api.together.xyz/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.TogetherBaseURL }, baseEdit: func(e personaEdit) *string { return e.TogetherBaseURL },
		baseDefault: "https://api.together.xyz/v1",
	},
	{
		key: "fireworksApiKey", label: "Fireworks.ai API key", provider: "Fireworks.ai", env: "MOONY_FIREWORKS_API_KEY",
		desc: "MOONY_FIREWORKS_API_KEY: authenticates api.fireworks.ai. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.FireworksAPIKey }, edit: func(e personaEdit) *string { return e.FireworksAPIKey },
		baseKey: "fireworksBaseUrl", baseLabel: "Fireworks.ai base URL", baseEnv: "MOONY_FIREWORKS_BASE_URL",
		baseDesc: "MOONY_FIREWORKS_BASE_URL: override the Fireworks.ai base (default https://api.fireworks.ai/inference/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.FireworksBaseURL }, baseEdit: func(e personaEdit) *string { return e.FireworksBaseURL },
		baseDefault: "https://api.fireworks.ai/inference/v1",
	},
	{
		key: "moonshotApiKey", label: "Moonshot API key", provider: "Moonshot", env: "MOONY_MOONSHOT_API_KEY",
		desc: "MOONY_MOONSHOT_API_KEY: authenticates api.moonshot.ai (Kimi). Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.MoonshotAPIKey }, edit: func(e personaEdit) *string { return e.MoonshotAPIKey },
		baseKey: "moonshotBaseUrl", baseLabel: "Moonshot base URL", baseEnv: "MOONY_MOONSHOT_BASE_URL",
		baseDesc: "MOONY_MOONSHOT_BASE_URL: override the Moonshot base (default https://api.moonshot.ai/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.MoonshotBaseURL }, baseEdit: func(e personaEdit) *string { return e.MoonshotBaseURL },
		baseDefault: "https://api.moonshot.ai/v1",
	},
	{
		key: "zaiApiKey", label: "Z.AI API key", provider: "Z.AI", env: "MOONY_ZAI_API_KEY",
		desc: "MOONY_ZAI_API_KEY: authenticates api.z.ai (Zhipu GLM). Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.ZAIAPIKey }, edit: func(e personaEdit) *string { return e.ZAIAPIKey },
		baseKey: "zaiBaseUrl", baseLabel: "Z.AI base URL", baseEnv: "MOONY_ZAI_BASE_URL",
		baseDesc: "MOONY_ZAI_BASE_URL: override the Z.AI base (default https://api.z.ai/api/paas/v4). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.ZAIBaseURL }, baseEdit: func(e personaEdit) *string { return e.ZAIBaseURL },
		baseDefault: "https://api.z.ai/api/paas/v4",
	},
	{
		key: "dashscopeApiKey", label: "DashScope API key", provider: "DashScope", env: "MOONY_DASHSCOPE_API_KEY",
		desc: "MOONY_DASHSCOPE_API_KEY: authenticates Alibaba DashScope (Qwen). Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.DashScopeAPIKey }, edit: func(e personaEdit) *string { return e.DashScopeAPIKey },
		baseKey: "dashscopeBaseUrl", baseLabel: "DashScope base URL", baseEnv: "MOONY_DASHSCOPE_BASE_URL",
		baseDesc: "MOONY_DASHSCOPE_BASE_URL: override the DashScope base (default https://dashscope-intl.aliyuncs.com/compatible-mode/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.DashScopeBaseURL }, baseEdit: func(e personaEdit) *string { return e.DashScopeBaseURL },
		baseDefault: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
	},
	{
		key: "nvidiaApiKey", label: "NVIDIA NIM API key", provider: "NVIDIA NIM", env: "MOONY_NVIDIA_API_KEY",
		desc: "MOONY_NVIDIA_API_KEY: authenticates integrate.api.nvidia.com. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.NvidiaAPIKey }, edit: func(e personaEdit) *string { return e.NvidiaAPIKey },
		baseKey: "nvidiaBaseUrl", baseLabel: "NVIDIA NIM base URL", baseEnv: "MOONY_NVIDIA_BASE_URL",
		baseDesc: "MOONY_NVIDIA_BASE_URL: override the NVIDIA NIM base (default https://integrate.api.nvidia.com/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.NvidiaBaseURL }, baseEdit: func(e personaEdit) *string { return e.NvidiaBaseURL },
		baseDefault: "https://integrate.api.nvidia.com/v1",
	},
	{
		key: "huggingfaceApiKey", label: "Hugging Face API key", provider: "Hugging Face", env: "MOONY_HUGGINGFACE_API_KEY",
		desc: "MOONY_HUGGINGFACE_API_KEY: authenticates router.huggingface.co. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.HuggingFaceAPIKey }, edit: func(e personaEdit) *string { return e.HuggingFaceAPIKey },
		baseKey: "huggingfaceBaseUrl", baseLabel: "Hugging Face base URL", baseEnv: "MOONY_HUGGINGFACE_BASE_URL",
		baseDesc: "MOONY_HUGGINGFACE_BASE_URL: override the Hugging Face router base (default https://router.huggingface.co/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.HuggingFaceBaseURL }, baseEdit: func(e personaEdit) *string { return e.HuggingFaceBaseURL },
		baseDefault: "https://router.huggingface.co/v1",
	},
	{
		key: "veniceApiKey", label: "Venice API key", provider: "Venice", env: "MOONY_VENICE_API_KEY",
		desc: "MOONY_VENICE_API_KEY: authenticates api.venice.ai. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.VeniceAPIKey }, edit: func(e personaEdit) *string { return e.VeniceAPIKey },
		baseKey: "veniceBaseUrl", baseLabel: "Venice base URL", baseEnv: "MOONY_VENICE_BASE_URL",
		baseDesc: "MOONY_VENICE_BASE_URL: override the Venice base (default https://api.venice.ai/api/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.VeniceBaseURL }, baseEdit: func(e personaEdit) *string { return e.VeniceBaseURL },
		baseDefault: "https://api.venice.ai/api/v1",
	},
	{
		key: "omnirouteApiKey", label: "OmniRoute API key", provider: "OmniRoute", env: "MOONY_OMNIROUTE_API_KEY",
		desc: "MOONY_OMNIROUTE_API_KEY: authenticates the local OmniRoute gateway (Dashboard → Endpoints). Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.OmniRouteAPIKey }, edit: func(e personaEdit) *string { return e.OmniRouteAPIKey },
		baseKey: "omnirouteBaseUrl", baseLabel: "OmniRoute base URL", baseEnv: "MOONY_OMNIROUTE_BASE_URL",
		baseDesc: "MOONY_OMNIROUTE_BASE_URL: override the OmniRoute base (default http://localhost:20128/v1). Blank uses the default.",
		baseCfg:  func(c *config.Config) *string { return &c.OmniRouteBaseURL }, baseEdit: func(e personaEdit) *string { return e.OmniRouteBaseURL },
		baseDefault: "http://localhost:20128/v1",
	},
	{
		key: "ollamaToken", label: "Ollama token", provider: "Ollama", env: "MOONY_OLLAMA_TOKEN",
		desc: "MOONY_OLLAMA_TOKEN: optional bearer token for a secured Ollama server. Applied live and saved to ~/.config/moony/.env (persists).",
		cfg:  func(c *config.Config) *string { return &c.OllamaToken }, edit: func(e personaEdit) *string { return e.OllamaToken },
		// No base URL: the Ollama host is the top-level "ollamaHost" field.
	},
}

// personaEditedFields returns the names of the fields this edit touched, for
// logging. It never includes secret values — only field names.
func personaEditedFields(e personaEdit) []string {
	var f []string
	if e.OllamaHost != nil {
		f = append(f, "ollamaHost")
	}
	if e.AutoDelegate != nil {
		f = append(f, fmt.Sprintf("autoDelegate=%t", *e.AutoDelegate))
	}
	if e.AutoPlan != nil {
		f = append(f, fmt.Sprintf("autoPlan=%t", *e.AutoPlan))
	}
	if e.CheapReads != nil {
		f = append(f, fmt.Sprintf("cheapReads=%t", *e.CheapReads))
	}
	if e.SelfReview != nil {
		f = append(f, fmt.Sprintf("selfReview=%t", *e.SelfReview))
	}
	if e.AdaptiveModel != nil {
		f = append(f, fmt.Sprintf("adaptiveModel=%t", *e.AdaptiveModel))
	}
	if e.MaxAgentSteps != nil {
		f = append(f, fmt.Sprintf("maxAgentSteps=%s", *e.MaxAgentSteps))
	}
	if e.AutoContinue != nil {
		f = append(f, fmt.Sprintf("autoContinue=%s", *e.AutoContinue))
	}
	if e.SqeFastModel != nil {
		f = append(f, fmt.Sprintf("sqeFastModel=%q", *e.SqeFastModel))
	}
	if e.SqeAuxModel != nil {
		f = append(f, fmt.Sprintf("sqeAuxModel=%q", *e.SqeAuxModel))
	}
	if e.SessionEncrypt != nil {
		f = append(f, "sqeSessionEncrypt")
	}
	if e.SessionKey != nil {
		f = append(f, "sqeSessionKey") // name only, never the secret
	}
	if e.SessionKeyCmd != nil {
		f = append(f, "sqeSessionKeyCmd")
	}
	if e.SessionKeyFile != nil {
		f = append(f, "sqeSessionKeyFile")
	}
	if e.SessionPassphrase != nil {
		f = append(f, "sqeSessionPassphrase") // name only, never the secret
	}
	for _, pc := range providerCreds {
		if pc.edit(e) != nil {
			f = append(f, pc.key) // name only, never the secret
		}
		if pc.baseEdit != nil && pc.baseEdit(e) != nil {
			f = append(f, pc.baseKey)
		}
	}
	if len(f) == 0 {
		return []string{"no fields"}
	}
	return f
}

// handlePersona serves GET (current values) and POST (apply edits) for the panel.
func (s *Server) handlePersona(w http.ResponseWriter, r *http.Request) {
	// Persona is GLOBAL runtime config, not per-session, and the LLM client reads
	// it per-request from the shared s.cfg pointer (which the live terminal agent
	// also uses). Resolve the config without ever spinning up a parallel agent:
	//
	//   - The live TERMINAL session is driven through the hub, not store-backed.
	//     Routing it through s.lookup would resurrect a SECOND store-backed agent
	//     on the same on-disk session (spawning its MCP/LSP) — the very dual-run
	//     serveLive/evictStoreBacked exist to prevent. Short-circuit to s.cfg, the
	//     config that very agent shares, so edits land on the running terminal.
	//   - Any other (store-backed) session: use its agent's config when open.
	//   - Otherwise fall back to s.cfg (e.g. a not-yet-flushed session).
	id := r.PathValue("id")
	var cfg *config.Config
	if id != "" && id == s.activeID() {
		cfg = s.cfg
	} else if sess := s.lookup(r); sess != nil && sess.agent != nil {
		cfg = sess.agent.Config()
	} else {
		cfg = s.cfg
	}
	if cfg == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		// Fetch the live model list so the Fast-model dropdown matches /models.
		// Fall back to the static catalogue on error (same pattern as /models).
		client := llm.NewClient(cfg)
		models, err := client.ListModels(r.Context())
		if err != nil || len(models) == 0 {
			models = config.ModelFallback(cfg.LLMProvider)
		}
		writeJSON(w, map[string]any{"fields": personaFields(cfg, models)})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var edit personaEdit
	if err := json.Unmarshal(body, &edit); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	applyPersonaEdit(cfg, edit)
	if s.cfg != nil && s.cfg != cfg {
		applyPersonaEdit(s.cfg, edit) // mirror onto the shared config the LLM client reads
	}
	// Dynamically-discovered MCP/tool credential env vars (keyed "envToken:<NAME>").
	var rawFields map[string]json.RawMessage
	_ = json.Unmarshal(body, &rawFields)
	tokenChanged := applyEnvTokenEdits(rawFields)
	// Persist the savedPrefs subset (e.g. AutoPlan) so persistable settings stick.
	// Session-encryption secrets and the limit/URL overrides that aren't provider
	// credentials stay runtime-only and revert to the deployed env on restart.
	// Best-effort.
	saveErr := cfg.Save()
	// Provider credentials (API keys + base-URL overrides) DO persist: write them
	// to the auto-loaded dotenv (~/.config/moony/.env) so they survive a restart
	// rather than needing re-entry each launch. A real env var of the same name
	// still wins on load. Best-effort — a write failure only loses persistence,
	// not the live edit already applied above.
	var persistErr error
	if up := providerEnvPersist(edit); len(up) > 0 {
		persistErr = config.UpsertDotEnv(up)
	}
	if s.logf != nil {
		changed := append(personaEditedFields(edit), tokenChanged...)
		switch {
		case saveErr != nil || persistErr != nil:
			s.logf(fmt.Sprintf("persona: saved config (%s); persist warning: prefs=%v env=%v", strings.Join(changed, ", "), saveErr, persistErr))
		default:
			s.logf(fmt.Sprintf("persona: saved config (%s)", strings.Join(changed, ", ")))
		}
	}
	// Re-fetch live model list for the POST response so the dropdown stays current.
	client2 := llm.NewClient(cfg)
	models2, err2 := client2.ListModels(r.Context())
	if err2 != nil || len(models2) == 0 {
		models2 = config.ModelFallback(cfg.LLMProvider)
	}
	writeJSON(w, map[string]any{"fields": personaFields(cfg, models2)})
}

// providerEnvPersist collects the provider env vars an edit should write to the
// persistent dotenv (~/.config/moony/.env) so they survive a restart. API keys
// are included only when a non-blank value was submitted — a blank keeps the
// existing key, so it must be left untouched on disk. Base URLs are included
// whenever the field was submitted: a value sets the override; an empty string
// removes it (UpsertDotEnv deletes empty-valued keys) so the default is used.
func providerEnvPersist(e personaEdit) map[string]string {
	up := map[string]string{}
	for _, pc := range providerCreds {
		if v := pc.edit(e); v != nil {
			if t := strings.TrimSpace(*v); t != "" {
				up[pc.env] = t
			}
		}
		if pc.baseEdit != nil {
			if v := pc.baseEdit(e); v != nil {
				up[pc.baseEnv] = strings.TrimSpace(*v) // "" => remove the override
			}
		}
	}
	return up
}

// applyPersonaEdit mutates c with the supplied edits (in place; nil = unchanged,
// empty secret = keep existing).
func applyPersonaEdit(c *config.Config, e personaEdit) {
	if c == nil {
		return
	}
	if e.OllamaHost != nil {
		c.OllamaBaseURL = config.NormalizeOllamaBaseURL(*e.OllamaHost)
	}
	if e.AutoDelegate != nil {
		c.AutoDelegate = *e.AutoDelegate
	}
	if e.AutoPlan != nil {
		c.AutoPlan = *e.AutoPlan
		ap := *e.AutoPlan
		c.AutoPlanPersona = &ap // remember as explicit persona choice
	}
	if e.CheapReads != nil {
		c.CheapReads = *e.CheapReads
		cr := *e.CheapReads
		c.CheapReadsPersona = &cr // remember as explicit persona choice
	}
	if e.SelfReview != nil {
		c.SelfReview = *e.SelfReview
		sr := *e.SelfReview
		c.SelfReviewPersona = &sr // remember as explicit persona choice
	}
	if e.AdaptiveModel != nil {
		c.AdaptiveModel = *e.AdaptiveModel
		am := *e.AdaptiveModel
		c.AdaptiveModelPersona = &am // remember as explicit persona choice
	}
	if e.MaxAgentSteps != nil {
		setEnvPlain("MOONY_MAX_STEPS", e.MaxAgentSteps)
		if n, err := strconv.Atoi(strings.TrimSpace(*e.MaxAgentSteps)); err == nil && n > 0 {
			c.MaxAgentSteps = n
		}
	}
	if e.AutoContinue != nil {
		setEnvPlain("MOONY_AUTO_CONTINUE", e.AutoContinue)
		if n, err := strconv.Atoi(strings.TrimSpace(*e.AutoContinue)); err == nil && n >= 0 {
			c.AutoContinue = n
		}
	}
	if e.SqeFastModel != nil {
		setEnvPlain("MOONY_FAST_MODEL", e.SqeFastModel)
		fm := strings.TrimSpace(*e.SqeFastModel)
		c.FastModelPersona = &fm // remember as explicit persona choice
	}
	if e.SqeAuxModel != nil {
		setEnvPlain("MOONY_AUX_MODEL", e.SqeAuxModel)
		am := strings.TrimSpace(*e.SqeAuxModel)
		c.AuxModelPersona = &am // remember as explicit persona choice
	}
	// Session-encryption vars live only in the process env (session.NewStore reads
	// them). Secrets keep their existing value when submitted empty; plain fields
	// (mode/path) apply as given and clear when blanked.
	setEnvSecret("MOONY_SESSION_KEY", e.SessionKey)
	setEnvSecret("MOONY_SESSION_KEY_CMD", e.SessionKeyCmd)
	setEnvSecret("MOONY_SESSION_PASSPHRASE", e.SessionPassphrase)
	setEnvPlain("MOONY_SESSION_KEY_FILE", e.SessionKeyFile)
	setEnvPlain("MOONY_SESSION_ENCRYPT", e.SessionEncrypt)
	// Provider API keys / Ollama token. Unlike the session vars (read from env at
	// startup), the LLM client reads these from the config struct per request, so
	// update the live field too — the change reaches the running terminal at once.
	// Secret semantics: blank keeps the existing key (never wipes it on empty submit).
	for _, pc := range providerCreds {
		if v := pc.edit(e); v != nil {
			if t := strings.TrimSpace(*v); t != "" {
				_ = os.Setenv(pc.env, t)
				*pc.cfg(c) = t
			}
		}
		// Base-URL override (plain): set when given, clear when blank. An empty
		// config field is safe — the LLM client falls back to the built-in default.
		if pc.baseEdit == nil {
			continue
		}
		if v := pc.baseEdit(e); v != nil {
			t := strings.TrimSpace(*v)
			if t != "" {
				_ = os.Setenv(pc.baseEnv, t)
			} else {
				_ = os.Unsetenv(pc.baseEnv)
			}
			*pc.baseCfg(c) = t
		}
	}
}

// setEnvSecret updates a secret env var: nil = unchanged, empty = keep existing
// (so a blank submit doesn't wipe the stored secret), non-empty = set.
func setEnvSecret(name string, v *string) {
	if v == nil {
		return
	}
	if t := strings.TrimSpace(*v); t != "" {
		_ = os.Setenv(name, t)
	}
}

// setEnvPlain updates a non-secret env var: nil = unchanged, empty = clear, else set.
func setEnvPlain(name string, v *string) {
	if v == nil {
		return
	}
	if t := strings.TrimSpace(*v); t != "" {
		_ = os.Setenv(name, t)
	} else {
		_ = os.Unsetenv(name)
	}
}

// personaFields snapshots c into the panel's field list (secrets masked).
func personaFields(c *config.Config, models []config.ModelInfo) []personaField {
	fields := []personaField{
		{Key: "ollamaHost", Label: "Ollama host", Type: "text", Value: c.OllamaBaseURL,
			Desc: "Ollama server (MOONY_OLLAMA_HOST), used by the ollama backend."},
		{Key: "autoDelegate", Label: "Auto-delegate subtasks", Type: "bool", Value: c.AutoDelegate,
			Desc: "Let the agent spin up sub-agents for large multi-step tasks."},
		{Key: "autoPlan", Label: "Auto-plan large tasks", Type: "bool", Value: c.AutoPlan,
			Desc: "Auto-enter read-only plan mode for big/multi-step tasks before touching files (persists)."},
		{Key: "cheapReads", Label: "Cheap reads (MOONY_CHEAP_READS)", Type: "bool", Value: c.CheapReads,
			Desc: "Route pure-read rounds (grep / read_file / find_symbol / …) to the cheaper FastModel automatically. Reverts to the full model the moment writing starts, bash runs, or friction is detected. Persists across sessions."},
		{Key: "selfReview", Label: "Self-review diffs (MOONY_SELF_REVIEW)", Type: "bool", Value: c.SelfReview,
			Desc: "After each turn that edits code, run a cheap fast-model review of the diff for logic bugs. One feedback cycle per turn; skipped for sub-agents. Persists across sessions."},
		{Key: "adaptiveModel", Label: "Adaptive model (MOONY_ADAPTIVE_MODEL)", Type: "bool", Value: c.AdaptiveModel,
			Desc: "Start turns on the cheaper FastModel; escalate to the full model only when friction (errors/retries) is detected. Runtime override — resets on restart."},
		{Key: "maxAgentSteps", Label: "Max agent steps (MOONY_MAX_STEPS)", Type: "text", Value: fmt.Sprintf("%d", c.MaxAgentSteps),
			Desc: "Max tool-call iterations per turn (default 80). A guard against runaway loops — raise for complex tasks, lower for tighter control. Runtime override — resets on restart."},
		{Key: "autoContinue", Label: "Auto-continue extensions (MOONY_AUTO_CONTINUE)", Type: "text", Value: fmt.Sprintf("%d", c.AutoContinue),
			Desc: "How many times a turn auto-extends past the step limit when the model is still working (default 1). Hard ceiling = MaxAgentSteps × (1 + AutoContinue). Runtime override — resets on restart."},
		{Key: "sqeFastModel", Label: "Fast model (MOONY_FAST_MODEL)", Type: "select",
			Value:   os.Getenv("MOONY_FAST_MODEL"),
			Options: fastModelSelectOptions(models),
			Desc:    "Model used for cheap-reads rounds, AdaptiveModel exploration, and sub-agent aux calls. 'Auto' uses the built-in default. Runtime override — resets on restart."},
		{Key: "sqeAuxModel", Label: "Self-review model (MOONY_AUX_MODEL)", Type: "select",
			Value:   os.Getenv("MOONY_AUX_MODEL"),
			Options: auxModelSelectOptions(models),
			Desc:    "Model for self-review diffs, checkpoint distillation, goal-judge, and session titling — all tool-less 'quick answer' calls. 'Auto' falls back to Fast model, else the built-in Haiku default. Runtime override — resets on restart."},
	}
	// Provider API keys + base-URL overrides (and the optional Ollama token).
	// Secrets masked to a last-4 hint; leaving a key blank on save keeps the
	// existing one. Base URLs are overrides — blank uses the built-in default.
	fields = append(fields, providerCredFields(c)...)
	// Session-history encryption at rest (AES-256-GCM). Read from the env by
	// session.NewStore; edits here are runtime overrides that apply on next
	// restart. Default (no key set) auto-generates a local key.
	fields = append(fields,
		personaField{Key: "sqeSessionEncrypt", Label: "Session encryption mode", Type: "text",
			Value: os.Getenv("MOONY_SESSION_ENCRYPT"),
			Desc:  "MOONY_SESSION_ENCRYPT: auto (default, encrypt with auto/BYOK key) · required (refuse without a key) · off (plaintext). Takes effect on next restart."},
		personaField{Key: "sqeSessionKey", Label: "Session key (BYOK)", Type: "secret",
			Set: strings.TrimSpace(os.Getenv("MOONY_SESSION_KEY")) != "", Hint: lastFour(os.Getenv("MOONY_SESSION_KEY")),
			Desc: "MOONY_SESSION_KEY: your own 32-byte key (base64 or hex). Highest priority. Restart to apply."},
		personaField{Key: "sqeSessionKeyCmd", Label: "Session key command", Type: "secret",
			Set: strings.TrimSpace(os.Getenv("MOONY_SESSION_KEY_CMD")) != "", Hint: lastFour(os.Getenv("MOONY_SESSION_KEY_CMD")),
			Desc: "MOONY_SESSION_KEY_CMD: shell command whose stdout is the key (KMS/Keychain/1Password/Vault). Restart to apply."},
		personaField{Key: "sqeSessionKeyFile", Label: "Session key file", Type: "text",
			Value: os.Getenv("MOONY_SESSION_KEY_FILE"),
			Desc:  "MOONY_SESSION_KEY_FILE: path to a file containing the key. Restart to apply."},
		personaField{Key: "sqeSessionPassphrase", Label: "Session passphrase", Type: "secret",
			Set: strings.TrimSpace(os.Getenv("MOONY_SESSION_PASSPHRASE")) != "", Hint: lastFour(os.Getenv("MOONY_SESSION_PASSPHRASE")),
			Desc: "MOONY_SESSION_PASSPHRASE: a passphrase stretched to a key via PBKDF2. Restart to apply."},
	)
	// Auto-discover credential env vars any MCP/tool declares or references, so
	// personal tokens (GitHub, …) appear without hardcoding.
	fields = append(fields, tokenEnvFields(c.WorkingDir)...)
	return fields
}

// providerCredFields builds the Persona fields for each provider credential in
// providerCreds: a masked secret field for the API key (current value read from
// the live config field, which config.Load populated from MOONY_*_API_KEY or the
// bare-name fallback — so a key already in the env shows as set with a last-4
// hint), immediately followed by the plain base-URL override field where the
// provider has one. The base-URL value is the current override (MOONY_*_BASE_URL,
// blank when unset — meaning the built-in default is used).
func providerCredFields(c *config.Config) []personaField {
	out := make([]personaField, 0, len(providerCreds)*2)
	for _, pc := range providerCreds {
		cur := strings.TrimSpace(*pc.cfg(c))
		out = append(out, personaField{
			Key: pc.key, Label: pc.label, Type: "secret",
			Set: cur != "", Hint: lastFour(cur), Desc: pc.desc,
			Group: pc.provider,
		})
		if pc.baseEnv != "" {
			out = append(out, personaField{
				Key: pc.baseKey, Label: pc.baseLabel, Type: "text",
				Value: os.Getenv(pc.baseEnv), Desc: pc.baseDesc,
				Group: pc.provider, Placeholder: pc.baseDefault,
			})
		}
	}
	return out
}

// tokenEnvFields builds a secret Persona field for each token-like env var the
// MCP catalog / configured servers reference. Keys are namespaced with
// tokenEnvFieldPrefix; the value is read from (and written to) the process env.
func tokenEnvFields(workingDir string) []personaField {
	// Union env vars declared/referenced by installed MCP servers, skills, and
	// sub-agents, so a personal token any of them needs auto-appears here once
	// installed (no hardcoding).
	seen := map[string]bool{}
	var names []string
	addAll := func(list []string) {
		for _, n := range list {
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
	}
	addAll(mcp.ReferencedEnvVars(workingDir))
	addAll(skills.EnvVars(workingDir))
	addAll(subagents.EnvVars(workingDir))
	sort.Strings(names)

	var out []personaField
	for _, name := range names {
		if !looksLikeTokenVar(name) {
			continue
		}
		v := os.Getenv(name)
		out = append(out, personaField{
			Key:   tokenEnvFieldPrefix + name,
			Label: name,
			Type:  "secret",
			Set:   strings.TrimSpace(v) != "",
			Hint:  lastFour(v),
			Desc:  "Credential env var an installed MCP server / skill / agent needs; set at runtime for the next (re)connect.",
		})
	}
	return out
}

// applyEnvTokenEdits sets any "envToken:<NAME>" fields from a raw persona POST
// body into the process environment (blank = keep existing). Only names that
// look like credentials are honored, so a crafted key can't set arbitrary env.
// Returns the names that were changed.
func applyEnvTokenEdits(raw map[string]json.RawMessage) []string {
	var changed []string
	for k, rv := range raw {
		name, ok := strings.CutPrefix(k, tokenEnvFieldPrefix)
		if !ok || !looksLikeTokenVar(name) {
			continue
		}
		var val string
		if json.Unmarshal(rv, &val) != nil {
			continue
		}
		if v := strings.TrimSpace(val); v != "" {
			_ = os.Setenv(name, v)
			changed = append(changed, name)
		}
	}
	return changed
}

// lastFour returns a secret's last 4 chars for recognition (the full value is
// never sent to the client); "" when empty, all-dots when shorter than 4.
func lastFour(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return ""
	case len(s) <= 4:
		return strings.Repeat("•", len(s))
	default:
		return s[len(s)-4:]
	}
}

// fastModelSelectOptions builds the Fast-model dropdown from a live model list
// (identical source to /models). The empty-value entry means "auto" (FastModel
// resolves to config.FastDefaultModel); its label is derived from that model's
// display name when found in the catalog. Models are listed in catalog order.
func fastModelSelectOptions(models []config.ModelInfo) []personaSelectOption {
	defaultLabel := "Auto"
	for _, m := range models {
		if m.ID == config.FastDefaultModel {
			defaultLabel = "Auto (default — " + m.Name + ")"
			break
		}
	}
	opts := []personaSelectOption{{Value: "", Label: defaultLabel}}
	for _, m := range models {
		if m.ID != "" {
			opts = append(opts, personaSelectOption{Value: m.ID, Label: m.Name})
		}
	}
	return opts
}

// auxModelSelectOptions builds the Self-review-model dropdown. The empty-value
// entry means "auto", which resolves via config.AuxModel's own fallback chain:
// MOONY_FAST_MODEL if set (matches the persisted Fast-model field's current
// value, read live so the label stays correct if that field is edited first),
// else config.AuxDefaultModel (Haiku).
func auxModelSelectOptions(models []config.ModelInfo) []personaSelectOption {
	fallbackID := strings.TrimSpace(os.Getenv("MOONY_FAST_MODEL"))
	if fallbackID == "" {
		fallbackID = config.AuxDefaultModel
	}
	defaultLabel := "Auto"
	for _, m := range models {
		if m.ID == fallbackID {
			defaultLabel = "Auto (default — " + m.Name + ")"
			break
		}
	}
	opts := []personaSelectOption{{Value: "", Label: defaultLabel}}
	for _, m := range models {
		if m.ID != "" {
			opts = append(opts, personaSelectOption{Value: m.ID, Label: m.Name})
		}
	}
	return opts
}
