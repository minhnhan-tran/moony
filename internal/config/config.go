// Package config holds runtime configuration for moony.
package config

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/pysty1995/moony/internal/provider"
)

// Config controls how the agent talks to its LLM provider and behaves at
// runtime.
type Config struct {
	// OllamaBaseURL is the base URL of the Ollama server, up to and including the
	// "/v1" OpenAI-compatible segment (the client appends "/chat/completions").
	// Set via MN_OLLAMA_HOST / OLLAMA_HOST. Defaults to http://localhost:11434/v1.
	OllamaBaseURL string
	// OllamaToken is an optional bearer token for a remote/protected Ollama
	// server. Set via MN_OLLAMA_TOKEN.
	OllamaToken string
	// OpenAIAPIKey authenticates api.openai.com. Set via MN_OPENAI_API_KEY
	// (falls back to the conventional OPENAI_API_KEY).
	OpenAIAPIKey string
	// OpenAIBaseURL overrides the OpenAI-compatible base URL (default
	// https://api.openai.com/v1). Set via MN_OPENAI_BASE_URL.
	OpenAIBaseURL string
	// AnthropicAPIKey authenticates api.anthropic.com. Set via
	// MN_ANTHROPIC_API_KEY (falls back to ANTHROPIC_API_KEY).
	AnthropicAPIKey string
	// AnthropicBaseURL overrides the native Anthropic Messages API base URL
	// (default https://api.anthropic.com). Set via MN_ANTHROPIC_BASE_URL.
	AnthropicBaseURL string
	// AnthropicCustomHeaders are extra HTTP headers for Anthropic-wire requests,
	// for gateways that need their own auth in front of the Messages API (HTTP
	// Basic, a proxy token, ngrok's skip-warning header, ...). Set via
	// MN_ANTHROPIC_CUSTOM_HEADERS (falls back to ANTHROPIC_CUSTOM_HEADERS) as
	// "Name: value" pairs separated by newlines, semicolons, or nothing at all;
	// see ParseCustomHeaders. They are only sent to a CUSTOM base URL -- never to
	// api.anthropic.com -- so gateway credentials cannot leak upstream if the base
	// URL is cleared.
	AnthropicCustomHeaders string
	// SQEAnthropicAPIKey authenticates the SQE Anthropic backend (Claude via the
	// SQE LAN gateway). Set via MN_SQE_ANTHROPIC_API_KEY; falls back to
	// AnthropicAPIKey when unset.
	SQEAnthropicAPIKey string
	// SQEAnthropicBaseURL is the SQE gateway base URL (default
	// defaultSQEAnthropicBaseURL). Set via MN_SQE_ANTHROPIC_BASE_URL.
	SQEAnthropicBaseURL string
	// OpenRouterAPIKey authenticates openrouter.ai. Set via
	// MN_OPENROUTER_API_KEY (falls back to OPENROUTER_API_KEY).
	OpenRouterAPIKey string
	// OpenRouterBaseURL overrides the OpenRouter base URL (default
	// https://openrouter.ai/api/v1). Set via MN_OPENROUTER_BASE_URL.
	OpenRouterBaseURL string
	// GeminiAPIKey authenticates Google's OpenAI-compatible endpoint. Set via
	// MN_GEMINI_API_KEY (falls back to GEMINI_API_KEY).
	GeminiAPIKey string
	// GeminiBaseURL overrides the Gemini base URL (default
	// https://generativelanguage.googleapis.com/v1beta/openai). Set via
	// MN_GEMINI_BASE_URL.
	GeminiBaseURL string
	// DeepSeekAPIKey authenticates api.deepseek.com. Set via
	// MN_DEEPSEEK_API_KEY (falls back to DEEPSEEK_API_KEY).
	DeepSeekAPIKey string
	// DeepSeekBaseURL overrides the DeepSeek base URL (default
	// https://api.deepseek.com). Set via MN_DEEPSEEK_BASE_URL.
	DeepSeekBaseURL string
	// GroqAPIKey authenticates api.groq.com. Set via MN_GROQ_API_KEY (falls
	// back to GROQ_API_KEY).
	GroqAPIKey string
	// GroqBaseURL overrides the Groq base URL (default
	// https://api.groq.com/openai/v1). Set via MN_GROQ_BASE_URL.
	GroqBaseURL string
	// XAIAPIKey authenticates api.x.ai (Grok). Set via MN_XAI_API_KEY (falls
	// back to XAI_API_KEY).
	XAIAPIKey string
	// XAIBaseURL overrides the xAI base URL (default https://api.x.ai/v1). Set
	// via MN_XAI_BASE_URL.
	XAIBaseURL string
	// MistralAPIKey authenticates api.mistral.ai. Set via MN_MISTRAL_API_KEY
	// (falls back to MISTRAL_API_KEY).
	MistralAPIKey string
	// MistralBaseURL overrides the Mistral base URL (default
	// https://api.mistral.ai/v1). Set via MN_MISTRAL_BASE_URL.
	MistralBaseURL string
	// TogetherAPIKey authenticates api.together.xyz. Set via
	// MN_TOGETHER_API_KEY (falls back to TOGETHER_API_KEY).
	TogetherAPIKey string
	// TogetherBaseURL overrides the Together.ai base URL (default
	// https://api.together.xyz/v1). Set via MN_TOGETHER_BASE_URL.
	TogetherBaseURL string
	// FireworksAPIKey authenticates api.fireworks.ai. Set via
	// MN_FIREWORKS_API_KEY (falls back to FIREWORKS_API_KEY).
	FireworksAPIKey string
	// FireworksBaseURL overrides the Fireworks.ai base URL (default
	// https://api.fireworks.ai/inference/v1). Set via MN_FIREWORKS_BASE_URL.
	FireworksBaseURL string
	// MoonshotAPIKey authenticates api.moonshot.ai (Kimi). Set via
	// MN_MOONSHOT_API_KEY (falls back to MOONSHOT_API_KEY).
	MoonshotAPIKey string
	// MoonshotBaseURL overrides the Moonshot base URL (default
	// https://api.moonshot.ai/v1). Set via MN_MOONSHOT_BASE_URL.
	MoonshotBaseURL string
	// ZAIAPIKey authenticates api.z.ai (Zhipu GLM). Set via
	// MN_ZAI_API_KEY (falls back to ZAI_API_KEY).
	ZAIAPIKey string
	// ZAIBaseURL overrides the Z.AI base URL (default
	// https://api.z.ai/api/paas/v4). Set via MN_ZAI_BASE_URL.
	ZAIBaseURL string
	// DashScopeAPIKey authenticates Alibaba DashScope (Qwen), OpenAI-compatible
	// mode. Set via MN_DASHSCOPE_API_KEY (falls back to DASHSCOPE_API_KEY).
	DashScopeAPIKey string
	// DashScopeBaseURL overrides the DashScope base URL (default
	// https://dashscope-intl.aliyuncs.com/compatible-mode/v1). Set via
	// MN_DASHSCOPE_BASE_URL.
	DashScopeBaseURL string
	// NvidiaAPIKey authenticates integrate.api.nvidia.com (NVIDIA NIM). Set via
	// MN_NVIDIA_API_KEY (falls back to NVIDIA_API_KEY).
	NvidiaAPIKey string
	// NvidiaBaseURL overrides the NVIDIA NIM base URL (default
	// https://integrate.api.nvidia.com/v1). Set via MN_NVIDIA_BASE_URL.
	NvidiaBaseURL string
	// HuggingFaceAPIKey authenticates router.huggingface.co. Set via
	// MN_HUGGINGFACE_API_KEY (falls back to HF_TOKEN).
	HuggingFaceAPIKey string
	// HuggingFaceBaseURL overrides the Hugging Face router base URL (default
	// https://router.huggingface.co/v1). Set via MN_HUGGINGFACE_BASE_URL.
	HuggingFaceBaseURL string
	// VeniceAPIKey authenticates api.venice.ai. Set via MN_VENICE_API_KEY
	// (falls back to VENICE_API_KEY).
	VeniceAPIKey string
	// VeniceBaseURL overrides the Venice base URL (default
	// https://api.venice.ai/api/v1). Set via MN_VENICE_BASE_URL.
	VeniceBaseURL string
	// OmniRouteAPIKey authenticates a local OmniRoute gateway. Set via
	// MN_OMNIROUTE_API_KEY (falls back to OMNIROUTE_API_KEY).
	OmniRouteAPIKey string
	// OmniRouteBaseURL overrides the OmniRoute base URL (default
	// http://localhost:20128/v1). Set via MN_OMNIROUTE_BASE_URL.
	OmniRouteBaseURL string
	// CerebrasAPIKey authenticates api.cerebras.ai. Set via
	// MN_CEREBRAS_API_KEY (falls back to CEREBRAS_API_KEY).
	CerebrasAPIKey string
	// CerebrasBaseURL overrides the Cerebras base URL (default
	// https://api.cerebras.ai/v1). Set via MN_CEREBRAS_BASE_URL.
	CerebrasBaseURL string
	// PerplexityAPIKey authenticates api.perplexity.ai. Set via
	// MN_PERPLEXITY_API_KEY (falls back to PERPLEXITY_API_KEY).
	PerplexityAPIKey string
	// PerplexityBaseURL overrides the Perplexity base URL (default
	// https://api.perplexity.ai). Set via MN_PERPLEXITY_BASE_URL.
	PerplexityBaseURL string
	// CohereAPIKey authenticates api.cohere.ai. Set via MN_COHERE_API_KEY
	// (falls back to CO_API_KEY / COHERE_API_KEY).
	CohereAPIKey string
	// CohereBaseURL overrides the Cohere OpenAI-compatible base URL (default
	// https://api.cohere.ai/compatibility/v1). Set via MN_COHERE_BASE_URL.
	CohereBaseURL string
	// DeepInfraAPIKey authenticates api.deepinfra.com. Set via
	// MN_DEEPINFRA_API_KEY (falls back to DEEPINFRA_API_KEY / DEEPINFRA_TOKEN).
	DeepInfraAPIKey string
	// DeepInfraBaseURL overrides the DeepInfra OpenAI-compatible base URL
	// (default https://api.deepinfra.com/v1/openai). Set via
	// MN_DEEPINFRA_BASE_URL.
	DeepInfraBaseURL string
	// MetaAPIKey authenticates Meta Model API (api.meta.ai), which serves the
	// Muse Spark coding models over an OpenAI-compatible wire. Keys look like
	// "LLM|<id>|<secret>" and come from https://dev.meta.ai/. Set via
	// MN_META_API_KEY (falls back to Meta's own MODEL_API_KEY / META_API_KEY).
	MetaAPIKey string
	// MetaBaseURL overrides the Meta Model API base URL (default
	// https://api.meta.ai/v1). Set via MN_META_BASE_URL.
	MetaBaseURL string
	// LMStudioBaseURL overrides the local LM Studio server base URL (default
	// http://localhost:1234/v1). Set via MN_LMSTUDIO_BASE_URL (falls back to
	// LM_STUDIO_BASE_URL). LM Studio is keyless; an optional token can be set
	// via MN_LMSTUDIO_API_KEY if you've enabled auth on the server.
	LMStudioBaseURL string
	// LMStudioAPIKey is an optional bearer token for LM Studio (usually unset).
	LMStudioAPIKey string
	// LlamaCppBaseURL overrides the local llama.cpp server (llama-server) base
	// URL (default http://localhost:8080/v1). Set via MN_LLAMACPP_BASE_URL
	// (falls back to LLAMA_CPP_BASE_URL). llama.cpp's server is keyless.
	LlamaCppBaseURL string
	// AzureAPIKey authenticates an Azure OpenAI resource. Set via
	// MN_AZURE_API_KEY (falls back to AZURE_OPENAI_API_KEY / AZURE_API_KEY).
	AzureAPIKey string
	// AzureBaseURL is the Azure OpenAI resource endpoint, e.g.
	// https://my-resource.openai.azure.com. Required (no default). Set via
	// MN_AZURE_BASE_URL (falls back to AZURE_OPENAI_ENDPOINT).
	AzureBaseURL string
	// AzureAPIVersion is the Azure OpenAI api-version query param (default
	// defaultAzureAPIVersion). Set via MN_AZURE_API_VERSION.
	AzureAPIVersion string
	// VertexAPIKey is a Google Cloud OAuth access token authorizing Vertex AI
	// (e.g. `gcloud auth print-access-token`). Set via MN_VERTEX_API_KEY
	// (falls back to GOOGLE_VERTEX_API_KEY / the standard GOOGLE_* creds are not
	// read here — supply a token).
	VertexAPIKey string
	// VertexProject is the GCP project id for Vertex AI. Set via
	// MN_VERTEX_PROJECT (falls back to GOOGLE_CLOUD_PROJECT).
	VertexProject string
	// VertexLocation is the Vertex AI region (default defaultVertexLocation,
	// e.g. us-central1; "global" is also valid). Set via MN_VERTEX_LOCATION
	// (falls back to GOOGLE_CLOUD_LOCATION / CLOUD_ML_REGION).
	VertexLocation string
	// VertexBaseURL fully overrides the computed Vertex base URL. Normally left
	// empty and derived from project+location. Set via MN_VERTEX_BASE_URL.
	VertexBaseURL string
	// AWSAccessKeyID / AWSSecretAccessKey / AWSSessionToken are the credentials
	// used to SigV4-sign AWS Bedrock requests. Each falls back to the standard
	// AWS_* env var (and, when all are empty, the AWS SDK's default credential
	// chain — shared config/SSO/instance role — is used). Set via
	// MN_AWS_ACCESS_KEY_ID / MN_AWS_SECRET_ACCESS_KEY /
	// MN_AWS_SESSION_TOKEN.
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string
	// AWSRegion is the AWS region for Bedrock (default defaultAWSRegion). Set
	// via MN_AWS_REGION (falls back to AWS_REGION / AWS_DEFAULT_REGION).
	AWSRegion string
	// AWSProfile selects a named profile from the shared AWS config when the
	// explicit keys above are empty. Set via MN_AWS_PROFILE (falls back to
	// AWS_PROFILE).
	AWSProfile string
	// CopilotToken is the GitHub token used by the GitHub Copilot provider
	// (which drives the local `copilot` CLI through the copilot-sdk). Set via
	// MN_COPILOT_TOKEN, falling back to the CLI's own COPILOT_GITHUB_TOKEN /
	// GH_TOKEN / GITHUB_TOKEN. When empty, the SDK/CLI uses its own stored
	// GitHub credentials.
	CopilotToken string
	// CopilotCLIPath overrides the path to the `copilot` CLI executable. Set
	// via MN_COPILOT_CLI_PATH (falls back to COPILOT_CLI_PATH). When empty,
	// the SDK looks for `copilot` on PATH.
	CopilotCLIPath string
	// LLMProvider selects the backend model provider: "ollama" (default),
	// "openai", "anthropic", "openrouter", "gemini", "deepseek", "groq", "xai",
	// "mistral", "together", "fireworks", "moonshot", "zai", "dashscope",
	// "nvidia", "huggingface", or "venice". Saved across launches; override
	// per-run with MN_PROVIDER. See internal/provider.
	LLMProvider string
	// Provider is the wire protocol for model API calls (anthropic-native vs
	// openai-compatible), derived from LLMProvider. Kept as a field for
	// existing callers that key behavior (e.g. extended-thinking gating) off it.
	Provider string
	// Model is the provider's model identifier (e.g. "llama3.1:8b" on Ollama,
	// "gpt-4o" on OpenAI, "claude-sonnet-4-5" on Anthropic, or a
	// "vendor/model" slug on OpenRouter).
	Model string
	// MaxTokens is the response token cap per turn.
	MaxTokens int
	// MaxToolOutputBytes caps how much tool output is fed back to the model.
	MaxToolOutputBytes int
	// TaskResultMaxBytes, when > 0, caps a task/delegate SUB-AGENT's returned
	// result to this many bytes (head+tail clip), tighter than MaxToolOutputBytes.
	// A delegated deliverable is re-sent to the parent every step until it ages
	// out, so a concise cap trims a real token sink on delegate-heavy turns. 0
	// (default) = no task-specific cap (only the general MaxToolOutputBytes
	// applies). Set via MN_TASK_RESULT_MAX; MN_CHEAP sets it to 6000.
	TaskResultMaxBytes int
	// MaxAgentSteps bounds tool-call iterations within a single user turn.
	// Override with MN_MAX_STEPS; a guard against runaway loops, not a budget.
	MaxAgentSteps int
	// AutoContinue is how many times a turn may automatically extend past
	// MaxAgentSteps when the model is still working (so long todo-driven tasks
	// finish without a manual "continue"). 0 disables it. Override with
	// MN_AUTO_CONTINUE. The hard ceiling is MaxAgentSteps*(1+AutoContinue).
	AutoContinue int
	// BatchContext, when true, adds a stronger system-prompt directive telling
	// the model to front-load context-gathering: issue every independent
	// read/grep/glob/find_symbol call together in its first 1–2 tool rounds (they
	// run in parallel) and/or delegate broad investigation to the explore
	// sub-agent, instead of reading one file per round — so turns converge in
	// fewer steps. On by default; set MN_BATCH_CONTEXT=off to disable.
	BatchContext bool
	// RepoMap, when true (default), prepends a compact, ignore-aware structural
	// overview of the repository (detected stack, key files, a depth-2 directory
	// tree with file counts) to the system prompt, so the model can orient without
	// globbing the tree first. Static — recomputed when the prompt is rebuilt, no
	// model call. Disable with MN_REPO_MAP=0.
	RepoMap bool
	// LazyMCPTools, when true (default), keeps the (often bulky) tool schemas of
	// connected MCP servers OUT of the per-request tool list until the model
	// activates a server with the `enable_toolset` tool. This shrinks the tool
	// payload (and stops MCP schema churn from busting the prompt cache) while
	// keeping the capability discoverable via enable_toolset's description.
	// Built-in tools are never gated. Disable with MN_LAZY_MCP=0.
	LazyMCPTools bool
	// LazyBuiltinTools, when true (default), gates a few non-core built-in tool
	// GROUPS (git, browser) the same way LazyMCPTools gates MCP
	// servers: their schemas are kept out of the per-request tool list until the
	// model activates the group with `enable_toolset`. Core tools (read/write/
	// edit/search/bash/…) are always present. Trims cold-context tokens and
	// tool-selection surface for capabilities many sessions never touch. Disable
	// with MN_LAZY_BUILTIN_TOOLS=0.
	LazyBuiltinTools bool
	// ProjectProfile, when true, maintains a model-distilled, cross-session
	// "profile" of the codebase (purpose, layout, entry points, conventions,
	// gotchas) in the global config dir keyed by the working dir, and injects it
	// into the system prompt so a fresh session orients without re-discovering the
	// repo. It's refreshed (at most ~weekly per repo) from real session work and
	// merged. OPT-IN (default off): it injects auto-generated content into every
	// turn's prompt, which can mislead if it goes stale. Enable with
	// MN_PROJECT_PROFILE=1. Distinct from RepoMap (mechanical file tree) and
	// AGENTS.md (user-authored instructions).
	ProjectProfile bool
	// Terse, when true (default), adds a verbosity-steering "OUTPUT BUDGET"
	// directive to the system prompt so the model spends fewer OUTPUT tokens on
	// ceremony — preambles, sign-offs, restated context, re-printed unchanged
	// code — which are billed several times higher than input. It trims only
	// ceremony, never actual work (tool calls, edits, verification). On by
	// default; set MN_TERSE=off to disable.
	Terse bool
	// CodeOutline, when true (default), compresses oversized read_file output on
	// source files into a signatures-only outline (with line numbers + elision
	// markers) instead of a blunt byte clip, so the model still sees the whole
	// file's shape. Toggle with MN_CODE_OUTLINE.
	CodeOutline bool

	// DynamicEffort, when true (default), enables dynamic effort routing in "auto"
	// reasoning mode: a step that only digests a routine/benign tool batch (file
	// edits, a directory listing, a todo update — no errors) is run at a lower
	// reasoning floor, since the model is just continuing rather than solving. The
	// existing prompt prediction (new request) and friction ramp (tool errors)
	// still elevate effort, so this only trims thinking on passive steps. Auto mode
	// only; set MN_DYNAMIC_EFFORT=off to disable.
	DynamicEffort bool
	// AdaptiveModel, when true, runs the main turn on the cheaper FastModel until
	// the turn shows sustained friction (tool errors / rejected stops), then
	// escalates to the session model for the rest of the turn — right-sizing the
	// model to the task so easy turns cost less. ON by default; it swaps the
	// model you'd otherwise pay full price for on easy turns, escalating back
	// automatically on friction. Disable with MN_ADAPTIVE_MODEL=0. A no-op
	// when FastModel equals the session model (already cheap, or a non-cloud
	// backend) and on ultrathink turns. Independent of DynamicEffort (which only
	// adjusts reasoning depth on the SAME model).
	AdaptiveModel bool
	// CheapReads, when true, routes rounds whose PREVIOUS step consisted entirely
	// of idempotent read tools (read_file/grep/glob/find_symbol/…) to the cheaper
	// FastModel — those rounds are pure exploration where the strong model adds
	// little. As soon as the model starts writing, edits files, runs bash, or
	// hits friction the full session model is restored. ON by default; disable
	// with MN_CHEAP_READS=0. A no-op when FastModel equals the session model.
	// Composes with AdaptiveModel; friction/hard/ultrathink override both.
	CheapReads bool
	// CheapReadsPersona, FastModelPersona, AuxModelPersona, AutoPlanPersona, and
	// SelfReviewPersona hold the values last explicitly set via the Persona
	// modal. They are non-nil only after the user has made an explicit choice,
	// and are applied by Load() AFTER env vars so a persona save always survives
	// a restart regardless of the deployed env config.
	// Stored in savedPrefs; managed only by applyPersonaEdit + Load().
	CheapReadsPersona    *bool
	FastModelPersona     *string
	AuxModelPersona      *string
	AutoPlanPersona      *bool
	SelfReviewPersona    *bool
	AdaptiveModelPersona *bool
	// FastProviderPersona/FastReasoningPersona/AuxProviderPersona/AuxReasoningPersona
	// hold the per-tier provider + reasoning-level chosen in the Persona modal for
	// the Fast and Self-review (aux) models. Empty/nil = inherit (byte-identical to
	// today). Managed only by applyPersonaEdit + Load(), persisted in savedPrefs,
	// and consumed via Fast/AuxProvider + Fast/AuxReasoning + Fast/AuxModelRoute.
	FastProviderPersona  *string
	FastReasoningPersona *string
	AuxProviderPersona   *string
	AuxReasoningPersona  *string
	// Quota-fallback choices last set via the Persona modal (nil = never chosen).
	// Persisted in savedPrefs and applied by Load() AFTER env vars, so a saved
	// chain survives a restart and wins over (or clears) MN_QUOTA_FALLBACK /
	// MN_QUOTA_RESET_AT / MN_QUOTA_BUDGET_USD / MN_QUOTA_BUDGET_PCT /
	// MN_PROVIDER_FALLBACKS.
	//
	// The budgets are *float64 (not float64) so an explicit 0 — "disable this
	// trigger" — stays distinguishable from "never set"; ProviderFallbacksPersona
	// is a *[]string so an explicitly emptied chain is likewise distinct from nil.
	// It holds full "provider[:model[:reasoning]]" specs rather than bare provider
	// ids, so a per-hop model/reasoning choice persists too.
	QuotaFallbackPersona     *bool
	QuotaResetAtPersona      *string
	QuotaBudgetUSDPersona    *float64
	QuotaBudgetPctPersona    *float64
	ProviderFallbacksPersona *[]string
	// VerifyCommand, when non-empty, is a shell command (e.g. a test/build) run
	// automatically at the end of a turn that edited files; if it exits non-zero
	// the failure is fed back so the agent self-corrects before finishing. Set it
	// via the /verify command or MN_VERIFY_CMD. Empty disables the loop.
	VerifyCommand string
	// VerifyMaxRetries caps how many times one turn re-runs to fix a failing
	// verification before stopping for review (default 2; MN_VERIFY_MAX).
	VerifyMaxRetries int
	// VerifyTimeoutSec bounds a single verification run in seconds (default 120;
	// MN_VERIFY_TIMEOUT), so a hung test command can't wedge the turn.
	VerifyTimeoutSec int
	// ModelExplicit, ProviderExplicit and ReasoningExplicit record that the value
	// came from an explicit choice at startup (MN_MODEL / MN_LLM_PROVIDER /
	// MN_REASONING, or the -model/-provider/-reasoning flags via SetModel etc.)
	// rather than a default. A reopened session restores its own saved selection
	// (agent.restoreLLMSelection) ONLY for the fields not explicitly set, so
	// `moony -model X --session <old>` still runs on X.
	ModelExplicit     bool
	ProviderExplicit  bool
	ReasoningExplicit bool

	// VerifyAuto, when true and VerifyCommand is empty, makes Load() auto-detect a
	// sensible verify command from the project (Makefile test target, go.mod,
	// package.json test script, Cargo.toml, pyproject/pytest, …). Off by default
	// so the agent never runs a project command the user didn't opt into
	// (MN_VERIFY_AUTO=1). An explicit VerifyCommand always wins.
	VerifyAuto bool
	// VerifyCommandAuto is true when VerifyCommand was filled by auto-detection
	// (not set explicitly), so the UI can label it as detected.
	VerifyCommandAuto bool
	// SelfReview, when true, runs a cheap fast-model review of the turn's own diff
	// before finishing a turn that edited files: if it flags a likely introduced
	// bug, the finding is fed back once so the agent fixes it before stopping. Off
	// by default; enable with MN_SELF_REVIEW=1. Independent of VerifyCommand
	// (which runs the build/test); this catches logic bugs a build wouldn't.
	SelfReview bool
	// WorkingDir is the directory the agent operates in.
	WorkingDir string

	// ReasoningLevel controls extended thinking: off, low, medium, or high.
	// It reflects the level for the currently active model; the per-model
	// memory lives in ReasoningByModel and is consulted whenever the model
	// changes.
	ReasoningLevel string

	// ReasoningByModel remembers the reasoning level chosen for each model
	// id, so e.g. switching back to an Opus model restores "high" while a
	// Gemini model can keep "medium" independently. It is the persistent
	// source of truth; ReasoningLevel is the resolved value for the active
	// model.
	ReasoningByModel map[string]string

	// ApprovalPolicy controls when mutating tools are gated behind a user
	// confirmation prompt: "prompt" (all), "safe" (auto-run read-only bash),
	// or "auto" (never prompt). See approval.go.
	ApprovalPolicy string
	// SandboxMode controls the OS-level bash sandbox (macOS): "off" or
	// "workdir" (confine writes to the working dir + caches). See sandbox.go.
	SandboxMode string
	// SandboxNetwork allows sandboxed commands to use the network. Defaults
	// true; only meaningful when SandboxMode != off.
	SandboxNetwork bool
	// AutoPlan, when true, makes the agent auto-enter read-only plan mode for a
	// build-mode user turn that looks like a large/multi-step change, so it
	// drafts a plan for your approval before touching files. Off by default;
	// toggle with /plan auto on|off. /build always overrides it for a turn.
	AutoPlan bool
	// QuotaFallback, when true, lets the agent automatically switch to the next
	// backend in ProviderFallbacks once the current provider's spend crosses
	// QuotaBudgetUSD / QuotaBudgetPct, or when the provider reports a hard daily
	// quota 429. Off by default: silently moving someone's traffic to another
	// backend (different models, different billing) must be opt-in.
	QuotaFallback bool
	// QuotaResetAt is the local wall-clock time of day ("HH:MM", e.g. "00:00") at
	// which the provider's quota rolls over. It's only a FALLBACK: providers that
	// report their own reset timestamp always win. Without it, a hard 429 carries
	// no reset time, so the session would stay on the fallback backend forever
	// instead of returning to the primary once the quota refills. Empty disables
	// the time-based restore.
	QuotaResetAt string
	// QuotaBudgetUSD is the absolute spend, in dollars, at or above which the
	// agent falls back to the next provider. 0 disables the absolute check.
	QuotaBudgetUSD float64
	// QuotaBudgetPct is the percentage (0-100) of the provider's OWN reported
	// budget at or above which the agent falls back. 0 disables the relative
	// check. Only usable for providers that report a spend cap in their usage
	// endpoint; others report no cap, so the percentage is unknowable there.
	QuotaBudgetPct float64
	// ProviderFallbacks is the ordered switch chain, e.g.
	// ["anthropic","openai","ollama"]. The agent walks it left to right from the
	// CURRENT provider, skipping entries that aren't usable on this machine (no
	// API key / no base URL), and stops at the end — there is no wrap-around,
	// since cycling back to an exhausted provider would thrash. Empty means "no
	// fallback configured".
	//
	// Entries may carry a model and reasoning level ("provider:model:reasoning");
	// ParseFallbackChain splits them. A bare provider id uses that backend's
	// default model, because model ids are not portable across backends.
	ProviderFallbacks []string
	// AutoDelegate, when true, makes the agent auto-delegate a clearly read-heavy
	// investigation ("how/where/why does X work", "investigate …", "check why …")
	// to the read-only `explore` sub-agent via the task tool, so the file reads
	// stay in the sub-agent's context instead of bloating (and re-billing) this
	// turn on every round. Requires the task tool; skipped in plan mode. OFF by
	// default (it changes how a turn is answered); enable with MN_AUTO_DELEGATE=1.
	AutoDelegate bool
	// AutoOffloadReadBytes is the cumulative size (in bytes) of read-style tool
	// output within a single turn past which the agent — even when the turn did
	// not initially look like an investigation — starts steering further
	// exploration into the read-only `explore` sub-agent, so the bulky reads live
	// in the sub-agent's (discarded) context instead of re-billing this turn on
	// every round. Gated by AutoDelegate; 0 disables. MN_AUTO_OFFLOAD_READ_BYTES
	// overrides.
	AutoOffloadReadBytes int
	// ConciseToolSchemas, when true, trims each tool's description to its first
	// sentence in the specs sent to the model (both Anthropic- and OpenAI-style
	// routes). Off by default since prompt caching already amortizes the full
	// descriptions; MN_CONCISE_TOOL_SCHEMAS=1 enables it for uncached/token-tight
	// setups.
	ConciseToolSchemas bool
	// EmbeddingsModel, when non-empty, enables hybrid semantic reranking of
	// search_code and recall results: the top BM25 candidates are re-scored by
	// embedding cosine similarity to the query and blended with their lexical
	// score. Empty disables it (pure BM25). Env: MN_EMBEDDINGS_MODEL.
	EmbeddingsModel string
	// EmbeddingsProvider selects which backend Embed calls, independent of the
	// session's main LLMProvider — so a session on e.g. Anthropic can still
	// point embeddings at OpenAI (or any other OpenAI-compatible provider)
	// without switching its main model. Empty (or "ollama") uses Ollama's
	// native {host}/v1/embeddings endpoint, matching the historical behavior;
	// Anthropic has no embeddings API and is rejected with a clear error.
	// Env: MN_EMBEDDINGS_PROVIDER.
	EmbeddingsProvider string

	// AutoUpdate, when true (default), lets a background daemon connector
	// (internal/daemon) self-update: it polls for the published version every few
	// minutes and, if behind, downloads the new build and re-execs onto it.
	// Set MN_AUTO_UPDATE=0/off to keep a machine pinned to its current build instead
	// (e.g. a locked-down or manually-managed host). A live terminal (TUI/web) is never
	// auto-updated either way — it only shows a notice and waits for /update, because
	// swapping the process under someone who is watching would drop their turn.
	// Editable via the Persona modal (AutoUpdatePersona).
	AutoUpdate bool
	// AutoUpdatePersona remembers an explicit UI choice for AutoUpdate.
	AutoUpdatePersona *bool

	// ImageProvider selects which backend serves generate_image, independent of the
	// session's chat provider. Image generation needs an OpenAI-compatible /chat/
	// completions route that returns image parts, and few backends have one — so the
	// session can be on a local or text-only provider while images come from wherever
	// they actually work. Empty inherits the session provider.
	// Env: MN_IMAGE_PROVIDER.
	ImageProvider string

	// ImageProviderPersona remembers an explicit UI/persona choice for
	// ImageProvider, so it survives a restart and is distinguishable from "unset".
	ImageProviderPersona *string
	// CompactThresholdTokens is the estimated history size that triggers
	// context compaction before a turn.
	CompactThresholdTokens int
	// CompactKeepTokens is roughly how much recent history to retain when
	// compacting older messages into a summary.
	CompactKeepTokens int
	// ToolResultKeepRounds is how many of the most recent tool-result-carrying
	// messages keep their full output in history. Older tool results are elided
	// to a short stub in place, so large Read/Grep/Bash outputs from earlier in
	// a turn stop being re-sent verbatim on every subsequent model call (the
	// dominant cost of long tool loops). 0 disables the pruning. MN_TOOL_RESULT_KEEP overrides.
	ToolResultKeepRounds int
	// MaxReadLines is the default line cap for read_file when no explicit limit
	// is given. Smaller windows keep each read cheaper (less re-sent context) at
	// the cost of more paged reads on large files. MN_READ_LINES overrides.
	MaxReadLines int
	// RelayAddr is the host:port of a relay to auto-connect to in the
	// background at startup (so a remote browser can view/drive this terminal
	// session). Defaults to defaultRelayAddr (on); disable with `/relay off` or
	// MN_RELAY=off. Empty at runtime means disabled. MN_RELAY overrides the
	// saved preference.
	RelayAddr string
	// ContextWindow, when > 0, overrides the per-model context-window size used
	// for the "ctx N%" indicator (e.g. set 1_000_000 for a 1M-token model whose
	// stable default is lower). 0 means auto (config.ContextWindow(model)).
	// Saved as a preference; MN_CONTEXT_WINDOW overrides.
	ContextWindow int

	// TaskBoardMaxParallel caps how many web Task Board tasks run at once.
	// Set via MN_TASKBOARD_MAX_PARALLEL; 0 means the default (2).
	TaskBoardMaxParallel int

	// ImageModel, when non-empty, enables the generate_image tool: the agent can
	// generate/edit images with this image-output model (OpenAI-compatible route).
	// Set via MN_IMAGE_MODEL. Empty leaves the tool unregistered.
	ImageModel string
	// VideoModel, when non-empty, enables the generate_video tool with this
	// video-output model (e.g. a Veo model on the native Gemini route). Set via
	// MN_VIDEO_MODEL. Empty leaves the tool unregistered.
	VideoModel string

	// CodebaseMap, when true, keeps the generated structural section of
	// CODEBASE_MAP.md fresh on every codebase change and injects the map
	// (pinned + narrative + directory digest) into the system prompt. Also
	// enabled implicitly when a CODEBASE_MAP.md already exists at the project
	// root. MN_CODEBASE_MAP=0 forces it off.
	CodebaseMap bool
	// CodebaseMapPromptBytes caps how much of the codebase map is injected into
	// the system prompt (0 = the built-in default). MN_CODEBASE_MAP_BYTES.
	CodebaseMapPromptBytes int

	// RAGIndexDir overrides where the per-workspace semantic_search vector index
	// is stored (default <root>/.sqe/rag/index.gob). Set via MN_RAG_INDEX_DIR —
	// e.g. to share one index across git worktrees of the same repo.
	RAGIndexDir string

	// SubagentMaxDepth caps how deep the task-tool delegation tree may go: 1 = only
	// one level of sub-agents (no nesting). Default 2; MN_MAX_SUBAGENT_DEPTH
	// overrides, clamped to [1, SubagentDepthCeiling].
	SubagentMaxDepth int

	// AutoTestGen, when true, drafts unit tests for the source files a turn
	// changed and surfaces them as suggestions (nothing is written to the tree).
	// Opt-in via MN_AUTO_TESTGEN; see internal/agent/testgen.go.
	AutoTestGen bool
	// AutoTestGenMax caps how many files are drafted per turn under AutoTestGen
	// (MN_AUTO_TESTGEN_MAX; default defaultTestgenMax).
	AutoTestGenMax int
}

// Version is the moony release version. It's the single source of
// truth: surfaced by `--version`, and the TUI header.
// Keep it in sync with the top entry of CHANGELOG.md.
// SubagentDepthCeiling is the hard upper bound on task-tool delegation depth,
// regardless of MN_MAX_SUBAGENT_DEPTH, to cap worst-case fan-out cost/recursion.
const SubagentDepthCeiling = 4

const Version = "2.17.2"

// VersionLess reports whether semantic version a is strictly older than b, where
// each is "x.y.z" (an optional leading "v" is tolerated). It returns false when
// either side can't be parsed as three numeric components, so callers never
// raise a spurious "you're behind" on an unexpected string. Mirrors the web UI's
// versionLt() so the terminal and browser agree on what "behind" means.
func VersionLess(a, b string) bool {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

// parseSemver splits "x.y.z" (optional leading "v") into three non-negative ints.
func parseSemver(v string) ([3]int, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// DefaultModel is the model a fresh install starts with (Ollama's default;
// see internal/provider.OllamaDefaultModel).
const DefaultModel = "llama3.1:8b"

// defaultRelayAddr is the relay this terminal auto-connects to when the user has
// never configured one (no saved relay_addr and no MN_RELAY). Disable with
// `/relay off` (persisted) or MN_RELAY=off.
const defaultRelayAddr = "127.0.0.1:8771"

// Defaults for the enterprise cloud providers whose base URL/region aren't a
// single fixed host.
const (
	// defaultAzureAPIVersion is a recent stable Azure OpenAI data-plane version.
	defaultAzureAPIVersion = "2024-10-21"
	// defaultVertexLocation is the Vertex AI region used when none is set.
	defaultVertexLocation = "us-central1"
	// defaultAWSRegion is the AWS region used for Bedrock when none is set.
	defaultAWSRegion = "us-east-1"
)

// DefaultSQEAnthropicBaseURL is the SQE LAN gateway the "sqe-anthropic" provider
// targets by default (Bedrock-style proxy in front of Claude). Override with
// MN_SQE_ANTHROPIC_BASE_URL.
const DefaultSQEAnthropicBaseURL = "http://192.168.33.158:8787"

// Wire protocols. Anthropic's Messages API and everything else's
// OpenAI-compatible Chat Completions API. Derived 1:1 from LLMProvider (see
// SetModel / Load) — kept as a field/consts for callers that key display or
// reasoning behavior off it.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// wireRouteFor returns the wire protocol for a backend provider: Anthropic
// speaks its native Messages API, everything else (Ollama, OpenAI, OpenRouter)
// speaks OpenAI-compatible Chat Completions.
func wireRouteFor(llmProvider string) string {
	if provider.UsesAnthropicWire(llmProvider) {
		return ProviderAnthropic
	}
	return ProviderOpenAI
}

// IsOpenAIReasoningModel reports whether a model id is one of OpenAI's
// reasoning-model families (o1/o3/o4-mini, gpt-5, …), which are the only
// OpenAI models that accept a reasoning_effort parameter — every other OpenAI
// model (gpt-4o, gpt-4.1, …) errors on it.
func IsOpenAIReasoningModel(model string) bool {
	m := strings.ToLower(model)
	for _, prefix := range []string{"o1", "o3", "o4", "gpt-5"} {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

// firstEnv returns the value of the first set, non-empty environment variable.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// lookupFirstEnv returns the first environment variable that is SET, even when
// its value is empty, along with whether one was found.
//
// firstEnv can't express "set to empty on purpose", which some knobs need: for
// MN_QUOTA_RESET_AT an empty value means "disable the time-based restore", not
// "unset", so it must beat the built-in default.
func lookupFirstEnv(names ...string) (string, bool) {
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok {
			return v, true
		}
	}
	return "", false
}

// NormalizeOllamaBaseURL turns a user-supplied Ollama host into the OpenAI-
// compatible base URL the client posts to. It accepts a bare host
// ("host:11434", "http://host:29248"), a host already ending in "/v1", or a full
// URL, and always returns "<scheme>://<host>[/path]/v1" (no trailing slash).
// A scheme defaults to http:// when omitted.
func NormalizeOllamaBaseURL(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if !strings.Contains(h, "://") {
		h = "http://" + h
	}
	h = strings.TrimRight(h, "/")
	if !strings.HasSuffix(h, "/v1") {
		h += "/v1"
	}
	return h
}

// SetModel selects a model and keeps the derived route and the active reasoning
// level in sync with it. Use this everywhere a model is chosen (/model, the
// models menu, CLI flag) so the endpoint always matches the model id.
func (c *Config) SetModel(model string) {
	c.Model = model
	c.Provider = wireRouteFor(c.LLMProvider)
	c.ReasoningLevel = c.GetReasoningFor(model)
}

// SetLLMProvider switches the backend provider and re-derives the wire route
// with it. Assigning LLMProvider on its own leaves Provider describing the
// PREVIOUS backend's API family, which is what the status bar and the quota
// panel read -- so the UI ends up naming one provider while requests are shaped
// for another. Always change the two together.
func (c *Config) SetLLMProvider(id string) {
	c.LLMProvider = provider.Normalize(id)
	c.Provider = wireRouteFor(c.LLMProvider)
}

// AuxDefaultModel is the model tool-less auxiliary calls run on by default — the
// goal judge, session titling, checkpoint distillation, history compaction,
// profile refresh, self-review, and "btw" notes. Those calls want a quick
// structured answer, not deliberation, so they use the cheapest capable tier
// (Haiku, $1/$5 vs the session model's Opus $5/$25 or Sonnet $3/$15). It is
// deliberately cheaper than FastModel's Sonnet default: read-only sub-agents and
// the adaptive main turn do real investigation/coding work, whereas these calls
// are mechanical. MN_AUX_MODEL (falling back to MN_FAST_MODEL) overrides.
const AuxDefaultModel = "llama3.1:8b"

// FastDefaultModel is the model FastModel uses when MN_FAST_MODEL isn't
// pinned — a capable, cheaper Sonnet tier than the session's default Opus/
// Sonnet-4.6. Kept distinct from DefaultModel (the overall session default)
// so bumping one doesn't silently change the other.
const FastDefaultModel = "llama3.1:8b"

// FastModel returns the cheaper "fast" model to run grunt work on — read-only
// sub-agents (explore/code-reviewer) and the adaptive-mode main turn — so they
// don't burn the (often pricier) session model. Defaults to FastDefaultModel (a
// capable, cheaper Sonnet); MN_FAST_MODEL overrides. The more mechanical
// auxiliary calls use the even-cheaper AuxModel instead. It never UPGRADES a
// parent that's already as cheap (by input price), so a session already on
// Haiku/Sonnet keeps its own model.
//
// llmProvider is the active backend (see internal/provider). With no explicit
// fast-model pin, the built-in default id may be the wrong server's model —
// substituting it would send e.g. a Claude id to an Ollama server (404) — so the
// session model is kept unless the user explicitly pins one with MN_FAST_MODEL.
func FastModel(parent, llmProvider string) string {
	return cheapModel(parent, llmProvider, FastDefaultModel, "MN_FAST_MODEL")
}

// AuxModel returns the cheap model for tool-less auxiliary calls (see
// AuxDefaultModel). It applies the same safety rules as FastModel — never
// upgrading an already-cheaper parent, and keeping the session model unless a
// fast/aux model is explicitly pinned — and honors MN_AUX_MODEL first, then
// MN_FAST_MODEL, so a single fast-model pin covers both.
func AuxModel(parent, llmProvider string) string {
	return cheapModel(parent, llmProvider, AuxDefaultModel, "MN_AUX_MODEL", "MN_FAST_MODEL")
}

// cheapModel resolves a cheaper model to offload work from `parent` onto: it
// returns the first non-empty env override named in `envVars` unless doing so
// would UPGRADE a parent already as cheap by input price. With no override
// pinned it keeps `parent`, since the built-in `def` id may not be served by the
// active provider (e.g. a cloud id on an Ollama server).
func cheapModel(parent, llmProvider, def string, envVars ...string) string {
	var v string
	for _, e := range envVars {
		if v = strings.TrimSpace(os.Getenv(e)); v != "" {
			break
		}
	}
	if v == "" {
		// No override pinned: keep the parent model rather than substituting a
		// default id that the active provider may not serve.
		return parent
	}
	if v != "" {
		def = v
	}
	if def == parent {
		return parent
	}
	if pp, ok := PriceFor(parent); ok {
		if dp, ok2 := PriceFor(def); ok2 && pp.InputUSDPer1M <= dp.InputUSDPer1M {
			return parent // parent already costs the same or less per input token
		}
	}
	return def
}

// firstTrimmedEnv returns the first non-empty, trimmed value among the named env
// vars, or "" when none is set.
func firstTrimmedEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// ProvidersEqual reports whether two provider ids normalize to the same backend
// route (e.g. "sqe-anthropic" and "anthropic" are distinct ids but share the
// Anthropic wire — they are NOT equal here, since they use different base URLs).
func ProvidersEqual(a, b string) bool { return provider.Normalize(a) == provider.Normalize(b) }

// normalizedProviderEnv returns the normalized provider id from the first
// non-empty env var among names, or "" when unset or unknown. An unknown id
// yields "" (inherit) rather than an error, so a stale/typo'd value fails safe.
func normalizedProviderEnv(names ...string) string {
	v := firstTrimmedEnv(names...)
	if v == "" {
		return ""
	}
	if _, ok := provider.Lookup(v); !ok {
		return ""
	}
	return provider.Normalize(v)
}

// FastProvider is the provider the Fast tier (adaptive/cheap-reads downshift,
// read-only sub-agents, workflow judge) runs on. "" = inherit the session
// provider (MN_FAST_PROVIDER).
func FastProvider() string { return normalizedProviderEnv("MN_FAST_PROVIDER") }

// AuxProvider is the provider the Self-review (aux) tier runs on. It falls back
// to MN_FAST_PROVIDER, so a single "route my cheap work to X" pin covers both.
// "" = inherit the session provider.
func AuxProvider() string { return normalizedProviderEnv("MN_AUX_PROVIDER", "MN_FAST_PROVIDER") }

// ImageProviderFor reports which backend should serve generate_image: the explicit
// override when set, otherwise the session's own provider.
//
// Returns "" when there is no override, so callers can keep their existing
// session-provider path untouched rather than re-deriving it here.
func (c *Config) ImageProviderFor() string {
	if c == nil {
		return ""
	}
	// Validate with Lookup, not Normalize: Normalize resolves an unknown id to the
	// DEFAULT provider, so a typo would silently route images somewhere the user never
	// named — and report itself as a working override.
	raw := strings.TrimSpace(c.ImageProvider)
	if _, ok := provider.Lookup(raw); !ok {
		return ""
	}
	ov := provider.Normalize(raw)
	if ov == "" || strings.EqualFold(ov, provider.Normalize(c.LLMProvider)) {
		return "" // nothing to override
	}
	return ov
}

// reasoningEnv returns the normalized reasoning level from the named env var, or
// "" (inherit) when it is unset. A set-but-unrecognized value normalizes to
// "off" (NormalizeReasoning's safe default), not inherit.
func reasoningEnv(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return ""
	}
	return NormalizeReasoning(v)
}

// FastReasoning is the reasoning level for Fast-tier rounds. "" = inherit the
// turn's computed effort (MN_FAST_REASONING).
func FastReasoning() string { return reasoningEnv("MN_FAST_REASONING") }

// AuxReasoning is the reasoning level for Self-review (aux) calls. "" keeps aux's
// default of reasoning OFF (a quick, cheap answer); it deliberately does NOT fall
// back to MN_FAST_REASONING, so a fast-tier level can't silently raise aux
// cost (MN_AUX_REASONING).
func AuxReasoning() string { return reasoningEnv("MN_AUX_REASONING") }

// FastModelRoute resolves where a Fast-tier downshift runs: the model id, a
// provider override ("" = inherit the session provider), and whether this route
// differs from the session at all. differs==false means there is nothing to
// change (no cheaper model and no cross-provider override) — the caller must NOT
// downshift, matching today's no-op.
//
// When a Fast provider is explicitly set AND differs from the session provider,
// the cheapModel guards (keep-parent-when-unpinned, never-upgrade-a-cheaper-
// parent) are bypassed: those guards assume the same backend as the session, and
// returning the parent model would send a session-provider id to a different
// provider (404). Instead the pinned Fast model is used, or that provider's
// default when unpinned. Otherwise this is exactly FastModel with guards intact.
func FastModelRoute(parentModel, sessionProvider string) (model, providerOverride string, differs bool) {
	if fp := FastProvider(); fp != "" && !ProvidersEqual(fp, sessionProvider) {
		m := firstTrimmedEnv("MN_FAST_MODEL")
		if m == "" {
			m = provider.DefaultModelFor(fp)
		}
		return m, fp, true
	}
	m := FastModel(parentModel, sessionProvider)
	return m, "", m != parentModel
}

// AuxModelRoute mirrors FastModelRoute for the Self-review (aux) tier: the aux
// model/provider fallbacks apply (aux model falls back to MN_FAST_MODEL, aux
// provider to MN_FAST_PROVIDER). differs is returned for symmetry; aux callers
// generally ignore it since streamAux always runs.
func AuxModelRoute(parentModel, sessionProvider string) (model, providerOverride string, differs bool) {
	if ap := AuxProvider(); ap != "" && !ProvidersEqual(ap, sessionProvider) {
		m := firstTrimmedEnv("MN_AUX_MODEL", "MN_FAST_MODEL")
		if m == "" {
			m = provider.DefaultModelFor(ap)
		}
		return m, ap, true
	}
	m := AuxModel(parentModel, sessionProvider)
	return m, "", m != parentModel
}

// FastModelResolved and AuxModelResolved return the (model, provider) pair a
// downshifted / auxiliary call should use. When the provider is inherited (or
// explicitly equals the session backend) the model is resolved exactly as
// FastModel/AuxModel — unchanged behavior. When routed to a DIFFERENT backend,
// the session model id is meaningless there, so an explicit MN_FAST_MODEL /
// MN_AUX_MODEL pin wins, otherwise the target provider's own default model.
func (c *Config) FastModelResolved() (string, string) {
	prov := FastProvider()
	if prov == "" || prov == provider.Normalize(c.LLMProvider) {
		return FastModel(c.Model, c.LLMProvider), provider.Normalize(c.LLMProvider)
	}
	if v := firstTrimmedEnv("MN_FAST_MODEL"); v != "" {
		return v, prov
	}
	return provider.DefaultModelFor(prov), prov
}

func (c *Config) AuxModelResolved() (string, string) {
	prov := AuxProvider()
	if prov == "" || prov == provider.Normalize(c.LLMProvider) {
		return AuxModel(c.Model, c.LLMProvider), provider.Normalize(c.LLMProvider)
	}
	if v := firstTrimmedEnv("MN_AUX_MODEL"); v != "" {
		return v, prov
	}
	if fp := FastProvider(); fp == prov { // fast model fallback only if on same provider
		if v := firstTrimmedEnv("MN_FAST_MODEL"); v != "" {
			return v, prov
		}
	}
	return provider.DefaultModelFor(prov), prov
}

// ProviderLabel is the user-facing name of a route, e.g. "anthropic-native".
func ProviderLabel(p string) string {
	if p == ProviderOpenAI {
		return "openai-compatible"
	}
	return "anthropic-native"
}

// Reasoning (extended thinking) levels. Not every model supports every level —
// see ReasoningLevelsFor.
const (
	ReasoningOff     = "off"
	ReasoningMinimal = "minimal"
	ReasoningLow     = "low"
	ReasoningMedium  = "medium"
	ReasoningHigh    = "high"
	ReasoningXHigh   = "xhigh"
	ReasoningMax     = "max"
	// ReasoningAuto is a meta-level: keep adaptive thinking, but let the agent
	// pick the effort ceiling per turn (generous — a high baseline that adaptive
	// scales down on easy turns, bumped to the model's max after a hard turn). It
	// never reaches the API as "auto"; it's resolved to a concrete effort (see
	// EffortFor / the agent's per-turn override).
	ReasoningAuto = "auto"
)

// autoBaseline is the level "auto" falls back to before any per-turn signal: OFF
// by default, so adaptive thinking stays off on ordinary turns and the agent only
// escalates (to high) when the previous turn was hard or the prompt asks for it.
// Set /reasoning low|high|max for an always-on thinking budget.
const autoBaseline = ReasoningOff

// ReasoningLevels lists the default selectable levels in order. Most callers
// should use ReasoningLevelsFor to get the set a specific model supports.
var ReasoningLevels = []string{ReasoningOff, ReasoningLow, ReasoningMedium, ReasoningHigh}

// reasoningOrder ranks levels by effort, low to high (off = none). Used to clamp
// a level to the nearest one a model supports.
var reasoningOrder = map[string]int{
	ReasoningOff: 0, ReasoningMinimal: 1, ReasoningLow: 2, ReasoningMedium: 3,
	ReasoningHigh: 4, ReasoningXHigh: 5, ReasoningMax: 6,
}

func isGeminiModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "gcp:") || strings.Contains(m, "gemini")
}

func isOpusModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "opus")
}

// ReasoningLevelsFor returns the reasoning levels a model supports, in order.
// Model families differ: Anthropic Opus exposes the full range up to max, Gemini
// adds a minimal tier but stops at high, and everything else uses the common
// low/medium/high set.
func ReasoningLevelsFor(model string) []string {
	switch {
	case isGeminiModel(model):
		return []string{ReasoningOff, ReasoningAuto, ReasoningMinimal, ReasoningLow, ReasoningMedium, ReasoningHigh}
	case isOpusModel(model):
		return []string{ReasoningOff, ReasoningAuto, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax}
	default:
		return []string{ReasoningOff, ReasoningAuto, ReasoningLow, ReasoningMedium, ReasoningHigh}
	}
}

// ReasoningDescription returns a short human description of a level.
func ReasoningDescription(level string) string {
	switch NormalizeReasoning(level) {
	case ReasoningAuto:
		return "adaptive — off by default, escalates on hard turns"
	case ReasoningMinimal:
		return "minimal reasoning"
	case ReasoningLow:
		return "brief reasoning"
	case ReasoningMedium:
		return "balanced reasoning"
	case ReasoningHigh:
		return "deep reasoning"
	case ReasoningXHigh:
		return "very deep reasoning"
	case ReasoningMax:
		return "maximum reasoning"
	default:
		return "no extended thinking"
	}
}

// ReasoningBudget maps a reasoning level to its thinking token budget. A budget
// of 0 means extended thinking is disabled. Budgets must be >= 1024 per the
// Anthropic API. (Transmission uses the effort label; this is a sizing hint.)
func ReasoningBudget(level string) int {
	switch NormalizeReasoning(level) {
	case ReasoningAuto:
		return 24000 // representative (its baseline ceiling); the real budget is chosen per turn
	case ReasoningMinimal:
		return 1024
	case ReasoningLow:
		return 4000
	case ReasoningMedium:
		return 10000
	case ReasoningHigh:
		return 24000
	case ReasoningXHigh:
		return 48000
	case ReasoningMax:
		return 64000
	default:
		return 0
	}
}

// NormalizeReasoning canonicalizes a level string (accepting a few aliases),
// defaulting to off for anything unrecognized.
func NormalizeReasoning(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case ReasoningMinimal, "min":
		return ReasoningMinimal
	case ReasoningLow:
		return ReasoningLow
	case ReasoningMedium, "med":
		return ReasoningMedium
	case ReasoningHigh:
		return ReasoningHigh
	case ReasoningXHigh, "x-high", "x_high", "extra-high":
		return ReasoningXHigh
	case ReasoningMax, "maximum", "ultra", "ultrathink":
		return ReasoningMax
	case ReasoningAuto, "automatic":
		return ReasoningAuto
	default:
		return ReasoningOff
	}
}

// ReasoningRank returns a level's effort rank (off = 0, max = highest), for
// comparing and clamping levels.
func ReasoningRank(level string) int { return reasoningOrder[NormalizeReasoning(level)] }

// HigherReasoning returns whichever of two levels has the greater effort rank
// (canonicalized). Used to escalate without ever lowering the configured level.
func HigherReasoning(a, b string) string {
	if ReasoningRank(a) >= ReasoningRank(b) {
		return NormalizeReasoning(a)
	}
	return NormalizeReasoning(b)
}

// LowerReasoning returns whichever of two levels has the lesser effort rank
// (canonicalized). Used by dynamic effort routing to cap a step's effort at a
// passive floor without ever raising it.
func LowerReasoning(a, b string) string {
	if ReasoningRank(a) <= ReasoningRank(b) {
		return NormalizeReasoning(a)
	}
	return NormalizeReasoning(b)
}

// ReasoningKeyword detects a Claude-Code-style in-prompt thinking escalator in a
// user's message and returns the reasoning level it requests, or "" for none.
// The ladder: "ultrathink" → maximum; "think harder" / "think hard" / "megathink"
// → high. It's deliberately conservative (no bare "think") to avoid escalating on
// casual phrasing like "I think …". The caller clamps the result to what the
// model supports (NormalizeReasoningFor).
func ReasoningKeyword(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "think harder") || strings.Contains(t, "think hard") || strings.Contains(t, "megathink"):
		return ReasoningHigh
	default:
		return ""
	}
}

// Predictive "auto" reasoning cues, scanned from the user's message before the
// turn runs. High-precision on purpose: over-matching spends thinking tokens on
// simple turns, so the bar for "high" is clear reasoning/debugging/design intent.
var (
	// Strong cues that the turn needs deliberate reasoning → high. The
	// debug/"broken" family is included because forming a hypothesis benefits from
	// thinking — and a no-thinking turn is exactly where debugging tends to flail.
	autoHardRe = regexp.MustCompile(`(?i)\b(why|debug|diagnose|root[\s-]?cause|design|architect(ure)?|investigat\w+|trade[\s-]?offs?|refactor\w*|race[\s-]?condition|deadlock|concurren\w+|optimi[sz]\w+|algorithm|analy[sz]\w+|figure out|reason about|doesn'?t work|isn'?t working|not working|broken|stack ?trace|traceback|panic|crash\w*|regression|bugs?)\b`)
	// Trivial affirmations / continuations → off.
	autoTrivialRe = regexp.MustCompile(`(?i)^\s*(y(es|ep|eah)?|no|nope|ok(ay)?|sure|continue|go( on| ahead)?|proceed|do it|thx|thanks|thank you|next|done|stop)\b[\s.!]*$`)
	// Simple mechanical edits → off.
	autoMechanicalRe = regexp.MustCompile(`(?i)^\s*(rename|re-?format|format|add an? ?comment|remove (the )?comment|fix (the )?typo|bump|re-?indent|indent|lint|gofmt|prettier|tidy)\b`)
)

// PromptReasoningHint predicts the reasoning effort a turn needs from the user's
// message alone — no model call — for the "auto" level: "high" on clear
// reasoning/debugging/design cues, and "medium" as the default for any real
// request. Only CONFIDENTLY trivial turns (empty, an affirmation like
// "yes"/"continue", or a mechanical edit like "fix the typo"/"rename") get "off".
//
// The "medium" default is a generous CEILING, not a forced level: with adaptive
// thinking the model self-regulates DOWN within it (an easy step thinks little),
// while the ceiling gives a hard-but-plainly-phrased turn room to think deeply from
// its FIRST step — rather than being capped at "low" until friction accrues. The
// passive-step dampener trims routine continuation steps back to low, and the
// friction / model-thinking floors (see autoReasoningEffort / modelThinkFloor) take
// it to "high" when a turn proves hard. Mechanical/trivial cues win over a stray
// keyword (e.g. "fix the typo in the debug helper" stays off).
func PromptReasoningHint(userInput string) string {
	s := strings.TrimSpace(userInput)
	if s == "" || autoTrivialRe.MatchString(s) || autoMechanicalRe.MatchString(s) {
		return ReasoningOff // confidently trivial/mechanical — no thinking
	}
	if autoHardRe.MatchString(s) {
		return ReasoningHigh
	}
	// Everything else (a real, non-trivial request) gets a MEDIUM ceiling: enough
	// headroom for the model to think deeply on its first step when the turn is hard,
	// while adaptive thinking keeps easy steps cheap and the passive dampener trims
	// routine ones. Questions and long multi-part requests land here too.
	return ReasoningMedium
}

// investigationRe matches a read-heavy "understand how/where/why the code works"
// request — the kind best delegated to the read-only explore sub-agent (so its
// file reads stay in its own context). It catches diagnostic phrasings broadly:
// any message opening with "why", a "why … <can't|isn't|doesn't|not|…>" anywhere
// in the sentence (so a fronted clause like "why on the web UI can't I see …"
// still matches), and symptom reports that never say "why" ("can't see …",
// "isn't displayed", "shows no …"). It still does NOT match plain build/design/
// edit intent ("design …", "refactor …", "add …"), which wants the main agent's
// full context. A rare false positive (e.g. "why don't we refactor X") only costs
// a read-only explore pass whose findings the main agent continues from, so we
// bias toward recall. The [^.?!]* spans are sentence-bounded to avoid reaching
// across clauses into unrelated text.
var investigationRe = regexp.MustCompile(`(?i)(` +
	`\bcheck (why|how|where|what)\b|` +
	`\bhow (do|does|is|are|can|to)\b|` +
	`\bwhere (is|are|does|do|in)\b|` +
	`^\s*why\b|` +
	`\bwhy\b[^.?!]*\b(can'?t|cannot|can ?not|isn'?t|doesn'?t|don'?t|won'?t|do|does|did|is|are|not|fail|wrong|empty|missing|blank)\b|` +
	`\b(can'?t|cannot|can ?not)\b[^.?!]*\b(see|find|access|view|open|load)\b|` +
	`\b(isn'?t|aren'?t|not|never) (show|showing|shown|display|displaying|displayed|appear|appearing|visible|work|working|loading|rendering)\b|` +
	`\b(doesn'?t|don'?t|won'?t|didn'?t) (show|display|appear|work|load|render)\b|` +
	`\bshows? (no|nothing|empty|blank)\b|` +
	`\bwhat (calls|uses|triggers|happens|causes)\b|` +
	`\binvestigate\b|\bdiagnose\b|\bdebug\b|\broot[\s-]?cause\b|` +
	`\btrace (the|this|through|how|where)\b|` +
	`\bexplain how\b|\bwalk me through\b|\bfigure out\b|` +
	`\bunderstand (how|why|the|this)\b|\bfind where\b|\blook into\b` +
	`)`)

// LooksLikeInvestigation reports whether a user message is a read-heavy
// investigation (any non-none scope). See ClassifyInvestigation and the
// auto-delegate steer in the agent.
func LooksLikeInvestigation(userInput string) bool {
	return ClassifyInvestigation(userInput) != InvestigationNone
}

// InvestigationScope classifies a detected investigation by how much of the
// codebase it likely spans, so the agent can right-size delegation (see the
// auto-delegate branch in agent.go): read a NARROW one inline (one named target is
// cheaper to read than a sub-agent round-trip), delegate a NORMAL one to a single
// explore sub-agent, and fan a BROAD one out to several parallel explore agents.
type InvestigationScope int

const (
	InvestigationNone   InvestigationScope = iota // not a read-heavy investigation
	InvestigationNarrow                           // one concrete named target — read inline
	InvestigationNormal                           // typical — delegate to one explore agent
	InvestigationBroad                            // wide/multi-facet — fan out parallel explore agents
)

// investigationBroadRe flags a WIDE investigation — one spanning many files or the
// whole system — where decomposing and fanning out parallel explore agents beats a
// single pass.
var investigationBroadRe = regexp.MustCompile(`(?i)\b(` +
	`across|throughout|whole|entire|overall|holistic|` +
	`end[\s-]?to[\s-]?end|everywhere|architecture|code ?base|` +
	`all (the|of|these|those)|every (file|place|caller|usage|use)|` +
	`(full|entire|whole) (flow|picture|lifecycle|pipeline|path)` +
	`)\b`)

// investigationTargetRe matches ONE concrete target — a `code` symbol (backticked)
// or a file path (name.ext). Exactly one such target in a short, single-clause
// message marks a NARROW investigation the main agent can just read inline.
var investigationTargetRe = regexp.MustCompile("`[^`]+`|" +
	`\b[\w./-]+\.(go|js|jsx|ts|tsx|css|py|rb|java|json|ya?ml|md|sh|txt)\b`)

// investigationNarrowMaxLen bounds a NARROW investigation's message length: a long
// request, even one naming a single file, usually spans more than a quick read.
const investigationNarrowMaxLen = 120

// ClassifyInvestigation reports the scope of a read-heavy investigation, or
// InvestigationNone when the message isn't one. It refines investigationRe: BROAD
// on explicit wide-scope cues or several questions in one message; NARROW when a
// short, single-clause, single-question message names exactly one concrete target
// (a file or `symbol`); NORMAL otherwise. Ambiguous investigations fall to NORMAL —
// today's single-delegate behavior — so a misread only nudges toward the safe
// default (and NARROW's fallback is just reading inline, the pre-feature behavior).
func ClassifyInvestigation(userInput string) InvestigationScope {
	s := strings.TrimSpace(userInput)
	if !investigationRe.MatchString(s) {
		return InvestigationNone
	}
	if investigationBroadRe.MatchString(s) || strings.Count(s, "?") >= 2 {
		return InvestigationBroad
	}
	if len(s) <= investigationNarrowMaxLen &&
		strings.Count(s, "?") <= 1 &&
		!strings.Contains(strings.ToLower(s), " and ") &&
		len(investigationTargetRe.FindAllString(s, 2)) == 1 {
		return InvestigationNarrow
	}
	return InvestigationNormal
}

// start of the prompt or after whitespace (so it's an explicit invocation, not
// part of a word or an address like foo@ultrathink.example). It also eats one
// trailing space so stripping the token doesn't leave a double space mid-prompt.
var ultrathinkMention = regexp.MustCompile(`(?i)(^|\s)@ultrathink\b[ \t]?`)

// IsUltrathink reports whether the message invokes "@ultrathink" — the deepest
// mode, which maxes out thinking AND fans out parallel sub-agents to deep-dive
// the request before answering. Triggered ONLY by the @-mention, never the bare
// word.
func IsUltrathink(text string) bool { return ultrathinkMention.MatchString(text) }

// StripUltrathink removes the "@ultrathink" trigger token so the model receives
// a clean request (the deep-dive behavior is driven separately, not by the model
// reading the token).
func StripUltrathink(text string) string {
	return strings.TrimSpace(ultrathinkMention.ReplaceAllString(text, "$1"))
}

// IsAuto reports whether a level is the "auto" meta-level (the agent picks the
// per-turn effort).
func IsAuto(level string) bool { return NormalizeReasoning(level) == ReasoningAuto }

// EffortFor resolves a reasoning level to the concrete effort string sent to the
// model API. "auto" never goes over the wire — it resolves to its generous
// baseline (clamped to the model); other levels clamp to the model's range. The
// agent overrides this per turn for "auto" via a context effort (see the agent's
// auto/keyword handling); EffortFor is the fallback used when no override is set.
func EffortFor(model, level string) string {
	if IsAuto(level) {
		return NormalizeReasoningFor(model, autoBaseline)
	}
	return NormalizeReasoningFor(model, level)
}

// WireEffort resolves a reasoning level to the exact output_config.effort string
// to transmit for this provider/model, or "" when no thinking/effort should be
// sent. It is the single source of truth for the wire value: it resolves "auto"
// to its baseline, clamps to the model's supported range, and returns "" for
// "off"/unsupported. Gating on the *resolved* effort (not the raw level) is what
// prevents sending effort:"off" — the gateway's effort field accepts only
// low|medium|high|xhigh|max, so "off"/"auto" must never reach it.
func WireEffort(provider, model, level string) string {
	eff := EffortFor(model, level) // auto→baseline, clamped; may be "off"
	if !ReasoningActive(provider, model, eff) {
		return ""
	}
	return eff
}

// NormalizeReasoningFor canonicalizes a level and clamps it to what the model
// supports: a level above the model's max drops to its highest, and one below
// its minimum rises to its lowest, so switching models never silently disables
// thinking.
func NormalizeReasoningFor(model, level string) string {
	lvl := NormalizeReasoning(level)
	if lvl == ReasoningOff {
		return ReasoningOff
	}
	if lvl == ReasoningAuto {
		return ReasoningAuto // meta-level: valid on any thinking model, resolved per turn
	}
	valid := ReasoningLevelsFor(model)
	want := reasoningOrder[lvl]
	best, bestRank := ReasoningOff, -1
	var lowest string
	for _, v := range valid {
		if v == ReasoningOff || v == ReasoningAuto {
			continue // skip the off/auto meta-entries when clamping a concrete level
		}
		if lowest == "" {
			lowest = v
		}
		if r := reasoningOrder[v]; r <= want && r > bestRank {
			best, bestRank = v, r
		}
	}
	if best == ReasoningOff {
		return lowest // requested below the model's minimum → its lowest tier
	}
	return best
}

// GetReasoningFor returns the remembered reasoning level for the given model
// id, falling back to the legacy single-value ReasoningLevel when no per-model
// entry has been recorded yet, and finally to "off".
func (c *Config) GetReasoningFor(model string) string {
	if c == nil {
		return ReasoningOff
	}
	if lvl, ok := c.ReasoningByModel[model]; ok {
		return NormalizeReasoningFor(model, lvl)
	}
	return NormalizeReasoningFor(model, c.ReasoningLevel)
}

// SetReasoningFor records the reasoning level for the given model id and
// updates the active ReasoningLevel to match. The change is persisted by the
// caller via Config.Save.
func (c *Config) SetReasoningFor(model, level string) {
	if c == nil {
		return
	}
	lvl := NormalizeReasoningFor(model, level)
	if c.ReasoningByModel == nil {
		c.ReasoningByModel = map[string]string{}
	}
	c.ReasoningByModel[model] = lvl
	if model == c.Model {
		c.ReasoningLevel = lvl
	}
}

// ReasoningActive reports whether the current reasoning level actually
// activates extended thinking for the given backend + model. Some hosted APIs
// error if a reasoning parameter is sent to a model that doesn't support it, so
// providers with a narrow reasoning-model family are gated to just those ids;
// aggregators/providers known to safely ignore or normalize the parameter are
// always active; everything else falls back to the model catalog
// (ModelSupportsThinking, which defaults true for an unrecognized id).
func ReasoningActive(llmProvider, model, level string) bool {
	if NormalizeReasoning(level) == ReasoningOff {
		return false
	}
	switch llmProvider {
	case provider.OpenAI, provider.Azure:
		return IsOpenAIReasoningModel(model)
	case provider.DeepSeek:
		return strings.Contains(strings.ToLower(model), "reasoner")
	case provider.XAI:
		m := strings.ToLower(model)
		return strings.Contains(m, "grok-3-mini") || strings.Contains(m, "grok-4")
	case provider.Groq, provider.Cerebras:
		m := strings.ToLower(model)
		return strings.Contains(m, "qwq") || strings.Contains(m, "r1") || strings.Contains(m, "thinking")
	case provider.Mistral:
		return strings.Contains(strings.ToLower(model), "magistral")
	case provider.Perplexity:
		return strings.Contains(strings.ToLower(model), "reasoning")
	case provider.OpenRouter, provider.Gemini, provider.Vertex, provider.Together, provider.Fireworks, provider.Copilot:
		return true
	case provider.Meta:
		return true // every Muse Spark tier reasons before answering; /effort maps to its effort levels
	default: // ollama, anthropic, bedrock, cohere, deepinfra, lmstudio, llamacpp
		return ModelSupportsThinking(model)
	}
}

// ModelInfo is a curated preset model the user can pick from /models.
type ModelInfo struct {
	ID       string
	Name     string
	Thinking bool // whether the model supports extended thinking
}

// KnownImageModels is a curated fallback for the generate_image model picker
// (image-output models on an OpenAI-compatible route), used when the live
// /models fetch is unavailable or returns none.
var KnownImageModels = []ModelInfo{
	{"gemini-3-pro-image-preview", "Gemini 3 Pro Image (preview)", false},
	{"gemini-2.5-flash-image", "Gemini 2.5 Flash Image", false},
	{"gpt-image-1", "GPT Image 1", false},
}

// KnownVideoModels is a curated fallback for the generate_video model picker
// (Veo video-output models on the native Gemini route), used when the live model
// fetch is unavailable or returns none.
var KnownVideoModels = []ModelInfo{
	{"veo-3.1-generate-preview", "Veo 3.1 Generate (preview)", false},
	{"veo-3.1-fast-generate-preview", "Veo 3.1 Fast Generate (preview)", false},
	{"veo-3.0-generate-001", "Veo 3.0 Generate 001", false},
	{"veo-2.0-generate-001", "Veo 2.0 Generate 001", false},
}

// KnownModels is a best-effort curated list of Anthropic (Claude) models for
// the /models picker fallback (the live list is fetched from the provider when
// available). Users can still select any id via `/model <id>`.
var KnownModels = []ModelInfo{
	{"anthropic.claude-opus-4-8", "Claude Opus 4.8", true},
	{"anthropic.claude-sonnet-5", "Claude Sonnet 5", true},
	{"anthropic.claude-sonnet-4-6", "Claude Sonnet 4.6", true},
	{"anthropic.claude-haiku-4-5-20251001-v1:0", "Claude Haiku 4.5", false},
	{"anthropic.claude-opus-4-1-20250805-v1:0", "Claude Opus 4.1", true},
	{"anthropic.claude-sonnet-4-5-20250929-v1:0", "Claude Sonnet 4.5", true},
}

// KnownOpenAIModels is the curated list served via the provider's OpenAI-compatible
// endpoint — Gemini only. (Claude is served via the native Anthropic endpoint;
// the endpoint is chosen by ProviderForModel from the model id.)
var KnownOpenAIModels = []ModelInfo{
	{"gcp:gemini-3.1-pro-preview", "Gemini 3.1 Pro preview", true},
	{"gcp:gemini-2.5-pro", "Gemini 2.5 Pro", true},
}

// AllKnownModels is the merged curated list across both endpoints (Anthropic
// Claude + OpenAI-route Gemini), used as the /models fallback when the live
// fetch fails. The route for each is derived from its id by ProviderForModel.

// ModelFetchNote returns a user-facing note for a failed model fetch. It hides a
// raw region-restriction 403 behind a friendly message; otherwise it surfaces the
// underlying error. Both cases fall back to the built-in list.
func ModelFetchNote(err error) string {
	if ModelRegionRestricted(err) {
		return regionRestrictedNote + " Showing the built-in model list for now."
	}
	return "Could not fetch models (" + err.Error() + "); showing the built-in list."
}

const regionRestrictedNote = "This model is unavailable in your region."

// ModelRegionRestricted reports whether an error is the provider's "model unavailable
// in your region" 403 (a regional restriction).
func ModelRegionRestricted(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "unavailable in your region") || strings.Contains(s, "regional restrictions")
}

// TurnErrorNote rewrites known turn/inference errors into friendlier
// guidance for display; unrecognized errors return their raw text unchanged.
func TurnErrorNote(err error) string { return TurnErrorNoteFor(err, "") }

// TurnErrorNoteFor is TurnErrorNote with the model that produced the error, so
// context-overflow guidance can name it and suggest roomier alternatives.
func TurnErrorNoteFor(err error, model string) string {
	return TurnErrorNoteForSession(err, model, "")
}

// TurnErrorNoteForSession is TurnErrorNoteFor with the session's own model, so an
// overflow on a model pinned by a quick action can name the pin as the cause rather
// than blaming a session that is actually fine.
func TurnErrorNoteForSession(err error, model, sessionModel string) string {
	if err == nil {
		return ""
	}
	if ModelRegionRestricted(err) {
		return regionRestrictedNote
	}
	// Only reached once the automatic shrink-and-retry has given up, so the advice
	// ("bigger model, or /compact") is the genuine remaining option.
	if note := PromptTooLongNoteFor(err, model, sessionModel); note != "" {
		return note
	}
	return err.Error()
}

func AllKnownModels() []ModelInfo {
	out := make([]ModelInfo, 0, len(KnownModels)+len(KnownOpenAIModels))
	out = append(out, KnownModels...)
	out = append(out, KnownOpenAIModels...)
	return out
}

// ModelFallback returns what to show a "/models" picker when a live fetch
// fails, scoped to the active provider so it doesn't offer unservable models
// (e.g. showing the Anthropic/Gemini gateway catalog while using Hugging
// Face). KnownModels/KnownOpenAIModels are curated specifically for the
// Anthropic and Gemini gateways, so they're only relevant there; every other
// provider (OpenRouter, DeepSeek, Moonshot, Hugging Face, Ollama, ...) has no
// static catalog, so fall back to just that provider's own default model —
// never an unrelated list, and never empty (DefaultModelFor always resolves
// to something, down to Ollama's default).
func ModelFallback(providerID string) []ModelInfo {
	id := provider.Normalize(providerID)
	switch id {
	case provider.Anthropic, provider.SQEAnthropic, provider.Gemini:
		return AllKnownModels()
	default:
		def := provider.DefaultModelFor(id)
		return []ModelInfo{{ID: def, Name: def}}
	}
}

// ModelSupportsThinking reports whether a model id is known to support extended
// thinking. Unknown ids are assumed to support it (the user opted in).
func ModelSupportsThinking(id string) bool {
	for _, list := range [][]ModelInfo{KnownModels, KnownOpenAIModels} {
		for _, m := range list {
			if m.ID == id {
				return m.Thinking
			}
		}
	}
	return true
}

// Load builds a Config from environment variables and saved preferences with
// sensible defaults. Precedence (highest first): command-line flags (applied by
// the caller) > environment variables > saved preferences (config.json) >
// built-in defaults.
//
// Recognized env vars:
//
//	MN_OPENAI_BASE_URL      override the OpenAI route base URL (incl. /v1)
//	MN_MODEL                override the model identifier (the route is derived from it)
//	MN_REASONING            override the reasoning level (off/low/medium/high)
//
// cheapModeEnabled reports whether MN_CHEAP requests the maximum-savings preset.
func cheapModeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_CHEAP"))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// applyCheapPreset flips cost-relevant defaults toward the lowest token spend —
// the baseline for MN_CHEAP. It enables adaptive (cheaper) model routing and
// concise tool schemas, and ages/compacts context sooner. Individual MN_* env
// vars parsed afterward still take precedence. Trades a little answer quality for
// cost, and (via config inheritance) applies to sub-agents and workflows too.
func applyCheapPreset(c *Config) {
	c.AdaptiveModel = true           // run turns on the cheaper fast model, escalate only on friction
	c.CheapReads = true              // route pure-read rounds to the fast model
	c.ConciseToolSchemas = true      // send trimmed tool schemas on every call (all flows)
	c.ToolResultKeepRounds = 4       // stub stale tool outputs sooner (default 8)
	c.CompactThresholdTokens = 50000 // summarize history earlier (default 80k)
	c.TaskResultMaxBytes = 6000      // cap delegated sub-agent results tighter (default: none)
}

func Load() *Config {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}

	c := &Config{
		OllamaBaseURL:       "http://localhost:11434/v1",
		OpenAIBaseURL:       "https://api.openai.com/v1",
		AnthropicBaseURL:    "https://api.anthropic.com",
		SQEAnthropicBaseURL: DefaultSQEAnthropicBaseURL,
		OpenRouterBaseURL:   "https://openrouter.ai/api/v1",
		GeminiBaseURL:       "https://generativelanguage.googleapis.com/v1beta/openai",
		DeepSeekBaseURL:     "https://api.deepseek.com",
		GroqBaseURL:         "https://api.groq.com/openai/v1",
		XAIBaseURL:          "https://api.x.ai/v1",
		MistralBaseURL:      "https://api.mistral.ai/v1",
		TogetherBaseURL:     "https://api.together.xyz/v1",
		FireworksBaseURL:    "https://api.fireworks.ai/inference/v1",
		MoonshotBaseURL:     "https://api.moonshot.ai/v1",
		ZAIBaseURL:          "https://api.z.ai/api/paas/v4",
		DashScopeBaseURL:    "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		NvidiaBaseURL:       "https://integrate.api.nvidia.com/v1",
		HuggingFaceBaseURL:  "https://router.huggingface.co/v1",
		VeniceBaseURL:       "https://api.venice.ai/api/v1",
		OmniRouteBaseURL:    "http://localhost:20128/v1",
		CerebrasBaseURL:     "https://api.cerebras.ai/v1",
		PerplexityBaseURL:   "https://api.perplexity.ai",
		CohereBaseURL:       "https://api.cohere.ai/compatibility/v1",
		DeepInfraBaseURL:    "https://api.deepinfra.com/v1/openai",
		MetaBaseURL:         "https://api.meta.ai/v1",
		LMStudioBaseURL:     "http://localhost:1234/v1",
		LlamaCppBaseURL:     "http://localhost:8080/v1",
		AzureAPIVersion:     defaultAzureAPIVersion,
		VertexLocation:      defaultVertexLocation,
		AWSRegion:           defaultAWSRegion,
		LLMProvider:         "ollama",
		Model:               DefaultModel,
		RelayAddr:           defaultRelayAddr, // on by default; prefs/env can override or disable
		MaxTokens:           8192,
		// Quotas on the hosted providers roll over at midnight local time. Only a
		// fallback for providers that report no reset timestamp of their own (and
		// for hard 429s, which carry none); set MN_QUOTA_RESET_AT="" to disable the
		// time-based restore.
		QuotaResetAt:       "00:00",
		MaxToolOutputBytes: 16000,
		MaxAgentSteps:      80,
		SubagentMaxDepth:   2,
		AutoContinue:       1,
		BatchContext:       true,
		RepoMap:            true,
		LazyMCPTools:       true,
		LazyBuiltinTools:   true,
		Terse:              true,
		CodeOutline:        true,
		DynamicEffort:      true,
		AdaptiveModel:      true, // default on: right-size the model to the task, escalate only on friction
		CheapReads:         true, // default on: route pure-read rounds to the fast model
		VerifyMaxRetries:   2,
		VerifyTimeoutSec:   120,
		MaxReadLines:       800,
		WorkingDir:         wd,
		ReasoningLevel:     ReasoningAuto,
		ReasoningByModel:   map[string]string{},

		ApprovalPolicy:         ApprovalPrompt,
		SandboxMode:            SandboxOff,
		SandboxNetwork:         true,
		AutoDelegate:           false,
		AutoOffloadReadBytes:   32768,
		CompactThresholdTokens: 80000,
		CompactKeepTokens:      32000,
		ToolResultKeepRounds:   8,
	}

	// MN_CHEAP: one opt-in switch for maximum token savings. It flips the
	// built-in defaults toward lower cost; because sub-agents and workflow nodes
	// inherit this config (scoped.go: cfg := *base), the savings reach every flow.
	// Individual MN_* knobs parsed below still override it — opt in, then tune a
	// single dimension back if needed.
	if cheapModeEnabled() {
		applyCheapPreset(c)
	}

	// Saved preferences override built-in defaults.
	prefs := loadPrefs()
	if prefs.ReasoningLevel != "" {
		c.ReasoningLevel = NormalizeReasoning(prefs.ReasoningLevel)
	}
	for model, lvl := range prefs.ReasoningByModel {
		c.ReasoningByModel[model] = NormalizeReasoning(lvl)
	}
	if prefs.ApprovalPolicy != "" {
		c.ApprovalPolicy = NormalizeApprovalPolicy(prefs.ApprovalPolicy)
	}
	if prefs.SandboxMode != "" {
		c.SandboxMode = NormalizeSandboxMode(prefs.SandboxMode)
	}
	if prefs.RelayAddr != nil {
		// Explicit saved choice (an address, or "" from `/relay off`) overrides the
		// built-in default. nil = never configured, so the default stands.
		c.RelayAddr = strings.TrimSpace(*prefs.RelayAddr)
	}
	if prefs.ContextWindow > 0 {
		c.ContextWindow = prefs.ContextWindow
	}
	if prefs.VerifyCommand != "" {
		c.VerifyCommand = prefs.VerifyCommand
	}
	if prefs.VerifyMaxRetries > 0 {
		c.VerifyMaxRetries = prefs.VerifyMaxRetries
	}
	// NOTE: AutoPlan and SelfReview are no longer set here from prefs — they're
	// plain bools that used to be applied at this EARLY point, before the env
	// vars below, so a deployment's MN_AUTO_PLAN/MN_SELF_REVIEW could clobber
	// a saved persona choice on every restart. They're now applied in the
	// "Persona-explicit overrides" block at the end of Load(), same as
	// CheapReads/FastModel/AuxModel, so an explicit save always wins.

	// The model defaults to DefaultModel, then a saved preference, then the
	// environment. The route is derived from the final model id (below).
	if prefs.Model != "" {
		c.Model = prefs.Model
	}
	// Backend provider: saved preference, then MN_PROVIDER, normalized against
	// the registry (unknown/empty -> ollama).
	llmProvider := prefs.LLMProvider
	if v := strings.TrimSpace(os.Getenv("MN_PROVIDER")); v != "" {
		llmProvider = v
		c.ProviderExplicit = true
	}
	c.LLMProvider = provider.Normalize(llmProvider)
	// Ollama host (MN_OLLAMA_HOST preferred; OLLAMA_HOST is the upstream's own
	// convention). A bare host (no "/v1") gets the OpenAI-compatible suffix added.
	if v := strings.TrimSpace(firstEnv("MN_OLLAMA_HOST", "OLLAMA_HOST")); v != "" {
		c.OllamaBaseURL = NormalizeOllamaBaseURL(v)
	}
	c.OllamaToken = strings.TrimSpace(os.Getenv("MN_OLLAMA_TOKEN"))
	c.OpenAIAPIKey = strings.TrimSpace(firstEnv("MN_OPENAI_API_KEY", "OPENAI_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_OPENAI_BASE_URL")); v != "" {
		c.OpenAIBaseURL = v
	}
	c.AnthropicAPIKey = strings.TrimSpace(firstEnv("MN_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_ANTHROPIC_BASE_URL")); v != "" {
		c.AnthropicBaseURL = v
	}
	c.AnthropicCustomHeaders = strings.TrimSpace(firstEnv("MN_ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_CUSTOM_HEADERS"))
	// SQE Anthropic (Claude via the SQE LAN gateway): its own key/base URL, but the
	// key falls back to the Anthropic key so "same as Anthropic, different gateway"
	// works with a single credential. Read after AnthropicAPIKey for the fallback.
	c.SQEAnthropicAPIKey = strings.TrimSpace(firstEnv("MN_SQE_ANTHROPIC_API_KEY"))
	if c.SQEAnthropicAPIKey == "" {
		c.SQEAnthropicAPIKey = c.AnthropicAPIKey
	}
	if v := strings.TrimSpace(firstEnv("MN_SQE_ANTHROPIC_BASE_URL")); v != "" {
		c.SQEAnthropicBaseURL = v
	}
	c.OpenRouterAPIKey = strings.TrimSpace(firstEnv("MN_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_OPENROUTER_BASE_URL")); v != "" {
		c.OpenRouterBaseURL = v
	}
	c.GeminiAPIKey = strings.TrimSpace(firstEnv("MN_GEMINI_API_KEY", "GEMINI_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_GEMINI_BASE_URL")); v != "" {
		c.GeminiBaseURL = v
	}
	c.DeepSeekAPIKey = strings.TrimSpace(firstEnv("MN_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_DEEPSEEK_BASE_URL")); v != "" {
		c.DeepSeekBaseURL = v
	}
	c.GroqAPIKey = strings.TrimSpace(firstEnv("MN_GROQ_API_KEY", "GROQ_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_GROQ_BASE_URL")); v != "" {
		c.GroqBaseURL = v
	}
	c.XAIAPIKey = strings.TrimSpace(firstEnv("MN_XAI_API_KEY", "XAI_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_XAI_BASE_URL")); v != "" {
		c.XAIBaseURL = v
	}
	c.MistralAPIKey = strings.TrimSpace(firstEnv("MN_MISTRAL_API_KEY", "MISTRAL_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_MISTRAL_BASE_URL")); v != "" {
		c.MistralBaseURL = v
	}
	c.TogetherAPIKey = strings.TrimSpace(firstEnv("MN_TOGETHER_API_KEY", "TOGETHER_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_TOGETHER_BASE_URL")); v != "" {
		c.TogetherBaseURL = v
	}
	c.FireworksAPIKey = strings.TrimSpace(firstEnv("MN_FIREWORKS_API_KEY", "FIREWORKS_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_FIREWORKS_BASE_URL")); v != "" {
		c.FireworksBaseURL = v
	}
	c.MoonshotAPIKey = strings.TrimSpace(firstEnv("MN_MOONSHOT_API_KEY", "MOONSHOT_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_MOONSHOT_BASE_URL")); v != "" {
		c.MoonshotBaseURL = v
	}
	c.ZAIAPIKey = strings.TrimSpace(firstEnv("MN_ZAI_API_KEY", "ZAI_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_ZAI_BASE_URL")); v != "" {
		c.ZAIBaseURL = v
	}
	c.DashScopeAPIKey = strings.TrimSpace(firstEnv("MN_DASHSCOPE_API_KEY", "DASHSCOPE_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_DASHSCOPE_BASE_URL")); v != "" {
		c.DashScopeBaseURL = v
	}
	c.NvidiaAPIKey = strings.TrimSpace(firstEnv("MN_NVIDIA_API_KEY", "NVIDIA_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_NVIDIA_BASE_URL")); v != "" {
		c.NvidiaBaseURL = v
	}
	c.HuggingFaceAPIKey = strings.TrimSpace(firstEnv("MN_HUGGINGFACE_API_KEY", "HF_TOKEN"))
	if v := strings.TrimSpace(firstEnv("MN_HUGGINGFACE_BASE_URL")); v != "" {
		c.HuggingFaceBaseURL = v
	}
	c.VeniceAPIKey = strings.TrimSpace(firstEnv("MN_VENICE_API_KEY", "VENICE_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_VENICE_BASE_URL")); v != "" {
		c.VeniceBaseURL = v
	}
	c.OmniRouteAPIKey = strings.TrimSpace(firstEnv("MN_OMNIROUTE_API_KEY", "OMNIROUTE_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_OMNIROUTE_BASE_URL")); v != "" {
		c.OmniRouteBaseURL = v
	}
	c.CerebrasAPIKey = strings.TrimSpace(firstEnv("MN_CEREBRAS_API_KEY", "CEREBRAS_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_CEREBRAS_BASE_URL")); v != "" {
		c.CerebrasBaseURL = v
	}
	c.PerplexityAPIKey = strings.TrimSpace(firstEnv("MN_PERPLEXITY_API_KEY", "PERPLEXITY_API_KEY", "PPLX_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_PERPLEXITY_BASE_URL")); v != "" {
		c.PerplexityBaseURL = v
	}
	c.CohereAPIKey = strings.TrimSpace(firstEnv("MN_COHERE_API_KEY", "CO_API_KEY", "COHERE_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_COHERE_BASE_URL")); v != "" {
		c.CohereBaseURL = v
	}
	c.DeepInfraAPIKey = strings.TrimSpace(firstEnv("MN_DEEPINFRA_API_KEY", "DEEPINFRA_API_KEY", "DEEPINFRA_TOKEN"))
	if v := strings.TrimSpace(firstEnv("MN_DEEPINFRA_BASE_URL")); v != "" {
		c.DeepInfraBaseURL = v
	}
	c.MetaAPIKey = strings.TrimSpace(firstEnv("MN_META_API_KEY", "MODEL_API_KEY", "META_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_META_BASE_URL")); v != "" {
		c.MetaBaseURL = v
	}
	c.LMStudioAPIKey = strings.TrimSpace(firstEnv("MN_LMSTUDIO_API_KEY", "LM_STUDIO_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_LMSTUDIO_BASE_URL", "LM_STUDIO_BASE_URL")); v != "" {
		c.LMStudioBaseURL = v
	}
	if v := strings.TrimSpace(firstEnv("MN_LLAMACPP_BASE_URL", "LLAMA_CPP_BASE_URL")); v != "" {
		c.LlamaCppBaseURL = v
	}
	c.AzureAPIKey = strings.TrimSpace(firstEnv("MN_AZURE_API_KEY", "AZURE_OPENAI_API_KEY", "AZURE_API_KEY"))
	if v := strings.TrimSpace(firstEnv("MN_AZURE_BASE_URL", "AZURE_OPENAI_ENDPOINT")); v != "" {
		c.AzureBaseURL = v
	}
	if v := strings.TrimSpace(firstEnv("MN_AZURE_API_VERSION", "AZURE_OPENAI_API_VERSION")); v != "" {
		c.AzureAPIVersion = v
	}
	c.VertexAPIKey = strings.TrimSpace(firstEnv("MN_VERTEX_API_KEY", "GOOGLE_VERTEX_API_KEY"))
	c.VertexProject = strings.TrimSpace(firstEnv("MN_VERTEX_PROJECT", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT"))
	if v := strings.TrimSpace(firstEnv("MN_VERTEX_LOCATION", "GOOGLE_CLOUD_LOCATION", "CLOUD_ML_REGION")); v != "" {
		c.VertexLocation = v
	}
	if v := strings.TrimSpace(firstEnv("MN_VERTEX_BASE_URL")); v != "" {
		c.VertexBaseURL = v
	}
	c.AWSAccessKeyID = strings.TrimSpace(firstEnv("MN_AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"))
	c.AWSSecretAccessKey = strings.TrimSpace(firstEnv("MN_AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"))
	c.AWSSessionToken = strings.TrimSpace(firstEnv("MN_AWS_SESSION_TOKEN", "AWS_SESSION_TOKEN"))
	if v := strings.TrimSpace(firstEnv("MN_AWS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION")); v != "" {
		c.AWSRegion = v
	}
	c.AWSProfile = strings.TrimSpace(firstEnv("MN_AWS_PROFILE", "AWS_PROFILE"))
	c.CopilotToken = strings.TrimSpace(firstEnv("MN_COPILOT_TOKEN", "COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"))
	c.CopilotCLIPath = strings.TrimSpace(firstEnv("MN_COPILOT_CLI_PATH", "COPILOT_CLI_PATH"))
	if v := os.Getenv("MN_MODEL"); v != "" {
		c.ModelExplicit = true
		c.Model = v
	} else if prefs.Model == "" && c.LLMProvider != provider.Default {
		// No saved/explicit model, but a non-default provider was selected
		// (saved or via MN_PROVIDER): start on that provider's default model
		// instead of Ollama's.
		c.Model = provider.DefaultModelFor(c.LLMProvider)
	}
	// Provider is the wire protocol (anthropic-native vs openai-compatible),
	// derived directly from the backend now that each backend fully determines
	// its own wire shape (it no longer depends on guessing from the model id).
	c.Provider = wireRouteFor(c.LLMProvider)
	// Resolve the active reasoning level from the per-model memory now that
	// c.Model is finalized. A per-model entry wins over the legacy single
	// value; the MN_REASONING env below still overrides everything.
	if lvl, ok := c.ReasoningByModel[c.Model]; ok {
		c.ReasoningLevel = NormalizeReasoning(lvl)
	}
	if v := os.Getenv("MN_REASONING"); v != "" {
		c.ReasoningLevel = NormalizeReasoning(v)
		c.ReasoningExplicit = true
	}
	if v := os.Getenv("MN_APPROVAL"); v != "" {
		c.ApprovalPolicy = NormalizeApprovalPolicy(v)
	}
	if v := os.Getenv("MN_SANDBOX"); v != "" {
		c.SandboxMode = NormalizeSandboxMode(v)
	}
	if v := strings.TrimSpace(os.Getenv("MN_AUTO_PLAN")); v != "" {
		c.AutoPlan = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on")
	}
	if v := strings.TrimSpace(os.Getenv("MN_AUTO_DELEGATE")); v != "" {
		// Defaults off; the env var can turn it ON (1/true/on/yes) or explicitly OFF.
		c.AutoDelegate = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	// Quota-based provider fallback (see internal/agent/quotafallback.go).
	// MN_QUOTA_RESET_AT is read with LookupEnv rather than the non-empty guard used
	// for the others: for this knob an empty value means "disabled" (and so must
	// beat the built-in default), while for the rest empty means "unset".
	if v, ok := lookupFirstEnv("MN_QUOTA_RESET_AT"); ok {
		c.QuotaResetAt = strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(firstEnv("MN_QUOTA_FALLBACK")); v != "" {
		c.QuotaFallback = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(firstEnv("MN_QUOTA_BUDGET_USD")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			c.QuotaBudgetUSD = f
		}
	}
	if v := strings.TrimSpace(firstEnv("MN_QUOTA_BUDGET_PCT")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			c.QuotaBudgetPct = f
		}
	}
	if v := strings.TrimSpace(firstEnv("MN_PROVIDER_FALLBACKS")); v != "" {
		// Store the canonical per-hop specs so a model/reasoning choice in the env
		// spec survives into the chain (FallbackChain re-parses them).
		c.ProviderFallbacks = fallbackSpecs(ParseFallbackChain(v))
	}
	if v := strings.TrimSpace(os.Getenv("MN_RELAY")); v != "" {
		// env overrides the saved relay address; an off sentinel disables it even
		// when the default/saved value is on.
		switch strings.ToLower(v) {
		case "off", "none", "disable", "0", "false", "no":
			c.RelayAddr = ""
		default:
			c.RelayAddr = v
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_CONTEXT_WINDOW")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n > 0 {
			c.ContextWindow = n
		}
	}
	if v := os.Getenv("MN_SANDBOX_NET"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "off", "false", "no":
			c.SandboxNetwork = false
		}
	}
	if v := os.Getenv("MN_MAX_STEPS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			c.MaxAgentSteps = n
		}
	}
	if v := os.Getenv("MN_MAX_SUBAGENT_DEPTH"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 1 {
			c.SubagentMaxDepth = n
		}
	}
	if c.SubagentMaxDepth < 1 {
		c.SubagentMaxDepth = 1
	}
	if c.SubagentMaxDepth > SubagentDepthCeiling {
		c.SubagentMaxDepth = SubagentDepthCeiling
	}
	if v := os.Getenv("MN_AUTO_CONTINUE"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			c.AutoContinue = n
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_BATCH_CONTEXT"))) {
	case "1", "on", "true", "yes":
		c.BatchContext = true
	case "0", "off", "false", "no":
		c.BatchContext = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_LAZY_MCP"))) {
	case "1", "on", "true", "yes":
		c.LazyMCPTools = true
	case "0", "off", "false", "no":
		c.LazyMCPTools = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_LAZY_BUILTIN_TOOLS"))) {
	case "1", "on", "true", "yes":
		c.LazyBuiltinTools = true
	case "0", "off", "false", "no":
		c.LazyBuiltinTools = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_PROJECT_PROFILE"))) {
	case "1", "on", "true", "yes":
		c.ProjectProfile = true
	case "0", "off", "false", "no":
		c.ProjectProfile = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_REPO_MAP"))) {
	case "1", "on", "true", "yes":
		c.RepoMap = true
	case "0", "off", "false", "no":
		c.RepoMap = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_TERSE"))) {
	case "1", "on", "true", "yes":
		c.Terse = true
	case "0", "off", "false", "no":
		c.Terse = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_CODE_OUTLINE"))) {
	case "1", "on", "true", "yes":
		c.CodeOutline = true
	case "0", "off", "false", "no":
		c.CodeOutline = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_DYNAMIC_EFFORT"))) {
	case "1", "on", "true", "yes":
		c.DynamicEffort = true
	case "0", "off", "false", "no":
		c.DynamicEffort = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_ADAPTIVE_MODEL"))) {
	case "1", "on", "true", "yes":
		c.AdaptiveModel = true
	case "0", "off", "false", "no":
		c.AdaptiveModel = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_CHEAP_READS"))) {
	case "1", "on", "true", "yes":
		c.CheapReads = true
	case "0", "off", "false", "no":
		c.CheapReads = false
	}
	if v := strings.TrimSpace(os.Getenv("MN_VERIFY_CMD")); v != "" {
		c.VerifyCommand = v
	}
	if v := strings.TrimSpace(os.Getenv("MN_VERIFY_MAX")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.VerifyMaxRetries = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_VERIFY_AUTO")); v != "" {
		c.VerifyAuto = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(os.Getenv("MN_VERIFY_TIMEOUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.VerifyTimeoutSec = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_SELF_REVIEW")); v != "" {
		c.SelfReview = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("MN_TASKBOARD_MAX_PARALLEL"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			c.TaskBoardMaxParallel = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_IMAGE_MODEL")); v != "" {
		c.ImageModel = v
	}
	if v := strings.TrimSpace(os.Getenv("MN_VIDEO_MODEL")); v != "" {
		c.VideoModel = v
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_CODEBASE_MAP"))) {
	case "1", "on", "true", "yes":
		c.CodebaseMap = true
	case "0", "off", "false", "no":
		c.CodebaseMap = false
	}
	if v := strings.TrimSpace(os.Getenv("MN_CODEBASE_MAP_BYTES")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n > 0 {
			c.CodebaseMapPromptBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_RAG_INDEX_DIR")); v != "" {
		c.RAGIndexDir = v
	}
	if v := strings.TrimSpace(os.Getenv("MN_AUTO_TESTGEN")); v != "" {
		c.AutoTestGen = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(os.Getenv("MN_AUTO_TESTGEN_MAX")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.AutoTestGenMax = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_COMPACT_THRESHOLD")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.CompactThresholdTokens = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_COMPACT_KEEP")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.CompactKeepTokens = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_MAX_TOOL_OUTPUT")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n > 0 {
			c.MaxToolOutputBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_TASK_RESULT_MAX")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.TaskResultMaxBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_TOOL_RESULT_KEEP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.ToolResultKeepRounds = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_AUTO_OFFLOAD_READ_BYTES")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.AutoOffloadReadBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_CONCISE_TOOL_SCHEMAS")); v != "" {
		c.ConciseToolSchemas = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(os.Getenv("MN_READ_LINES")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n > 0 {
			c.MaxReadLines = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MN_EMBEDDINGS_MODEL")); v != "" {
		c.EmbeddingsModel = v
	}
	if v := normalizedProviderEnv("MN_EMBEDDINGS_PROVIDER"); v != "" {
		c.EmbeddingsProvider = v
	}
	if v := normalizedProviderEnv("MN_IMAGE_PROVIDER"); v != "" {
		c.ImageProvider = v
	}
	// Default ON: an always-on daemon that silently rots on an old build is worse than
	// one that keeps itself current, and it has no user watching to run /update.
	c.AutoUpdate = true
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_AUTO_UPDATE"))) {
	case "1", "on", "true", "yes":
		c.AutoUpdate = true
	case "0", "off", "false", "no":
		c.AutoUpdate = false
	}

	// Persona-explicit overrides — applied LAST so a user's explicit save from
	// the web UI always wins over both the deployment env and the cheap preset.
	// Only active when the user has actually made a choice (non-nil pointer).
	if prefs.AutoUpdate != nil {
		c.AutoUpdate = *prefs.AutoUpdate
		c.AutoUpdatePersona = prefs.AutoUpdate
	}
	if prefs.ImageProvider != nil {
		// Lookup, not Normalize: an unrecognised id must mean "inherit", never the
		// default provider (see ImageProviderFor).
		raw := strings.TrimSpace(*prefs.ImageProvider)
		c.ImageProvider = ""
		if _, ok := provider.Lookup(raw); ok {
			c.ImageProvider = provider.Normalize(raw)
		}
		c.ImageProviderPersona = prefs.ImageProvider
	}
	if prefs.CheapReads != nil {
		c.CheapReads = *prefs.CheapReads
		c.CheapReadsPersona = prefs.CheapReads
	}
	if prefs.AutoPlan != nil {
		c.AutoPlan = *prefs.AutoPlan
		c.AutoPlanPersona = prefs.AutoPlan
	}
	// Quota fallback: applied after env vars for the same reason as the toggles
	// above — a deployment's MN_QUOTA_* must not clobber an explicit persona
	// choice on every restart.
	if prefs.QuotaFallback != nil {
		c.QuotaFallback = *prefs.QuotaFallback
		c.QuotaFallbackPersona = prefs.QuotaFallback
	}
	if prefs.QuotaResetAt != nil {
		ra := strings.TrimSpace(*prefs.QuotaResetAt)
		c.QuotaResetAt = ra // "" is a real choice: disables the time-based restore
		c.QuotaResetAtPersona = &ra
	}
	if prefs.QuotaBudgetUSD != nil {
		c.QuotaBudgetUSD = *prefs.QuotaBudgetUSD
		c.QuotaBudgetUSDPersona = prefs.QuotaBudgetUSD
	}
	if prefs.QuotaBudgetPct != nil {
		c.QuotaBudgetPct = *prefs.QuotaBudgetPct
		c.QuotaBudgetPctPersona = prefs.QuotaBudgetPct
	}
	if prefs.ProviderFallbacks != nil {
		// Re-parse rather than trusting the stored strings: a provider removed from
		// the build (or a hand-edited config.json) must not survive as a live hop.
		chain := fallbackSpecs(ParseFallbackChain(strings.Join(*prefs.ProviderFallbacks, ",")))
		c.ProviderFallbacks = chain
		c.ProviderFallbacksPersona = &chain
	}
	if prefs.SelfReview != nil {
		c.SelfReview = *prefs.SelfReview
		c.SelfReviewPersona = prefs.SelfReview
	}
	if prefs.AdaptiveModel != nil {
		c.AdaptiveModel = *prefs.AdaptiveModel
		c.AdaptiveModelPersona = prefs.AdaptiveModel
	}
	if prefs.FastModel != nil {
		fm := strings.TrimSpace(*prefs.FastModel)
		c.FastModelPersona = &fm
		if fm != "" {
			os.Setenv("MN_FAST_MODEL", fm)
		} else {
			os.Unsetenv("MN_FAST_MODEL")
		}
	}
	if prefs.AuxModel != nil {
		am := strings.TrimSpace(*prefs.AuxModel)
		c.AuxModelPersona = &am
		if am != "" {
			os.Setenv("MN_AUX_MODEL", am)
		} else {
			os.Unsetenv("MN_AUX_MODEL")
		}
	}
	// Per-tier provider + reasoning for the Fast and Self-review (aux) models. Each
	// mirrors the FastModel/AuxModel pattern: remember the persona pointer and
	// re-apply the env var so the resolvers (Fast/AuxProvider, Fast/AuxReasoning,
	// Fast/AuxModelRoute) see a saved choice across restarts. Empty = inherit.
	if prefs.FastProvider != nil {
		fp := strings.TrimSpace(*prefs.FastProvider)
		c.FastProviderPersona = &fp
		if fp != "" {
			os.Setenv("MN_FAST_PROVIDER", fp)
		} else {
			os.Unsetenv("MN_FAST_PROVIDER")
		}
	}
	if prefs.FastReasoning != nil {
		fr := strings.TrimSpace(*prefs.FastReasoning)
		c.FastReasoningPersona = &fr
		if fr != "" {
			os.Setenv("MN_FAST_REASONING", fr)
		} else {
			os.Unsetenv("MN_FAST_REASONING")
		}
	}
	if prefs.AuxProvider != nil {
		ap := strings.TrimSpace(*prefs.AuxProvider)
		c.AuxProviderPersona = &ap
		if ap != "" {
			os.Setenv("MN_AUX_PROVIDER", ap)
		} else {
			os.Unsetenv("MN_AUX_PROVIDER")
		}
	}
	if prefs.AuxReasoning != nil {
		ar := strings.TrimSpace(*prefs.AuxReasoning)
		c.AuxReasoningPersona = &ar
		if ar != "" {
			os.Setenv("MN_AUX_REASONING", ar)
		} else {
			os.Unsetenv("MN_AUX_REASONING")
		}
	}

	// Verify-command auto-detection: opt-in (MN_VERIFY_AUTO), and only ever fills
	// an UNSET VerifyCommand — an explicit command (env/prefs/flag) always wins and
	// is never overwritten. Off by default, so the agent won't run a project
	// command the user didn't ask for.
	if c.VerifyAuto && strings.TrimSpace(c.VerifyCommand) == "" {
		if cmd := detectVerifyCommand(c.WorkingDir); cmd != "" {
			c.VerifyCommand = cmd
			c.VerifyCommandAuto = true
		}
	}

	return c
}
