package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }

// TestLastFour: a secret is reduced to its last 4 chars (recognizer only); short
// secrets are fully dotted; empty stays empty.
// TestApplyEnvTokenEdits: dynamically-discovered credential fields
// ("envToken:<NAME>") set the process env var; blanks are ignored and only
// token-like names are honored.
func TestApplyEnvTokenEdits(t *testing.T) {
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")
	t.Setenv("NPM_TOKEN", "keep-npm")
	t.Setenv("NODE_ENV", "production")

	raw := map[string]json.RawMessage{
		"envToken:GITHUB_PERSONAL_ACCESS_TOKEN": json.RawMessage(`"ghp_abc123"`),
		"envToken:NPM_TOKEN":                    json.RawMessage(`""`),        // blank → keep
		"envToken:NODE_ENV":                     json.RawMessage(`"staging"`), // not token-like → ignored
		"unknownProjectToken":                   json.RawMessage(`"x"`),       // wrong prefix → ignored
	}
	changed := applyEnvTokenEdits(raw)

	if got := os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN"); got != "ghp_abc123" {
		t.Errorf("github token = %q, want ghp_abc123", got)
	}
	if got := os.Getenv("NPM_TOKEN"); got != "keep-npm" {
		t.Errorf("npm token = %q, want keep-npm (blank must not clear)", got)
	}
	if got := os.Getenv("NODE_ENV"); got != "production" {
		t.Errorf("NODE_ENV = %q, want production (non-token must be ignored)", got)
	}
	if len(changed) != 1 || changed[0] != "GITHUB_PERSONAL_ACCESS_TOKEN" {
		t.Errorf("changed = %v, want [GITHUB_PERSONAL_ACCESS_TOKEN]", changed)
	}
}

func TestLooksLikeTokenVar(t *testing.T) {
	yes := []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "NPM_TOKEN", "OPENAI_API_KEY", "FOO_SECRET", "DB_PASSWORD", "MY_PAT", "SERVICE_KEY"}
	no := []string{"NODE_ENV", "PATH", "HOME", "LOG_LEVEL", "PORT"}
	for _, n := range yes {
		if !looksLikeTokenVar(n) {
			t.Errorf("looksLikeTokenVar(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if looksLikeTokenVar(n) {
			t.Errorf("looksLikeTokenVar(%q) = true, want false", n)
		}
	}
}

func TestLastFour(t *testing.T) {
	cases := map[string]string{"": "", "ab": "••", "abcd": "••••", "secret-token-1234": "1234"}
	for in, want := range cases {
		if got := lastFour(in); got != want {
			t.Errorf("lastFour(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestApplyPersonaEdit: nil fields are left unchanged, a blank secret keeps the
// existing token, a non-empty secret replaces it, and the ollama host is
// normalized to the OpenAI-compatible base.
func TestApplyPersonaEdit(t *testing.T) {
	c := &config.Config{OllamaBaseURL: "http://old:11434/v1", AutoDelegate: true, AutoPlan: false}

	applyPersonaEdit(c, personaEdit{
		OllamaHost:   strptr("newhost:11434"),
		AutoDelegate: boolptr(false),
		AutoPlan:     boolptr(true),
	})
	if c.OllamaBaseURL != "http://newhost:11434/v1" {
		t.Errorf("ollama host not normalized/applied: %q", c.OllamaBaseURL)
	}
	if c.AutoDelegate {
		t.Error("autoDelegate should be false")
	}
	if !c.AutoPlan {
		t.Error("autoPlan should be true")
	}
	if c.AutoPlanPersona == nil || !*c.AutoPlanPersona {
		t.Error("AutoPlanPersona should remember the explicit true choice")
	}

	applyPersonaEdit(c, personaEdit{OllamaHost: nil})
	if c.OllamaBaseURL != "http://newhost:11434/v1" {
		t.Error("nil field should be left unchanged")
	}
}

// TestApplyPersonaEditAuxModel verifies the Self-review model persona field
// sets MOONY_AUX_MODEL and remembers the choice as an explicit persona override
// (AuxModelPersona), mirroring the existing Fast-model field's behavior.
func TestApplyPersonaEditAuxModel(t *testing.T) {
	defer os.Unsetenv("MOONY_AUX_MODEL")
	c := &config.Config{}

	applyPersonaEdit(c, personaEdit{SqeAuxModel: strptr("anthropic.claude-sonnet-5")})
	if got := os.Getenv("MOONY_AUX_MODEL"); got != "anthropic.claude-sonnet-5" {
		t.Errorf("MOONY_AUX_MODEL = %q, want the pinned model", got)
	}
	if c.AuxModelPersona == nil || *c.AuxModelPersona != "anthropic.claude-sonnet-5" {
		t.Errorf("AuxModelPersona = %v, want pinned model remembered", c.AuxModelPersona)
	}

	applyPersonaEdit(c, personaEdit{SqeAuxModel: strptr("")}) // back to Auto
	if got := os.Getenv("MOONY_AUX_MODEL"); got != "" {
		t.Errorf("MOONY_AUX_MODEL after clearing = %q, want unset", got)
	}
	if c.AuxModelPersona == nil || *c.AuxModelPersona != "" {
		t.Errorf("AuxModelPersona after clearing = %v, want empty string (explicit auto)", c.AuxModelPersona)
	}
}

// TestPersonaFieldsIncludesAuxModel verifies the Self-review model select
// field is present with an "Auto" default option and the full model catalog.
func TestPersonaFieldsIncludesAuxModel(t *testing.T) {
	c := &config.Config{}
	m := config.AllKnownModels()
	fields := personaFields(c, m, m, m)
	var f *personaField
	for i := range fields {
		if fields[i].Key == "sqeAuxModel" {
			f = &fields[i]
		}
	}
	if f == nil {
		t.Fatal("sqeAuxModel field not found in personaFields")
	}
	if f.Type != "select" {
		t.Errorf("sqeAuxModel type = %q, want select", f.Type)
	}
	if len(f.Options) < 2 {
		t.Fatalf("sqeAuxModel options = %v, want Auto + at least one model", f.Options)
	}
	if f.Options[0].Value != "" || !strings.Contains(f.Options[0].Label, "Auto") {
		t.Errorf("first option = %+v, want the empty-value Auto default", f.Options[0])
	}
}

// TestApplyPersonaEditFastAuxProviderReasoning verifies the four per-tier
// provider/reasoning selects set their env var + persona pointer, and that
// posting "" (Inherit) unsets them.
func TestApplyPersonaEditFastAuxProviderReasoning(t *testing.T) {
	for _, k := range []string{"MOONY_FAST_PROVIDER", "MOONY_FAST_REASONING", "MOONY_AUX_PROVIDER", "MOONY_AUX_REASONING"} {
		defer os.Unsetenv(k)
	}
	c := &config.Config{}
	applyPersonaEdit(c, personaEdit{
		FastProvider:  strptr("anthropic"),
		FastReasoning: strptr("high"),
		AuxProvider:   strptr("openai"),
		AuxReasoning:  strptr("low"),
	})
	checks := []struct {
		env, want string
		persona   *string
	}{
		{"MOONY_FAST_PROVIDER", "anthropic", c.FastProviderPersona},
		{"MOONY_FAST_REASONING", "high", c.FastReasoningPersona},
		{"MOONY_AUX_PROVIDER", "openai", c.AuxProviderPersona},
		{"MOONY_AUX_REASONING", "low", c.AuxReasoningPersona},
	}
	for _, ch := range checks {
		if got := os.Getenv(ch.env); got != ch.want {
			t.Errorf("%s = %q, want %q", ch.env, got, ch.want)
		}
		if ch.persona == nil || *ch.persona != ch.want {
			t.Errorf("%s persona pointer = %v, want %q remembered", ch.env, ch.persona, ch.want)
		}
	}
	// Posting "" (Inherit) unsets each override.
	applyPersonaEdit(c, personaEdit{
		FastProvider:  strptr(""),
		FastReasoning: strptr(""),
		AuxProvider:   strptr(""),
		AuxReasoning:  strptr(""),
	})
	for _, ch := range checks {
		if got := os.Getenv(ch.env); got != "" {
			t.Errorf("%s after clearing = %q, want unset", ch.env, got)
		}
	}
}

// TestPersonaFieldsIncludesProviderReasoning verifies the four per-tier selects
// are present, each a "select" whose first option is the empty inherit sentinel,
// and that the provider selects list every registered provider.
func TestPersonaFieldsIncludesProviderReasoning(t *testing.T) {
	for _, k := range []string{"MOONY_FAST_PROVIDER", "MOONY_FAST_REASONING", "MOONY_AUX_PROVIDER", "MOONY_AUX_REASONING"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	pm := config.AllKnownModels()
	fields := personaFields(&config.Config{LLMProvider: provider.Ollama}, pm, pm, pm)
	byKey := map[string]personaField{}
	for _, f := range fields {
		byKey[f.Key] = f
	}
	for _, key := range []string{"fastProvider", "auxProvider", "fastReasoning", "auxReasoning"} {
		f, ok := byKey[key]
		if !ok {
			t.Fatalf("%s field missing from personaFields", key)
		}
		if f.Type != "select" {
			t.Errorf("%s type = %q, want select", key, f.Type)
		}
		if len(f.Options) == 0 || f.Options[0].Value != "" {
			t.Errorf("%s first option = %+v, want the empty inherit sentinel", key, f.Options)
		}
	}
	// Provider selects list inherit + every registered provider.
	if got, want := len(byKey["fastProvider"].Options), len(provider.List())+1; got != want {
		t.Errorf("fastProvider options = %d, want inherit + all providers (%d)", got, want)
	}
}

// TestFastModelSelectOptionsInjectsPinned verifies a pinned id absent from the
// catalog (e.g. a cross-provider model) is injected as a selectable option so it
// round-trips instead of being reset to option 0.
func TestFastModelSelectOptionsInjectsPinned(t *testing.T) {
	t.Setenv("MOONY_FAST_MODEL", "acme/cross-provider-model")
	opts := fastModelSelectOptions([]config.ModelInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}})
	var found bool
	for _, o := range opts {
		if o.Value == "acme/cross-provider-model" {
			found = true
		}
	}
	if !found {
		t.Errorf("pinned id absent from catalog was not injected; options = %+v", opts)
	}
}

func TestPersonaSessionEncryptionFields(t *testing.T) {
	for _, k := range []string{"MOONY_SESSION_ENCRYPT", "MOONY_SESSION_KEY", "MOONY_SESSION_KEY_CMD", "MOONY_SESSION_KEY_FILE", "MOONY_SESSION_PASSPHRASE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	// The five MOONY_SESSION_* fields are surfaced, with the right secret/text types.
	sm := config.AllKnownModels()
	fields := personaFields(&config.Config{}, sm, sm, sm)
	byKey := map[string]personaField{}
	for _, f := range fields {
		byKey[f.Key] = f
	}
	want := map[string]string{
		"sqeSessionEncrypt": "text", "sqeSessionKey": "secret", "sqeSessionKeyCmd": "secret",
		"sqeSessionKeyFile": "text", "sqeSessionPassphrase": "secret",
	}
	for k, typ := range want {
		f, ok := byKey[k]
		if !ok {
			t.Fatalf("missing persona field %q", k)
		}
		if f.Type != typ {
			t.Errorf("%s type = %q, want %q", k, f.Type, typ)
		}
	}

	// Edits push into the env: plain applies as-is, secret keeps existing when blank.
	os.Setenv("MOONY_SESSION_KEY", "existing-secret")
	applyPersonaEdit(&config.Config{}, personaEdit{
		SessionEncrypt: strptr("required"),
		SessionKeyFile: strptr("/tmp/k.key"),
		SessionKey:     strptr(""), // blank → keep existing
	})
	if got := os.Getenv("MOONY_SESSION_ENCRYPT"); got != "required" {
		t.Errorf("MOONY_SESSION_ENCRYPT = %q, want required", got)
	}
	if got := os.Getenv("MOONY_SESSION_KEY_FILE"); got != "/tmp/k.key" {
		t.Errorf("MOONY_SESSION_KEY_FILE = %q", got)
	}
	if got := os.Getenv("MOONY_SESSION_KEY"); got != "existing-secret" {
		t.Errorf("blank secret should keep existing, got %q", got)
	}

	// A non-empty secret replaces; a blanked plain field clears.
	applyPersonaEdit(&config.Config{}, personaEdit{
		SessionKey:     strptr("new-secret"),
		SessionKeyFile: strptr(""),
	})
	if got := os.Getenv("MOONY_SESSION_KEY"); got != "new-secret" {
		t.Errorf("non-empty secret should replace, got %q", got)
	}
	if got := os.Getenv("MOONY_SESSION_KEY_FILE"); got != "" {
		t.Errorf("blanked plain field should clear, got %q", got)
	}
}

// TestPersonaProviderKeyFields verifies each provider credential is surfaced as a
// masked secret field (with a base-URL text field where the provider has one),
// that an already-set key (from the config struct) reports set=true with a
// last-4 hint, and that unset keys report set=false.
func TestPersonaProviderKeyFields(t *testing.T) {
	for _, pc := range providerCreds {
		if pc.baseEnv != "" {
			t.Setenv(pc.baseEnv, "")
			os.Unsetenv(pc.baseEnv)
		}
	}
	c := &config.Config{OpenAIAPIKey: "sk-live-abcd1234"} // pre-set one key
	byKey := map[string]personaField{}
	km := config.AllKnownModels()
	for _, f := range personaFields(c, km, km, km) {
		byKey[f.Key] = f
	}
	// Every providerCreds row must appear as a secret field, plus a text base-URL
	// field where baseEnv is set.
	for _, pc := range providerCreds {
		f, ok := byKey[pc.key]
		if !ok {
			t.Fatalf("missing provider field %q", pc.key)
		}
		if f.Type != "secret" {
			t.Errorf("%s type = %q, want secret", pc.key, f.Type)
		}
		if f.Value != nil {
			t.Errorf("%s must not expose a value (secret), got %v", pc.key, f.Value)
		}
		if pc.baseEnv == "" {
			continue
		}
		bf, ok := byKey[pc.baseKey]
		if !ok {
			t.Fatalf("missing base-URL field %q", pc.baseKey)
		}
		if bf.Type != "text" {
			t.Errorf("%s type = %q, want text", pc.baseKey, bf.Type)
		}
		if bf.Value != "" { // no override set → blank (default in use)
			t.Errorf("%s value = %v, want blank when no override set", pc.baseKey, bf.Value)
		}
	}
	if f := byKey["openaiApiKey"]; !f.Set || f.Hint != "1234" {
		t.Errorf("openaiApiKey set=%v hint=%q, want set=true hint=1234", f.Set, f.Hint)
	}
	if f := byKey["anthropicApiKey"]; f.Set {
		t.Error("anthropicApiKey should report set=false when unset")
	}
	if _, ok := byKey["ollamaBaseUrl"]; ok {
		t.Error("ollama token must not emit a base-URL field (host is the ollamaHost field)")
	}
}

// TestApplyPersonaEditProviderKeys verifies a provider key edit sets both the env
// var and the live config field (blank secret keeps the existing key), and that a
// base-URL override sets both env + field while a blank clears the override.
func TestApplyPersonaEditProviderKeys(t *testing.T) {
	for _, pc := range providerCreds {
		os.Unsetenv(pc.env)
		if pc.baseEnv != "" {
			os.Unsetenv(pc.baseEnv)
		}
	}
	c := &config.Config{}

	applyPersonaEdit(c, personaEdit{
		OpenAIAPIKey:  strptr("sk-openai-xyz"),
		OllamaToken:   strptr("ollama-bearer"),
		OpenAIBaseURL: strptr("https://proxy.local/v1"),
	})
	if got := os.Getenv("MOONY_OPENAI_API_KEY"); got != "sk-openai-xyz" {
		t.Errorf("MOONY_OPENAI_API_KEY = %q, want sk-openai-xyz", got)
	}
	if c.OpenAIAPIKey != "sk-openai-xyz" {
		t.Errorf("c.OpenAIAPIKey = %q, want live update sk-openai-xyz", c.OpenAIAPIKey)
	}
	if got := os.Getenv("MOONY_OLLAMA_TOKEN"); got != "ollama-bearer" || c.OllamaToken != "ollama-bearer" {
		t.Errorf("ollama token env=%q field=%q, want ollama-bearer", got, c.OllamaToken)
	}
	if got := os.Getenv("MOONY_OPENAI_BASE_URL"); got != "https://proxy.local/v1" || c.OpenAIBaseURL != "https://proxy.local/v1" {
		t.Errorf("openai base env=%q field=%q, want https://proxy.local/v1", got, c.OpenAIBaseURL)
	}

	// Blank secret keeps the existing key; blank base-URL clears the override.
	applyPersonaEdit(c, personaEdit{
		OpenAIAPIKey:  strptr(""),
		OpenAIBaseURL: strptr(""),
	})
	if got := os.Getenv("MOONY_OPENAI_API_KEY"); got != "sk-openai-xyz" {
		t.Errorf("blank secret cleared env, got %q", got)
	}
	if c.OpenAIAPIKey != "sk-openai-xyz" {
		t.Errorf("blank secret cleared config field, got %q", c.OpenAIAPIKey)
	}
	if got := os.Getenv("MOONY_OPENAI_BASE_URL"); got != "" {
		t.Errorf("blank base-URL should unset env, got %q", got)
	}
	if c.OpenAIBaseURL != "" {
		t.Errorf("blank base-URL should clear the field (client falls back to default), got %q", c.OpenAIBaseURL)
	}

	// A nil (absent) field leaves everything unchanged.
	applyPersonaEdit(c, personaEdit{})
	if c.OpenAIAPIKey != "sk-openai-xyz" {
		t.Errorf("nil field mutated config, got %q", c.OpenAIAPIKey)
	}

	for _, pc := range providerCreds {
		os.Unsetenv(pc.env)
		if pc.baseEnv != "" {
			os.Unsetenv(pc.baseEnv)
		}
	}
}

// TestProviderEnvPersist verifies which env vars an edit persists to the dotenv:
// non-blank keys are included, blank keys are skipped (keep existing), and a
// base-URL edit is always included (empty value = delete the override).
func TestProviderEnvPersist(t *testing.T) {
	up := providerEnvPersist(personaEdit{
		OpenAIAPIKey:    strptr("sk-openai"),
		AnthropicAPIKey: strptr(""), // blank → keep existing, not persisted
		OpenAIBaseURL:   strptr("https://proxy/v1"),
		GroqBaseURL:     strptr(""), // submitted-but-empty → delete override
	})
	if up["MOONY_OPENAI_API_KEY"] != "sk-openai" {
		t.Errorf("openai key = %q, want sk-openai", up["MOONY_OPENAI_API_KEY"])
	}
	if _, ok := up["MOONY_ANTHROPIC_API_KEY"]; ok {
		t.Error("blank key must not be persisted (keeps existing)")
	}
	if up["MOONY_OPENAI_BASE_URL"] != "https://proxy/v1" {
		t.Errorf("openai base = %q, want https://proxy/v1", up["MOONY_OPENAI_BASE_URL"])
	}
	if v, ok := up["MOONY_GROQ_BASE_URL"]; !ok || v != "" {
		t.Errorf("blanked base URL must map to \"\" (delete), got %q ok=%v", v, ok)
	}
	// Untouched providers contribute nothing.
	if _, ok := up["MOONY_XAI_API_KEY"]; ok {
		t.Error("untouched provider must not appear")
	}
}
