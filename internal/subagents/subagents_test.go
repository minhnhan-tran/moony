package subagents

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseFile(t *testing.T) {
	src := "---\ndescription: read-only explorer\nmodel: anthropic.claude-opus-4-8\ntools: read_file, glob,grep\n---\n\nYou explore.\n"
	desc, model, _, tools, _, body := parseFile(src)
	if desc != "read-only explorer" {
		t.Errorf("desc = %q", desc)
	}
	if model != "anthropic.claude-opus-4-8" {
		t.Errorf("model = %q", model)
	}
	want := []string{"read_file", "glob", "grep"}
	if !reflect.DeepEqual(tools, want) {
		t.Errorf("tools = %+v, want %+v", tools, want)
	}
	if body != "You explore." {
		t.Errorf("body = %q", body)
	}
}

func TestParseFileNoModel(t *testing.T) {
	_, model, _, _, _, _ := parseFile("---\ndescription: x\n---\nbody")
	if model != "" {
		t.Errorf("model should be empty when omitted, got %q", model)
	}
}

// TestParseFilePermissionAndMode covers the opencode-style mode + nested
// permission block, and the ToAgent conversion.
func TestParseFilePermissionAndMode(t *testing.T) {
	src := "---\nname: reviewer\nmode: primary\npermission:\n  edit_file: deny\n  bash: ask\n  read_file: allow\n---\nReview code.\n"
	_, _, mode, _, perm, body := parseFile(src)
	if mode != "primary" {
		t.Errorf("mode = %q, want primary", mode)
	}
	if perm["edit_file"] != "deny" || perm["bash"] != "ask" || perm["read_file"] != "allow" {
		t.Errorf("permission map not parsed: %+v", perm)
	}
	if body != "Review code." {
		t.Errorf("body = %q", body)
	}

	p := Persona{Name: "reviewer", Mode: mode, Permission: perm}
	if !p.PrimaryCapable() {
		t.Error("primary persona must be primary-capable")
	}
	prof := p.ToAgent()
	if prof == nil || prof.Rule("edit_file") != "deny" || prof.Rule("bash") != "ask" {
		t.Errorf("ToAgent did not carry the permission map: %+v", prof)
	}

	// A subagent-only persona is not primary-capable.
	if (Persona{Name: "x", Mode: "subagent"}).ToAgent() != nil {
		t.Error("subagent-only persona must not produce a primary profile")
	}

	// Fail-safe: an unrecognized permission value denies the tool rather than
	// leaving it permissive.
	typo := Persona{Name: "t", Mode: "primary", Permission: map[string]string{"edit_file": "denied"}}.ToAgent()
	if typo == nil || typo.Rule("edit_file") != "deny" {
		t.Errorf("typo'd permission value must fail safe to deny, got %+v", typo)
	}
}

func TestLoadProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	gd := filepath.Join(home, ".config", "moony", "agents")
	if err := os.MkdirAll(gd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gd, "explore.md"),
		[]byte("---\ndescription: G\n---\nGlobal"), 0o644); err != nil {
		t.Fatal(err)
	}

	proj := t.TempDir()
	pd := filepath.Join(proj, ".sqe", "agents")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pd, "explore.md"),
		[]byte("---\ndescription: P\n---\nProject"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A project persona overrides both the global file AND the built-in explore.
	exp := findPersona(Load(proj), "explore")
	if exp == nil || exp.Description != "P" || exp.SystemPrompt != "Project" {
		t.Fatalf("project should win, got %+v", exp)
	}
}

// findPersona returns the persona with the given name, or nil.
func findPersona(ps []Persona, name string) *Persona {
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i]
		}
	}
	return nil
}

// TestBuiltinPersonasAlwaysPresent verifies the read-only surveyors are available
// out of the box (no install), delegation-only, and read-only — so the default-on
// auto-delegate has an `explore` target. A writing persona (test-author) is NOT a
// built-in.
func TestBuiltinPersonasAlwaysPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "0") // isolate from any host agents

	got := Load(t.TempDir()) // empty project + empty global → only built-ins
	for _, name := range []string{"explore", "code-reviewer", "planner"} {
		p := findPersona(got, name)
		if p == nil {
			t.Fatalf("built-in %q should be available out of the box; loaded %d personas", name, len(got))
		}
		if !p.Builtin() || p.Source != SourceBuiltin {
			t.Errorf("%q should be tagged builtin, got source %q", name, p.Source)
		}
		if !p.SubagentCapable() || p.PrimaryCapable() {
			t.Errorf("%q should be delegation-only (subagent, not a primary profile)", name)
		}
		if !p.ReadOnly() {
			t.Errorf("%q should be read-only", name)
		}
		if p.SystemPrompt == "" {
			t.Errorf("%q should carry the catalog system prompt", name)
		}
	}
	if findPersona(got, "test-author") != nil {
		t.Error("test-author writes files; it must NOT be an always-on built-in")
	}
}

func TestLoadImportsExternalAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Claude agent (with a model that isn't a valid sqe model id).
	claude := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "reviewer.md"),
		[]byte("---\nname: reviewer\ndescription: reviews code\nmodel: opus\n---\nYou review."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Optimus global agent.
	optimus := filepath.Join(home, ".config", "optimus", "agents")
	if err := os.MkdirAll(optimus, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(optimus, "explorer.md"),
		[]byte("---\ndescription: explores\n---\nYou explore."), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Load(t.TempDir())
	rev := findPersona(got, "reviewer")
	if rev == nil || rev.Source != "claude" {
		t.Fatalf("expected an imported claude 'reviewer', got %+v", got)
	}
	if rev.Model != "" {
		t.Errorf("external model should be dropped (inherit parent), got %q", rev.Model)
	}
	if rev.SystemPrompt != "You review." {
		t.Errorf("body not imported: %q", rev.SystemPrompt)
	}
	if exp := findPersona(got, "explorer"); exp == nil || exp.Source != "optimus" {
		t.Fatalf("expected an imported optimus 'explorer', got %+v", got)
	}
}

func TestSQEProjectOverridesExternalAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	claude := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "dup.md"),
		[]byte("---\ndescription: C\n---\nClaude"), 0o644); err != nil {
		t.Fatal(err)
	}

	proj := t.TempDir()
	pd := filepath.Join(proj, ".sqe", "agents")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pd, "dup.md"),
		[]byte("---\ndescription: P\n---\nProject"), 0o644); err != nil {
		t.Fatal(err)
	}

	dup := findPersona(Load(proj), "dup")
	if dup == nil || dup.Source != "project" || dup.Description != "P" {
		t.Fatalf("sqe project persona should win over the imported one, got %+v", dup)
	}
}

func TestImportExternalAgentsDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "0")

	claude := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "x.md"), []byte("---\ndescription: X\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := Load(t.TempDir()); findPersona(got, "x") != nil {
		t.Fatalf("with import disabled, external agent %q should not load, got %+v", "x", got)
	}
}

func TestRemoveRefusesImportedAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	claude := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(claude, "ext.md")
	if err := os.WriteFile(path, []byte("---\ndescription: E\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Remove("ext", t.TempDir()); err == nil {
		t.Fatal("Remove should refuse an imported external agent")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the external file must not be deleted: %v", err)
	}
}

// TestPersonaReadOnly covers the read-only detection used to route surveyor
// personas to the cheaper fast model.
func TestPersonaReadOnly(t *testing.T) {
	if !(Persona{Tools: []string{"read_file", "grep"}}).ReadOnly() {
		t.Error("a read-only tool subset should be read-only")
	}
	if !(Persona{Tools: readOnlyTools}).ReadOnly() {
		t.Error("the full read-only allowlist should be read-only")
	}
	if (Persona{Tools: []string{"read_file", "bash"}}).ReadOnly() {
		t.Error("a persona with bash is not read-only")
	}
	if (Persona{}).ReadOnly() {
		t.Error("a persona with no tool allowlist (full access) is not read-only")
	}
}

// TestSubagentVsPrimaryCapable covers the mode gating used to keep "primary"
// profiles (build/plan) out of the task delegation list and "subagent" personas
// out of the primary switcher.
func TestSubagentVsPrimaryCapable(t *testing.T) {
	cases := []struct {
		mode              string
		primary, subagent bool
	}{
		{"", true, true}, // default "all"
		{"all", true, true},
		{"primary", true, false},  // switch-only (build/plan)
		{"subagent", false, true}, // delegate-only
	}
	for _, c := range cases {
		p := Persona{Mode: c.mode}
		if p.PrimaryCapable() != c.primary {
			t.Errorf("mode %q PrimaryCapable=%v, want %v", c.mode, p.PrimaryCapable(), c.primary)
		}
		if p.SubagentCapable() != c.subagent {
			t.Errorf("mode %q SubagentCapable=%v, want %v", c.mode, p.SubagentCapable(), c.subagent)
		}
	}
}

// TestDefaultAgentFilesRoundTrip ensures the scaffolded build/plan files parse
// back into the exact permission maps the Go built-ins use, so on-disk defaults
// reproduce the shipped behavior.
func TestDefaultAgentFilesRoundTrip(t *testing.T) {
	defs := DefaultAgentFiles()
	if len(defs) != 2 || defs[0].Name != "build" || defs[1].Name != "plan" {
		t.Fatalf("expected build+plan in order, got %+v", defs)
	}

	build := defs[0]
	_, _, mode, _, perm, body := parseFile(build.Content)
	if mode != "primary" {
		t.Errorf("build mode = %q, want primary", mode)
	}
	if body == "" {
		t.Error("build should have a system-prompt body")
	}
	bprof := (Persona{Name: "build", Mode: mode, Permission: perm}).ToAgent()
	for _, tool := range []string{"write_file", "edit_file", "bash", "task", "delegate", "remember"} {
		if bprof.Rule(tool) != "allow" {
			t.Errorf("build %q = %q, want allow", tool, bprof.Rule(tool))
		}
	}

	plan := defs[1]
	_, _, pmode, _, pperm, _ := parseFile(plan.Content)
	if pmode != "primary" {
		t.Errorf("plan mode = %q, want primary", pmode)
	}
	pprof := (Persona{Name: "plan", Mode: pmode, Permission: pperm}).ToAgent()
	for _, tool := range []string{"write_file", "edit_file", "multi_edit", "apply_patch", "task", "delegate", "remember"} {
		if pprof.Rule(tool) != "deny" {
			t.Errorf("plan %q = %q, want deny", tool, pprof.Rule(tool))
		}
	}
	if pprof.Rule("bash") != "ask" {
		t.Errorf("plan bash = %q, want ask", pprof.Rule("bash"))
	}
	if !pprof.ReadOnly() {
		t.Error("plan profile must be read-only")
	}
}

// TestWriteDefaultAgents covers scaffolding: it writes missing files, skips
// existing ones unless force is set, and the result is loadable.
func TestWriteDefaultAgents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".sqe", "agents")

	written, err := WriteDefaultAgents(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 {
		t.Fatalf("first run should write both files, got %v", written)
	}

	// Second run without force: nothing rewritten (don't clobber user edits).
	custom := []byte("---\ndescription: my build\nmode: primary\n---\nCustom.")
	if err := os.WriteFile(filepath.Join(dir, "build.md"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := WriteDefaultAgents(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second run should not overwrite existing files, got %v", again)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "build.md")); string(got) != string(custom) {
		t.Error("existing build.md must be preserved without force")
	}

	// Force rewrites everything back to the canonical content.
	forced, err := WriteDefaultAgents(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(forced) != 2 {
		t.Fatalf("force should rewrite both files, got %v", forced)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "build.md")); string(got) != DefaultAgentFiles()[0].Content {
		t.Error("force should restore canonical build.md")
	}
}

// TestSeedGlobalDefaultAgents covers seeding into the global config dir and that
// the seeded files then load as primary-capable personas (so they appear in
// /agents and are switchable via /agent).
func TestSeedGlobalDefaultAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	names, err := SeedGlobalDefaultAgents()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("first seed should write build+plan, got %v", names)
	}

	// They load as personas from the global dir.
	loaded := Load(t.TempDir())
	build := findPersona(loaded, "build")
	plan := findPersona(loaded, "plan")
	if build == nil || plan == nil {
		t.Fatalf("seeded build/plan must load as personas, got %+v", loaded)
	}
	if build.Source != "global" || build.Mode != "primary" || !build.PrimaryCapable() {
		t.Errorf("seeded build should be a global primary persona, got %+v", build)
	}
	if plan.ToAgent().Rule("edit_file") != "deny" {
		t.Error("seeded plan must be read-only")
	}

	// Idempotent: a second seed writes nothing.
	again, err := SeedGlobalDefaultAgents()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second seed should be a no-op, got %v", again)
	}
}

func TestPersonaEnvVars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MN_IMPORT_EXTERNAL_AGENTS", "0")
	wd := t.TempDir()
	dir := filepath.Join(home, ".config", "moony", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"),
		[]byte("---\ndescription: Notes agent\nenv: [NOTES_MCP_TOKEN]\n---\nprompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range EnvVars(wd) {
		if e == "NOTES_MCP_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Errorf("EnvVars should include NOTES_MCP_TOKEN; got %v", EnvVars(wd))
	}
}
