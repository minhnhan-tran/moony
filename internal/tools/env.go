package tools

import (
	"os"
	"strings"
)

// sensitiveEnvKeys are secrets / sensitive config that must never leak into the
// commands the agent runs. The bash and background-job tools strip these from
// the subprocess environment, so the model can't read them via `$VAR`, `env`, or
// `printenv`.
var sensitiveEnvKeys = map[string]bool{
	"MN_OLLAMA_TOKEN":       true, // optional bearer for a remote Ollama server
	"MN_OPENAI_API_KEY":     true,
	"OPENAI_API_KEY":        true,
	"MN_ANTHROPIC_API_KEY":  true,
	"ANTHROPIC_API_KEY":     true,
	"MN_OPENROUTER_API_KEY": true,
	"OPENROUTER_API_KEY":    true,
	"MN_GEMINI_API_KEY":     true,
	"GEMINI_API_KEY":        true,
	"MN_DEEPSEEK_API_KEY":   true,
	"DEEPSEEK_API_KEY":      true,
	"MN_GROQ_API_KEY":       true,
	"GROQ_API_KEY":          true,
	"MN_XAI_API_KEY":        true,
	"XAI_API_KEY":           true,
	"MN_MISTRAL_API_KEY":    true,
	"MISTRAL_API_KEY":       true,
	"MN_TOGETHER_API_KEY":   true,
	"TOGETHER_API_KEY":      true,
	"MN_FIREWORKS_API_KEY":  true,
	"FIREWORKS_API_KEY":     true,
	"MN_CEREBRAS_API_KEY":   true,
	"CEREBRAS_API_KEY":      true,
	"MN_PERPLEXITY_API_KEY": true,
	"PERPLEXITY_API_KEY":    true,
	"PPLX_API_KEY":          true,
	"MN_COHERE_API_KEY":     true,
	"CO_API_KEY":            true,
	"COHERE_API_KEY":        true,
	"MN_DEEPINFRA_API_KEY":  true,
	"DEEPINFRA_API_KEY":     true,
	"DEEPINFRA_TOKEN":       true,
	"MN_LMSTUDIO_API_KEY":   true,
	"LM_STUDIO_API_KEY":     true,
	"MN_AZURE_API_KEY":      true,
	"AZURE_OPENAI_API_KEY":  true,
	"AZURE_API_KEY":         true,
	"MN_VERTEX_API_KEY":     true,
	"GOOGLE_VERTEX_API_KEY": true,
	// AWS Bedrock: scrub the MN_-prefixed credentials. The standard AWS_*
	// vars are deliberately left in place (like GH_TOKEN) so the agent's own
	// `aws` CLI shell tools keep working.
	"MN_AWS_ACCESS_KEY_ID":     true,
	"MN_AWS_SECRET_ACCESS_KEY": true,
	"MN_AWS_SESSION_TOKEN":     true,
	"MN_COPILOT_TOKEN":         true, // GitHub token for the Copilot provider
	"COPILOT_GITHUB_TOKEN":     true,
	"MN_ENV_FILE":              true, // points at the secrets file
}

// scrubbedEnv returns the current environment with sensitiveEnvKeys removed, for
// use as a child process's cmd.Env. Removing an absent key is a no-op, so this is
// always safe to apply.
func scrubbedEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		key, _, _ := strings.Cut(kv, "=")
		if sensitiveEnvKeys[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// scrubbedEnvFor is scrubbedEnv with PWD pinned to dir, so a subprocess's $PWD
// matches its actual working directory (cmd.Dir). Without this, $PWD would carry
// over from the host process (the launch dir) and a script reading $PWD would see
// the wrong directory for any session NOT rooted at the launch dir — e.g. a web
// session opened elsewhere, or after a /cd. `pwd`/getcwd already report cmd.Dir;
// this aligns $PWD with it. Any inherited PWD is dropped first so there's no
// duplicate key.
func scrubbedEnvFor(dir string) []string {
	src := scrubbedEnv()
	out := src[:0:0] // fresh backing array; don't alias scrubbedEnv's slice
	for _, kv := range src {
		if key, _, _ := strings.Cut(kv, "="); key == "PWD" {
			continue
		}
		out = append(out, kv)
	}
	if dir != "" {
		out = append(out, "PWD="+dir)
	}
	return out
}

// ScrubbedEnv is the exported form of scrubbedEnv: the process environment minus
// the auth/API secrets. Agent-run subprocesses defined OUTSIDE this package
// (e.g. the verify loop in internal/agent) use it so they strip the same secrets
// the bash/job tools do — keeping credentials out of build/test commands.
func ScrubbedEnv() []string { return scrubbedEnv() }
