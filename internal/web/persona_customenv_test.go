package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// A hand-typed name doesn't have to look like a credential — that's the whole point of
// the custom field, since MN_FOO_BASE_URL-style vars could never be added before. But
// "explicit" is not "safe": the panel is reachable by anyone who can reach the session,
// so vars that would hijack or break the connector stay refused.
func TestValidEnvVarName(t *testing.T) {
	ok := []string{"FOO_API_KEY", "FOO_BASE_URL", "MY_REGION", "_UNDERSCORE", "A", "x9"}
	for _, n := range ok {
		if !validEnvVarName(n) {
			t.Errorf("validEnvVarName(%q) = false, want true", n)
		}
	}
	bad := []string{
		"", "9LEADING", "HAS-DASH", "HAS SPACE", "HAS.DOT", "HAS$SIGN", "a\nb",
		// process-hijacking / agent-critical, in both cases
		"PATH", "path", "Home", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "LD_LIBRARY_PATH",
		"SHELL", "COMSPEC", "SYSTEMROOT", "TMPDIR", "GOROOT",
		// the dotenv redirect, which would move the file this panel writes
		"MN_ENV_FILE",
	}
	for _, n := range bad {
		if validEnvVarName(n) {
			t.Errorf("validEnvVarName(%q) = true, want false", n)
		}
	}
	if validEnvVarName(strings.Repeat("A", 129)) {
		t.Error("a 129-char name should be refused")
	}
}

// applyCustomEnvEdits mirrors the secret-field contract: a value is applied, a blank
// means "keep what's there", and anything the gate rejects is dropped rather than
// half-applied.
func TestApplyCustomEnvEdits(t *testing.T) {
	t.Setenv("FOO_BASE_URL", "")
	t.Setenv("KEEP_ME_TOKEN", "original")
	t.Setenv("PATH_SHOULD_NOT_CHANGE", "")
	pathBefore := os.Getenv("PATH")

	changed := applyCustomEnvEdits(map[string]json.RawMessage{
		"envVar:FOO_BASE_URL":  json.RawMessage(`"https://example.test"`),
		"envVar:KEEP_ME_TOKEN": json.RawMessage(`"   "`), // blank => keep
		"envVar:PATH":          json.RawMessage(`"/evil"`),
		"envVar:LD_PRELOAD":    json.RawMessage(`"/evil.so"`),
		"envVar:NOT A NAME":    json.RawMessage(`"x"`),
		"envToken:OTHER_TOKEN": json.RawMessage(`"not-mine"`), // different prefix, ignored here
		"someUnrelatedField":   json.RawMessage(`"x"`),
	})

	if got := os.Getenv("FOO_BASE_URL"); got != "https://example.test" {
		t.Errorf("FOO_BASE_URL = %q, want the submitted value", got)
	}
	if got := os.Getenv("KEEP_ME_TOKEN"); got != "original" {
		t.Errorf("KEEP_ME_TOKEN = %q, want original (blank must not clear)", got)
	}
	if os.Getenv("PATH") != pathBefore {
		t.Fatal("PATH was modified through the persona panel")
	}
	if len(changed) != 1 || changed["FOO_BASE_URL"] == "" {
		t.Errorf("changed = %v, want only FOO_BASE_URL", changed)
	}
	for _, refused := range []string{"PATH", "LD_PRELOAD", "NOT A NAME", "OTHER_TOKEN"} {
		if _, ok := changed[refused]; ok {
			t.Errorf("%q must not appear in the changed set", refused)
		}
	}
}

// Removals must delete from the dotenv (empty value) and unset the live env, but must
// never touch a name a provider card owns — that field has its own editor, and clearing
// it from here would wipe a credential from a control the user isn't looking at.
func TestApplyEnvVarRemovals(t *testing.T) {
	t.Setenv("DROP_ME_TOKEN", "gone-soon")
	t.Setenv("MN_OPENAI_API_KEY", "keep-provider-key")

	got := applyEnvVarRemovals(map[string]json.RawMessage{
		customEnvRemoveKey: json.RawMessage(`["DROP_ME_TOKEN","MN_OPENAI_API_KEY","PATH","bad name"]`),
	})

	if len(got) != 1 || got[0] != "DROP_ME_TOKEN" {
		t.Fatalf("removals = %v, want [DROP_ME_TOKEN]", got)
	}
	if _, present := os.LookupEnv("DROP_ME_TOKEN"); present {
		t.Error("DROP_ME_TOKEN should have been unset live")
	}
	if os.Getenv("MN_OPENAI_API_KEY") != "keep-provider-key" {
		t.Error("a provider-owned env var must not be removable from the custom list")
	}
}

func TestApplyEnvVarRemovalsAbsent(t *testing.T) {
	if got := applyEnvVarRemovals(map[string]json.RawMessage{}); got != nil {
		t.Errorf("no removal key should yield nil, got %v", got)
	}
	if got := applyEnvVarRemovals(map[string]json.RawMessage{customEnvRemoveKey: json.RawMessage(`"not-an-array"`)}); got != nil {
		t.Errorf("a malformed removal payload should yield nil, got %v", got)
	}
}

// Precedence: a provider card beats a discovered token beats a hand-added var, so a name
// that appears in more than one list keeps the value from its canonical editor.
func TestEnvPersistSetPrecedence(t *testing.T) {
	typed := "from-provider-card"
	up := envPersistSet(personaEdit{OpenAIAPIKey: &typed}, envWrite{
		tokens: map[string]string{"MN_OPENAI_API_KEY": "from-discovered", "SHARED_TOKEN": "from-discovered"},
		custom: map[string]string{"MN_OPENAI_API_KEY": "from-custom", "SHARED_TOKEN": "from-custom", "OWN_VAR": "from-custom"},
	})
	if up["MN_OPENAI_API_KEY"] != typed {
		t.Errorf("MN_OPENAI_API_KEY = %q, want the provider card's %q", up["MN_OPENAI_API_KEY"], typed)
	}
	if up["SHARED_TOKEN"] != "from-discovered" {
		t.Errorf("SHARED_TOKEN = %q, want the discovered value", up["SHARED_TOKEN"])
	}
	if up["OWN_VAR"] != "from-custom" {
		t.Errorf("OWN_VAR = %q, want the custom value", up["OWN_VAR"])
	}
}

// Only an explicit removal may produce an empty value, because UpsertDotEnv treats empty
// as delete. Anything else emitting "" would silently wipe a stored credential.
func TestEnvPersistSetEmptyOnlyForRemovals(t *testing.T) {
	up := envPersistSet(personaEdit{}, envWrite{
		tokens: map[string]string{"A_TOKEN": "v"},
		custom: map[string]string{"B_VAR": "v"},
		remove: []string{"C_GONE"},
	})
	if up["C_GONE"] != "" {
		t.Errorf("C_GONE = %q, want \"\" so UpsertDotEnv deletes it", up["C_GONE"])
	}
	for k, v := range up {
		if v == "" && k != "C_GONE" {
			t.Errorf("%s is empty but was not a removal; it would be deleted from the dotenv", k)
		}
	}
}

// The round trip that makes the feature usable: add a var, and it comes back as a
// removable field. Without this the value sits in the dotenv invisible and un-editable,
// which is worse than not persisting at all.
func TestCustomEnvVarRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	t.Setenv("MN_ENV_FILE", path)
	t.Setenv("FOO_BASE_URL", "")

	envw := envWrite{custom: applyCustomEnvEdits(map[string]json.RawMessage{
		"envVar:FOO_BASE_URL": json.RawMessage(`"https://example.test"`),
	})}
	if err := config.UpsertDotEnv(envPersistSet(personaEdit{}, envw)); err != nil {
		t.Fatalf("UpsertDotEnv: %v", err)
	}

	fields := customEnvFields(map[string]bool{})
	var found *personaField
	for i := range fields {
		if fields[i].Label == "FOO_BASE_URL" {
			found = &fields[i]
		}
	}
	if found == nil {
		t.Fatalf("FOO_BASE_URL did not come back as a field; got %d fields", len(fields))
	}
	if found.Key != customEnvFieldPrefix+"FOO_BASE_URL" {
		t.Errorf("key = %q, want the envVar: prefix so the save path recognises it", found.Key)
	}
	if !found.Custom {
		t.Error("field must be marked custom, or the UI shows no remove control")
	}
	if !found.Set {
		t.Error("field should report itself set")
	}

	// A name already shown elsewhere in the panel must not be duplicated here.
	if got := customEnvFields(map[string]bool{"FOO_BASE_URL": true}); len(got) != 0 {
		t.Errorf("covered names must be skipped, got %d fields", len(got))
	}

	// And removing it takes it back out of the file.
	if err := config.UpsertDotEnv(envPersistSet(personaEdit{}, envWrite{remove: []string{"FOO_BASE_URL"}})); err != nil {
		t.Fatalf("UpsertDotEnv (remove): %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dotenv: %v", err)
	}
	if strings.Contains(string(b), "FOO_BASE_URL") {
		t.Errorf("FOO_BASE_URL still in the dotenv after removal:\n%s", b)
	}
}

// personaFields must not list the same env var twice — once as a discovered token and
// again as a custom var. The dedupe lives in personaFields, so it's checked there.
func TestPersonaFieldsNoDuplicateEnvKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	t.Setenv("MN_ENV_FILE", path)
	if err := config.UpsertDotEnv(map[string]string{"MN_OPENAI_API_KEY": "x", "SOME_EXTRA_TOKEN": "y"}); err != nil {
		t.Fatalf("seed dotenv: %v", err)
	}
	seen := map[string]int{}
	for _, f := range personaFields(&config.Config{}, nil, nil, nil) {
		seen[f.Key]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("field key %q appears %d times", k, n)
		}
	}
	// A provider-owned name must never surface as a custom field: the card owns it.
	if _, dup := seen[customEnvFieldPrefix+"MN_OPENAI_API_KEY"]; dup {
		t.Error("MN_OPENAI_API_KEY appeared as a custom var despite having a provider card")
	}
	// And personaFields must actually CONTAIN the custom var. customEnvFields can be
	// correct in isolation while never being called from here, in which case an added
	// var still never reaches the panel.
	if _, listed := seen[customEnvFieldPrefix+"SOME_EXTRA_TOKEN"]; !listed {
		t.Error("personaFields does not include hand-added env vars, so they never reach the UI")
	}
}

// The panel's UI must actually expose the add form and the remove control, or the whole
// backend path is unreachable — the failure mode the icon bugs already demonstrated.
func TestPersonaAddEnvUIWired(t *testing.T) {
	js := mustReadStatic(t, "app-core.js")
	for _, want := range []struct{ needle, why string }{
		{`"envVar:" + name`, "the add form must post the envVar: prefix"},
		{"payload.envVarRemove", "queued removals must be sent"},
		{"pendingRemovals", "removals must be staged until Save"},
		{"persona-addenv", "the add form needs its container class"},
	} {
		if !strings.Contains(js, want.needle) {
			t.Errorf("app-core.js missing %q: %s", want.needle, want.why)
		}
	}
	if css := mustReadStatic(t, "app.css"); !strings.Contains(css, ".persona-addenv") {
		t.Error("app.css is missing .persona-addenv styles")
	}
}
