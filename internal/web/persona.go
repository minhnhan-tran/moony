package web

// Persona panel — view/edit the connected terminal's runtime env/config from the
// web UI's user menu. GET returns the current values (secrets masked to a last-4
// hint — the full token never leaves the server); POST applies edits to the live
// config (immediate effect, since the LLM client reads these per-request via its
// shared *config.Config pointer). Edits are runtime-only: not persisted, so a
// restart reverts to the deployed env/Kube config. Mirrors the /provider + /model
// pattern of mutating the session config and s.cfg together.

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
			models = config.AllKnownModels()
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
	// Env-only fields (tokens, URLs, limits) aren't in prefs, so they remain runtime
	// overrides that revert to the deployed env on restart. Best-effort.
	saveErr := cfg.Save()
	if s.logf != nil {
		changed := append(personaEditedFields(edit), tokenChanged...)
		if saveErr != nil {
			s.logf(fmt.Sprintf("persona: saved config (%s); persist warning: %v", strings.Join(changed, ", "), saveErr))
		} else {
			s.logf(fmt.Sprintf("persona: saved config (%s)", strings.Join(changed, ", ")))
		}
	}
	// Re-fetch live model list for the POST response so the dropdown stays current.
	client2 := llm.NewClient(cfg)
	models2, err2 := client2.ListModels(r.Context())
	if err2 != nil || len(models2) == 0 {
		models2 = config.AllKnownModels()
	}
	writeJSON(w, map[string]any{"fields": personaFields(cfg, models2)})
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
	}
	if e.MaxAgentSteps != nil {
		setEnvPlain("SQE_MAX_STEPS", e.MaxAgentSteps)
		if n, err := strconv.Atoi(strings.TrimSpace(*e.MaxAgentSteps)); err == nil && n > 0 {
			c.MaxAgentSteps = n
		}
	}
	if e.AutoContinue != nil {
		setEnvPlain("SQE_AUTO_CONTINUE", e.AutoContinue)
		if n, err := strconv.Atoi(strings.TrimSpace(*e.AutoContinue)); err == nil && n >= 0 {
			c.AutoContinue = n
		}
	}
	if e.SqeFastModel != nil {
		setEnvPlain("SQE_FAST_MODEL", e.SqeFastModel)
		fm := strings.TrimSpace(*e.SqeFastModel)
		c.FastModelPersona = &fm // remember as explicit persona choice
	}
	if e.SqeAuxModel != nil {
		setEnvPlain("SQE_AUX_MODEL", e.SqeAuxModel)
		am := strings.TrimSpace(*e.SqeAuxModel)
		c.AuxModelPersona = &am // remember as explicit persona choice
	}
	// Session-encryption vars live only in the process env (session.NewStore reads
	// them). Secrets keep their existing value when submitted empty; plain fields
	// (mode/path) apply as given and clear when blanked.
	setEnvSecret("SQE_SESSION_KEY", e.SessionKey)
	setEnvSecret("SQE_SESSION_KEY_CMD", e.SessionKeyCmd)
	setEnvSecret("SQE_SESSION_PASSPHRASE", e.SessionPassphrase)
	setEnvPlain("SQE_SESSION_KEY_FILE", e.SessionKeyFile)
	setEnvPlain("SQE_SESSION_ENCRYPT", e.SessionEncrypt)
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
			Desc: "Ollama server (SQE_OLLAMA_HOST), used by the ollama backend."},
		{Key: "autoDelegate", Label: "Auto-delegate subtasks", Type: "bool", Value: c.AutoDelegate,
			Desc: "Let the agent spin up sub-agents for large multi-step tasks."},
		{Key: "autoPlan", Label: "Auto-plan large tasks", Type: "bool", Value: c.AutoPlan,
			Desc: "Auto-enter read-only plan mode for big/multi-step tasks before touching files (persists)."},
		{Key: "cheapReads", Label: "Cheap reads (SQE_CHEAP_READS)", Type: "bool", Value: c.CheapReads,
			Desc: "Route pure-read rounds (grep / read_file / find_symbol / …) to the cheaper FastModel automatically. Reverts to the full model the moment writing starts, bash runs, or friction is detected. Persists across sessions."},
		{Key: "selfReview", Label: "Self-review diffs (SQE_SELF_REVIEW)", Type: "bool", Value: c.SelfReview,
			Desc: "After each turn that edits code, run a cheap fast-model review of the diff for logic bugs. One feedback cycle per turn; skipped for sub-agents. Persists across sessions."},
		{Key: "adaptiveModel", Label: "Adaptive model (SQE_ADAPTIVE_MODEL)", Type: "bool", Value: c.AdaptiveModel,
			Desc: "Start turns on the cheaper FastModel; escalate to the full model only when friction (errors/retries) is detected. Runtime override — resets on restart."},
		{Key: "maxAgentSteps", Label: "Max agent steps (SQE_MAX_STEPS)", Type: "text", Value: fmt.Sprintf("%d", c.MaxAgentSteps),
			Desc: "Max tool-call iterations per turn (default 80). A guard against runaway loops — raise for complex tasks, lower for tighter control. Runtime override — resets on restart."},
		{Key: "autoContinue", Label: "Auto-continue extensions (SQE_AUTO_CONTINUE)", Type: "text", Value: fmt.Sprintf("%d", c.AutoContinue),
			Desc: "How many times a turn auto-extends past the step limit when the model is still working (default 1). Hard ceiling = MaxAgentSteps × (1 + AutoContinue). Runtime override — resets on restart."},
		{Key: "sqeFastModel", Label: "Fast model (SQE_FAST_MODEL)", Type: "select",
			Value:   os.Getenv("SQE_FAST_MODEL"),
			Options: fastModelSelectOptions(models),
			Desc:    "Model used for cheap-reads rounds, AdaptiveModel exploration, and sub-agent aux calls. 'Auto' uses the built-in default. Runtime override — resets on restart."},
		{Key: "sqeAuxModel", Label: "Self-review model (SQE_AUX_MODEL)", Type: "select",
			Value:   os.Getenv("SQE_AUX_MODEL"),
			Options: auxModelSelectOptions(models),
			Desc:    "Model for self-review diffs, checkpoint distillation, goal-judge, and session titling — all tool-less 'quick answer' calls. 'Auto' falls back to Fast model, else the built-in Haiku default. Runtime override — resets on restart."},
	}
	// Session-history encryption at rest (AES-256-GCM). Read from the env by
	// session.NewStore; edits here are runtime overrides that apply on next
	// restart. Default (no key set) auto-generates a local key.
	fields = append(fields,
		personaField{Key: "sqeSessionEncrypt", Label: "Session encryption mode", Type: "text",
			Value: os.Getenv("SQE_SESSION_ENCRYPT"),
			Desc:  "SQE_SESSION_ENCRYPT: auto (default, encrypt with auto/BYOK key) · required (refuse without a key) · off (plaintext). Takes effect on next restart."},
		personaField{Key: "sqeSessionKey", Label: "Session key (BYOK)", Type: "secret",
			Set: strings.TrimSpace(os.Getenv("SQE_SESSION_KEY")) != "", Hint: lastFour(os.Getenv("SQE_SESSION_KEY")),
			Desc: "SQE_SESSION_KEY: your own 32-byte key (base64 or hex). Highest priority. Restart to apply."},
		personaField{Key: "sqeSessionKeyCmd", Label: "Session key command", Type: "secret",
			Set: strings.TrimSpace(os.Getenv("SQE_SESSION_KEY_CMD")) != "", Hint: lastFour(os.Getenv("SQE_SESSION_KEY_CMD")),
			Desc: "SQE_SESSION_KEY_CMD: shell command whose stdout is the key (KMS/Keychain/1Password/Vault). Restart to apply."},
		personaField{Key: "sqeSessionKeyFile", Label: "Session key file", Type: "text",
			Value: os.Getenv("SQE_SESSION_KEY_FILE"),
			Desc:  "SQE_SESSION_KEY_FILE: path to a file containing the key. Restart to apply."},
		personaField{Key: "sqeSessionPassphrase", Label: "Session passphrase", Type: "secret",
			Set: strings.TrimSpace(os.Getenv("SQE_SESSION_PASSPHRASE")) != "", Hint: lastFour(os.Getenv("SQE_SESSION_PASSPHRASE")),
			Desc: "SQE_SESSION_PASSPHRASE: a passphrase stretched to a key via PBKDF2. Restart to apply."},
	)
	// Auto-discover credential env vars any MCP/tool declares or references, so
	// personal tokens (GitHub, Radar, …) appear without hardcoding.
	fields = append(fields, tokenEnvFields(c.WorkingDir)...)
	return fields
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
// SQE_FAST_MODEL if set (matches the persisted Fast-model field's current
// value, read live so the label stays correct if that field is edited first),
// else config.AuxDefaultModel (Haiku).
func auxModelSelectOptions(models []config.ModelInfo) []personaSelectOption {
	fallbackID := strings.TrimSpace(os.Getenv("SQE_FAST_MODEL"))
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
