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
	// Set via SQE_OLLAMA_HOST / OLLAMA_HOST. Defaults to http://localhost:11434/v1.
	OllamaBaseURL string
	// OllamaToken is an optional bearer token for a remote/protected Ollama
	// server. Set via SQE_OLLAMA_TOKEN.
	OllamaToken string
	// OpenAIAPIKey authenticates api.openai.com. Set via SQE_OPENAI_API_KEY
	// (falls back to the conventional OPENAI_API_KEY).
	OpenAIAPIKey string
	// OpenAIBaseURL overrides the OpenAI-compatible base URL (default
	// https://api.openai.com/v1). Set via SQE_OPENAI_BASE_URL.
	OpenAIBaseURL string
	// AnthropicAPIKey authenticates api.anthropic.com. Set via
	// SQE_ANTHROPIC_API_KEY (falls back to ANTHROPIC_API_KEY).
	AnthropicAPIKey string
	// AnthropicBaseURL overrides the native Anthropic Messages API base URL
	// (default https://api.anthropic.com). Set via SQE_ANTHROPIC_BASE_URL.
	AnthropicBaseURL string
	// OpenRouterAPIKey authenticates openrouter.ai. Set via
	// SQE_OPENROUTER_API_KEY (falls back to OPENROUTER_API_KEY).
	OpenRouterAPIKey string
	// OpenRouterBaseURL overrides the OpenRouter base URL (default
	// https://openrouter.ai/api/v1). Set via SQE_OPENROUTER_BASE_URL.
	OpenRouterBaseURL string
	// GeminiAPIKey authenticates Google's OpenAI-compatible endpoint. Set via
	// SQE_GEMINI_API_KEY (falls back to GEMINI_API_KEY).
	GeminiAPIKey string
	// GeminiBaseURL overrides the Gemini base URL (default
	// https://generativelanguage.googleapis.com/v1beta/openai). Set via
	// SQE_GEMINI_BASE_URL.
	GeminiBaseURL string
	// DeepSeekAPIKey authenticates api.deepseek.com. Set via
	// SQE_DEEPSEEK_API_KEY (falls back to DEEPSEEK_API_KEY).
	DeepSeekAPIKey string
	// DeepSeekBaseURL overrides the DeepSeek base URL (default
	// https://api.deepseek.com). Set via SQE_DEEPSEEK_BASE_URL.
	DeepSeekBaseURL string
	// GroqAPIKey authenticates api.groq.com. Set via SQE_GROQ_API_KEY (falls
	// back to GROQ_API_KEY).
	GroqAPIKey string
	// GroqBaseURL overrides the Groq base URL (default
	// https://api.groq.com/openai/v1). Set via SQE_GROQ_BASE_URL.
	GroqBaseURL string
	// XAIAPIKey authenticates api.x.ai (Grok). Set via SQE_XAI_API_KEY (falls
	// back to XAI_API_KEY).
	XAIAPIKey string
	// XAIBaseURL overrides the xAI base URL (default https://api.x.ai/v1). Set
	// via SQE_XAI_BASE_URL.
	XAIBaseURL string
	// MistralAPIKey authenticates api.mistral.ai. Set via SQE_MISTRAL_API_KEY
	// (falls back to MISTRAL_API_KEY).
	MistralAPIKey string
	// MistralBaseURL overrides the Mistral base URL (default
	// https://api.mistral.ai/v1). Set via SQE_MISTRAL_BASE_URL.
	MistralBaseURL string
	// TogetherAPIKey authenticates api.together.xyz. Set via
	// SQE_TOGETHER_API_KEY (falls back to TOGETHER_API_KEY).
	TogetherAPIKey string
	// TogetherBaseURL overrides the Together.ai base URL (default
	// https://api.together.xyz/v1). Set via SQE_TOGETHER_BASE_URL.
	TogetherBaseURL string
	// FireworksAPIKey authenticates api.fireworks.ai. Set via
	// SQE_FIREWORKS_API_KEY (falls back to FIREWORKS_API_KEY).
	FireworksAPIKey string
	// FireworksBaseURL overrides the Fireworks.ai base URL (default
	// https://api.fireworks.ai/inference/v1). Set via SQE_FIREWORKS_BASE_URL.
	FireworksBaseURL string
	// LLMProvider selects the backend model provider: "ollama" (default),
	// "openai", "anthropic", "openrouter", "gemini", "deepseek", "groq", "xai",
	// "mistral", "together", or "fireworks". Saved across launches; override
	// per-run with SQE_PROVIDER. See internal/provider.
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
	// applies). Set via SQE_TASK_RESULT_MAX; SQE_CHEAP sets it to 6000.
	TaskResultMaxBytes int
	// MaxAgentSteps bounds tool-call iterations within a single user turn.
	// Override with SQE_MAX_STEPS; a guard against runaway loops, not a budget.
	MaxAgentSteps int
	// AutoContinue is how many times a turn may automatically extend past
	// MaxAgentSteps when the model is still working (so long todo-driven tasks
	// finish without a manual "continue"). 0 disables it. Override with
	// SQE_AUTO_CONTINUE. The hard ceiling is MaxAgentSteps*(1+AutoContinue).
	AutoContinue int
	// BatchContext, when true, adds a stronger system-prompt directive telling
	// the model to front-load context-gathering: issue every independent
	// read/grep/glob/find_symbol call together in its first 1–2 tool rounds (they
	// run in parallel) and/or delegate broad investigation to the explore
	// sub-agent, instead of reading one file per round — so turns converge in
	// fewer steps. On by default; set SQE_BATCH_CONTEXT=off to disable.
	BatchContext bool
	// RepoMap, when true (default), prepends a compact, ignore-aware structural
	// overview of the repository (detected stack, key files, a depth-2 directory
	// tree with file counts) to the system prompt, so the model can orient without
	// globbing the tree first. Static — recomputed when the prompt is rebuilt, no
	// model call. Disable with SQE_REPO_MAP=0.
	RepoMap bool
	// LazyMCPTools, when true (default), keeps the (often bulky) tool schemas of
	// connected MCP servers OUT of the per-request tool list until the model
	// activates a server with the `enable_toolset` tool. This shrinks the tool
	// payload (and stops MCP schema churn from busting the prompt cache) while
	// keeping the capability discoverable via enable_toolset's description.
	// Built-in tools are never gated. Disable with SQE_LAZY_MCP=0.
	LazyMCPTools bool
	// LazyBuiltinTools, when true (default), gates a few non-core built-in tool
	// GROUPS (git, browser, apple-fetch) the same way LazyMCPTools gates MCP
	// servers: their schemas are kept out of the per-request tool list until the
	// model activates the group with `enable_toolset`. Core tools (read/write/
	// edit/search/bash/…) are always present. Trims cold-context tokens and
	// tool-selection surface for capabilities many sessions never touch. Disable
	// with SQE_LAZY_BUILTIN_TOOLS=0.
	LazyBuiltinTools bool
	// ProjectProfile, when true, maintains a model-distilled, cross-session
	// "profile" of the codebase (purpose, layout, entry points, conventions,
	// gotchas) in the global config dir keyed by the working dir, and injects it
	// into the system prompt so a fresh session orients without re-discovering the
	// repo. It's refreshed (at most ~weekly per repo) from real session work and
	// merged. OPT-IN (default off): it injects auto-generated content into every
	// turn's prompt, which can mislead if it goes stale. Enable with
	// SQE_PROJECT_PROFILE=1. Distinct from RepoMap (mechanical file tree) and
	// AGENTS.md (user-authored instructions).
	ProjectProfile bool
	// Terse, when true (default), adds a verbosity-steering "OUTPUT BUDGET"
	// directive to the system prompt so the model spends fewer OUTPUT tokens on
	// ceremony — preambles, sign-offs, restated context, re-printed unchanged
	// code — which are billed several times higher than input. It trims only
	// ceremony, never actual work (tool calls, edits, verification). On by
	// default; set SQE_TERSE=off to disable.
	Terse bool
	// CodeOutline, when true (default), compresses oversized read_file output on
	// source files into a signatures-only outline (with line numbers + elision
	// markers) instead of a blunt byte clip, so the model still sees the whole
	// file's shape. Toggle with SQE_CODE_OUTLINE.
	CodeOutline bool

	// DynamicEffort, when true (default), enables dynamic effort routing in "auto"
	// reasoning mode: a step that only digests a routine/benign tool batch (file
	// edits, a directory listing, a todo update — no errors) is run at a lower
	// reasoning floor, since the model is just continuing rather than solving. The
	// existing prompt prediction (new request) and friction ramp (tool errors)
	// still elevate effort, so this only trims thinking on passive steps. Auto mode
	// only; set SQE_DYNAMIC_EFFORT=off to disable.
	DynamicEffort bool
	// AdaptiveModel, when true, runs the main turn on the cheaper FastModel until
	// the turn shows sustained friction (tool errors / rejected stops), then
	// escalates to the session model for the rest of the turn — right-sizing the
	// model to the task so easy turns cost less. OFF by default (it swaps the
	// model you explicitly selected); enable with SQE_ADAPTIVE_MODEL=1. A no-op
	// when FastModel equals the session model (already cheap, or a non-Floodgate
	// backend) and on ultrathink turns. Independent of DynamicEffort (which only
	// adjusts reasoning depth on the SAME model).
	AdaptiveModel bool
	// CheapReads, when true, routes rounds whose PREVIOUS step consisted entirely
	// of idempotent read tools (read_file/grep/glob/find_symbol/…) to the cheaper
	// FastModel — those rounds are pure exploration where the strong model adds
	// little. As soon as the model starts writing, edits files, runs bash, or
	// hits friction the full session model is restored. OFF by default; enable
	// with SQE_CHEAP_READS=1. A no-op when FastModel equals the session model.
	// Composes with AdaptiveModel; friction/hard/ultrathink override both.
	CheapReads bool
	// CheapReadsPersona, FastModelPersona, AuxModelPersona, AutoPlanPersona, and
	// SelfReviewPersona hold the values last explicitly set via the Persona
	// modal. They are non-nil only after the user has made an explicit choice,
	// and are applied by Load() AFTER env vars so a persona save always survives
	// a restart regardless of the deployed env config.
	// Stored in savedPrefs; managed only by applyPersonaEdit + Load().
	CheapReadsPersona *bool
	FastModelPersona  *string
	AuxModelPersona   *string
	AutoPlanPersona   *bool
	SelfReviewPersona *bool
	// VerifyCommand, when non-empty, is a shell command (e.g. a test/build) run
	// automatically at the end of a turn that edited files; if it exits non-zero
	// the failure is fed back so the agent self-corrects before finishing. Set it
	// via the /verify command or SQE_VERIFY_CMD. Empty disables the loop.
	VerifyCommand string
	// VerifyMaxRetries caps how many times one turn re-runs to fix a failing
	// verification before stopping for review (default 2; SQE_VERIFY_MAX).
	VerifyMaxRetries int
	// VerifyTimeoutSec bounds a single verification run in seconds (default 120;
	// SQE_VERIFY_TIMEOUT), so a hung test command can't wedge the turn.
	VerifyTimeoutSec int
	// SelfReview, when true, runs a cheap fast-model review of the turn's own diff
	// before finishing a turn that edited files: if it flags a likely introduced
	// bug, the finding is fed back once so the agent fixes it before stopping. Off
	// by default; enable with SQE_SELF_REVIEW=1. Independent of VerifyCommand
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
	// AutoDelegate, when true, makes the agent auto-delegate a clearly read-heavy
	// investigation ("how/where/why does X work", "investigate …", "check why …")
	// to the read-only `explore` sub-agent via the task tool, so the file reads
	// stay in the sub-agent's context instead of bloating (and re-billing) this
	// turn on every round. Requires the task tool; skipped in plan mode. OFF by
	// default (it changes how a turn is answered); enable with SQE_AUTO_DELEGATE=1.
	AutoDelegate bool
	// AutoOffloadReadBytes is the cumulative size (in bytes) of read-style tool
	// output within a single turn past which the agent — even when the turn did
	// not initially look like an investigation — starts steering further
	// exploration into the read-only `explore` sub-agent, so the bulky reads live
	// in the sub-agent's (discarded) context instead of re-billing this turn on
	// every round. Gated by AutoDelegate; 0 disables. SQE_AUTO_OFFLOAD_READ_BYTES
	// overrides.
	AutoOffloadReadBytes int
	// ConciseToolSchemas, when true, trims each tool's description to its first
	// sentence in the specs sent to the model (both Anthropic- and OpenAI-style
	// routes). Off by default since prompt caching already amortizes the full
	// descriptions; SQE_CONCISE_TOOL_SCHEMAS=1 enables it for uncached/token-tight
	// setups.
	ConciseToolSchemas bool
	// EmbeddingsModel, when non-empty, enables hybrid semantic reranking of
	// search_code results: the top BM25 candidates are re-scored by embedding
	// cosine similarity to the query and blended with their lexical score. The
	// embeddings are fetched from the OpenAI-compatible {base}/embeddings
	// endpoint (works on both Floodgate and the SQE Proxy). Empty disables it
	// (pure BM25). Env: SQE_EMBEDDINGS_MODEL.
	EmbeddingsModel string
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
	// dominant cost of long tool loops). 0 disables the pruning. SQE_TOOL_RESULT_KEEP overrides.
	ToolResultKeepRounds int
	// MaxReadLines is the default line cap for read_file when no explicit limit
	// is given. Smaller windows keep each read cheaper (less re-sent context) at
	// the cost of more paged reads on large files. SQE_READ_LINES overrides.
	MaxReadLines int
	// RelayAddr is the host:port of a relay to auto-connect to in the
	// background at startup (so a remote browser can view/drive this terminal
	// session). Empty disables it. Saved as a preference; SQE_RELAY overrides.
	RelayAddr string
	// ContextWindow, when > 0, overrides the per-model context-window size used
	// for the "ctx N%" indicator (e.g. set 1_000_000 for a 1M-token model whose
	// stable default is lower). 0 means auto (config.ContextWindow(model)).
	// Saved as a preference; SQE_CONTEXT_WINDOW overrides.
	ContextWindow int
}

// Version is the moony release version. It's the single source of
// truth: surfaced by `--version`, and the TUI header.
// Keep it in sync with the top entry of CHANGELOG.md.
const Version = "1.0.0"

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
	if llmProvider == provider.Anthropic {
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

// AuxDefaultModel is the model tool-less auxiliary calls run on by default — the
// goal judge, session titling, checkpoint distillation, history compaction,
// profile refresh, self-review, and "btw" notes. Those calls want a quick
// structured answer, not deliberation, so they use the cheapest capable tier
// (Haiku, $1/$5 vs the session model's Opus $5/$25 or Sonnet $3/$15). It is
// deliberately cheaper than FastModel's Sonnet default: read-only sub-agents and
// the adaptive main turn do real investigation/coding work, whereas these calls
// are mechanical. SQE_AUX_MODEL (falling back to SQE_FAST_MODEL) overrides.
const AuxDefaultModel = "llama3.1:8b"

// FastDefaultModel is the model FastModel uses when SQE_FAST_MODEL isn't
// pinned — a capable, cheaper Sonnet tier than the session's default Opus/
// Sonnet-4.6. Kept distinct from DefaultModel (the overall session default)
// so bumping one doesn't silently change the other.
const FastDefaultModel = "llama3.1:8b"

// FastModel returns the cheaper "fast" model to run grunt work on — read-only
// sub-agents (explore/code-reviewer) and the adaptive-mode main turn — so they
// don't burn the (often pricier) session model. Defaults to FastDefaultModel (a
// capable, cheaper Sonnet); SQE_FAST_MODEL overrides. The more mechanical
// auxiliary calls use the even-cheaper AuxModel instead. It never UPGRADES a
// parent that's already as cheap (by input price), so a session already on
// Haiku/Sonnet keeps its own model.
//
// llmProvider is the backend (floodgate/sqe-proxy/afm/ollama). The default is a
// Floodgate/Anthropic-route id, valid on Floodgate and on SQE Proxy (a
// relay-fronted Floodgate gateway serving the same ids). On AFM/Ollama it's the
// wrong server's model — substituting it would send e.g. a Claude id to an
// Ollama server (404) — so there, keep the session model unless the user
// explicitly pins one with SQE_FAST_MODEL.
func FastModel(parent, llmProvider string) string {
	return cheapModel(parent, llmProvider, FastDefaultModel, "SQE_FAST_MODEL")
}

// AuxModel returns the cheap model for tool-less auxiliary calls (see
// AuxDefaultModel). It applies the same safety rules as FastModel — never
// upgrading an already-cheaper parent, and keeping the session model on AFM/
// Ollama (which don't serve the Floodgate catalog) unless pinned — and honors
// SQE_AUX_MODEL first, then SQE_FAST_MODEL, so a single fast-model pin covers
// both. The Haiku downshift applies on Floodgate and SQE Proxy alike.
func AuxModel(parent, llmProvider string) string {
	return cheapModel(parent, llmProvider, AuxDefaultModel, "SQE_AUX_MODEL", "SQE_FAST_MODEL")
}

// cheapModel resolves a cheaper model to offload work from `parent` onto: it
// returns `def` (or the first non-empty env override named in `envVars`) unless
// doing so would UPGRADE a parent already as cheap by input price, or the backend
// doesn't serve Floodgate's model catalog (AFM/Ollama, where `def` is the wrong
// server's id) and no override is pinned — in which case it keeps `parent`.
func cheapModel(parent, llmProvider, def string, envVars ...string) string {
	var v string
	for _, e := range envVars {
		if v = strings.TrimSpace(os.Getenv(e)); v != "" {
			break
		}
	}
	if v == "" && !provider.ServesFloodgateModels(llmProvider) {
		// AFM/Ollama: the Floodgate/Anthropic-route default id isn't served here.
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

// ProviderLabel is the user-facing name of a route, e.g. "floodgate anthropic".
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
// prevents sending effort:"off" — Floodgate's effort field accepts only
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
	case provider.OpenAI:
		return IsOpenAIReasoningModel(model)
	case provider.DeepSeek:
		return strings.Contains(strings.ToLower(model), "reasoner")
	case provider.XAI:
		m := strings.ToLower(model)
		return strings.Contains(m, "grok-3-mini") || strings.Contains(m, "grok-4")
	case provider.Groq:
		m := strings.ToLower(model)
		return strings.Contains(m, "qwq") || strings.Contains(m, "r1")
	case provider.Mistral:
		return strings.Contains(strings.ToLower(model), "magistral")
	case provider.OpenRouter, provider.Gemini, provider.Together, provider.Fireworks:
		return true
	default: // ollama, anthropic
		return ModelSupportsThinking(model)
	}
}

// ModelInfo is a curated preset model the user can pick from /models.
type ModelInfo struct {
	ID       string
	Name     string
	Thinking bool // whether the model supports extended thinking
}

// KnownModels is a best-effort curated list of Floodgate Anthropic models for
// the /models picker fallback (the live list is fetched from Floodgate when
// available). Users can still select any id via `/model <id>`.
var KnownModels = []ModelInfo{
	{"anthropic.claude-opus-4-8", "Claude Opus 4.8", true},
	{"anthropic.claude-sonnet-5", "Claude Sonnet 5", true},
	{"anthropic.claude-sonnet-4-6", "Claude Sonnet 4.6", true},
	{"anthropic.claude-haiku-4-5-20251001-v1:0", "Claude Haiku 4.5", false},
	{"anthropic.claude-opus-4-1-20250805-v1:0", "Claude Opus 4.1", true},
	{"anthropic.claude-sonnet-4-5-20250929-v1:0", "Claude Sonnet 4.5", true},
}

// KnownOpenAIModels is the curated list served via Floodgate's OpenAI-compatible
// endpoint — Gemini only. (Claude is served via the native Anthropic endpoint;
// the endpoint is chosen by ProviderForModel from the model id.)
var KnownOpenAIModels = []ModelInfo{
	{"gcp:gemini-3.1-pro-preview", "Gemini 3.1 Pro preview", true},
	{"gcp:gemini-2.5-pro", "Gemini 2.5 Pro", true},
}

// AllKnownModels is the merged curated list across both endpoints (Anthropic
// Claude + OpenAI-route Gemini), used as the /models fallback when the live
// fetch fails. The route for each is derived from its id by ProviderForModel.
// GenAIOnboardingURL is where users join the GenAI-authorized group that grants
// access to the Floodgate model endpoints.
const GenAIOnboardingURL = "https://genai.apple.com/profile/onboarding"

// NotInGenAIGroup reports whether a model-fetch error is the Floodgate proxy's
// "Caller not in authorized group" 403 — i.e. the user hasn't onboarded to GenAI.
func NotInGenAIGroup(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "authorized group")
}

// ModelFetchNote returns a user-facing note for a failed model fetch. For the
// not-onboarded case it hides the raw 403 and points to onboarding; otherwise it
// surfaces the underlying error. Both cases fall back to the built-in list.
func ModelFetchNote(err error) string {
	if NotInGenAIGroup(err) {
		return "You're not in the GenAI authorized group yet — finish onboarding at " +
			GenAIOnboardingURL + ", then retry. Showing the built-in model list for now."
	}
	if ModelRegionRestricted(err) {
		return regionRestrictedNote + " Showing the built-in model list for now."
	}
	return "Could not fetch models (" + err.Error() + "); showing the built-in list."
}

// ModelRegionRestrictionsURL documents which models are usable in which regions.
const ModelRegionRestrictionsURL = "https://genai.apple.com/policies/models/#model-regional-restrictions"

const regionRestrictedNote = "This model is unavailable in your region."

// ModelRegionRestricted reports whether an error is Floodgate's "model unavailable
// in your region" 403 (a regional restriction, not a group/onboarding problem).
func ModelRegionRestricted(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "unavailable in your region") || strings.Contains(s, "regional restrictions")
}

// TurnErrorNote rewrites known Floodgate turn/inference errors into friendlier
// guidance for display; unrecognized errors return their raw text unchanged.
func TurnErrorNote(err error) string {
	if err == nil {
		return ""
	}
	if ModelRegionRestricted(err) {
		return regionRestrictedNote
	}
	return err.Error()
}

func AllKnownModels() []ModelInfo {
	out := make([]ModelInfo, 0, len(KnownModels)+len(KnownOpenAIModels))
	out = append(out, KnownModels...)
	out = append(out, KnownOpenAIModels...)
	return out
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
//	FLOODGATE_BASE_URL       override the Floodgate base URL (Anthropic route)
//	SQE_OPENAI_BASE_URL      override the OpenAI route base URL (incl. /v1)
//	SQE_MODEL                override the model identifier (the route is derived from it)
//	SQE_REASONING            override the reasoning level (off/low/medium/high)
//	FLOODGATE_PROJECT_TOKEN  use a dedicated project quota
//
// cheapModeEnabled reports whether SQE_CHEAP requests the maximum-savings preset.
func cheapModeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_CHEAP"))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// applyCheapPreset flips cost-relevant defaults toward the lowest token spend —
// the baseline for SQE_CHEAP. It enables adaptive (cheaper) model routing and
// concise tool schemas, and ages/compacts context sooner. Individual SQE_* env
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
		OllamaBaseURL:      "http://localhost:11434/v1",
		OpenAIBaseURL:      "https://api.openai.com/v1",
		AnthropicBaseURL:   "https://api.anthropic.com",
		OpenRouterBaseURL:  "https://openrouter.ai/api/v1",
		GeminiBaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai",
		DeepSeekBaseURL:    "https://api.deepseek.com",
		GroqBaseURL:        "https://api.groq.com/openai/v1",
		XAIBaseURL:         "https://api.x.ai/v1",
		MistralBaseURL:     "https://api.mistral.ai/v1",
		TogetherBaseURL:    "https://api.together.xyz/v1",
		FireworksBaseURL:   "https://api.fireworks.ai/inference/v1",
		LLMProvider:        "ollama",
		Model:              DefaultModel,
		MaxTokens:          8192,
		MaxToolOutputBytes: 16000,
		MaxAgentSteps:      80,
		AutoContinue:       1,
		BatchContext:       true,
		RepoMap:            true,
		LazyMCPTools:       true,
		LazyBuiltinTools:   true,
		Terse:              true,
		CodeOutline:        true,
		DynamicEffort:      true,
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

	// SQE_CHEAP: one opt-in switch for maximum token savings. It flips the
	// built-in defaults toward lower cost; because sub-agents and workflow nodes
	// inherit this config (scoped.go: cfg := *base), the savings reach every flow.
	// Individual SQE_* knobs parsed below still override it — opt in, then tune a
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
	c.RelayAddr = strings.TrimSpace(prefs.RelayAddr)
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
	// vars below, so a deployment's SQE_AUTO_PLAN/SQE_SELF_REVIEW could clobber
	// a saved persona choice on every restart. They're now applied in the
	// "Persona-explicit overrides" block at the end of Load(), same as
	// CheapReads/FastModel/AuxModel, so an explicit save always wins.

	// The model defaults to DefaultModel, then a saved preference, then the
	// environment. The route is derived from the final model id (below).
	if prefs.Model != "" {
		c.Model = prefs.Model
	}
	// Backend provider: saved preference, then SQE_PROVIDER, normalized against
	// the registry (unknown/empty -> ollama).
	llmProvider := prefs.LLMProvider
	if v := strings.TrimSpace(os.Getenv("SQE_PROVIDER")); v != "" {
		llmProvider = v
	}
	c.LLMProvider = provider.Normalize(llmProvider)
	// Ollama host (SQE_OLLAMA_HOST preferred; OLLAMA_HOST is the upstream's own
	// convention). A bare host (no "/v1") gets the OpenAI-compatible suffix added.
	if v := strings.TrimSpace(firstEnv("SQE_OLLAMA_HOST", "OLLAMA_HOST")); v != "" {
		c.OllamaBaseURL = NormalizeOllamaBaseURL(v)
	}
	c.OllamaToken = strings.TrimSpace(os.Getenv("SQE_OLLAMA_TOKEN"))
	c.OpenAIAPIKey = strings.TrimSpace(firstEnv("SQE_OPENAI_API_KEY", "OPENAI_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_OPENAI_BASE_URL")); v != "" {
		c.OpenAIBaseURL = v
	}
	c.AnthropicAPIKey = strings.TrimSpace(firstEnv("SQE_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_ANTHROPIC_BASE_URL")); v != "" {
		c.AnthropicBaseURL = v
	}
	c.OpenRouterAPIKey = strings.TrimSpace(firstEnv("SQE_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_OPENROUTER_BASE_URL")); v != "" {
		c.OpenRouterBaseURL = v
	}
	c.GeminiAPIKey = strings.TrimSpace(firstEnv("SQE_GEMINI_API_KEY", "GEMINI_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_GEMINI_BASE_URL")); v != "" {
		c.GeminiBaseURL = v
	}
	c.DeepSeekAPIKey = strings.TrimSpace(firstEnv("SQE_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_DEEPSEEK_BASE_URL")); v != "" {
		c.DeepSeekBaseURL = v
	}
	c.GroqAPIKey = strings.TrimSpace(firstEnv("SQE_GROQ_API_KEY", "GROQ_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_GROQ_BASE_URL")); v != "" {
		c.GroqBaseURL = v
	}
	c.XAIAPIKey = strings.TrimSpace(firstEnv("SQE_XAI_API_KEY", "XAI_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_XAI_BASE_URL")); v != "" {
		c.XAIBaseURL = v
	}
	c.MistralAPIKey = strings.TrimSpace(firstEnv("SQE_MISTRAL_API_KEY", "MISTRAL_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_MISTRAL_BASE_URL")); v != "" {
		c.MistralBaseURL = v
	}
	c.TogetherAPIKey = strings.TrimSpace(firstEnv("SQE_TOGETHER_API_KEY", "TOGETHER_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_TOGETHER_BASE_URL")); v != "" {
		c.TogetherBaseURL = v
	}
	c.FireworksAPIKey = strings.TrimSpace(firstEnv("SQE_FIREWORKS_API_KEY", "FIREWORKS_API_KEY"))
	if v := strings.TrimSpace(firstEnv("SQE_FIREWORKS_BASE_URL")); v != "" {
		c.FireworksBaseURL = v
	}
	if v := os.Getenv("SQE_MODEL"); v != "" {
		c.Model = v
	} else if prefs.Model == "" && c.LLMProvider != provider.Default {
		// No saved/explicit model, but a non-default provider was selected
		// (saved or via SQE_PROVIDER): start on that provider's default model
		// instead of Ollama's.
		c.Model = provider.DefaultModelFor(c.LLMProvider)
	}
	// Provider is the wire protocol (anthropic-native vs openai-compatible),
	// derived directly from the backend now that each backend fully determines
	// its own wire shape (it no longer depends on guessing from the model id).
	c.Provider = wireRouteFor(c.LLMProvider)
	// Resolve the active reasoning level from the per-model memory now that
	// c.Model is finalized. A per-model entry wins over the legacy single
	// value; the SQE_REASONING env below still overrides everything.
	if lvl, ok := c.ReasoningByModel[c.Model]; ok {
		c.ReasoningLevel = NormalizeReasoning(lvl)
	}
	if v := os.Getenv("SQE_REASONING"); v != "" {
		c.ReasoningLevel = NormalizeReasoning(v)
	}
	if v := os.Getenv("SQE_APPROVAL"); v != "" {
		c.ApprovalPolicy = NormalizeApprovalPolicy(v)
	}
	if v := os.Getenv("SQE_SANDBOX"); v != "" {
		c.SandboxMode = NormalizeSandboxMode(v)
	}
	if v := strings.TrimSpace(os.Getenv("SQE_AUTO_PLAN")); v != "" {
		c.AutoPlan = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on")
	}
	if v := strings.TrimSpace(os.Getenv("SQE_AUTO_DELEGATE")); v != "" {
		// Defaults off; the env var can turn it ON (1/true/on/yes) or explicitly OFF.
		c.AutoDelegate = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(os.Getenv("SQE_RELAY")); v != "" {
		c.RelayAddr = v // env overrides the saved relay address
	}
	if v := strings.TrimSpace(os.Getenv("SQE_CONTEXT_WINDOW")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n > 0 {
			c.ContextWindow = n
		}
	}
	if v := os.Getenv("SQE_SANDBOX_NET"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "off", "false", "no":
			c.SandboxNetwork = false
		}
	}
	if v := os.Getenv("SQE_MAX_STEPS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			c.MaxAgentSteps = n
		}
	}
	if v := os.Getenv("SQE_AUTO_CONTINUE"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			c.AutoContinue = n
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_BATCH_CONTEXT"))) {
	case "1", "on", "true", "yes":
		c.BatchContext = true
	case "0", "off", "false", "no":
		c.BatchContext = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_LAZY_MCP"))) {
	case "1", "on", "true", "yes":
		c.LazyMCPTools = true
	case "0", "off", "false", "no":
		c.LazyMCPTools = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_LAZY_BUILTIN_TOOLS"))) {
	case "1", "on", "true", "yes":
		c.LazyBuiltinTools = true
	case "0", "off", "false", "no":
		c.LazyBuiltinTools = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_PROJECT_PROFILE"))) {
	case "1", "on", "true", "yes":
		c.ProjectProfile = true
	case "0", "off", "false", "no":
		c.ProjectProfile = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_REPO_MAP"))) {
	case "1", "on", "true", "yes":
		c.RepoMap = true
	case "0", "off", "false", "no":
		c.RepoMap = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_TERSE"))) {
	case "1", "on", "true", "yes":
		c.Terse = true
	case "0", "off", "false", "no":
		c.Terse = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_CODE_OUTLINE"))) {
	case "1", "on", "true", "yes":
		c.CodeOutline = true
	case "0", "off", "false", "no":
		c.CodeOutline = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_DYNAMIC_EFFORT"))) {
	case "1", "on", "true", "yes":
		c.DynamicEffort = true
	case "0", "off", "false", "no":
		c.DynamicEffort = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_ADAPTIVE_MODEL"))) {
	case "1", "on", "true", "yes":
		c.AdaptiveModel = true
	case "0", "off", "false", "no":
		c.AdaptiveModel = false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_CHEAP_READS"))) {
	case "1", "on", "true", "yes":
		c.CheapReads = true
	case "0", "off", "false", "no":
		c.CheapReads = false
	}
	if v := strings.TrimSpace(os.Getenv("SQE_VERIFY_CMD")); v != "" {
		c.VerifyCommand = v
	}
	if v := strings.TrimSpace(os.Getenv("SQE_VERIFY_MAX")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.VerifyMaxRetries = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_VERIFY_TIMEOUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.VerifyTimeoutSec = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_SELF_REVIEW")); v != "" {
		c.SelfReview = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(os.Getenv("SQE_COMPACT_THRESHOLD")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.CompactThresholdTokens = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_COMPACT_KEEP")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.CompactKeepTokens = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_MAX_TOOL_OUTPUT")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n > 0 {
			c.MaxToolOutputBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_TASK_RESULT_MAX")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.TaskResultMaxBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_TOOL_RESULT_KEEP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.ToolResultKeepRounds = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_AUTO_OFFLOAD_READ_BYTES")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n >= 0 {
			c.AutoOffloadReadBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_CONCISE_TOOL_SCHEMAS")); v != "" {
		c.ConciseToolSchemas = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
	}
	if v := strings.TrimSpace(os.Getenv("SQE_READ_LINES")); v != "" {
		if n, err := ParseTokenCount(v); err == nil && n > 0 {
			c.MaxReadLines = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SQE_EMBEDDINGS_MODEL")); v != "" {
		c.EmbeddingsModel = v
	}

	// Persona-explicit overrides — applied LAST so a user's explicit save from
	// the web UI always wins over both the deployment env and the cheap preset.
	// Only active when the user has actually made a choice (non-nil pointer).
	if prefs.CheapReads != nil {
		c.CheapReads = *prefs.CheapReads
		c.CheapReadsPersona = prefs.CheapReads
	}
	if prefs.AutoPlan != nil {
		c.AutoPlan = *prefs.AutoPlan
		c.AutoPlanPersona = prefs.AutoPlan
	}
	if prefs.SelfReview != nil {
		c.SelfReview = *prefs.SelfReview
		c.SelfReviewPersona = prefs.SelfReview
	}
	if prefs.FastModel != nil {
		fm := strings.TrimSpace(*prefs.FastModel)
		c.FastModelPersona = &fm
		if fm != "" {
			os.Setenv("SQE_FAST_MODEL", fm)
		} else {
			os.Unsetenv("SQE_FAST_MODEL")
		}
	}
	if prefs.AuxModel != nil {
		am := strings.TrimSpace(*prefs.AuxModel)
		c.AuxModelPersona = &am
		if am != "" {
			os.Setenv("SQE_AUX_MODEL", am)
		} else {
			os.Unsetenv("SQE_AUX_MODEL")
		}
	}

	return c
}
