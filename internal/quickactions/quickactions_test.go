package quickactions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// isolate points the global scope at a temp dir so tests never touch the real
// ~/.config (XDG_CONFIG_HOME is what GlobalConfigDir checks first), and returns
// a working dir for the project scope.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	return t.TempDir()
}

func prompts(list []Action) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Prompt)
	}
	return out
}

func TestAddListRemove(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)

	if got := s.ListScope(Project); len(got) != 0 {
		t.Fatalf("fresh project scope should be empty, got %v", prompts(got))
	}
	if err := s.Add(Action{Prompt: "make test", Label: "Test"}, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(Action{Prompt: "make build"}, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := s.ListScope(Project)
	if want := []string{"make test", "make build"}; strings.Join(prompts(got), ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", prompts(got), want)
	}
	// Label defaults to the prompt when omitted.
	if got[1].Label != "make build" {
		t.Errorf("Label should default to Prompt, got %q", got[1].Label)
	}
	if err := s.Remove("make test", Project); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := prompts(s.ListScope(Project)); strings.Join(got, ",") != "make build" {
		t.Fatalf("after remove got %v", got)
	}
	// Removing something absent is a no-op, not an error.
	if err := s.Remove("nope", Project); err != nil {
		t.Errorf("removing a missing action should not error: %v", err)
	}
}

func TestAddRejectsEmptyAndDuplicate(t *testing.T) {
	s := Load(isolate(t))
	if err := s.Add(Action{Prompt: "   "}, Project); err == nil {
		t.Error("blank prompt should be rejected")
	}
	if err := s.Add(Action{Prompt: "x"}, Project); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Action{Prompt: " x "}, Project); err == nil {
		t.Error("duplicate prompt (after trimming) should be rejected")
	}
	if err := s.Add(Action{Prompt: strings.Repeat("a", maxPromptLen+1)}, Project); err == nil {
		t.Error("over-long prompt should be rejected")
	}
}

func TestUpdate(t *testing.T) {
	s := Load(isolate(t))
	_ = s.Add(Action{Prompt: "one", Label: "One"}, Project)
	_ = s.Add(Action{Prompt: "two", Label: "Two"}, Project)

	if err := s.Update("one", Action{Prompt: "uno", Label: "Uno", Sub: "first"}, Project); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := s.ListScope(Project)
	if got[0].Prompt != "uno" || got[0].Label != "Uno" || got[0].Sub != "first" {
		t.Fatalf("update did not apply: %+v", got[0])
	}
	if len(got) != 2 || got[1].Prompt != "two" {
		t.Fatalf("update disturbed the rest: %v", prompts(got))
	}
	// Position is preserved by an update.
	if indexOf(got, "uno") != 0 {
		t.Error("update should keep the action's position")
	}
	if err := s.Update("missing", Action{Prompt: "z"}, Project); err == nil {
		t.Error("updating a missing action should error")
	}
	if err := s.Update("uno", Action{Prompt: "two"}, Project); err == nil {
		t.Error("renaming onto an existing prompt should be rejected")
	}
}

// TestMove covers the reorder index math, including the clamped ends — the
// first action is the split button's default, so order is user-visible.
func TestMove(t *testing.T) {
	s := Load(isolate(t))
	for _, p := range []string{"a", "b", "c"} {
		if err := s.Add(Action{Prompt: p}, Project); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		if got := strings.Join(prompts(s.ListScope(Project)), ","); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	check("a,b,c")
	if err := s.Move("c", -1, Project); err != nil {
		t.Fatal(err)
	}
	check("a,c,b")
	if err := s.Move("a", +2, Project); err != nil {
		t.Fatal(err)
	}
	check("c,b,a")
	// Clamped at the ends rather than erroring or wrapping.
	if err := s.Move("c", -5, Project); err != nil {
		t.Fatal(err)
	}
	check("c,b,a")
	if err := s.Move("a", +5, Project); err != nil {
		t.Fatal(err)
	}
	check("c,b,a")
	if err := s.Move("zzz", 1, Project); err == nil {
		t.Error("moving a missing action should error")
	}
}

// TestProjectOverridesGlobal locks in the fallback: the project list wins when
// it has entries, otherwise the global list shows.
func TestProjectOverridesGlobal(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)

	if err := s.Add(Action{Prompt: "global one"}, Global); err != nil {
		t.Fatal(err)
	}
	if got := prompts(s.List()); strings.Join(got, ",") != "global one" {
		t.Fatalf("with no project actions the global list should show, got %v", got)
	}
	if s.EffectiveScope() != Global {
		t.Errorf("EffectiveScope should be global, got %q", s.EffectiveScope())
	}

	if err := s.Add(Action{Prompt: "proj one"}, Project); err != nil {
		t.Fatal(err)
	}
	if got := prompts(s.List()); strings.Join(got, ",") != "proj one" {
		t.Fatalf("project actions should win, got %v", got)
	}
	if s.EffectiveScope() != Project {
		t.Errorf("EffectiveScope should be project, got %q", s.EffectiveScope())
	}

	// Emptying the project list falls back to global again.
	if err := s.Remove("proj one", Project); err != nil {
		t.Fatal(err)
	}
	if got := prompts(s.List()); strings.Join(got, ",") != "global one" {
		t.Fatalf("after emptying project, global should show again, got %v", got)
	}
}

func TestScopesAreSeparateFiles(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	if err := s.Add(Action{Prompt: "p"}, Project); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Action{Prompt: "g"}, Global); err != nil {
		t.Fatal(err)
	}
	if got := prompts(s.ListScope(Project)); strings.Join(got, ",") != "p" {
		t.Errorf("project scope leaked: %v", got)
	}
	if got := prompts(s.ListScope(Global)); strings.Join(got, ",") != "g" {
		t.Errorf("global scope leaked: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, config.ProjectDir, "quickactions.json")); err != nil {
		t.Errorf("project file should live at .moony/quickactions.json: %v", err)
	}
}

func TestEnsureGlobalDefaultsDoesNotClobber(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)

	EnsureGlobalDefaults()
	if got := len(s.ListScope(Global)); got != len(Defaults()) {
		t.Fatalf("defaults not seeded: got %d", got)
	}
	if err := s.Remove(Defaults()[0].Prompt, Global); err != nil {
		t.Fatal(err)
	}
	before := prompts(s.ListScope(Global))
	EnsureGlobalDefaults() // must not resurrect the removed default
	if after := prompts(s.ListScope(Global)); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("EnsureGlobalDefaults clobbered user edits: %v -> %v", before, after)
	}
}

// TestMalformedFileReadsEmpty mirrors the permissions package: a corrupt file
// degrades to an empty list rather than breaking the UI.
func TestMalformedFileReadsEmpty(t *testing.T) {
	dir := isolate(t)
	p := filepath.Join(dir, config.ProjectDir, "quickactions.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir).ListScope(Project); len(got) != 0 {
		t.Errorf("malformed file should read as empty, got %v", prompts(got))
	}
}

// TestReadDropsUnusableEntries: a hand-edited file with a blank prompt must not
// put an empty row in the menu.
func TestReadDropsUnusableEntries(t *testing.T) {
	dir := isolate(t)
	p := filepath.Join(dir, config.ProjectDir, "quickactions.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"actions":[{"prompt":"good"},{"prompt":"  "},{"label":"no prompt"}]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := prompts(Load(dir).ListScope(Project))
	if strings.Join(got, ",") != "good" {
		t.Errorf("unusable entries should be dropped, got %v", got)
	}
}

func TestPersistedShapeIsStable(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	if err := s.Add(Action{Prompt: "make test", Label: "Test", Sub: "run it", Icon: "hammer"}, Project); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, config.ProjectDir, "quickactions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	if len(f.Actions) != 1 || f.Actions[0].Icon != "hammer" || f.Actions[0].Sub != "run it" {
		t.Errorf("round-trip lost fields: %+v", f.Actions)
	}
}

func TestResetDefaults(t *testing.T) {
	s := Load(isolate(t))
	_ = s.Add(Action{Prompt: "custom"}, Project)
	if err := s.ResetDefaults(Project); err != nil {
		t.Fatal(err)
	}
	if got, want := prompts(s.ListScope(Project)), prompts(Defaults()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestMaxActions(t *testing.T) {
	s := Load(isolate(t))
	for i := 0; i < maxActions; i++ {
		if err := s.Add(Action{Prompt: string(rune('a'+i%26)) + strings.Repeat("x", i)}, Project); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if err := s.Add(Action{Prompt: "one too many"}, Project); err == nil {
		t.Error("should refuse to exceed maxActions")
	}
}

// TestFieldsAreClipped keeps one pathological entry from bloating the menu.
func TestFieldsAreClipped(t *testing.T) {
	s := Load(isolate(t))
	if err := s.Add(Action{
		Prompt: "p",
		Label:  strings.Repeat("L", maxLabelLen*2),
		Sub:    strings.Repeat("S", maxSubLen*2),
	}, Project); err != nil {
		t.Fatal(err)
	}
	got := s.ListScope(Project)[0]
	if len([]rune(got.Label)) != maxLabelLen {
		t.Errorf("label not clipped: %d", len([]rune(got.Label)))
	}
	if len([]rune(got.Sub)) != maxSubLen {
		t.Errorf("sub not clipped: %d", len([]rune(got.Sub)))
	}
}

// --- per-turn model overrides -----------------------------------------------
//
// An action may pin a model/provider/reasoning for its own turn only. Empty
// fields mean "use the session's settings", so existing files keep working.

func TestOverridesRoundTrip(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)

	in := Action{Prompt: "review the diff", Model: "gpt-5", Provider: "OpenAI", Reasoning: "HIGH"}
	if err := s.Add(in, Project); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reload from disk: the fields must actually persist, not just live in memory.
	got := Load(dir).ListScope(Project)
	if len(got) != 1 {
		t.Fatalf("want 1 action, got %d", len(got))
	}
	a := got[0]
	if a.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", a.Model)
	}
	if a.Provider != "openai" {
		t.Errorf("Provider should be canonicalized to lowercase, got %q", a.Provider)
	}
	if a.Reasoning != "high" {
		t.Errorf("Reasoning should be canonicalized, got %q", a.Reasoning)
	}
}

func TestOverridesAreOptional(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	if err := s.Add(Action{Prompt: "make test"}, Project); err != nil {
		t.Fatalf("an action without overrides must still be valid: %v", err)
	}
	a := s.ListScope(Project)[0]
	if a.Model != "" || a.Provider != "" || a.Reasoning != "" {
		t.Errorf("unset overrides should stay empty, got %q/%q/%q", a.Model, a.Provider, a.Reasoning)
	}
}

func TestOverridesRejectBadProvider(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	err := s.Add(Action{Prompt: "x", Provider: "floodgat"}, Project)
	if err == nil {
		t.Fatal("a typo'd provider must be rejected at save time, not mid-turn")
	}
	if !strings.Contains(err.Error(), "floodgat") {
		t.Errorf("error should quote the offending value, got %v", err)
	}
}

// TestOverridesRejectBadReasoning is the important validation case: config's
// NormalizeReasoning returns "off" for anything unrecognised, so a naive
// validator would let "hgih" through and silently DISABLE thinking.
func TestOverridesRejectBadReasoning(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	if err := s.Add(Action{Prompt: "x", Reasoning: "hgih"}, Project); err == nil {
		t.Fatal("a typo'd reasoning level must be rejected, not silently coerced to off")
	}
}

func TestOverridesAcceptKnownReasoningLevels(t *testing.T) {
	// Includes aliases and the meta-level "auto"; "none" is a natural spelling of
	// off and must canonicalize to it.
	for _, lvl := range []string{"off", "none", "minimal", "low", "medium", "high", "xhigh", "auto"} {
		dir := isolate(t)
		s := Load(dir)
		if err := s.Add(Action{Prompt: "x", Reasoning: lvl}, Project); err != nil {
			t.Errorf("%q should be a valid reasoning level: %v", lvl, err)
			continue
		}
		if got := s.ListScope(Project)[0].Reasoning; got == "" {
			t.Errorf("%q should persist a canonical level, got empty", lvl)
		}
	}
}

func TestOverridesSurviveUpdate(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	if err := s.Add(Action{Prompt: "a", Model: "gpt-5"}, Project); err != nil {
		t.Fatal(err)
	}
	// Editing an action must be able to clear an override, not just set one.
	if err := s.Update("a", Action{Prompt: "a", Model: ""}, Project); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.ListScope(Project)[0].Model; got != "" {
		t.Errorf("clearing the model should stick, got %q", got)
	}
}

// A built-in added after a user's file already exists has to reach them, exactly once,
// without touching what they already have. Before Seeded existed there was no way to
// do that: the file existing meant seeding was skipped forever.
func TestEnsureGlobalDefaultsDeliversNewBuiltInsOnce(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)

	// A file as it looked before the AWS preset shipped: the two original built-ins
	// and no seeded record.
	if err := writeRawGlobal(t, `{"actions":[
		{"prompt":"rio build","label":"Rio build","icon":"checkmark.seal"},
		{"prompt":"rio unsigned build","label":"Rio unsigned build","icon":"hammer"},
		{"prompt":"my own thing","label":"Mine","icon":"star"}
	]}`); err != nil {
		t.Fatal(err)
	}

	EnsureGlobalDefaults()
	got := prompts(s.ListScope(Global))

	// The user's own action stays, in place, and the new built-in is appended.
	if len(got) != 4 || got[2] != "my own thing" {
		t.Fatalf("existing actions were disturbed: %v", got)
	}
	var deploy string
	for _, d := range Defaults() {
		if strings.Contains(d.Prompt, "moony deploy aws") {
			deploy = d.Prompt
		}
	}
	if deploy == "" {
		t.Fatal("no AWS deploy preset in Defaults()")
	}
	if got[3] != deploy {
		t.Fatalf("the new built-in was not appended: %v", got)
	}

	// Idempotent: running again changes nothing.
	EnsureGlobalDefaults()
	if again := prompts(s.ListScope(Global)); strings.Join(again, ",") != strings.Join(got, ",") {
		t.Fatalf("second run changed the list: %v -> %v", got, again)
	}

	// And once removed, it stays removed.
	if err := s.Remove(deploy, Global); err != nil {
		t.Fatal(err)
	}
	before := prompts(s.ListScope(Global))
	EnsureGlobalDefaults()
	if after := prompts(s.ListScope(Global)); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("a removed built-in came back: %v -> %v", before, after)
	}
}

// The AWS preset is what the composer's button sends, so it has to be a usable prompt
// and carry an icon the UI can render (TestQuickActionIconsAreMapped guards the map).
func TestAWSDeployPresetIsWellFormed(t *testing.T) {
	var a Action
	for _, d := range Defaults() {
		if strings.Contains(d.Prompt, "moony deploy aws") {
			a = d
		}
	}
	if a.Prompt == "" {
		t.Fatal("no AWS deploy preset in Defaults()")
	}
	if a.Label == "" || a.Icon == "" || a.Sub == "" {
		t.Fatalf("preset is missing display fields: %+v", a)
	}
	if len(a.Prompt) > maxPromptLen {
		t.Fatalf("prompt is %d bytes, over the %d limit", len(a.Prompt), maxPromptLen)
	}
	// It must mention the safety point: a relay with no --env-parameter has no auth.
	if !strings.Contains(a.Prompt, "--env-parameter") {
		t.Error("the preset does not mention the authentication caveat")
	}
}

// writeRawGlobal writes a global quickactions file verbatim, for migration tests.
func writeRawGlobal(t *testing.T, body string) error {
	t.Helper()
	path := globalPath()
	if path == "" {
		t.Skip("no global config path in this environment")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// The built-ins are only reachable because BuildServer calls EnsureGlobalDefaults:
// nothing else creates the file, and List() returns an empty slice when neither scope
// has one. This pins that a fresh installation gets a populated menu.
func TestFirstRunSeedsTheMenu(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	if got := s.List(); len(got) != 0 {
		t.Fatalf("a fresh environment should start empty, got %d", len(got))
	}
	EnsureGlobalDefaults()
	got := s.List()
	if len(got) != len(Defaults()) {
		t.Fatalf("first run seeded %d actions, want %d", len(got), len(Defaults()))
	}
	if prompts(got)[len(got)-1] != Defaults()[len(Defaults())-1].Prompt {
		t.Errorf("seeded list does not match Defaults(): %v", prompts(got))
	}

	// Named explicitly: a fresh install must not take the legacy-migration path, which
	// records the original presets as already offered and would leave a new user with
	// only the newest one.
	joined := strings.Join(prompts(got), " | ")
	for _, want := range []string{"rio build", "rio unsigned build", "moony deploy aws"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a fresh install is missing the %q preset: %v", want, prompts(got))
		}
	}
}

// An existing file with an empty list means "I deleted them all", so seeding must not
// refill it - only the one built-in this user has never been offered may be appended.
func TestEmptiedListStaysEmptiedApartFromNewBuiltIns(t *testing.T) {
	dir := isolate(t)
	s := Load(dir)
	if err := writeRawGlobal(t, `{"actions":[]}`); err != nil {
		t.Fatal(err)
	}

	EnsureGlobalDefaults()
	got := prompts(s.ListScope(Global))
	if len(got) != 1 || !strings.Contains(got[0], "moony deploy aws") {
		t.Fatalf("emptied list gained %v; want only the never-offered built-in", got)
	}

	EnsureGlobalDefaults()
	if again := prompts(s.ListScope(Global)); len(again) != 1 {
		t.Fatalf("a second run added more: %v", again)
	}
}
