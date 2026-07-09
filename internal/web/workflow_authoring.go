package web

// Agent Builder authoring endpoints: create a brand-new subagent persona or Agent
// Skill from the builder, persisting caller-supplied content (not a fixed
// template or a catalog entry). These are laptop-scoped (siblings of the other
// /api/workflows routes) since the builder runs per-machine without a chat
// session — so after writing the file we re-attach to every live session so the
// new capability is immediately usable there too, not just in workflow runs
// (which load subagents/skills fresh each run).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
)

// nameSanitize keeps a builder-supplied name to the safe slug both stores accept
// (a single path segment): lowercase, digits, dash. Spaces/underscores collapse
// to dashes; anything else is dropped.
var nameSanitize = regexp.MustCompile(`[^a-z0-9-]+`)

func normalizeCapName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = nameSanitize.ReplaceAllString(s, "")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// reattachSubAgentsAll re-attaches the enabled persona set to every live session
// so a newly created/changed persona is delegate-able immediately.
func (s *Server) reattachSubAgentsAll(wd string) {
	s.mu.Lock()
	live := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess != nil && sess.agent != nil {
			live = append(live, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range live {
		s.reattachSubAgents(wd, sess)
	}
}

// reattachSkillsAll re-attaches the enabled skill set to every live session.
func (s *Server) reattachSkillsAll(wd string) {
	s.mu.Lock()
	live := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess != nil && sess.agent != nil {
			live = append(live, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range live {
		s.reattachSkills(wd, sess)
	}
}

// handleCreateAgent: POST /api/workflows/agents — create a new subagent persona
// from the builder. Body: {name, description, model?, tools?[], systemPrompt}.
// Writes to the global agents dir (so it's available across projects) and
// re-attaches live sessions. Returns {name, description}.
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Model        string   `json:"model"`
		Tools        []string `json:"tools"`
		SystemPrompt string   `json:"systemPrompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	name := normalizeCapName(body.Name)
	if name == "" {
		http.Error(w, "a name (letters/digits/dashes) is required", http.StatusBadRequest)
		return
	}
	wd := s.cfg.WorkingDir
	if _, err := subagents.Create(name, body.Description, body.Model, body.Tools, body.SystemPrompt, true, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.reattachSubAgentsAll(wd)
	writeJSON(w, map[string]any{"name": name, "description": strings.TrimSpace(body.Description)})
}

// handleCreateSkill: POST /api/workflows/skills — create a new Agent Skill from
// the builder. Body: {name, description, body}. Writes the SKILL.md to the global
// skills dir and re-attaches live sessions. Returns {name, description}.
func (s *Server) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	name := normalizeCapName(body.Name)
	if name == "" {
		http.Error(w, "a name (letters/digits/dashes) is required", http.StatusBadRequest)
		return
	}
	wd := s.cfg.WorkingDir
	if _, err := skills.Create(name, body.Description, body.Body, true, wd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.reattachSkillsAll(wd)
	writeJSON(w, map[string]any{"name": name, "description": strings.TrimSpace(body.Description)})
}

// workflowModelOption is one row in the builder's model picker.
type workflowModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleWorkflowModels: GET /api/workflows/models → the models an agent node (or
// a new persona) can pin, for the Agent Builder's model picker. Mirrors
// handleAgentModels but laptop-scoped (no session): the live list from the active
// backend, falling back on failure to config.ModelFallback (scoped to the active
// provider, e.g. its own default model or the Anthropic/Gemini curated list, never an
// unrelated one) so the picker never offers unservable models; the UI still shows
// "inherit" plus any existing value. The empty id ("inherit the parent's model") is added by the UI.
func (s *Server) handleWorkflowModels(w http.ResponseWriter, r *http.Request) {
	client := llm.NewClient(s.cfg)
	models, err := client.ListModels(r.Context())
	if err != nil || len(models) == 0 {
		// Fall back scoped to the active provider, not an unrelated static catalog.
		models = config.ModelFallback(s.cfg.LLMProvider)
	}
	out := make([]workflowModelOption, 0, len(models))
	for _, m := range models {
		out = append(out, workflowModelOption{ID: m.ID, Name: m.Name})
	}
	writeJSON(w, out)
}

// workflowRuntime is the connector's current run settings (what a workflow run
// uses) plus the choices for the builder's provider/model/reasoning switcher.
type workflowRuntime struct {
	Provider        string           `json:"provider"`
	Model           string           `json:"model"`
	Reasoning       string           `json:"reasoning"`
	Providers       []providerOption `json:"providers"`
	ReasoningLevels []string         `json:"reasoningLevels"`
}

type providerOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc,omitempty"`
}

func (s *Server) runtimeSnapshot() workflowRuntime {
	provs := make([]providerOption, 0)
	for _, p := range provider.List() {
		provs = append(provs, providerOption{ID: p.ID, Label: p.Label, Desc: p.Desc})
	}
	return workflowRuntime{
		Provider:        provider.Normalize(s.cfg.LLMProvider),
		Model:           s.cfg.Model,
		Reasoning:       s.cfg.GetReasoningFor(s.cfg.Model),
		Providers:       provs,
		ReasoningLevels: config.ReasoningLevelsFor(s.cfg.Model),
	}
}

// handleWorkflowRuntime: GET /api/workflows/runtime → the connector's current
// provider/model/reasoning plus the available providers and (model-specific)
// reasoning levels, for the builder's runtime switcher.
func (s *Server) handleWorkflowRuntime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.runtimeSnapshot())
}

// handleSetWorkflowRuntime: POST /api/workflows/runtime — set the connector's
// provider, model, and/or reasoning (whichever fields are present). These drive
// what a workflow run uses (the runner builds its client + scoped config from
// s.cfg). Mirrors the chat /provider, /model, /reasoning commands but operates
// laptop-scoped on s.cfg, then persists. Returns the refreshed runtime (so the
// UI can update the reasoning levels when the model changed). Switching TO the
// relay proxy is gated on group membership, like providerCommand.
func (s *Server) handleSetWorkflowRuntime(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider  *string `json:"provider"`
		Model     *string `json:"model"`
		Reasoning *string `json:"reasoning"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if body.Provider != nil {
		name := strings.ToLower(strings.TrimSpace(*body.Provider))
		p, ok := provider.Lookup(name)
		if !ok {
			http.Error(w, "unknown provider "+name, http.StatusBadRequest)
			return
		}
		s.cfg.LLMProvider = p.ID
		// A model id is backend-specific, so move to this provider's default.
		if dm := provider.DefaultModelFor(p.ID); dm != "" && dm != s.cfg.Model {
			s.cfg.SetModel(dm)
		}
	}
	if body.Model != nil {
		if m := strings.TrimSpace(*body.Model); m != "" {
			s.cfg.SetModel(m) // re-derives the route + reasoning for the new model
		}
	}
	if body.Reasoning != nil {
		lvl := config.NormalizeReasoningFor(s.cfg.Model, strings.ToLower(strings.TrimSpace(*body.Reasoning)))
		s.cfg.SetReasoningFor(s.cfg.Model, lvl)
	}
	if err := s.cfg.Save(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.runtimeSnapshot())
}

// agentGenSystem / skillGenSystem are the meta-prompts that turn a one-line
// description into a draft agent persona / skill, like Claude Code's "generate".
// They demand a single JSON object so the result drops straight into the form.
const agentGenSystem = `You are an expert at authoring subagent personas for an AI coding agent. Given the user's description, design a focused, practical subagent.

Respond with ONLY a single JSON object — no prose, no markdown, no code fences — with exactly these keys:
- "name": a short kebab-case identifier (lowercase letters, digits, dashes), e.g. "security-reviewer".
- "description": one sentence on what it does and WHEN to delegate to it (this is what the parent agent reads to decide).
- "systemPrompt": the subagent's full system prompt, written in the second person ("You are the … subagent."). Describe its role, method, constraints, and exactly what output it must return to the parent. Keep it focused on the single subtask it owns.
- "tools": an array of tool names it needs, chosen ONLY from this list: [%s]. Use [] (empty) if it should have all tools or you are unsure.

Make the system prompt genuinely useful and specific to the request — not a template.`

const skillGenSystem = `You are an expert at authoring Agent Skills (reusable instruction modules) for an AI coding agent. A skill is loaded on demand when its description matches the task. Given the user's description, design a practical skill.

Respond with ONLY a single JSON object — no prose, no markdown fences — with exactly these keys:
- "name": a short kebab-case identifier (lowercase letters, digits, dashes), e.g. "pdf-tools".
- "description": one sentence on what the skill does and WHEN to use it (this is how the agent decides to load it — make it trigger-rich).
- "body": the skill instructions in Markdown — the concrete workflow, steps, commands, and conventions the agent should follow once this skill is loaded. Be specific and actionable.`

// handleGenerateCapability: POST /api/workflows/generate { kind, prompt } → a
// model-drafted agent/skill definition the New-agent/New-skill form pre-fills.
// One tool-less LLM completion on the connector's current model; the user reviews
// and edits before Create, so this is a drafting aid, not an auto-writer.
func (s *Server) handleGenerateCapability(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind   string `json:"kind"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		http.Error(w, "a description is required", http.StatusBadRequest)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	var system string
	switch kind {
	case "skill":
		system = skillGenSystem
	case "", "agent":
		kind = "agent"
		reg := tools.Default(s.cfg.WorkingDir, s.cfg.MaxToolOutputBytes)
		system = fmt.Sprintf(agentGenSystem, strings.Join(reg.Names(), ", "))
	default:
		http.Error(w, "unknown kind "+kind, http.StatusBadRequest)
		return
	}

	client := llm.NewClient(s.cfg)
	resp, err := client.StreamMessages(r.Context(), system, []llm.Message{llm.UserText(prompt)}, nil, llm.Handler{})
	if err != nil {
		http.Error(w, "generation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	js := firstJSONObject(llmText(resp))
	if js == "" {
		http.Error(w, "the model didn't return a usable definition — try rephrasing your description", http.StatusUnprocessableEntity)
		return
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		http.Error(w, "the model returned malformed JSON — try again", http.StatusUnprocessableEntity)
		return
	}
	// Normalize the name to the slug both stores accept; echo the kind back.
	if n, ok := out["name"].(string); ok {
		out["name"] = normalizeCapName(n)
	}
	out["kind"] = kind
	writeJSON(w, out)
}

// llmText concatenates the text blocks of an llm.Response.
func llmText(r *llm.Response) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range r.Blocks {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// firstJSONObject returns the first balanced {...} object in s — models often wrap
// JSON in prose or code fences despite instructions. Returns "" if none is found.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
