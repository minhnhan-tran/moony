package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/tools"
)

// TestAgentsSectionsCRUD covers the web editor's API over all AGENTS.md sections:
// add (into a named section, creating it), edit (replace in place), delete, and
// the bullet count — each reflected in AGENTS.md and the rebuilt system prompt.
func TestAgentsSectionsCRUD(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	ag := New(&config.Config{Model: "test", WorkingDir: dir}, nil, tools.NewRegistry(dir), nil, nil)

	if ag.MemoryBulletCount() != 0 {
		t.Fatal("precondition: no bullets yet")
	}
	// Add into two distinct sections (one creates "Memory", one creates "Versioning").
	if err := ag.AddBullet("Memory", "always run gofmt", false); err != nil {
		t.Fatalf("AddBullet: %v", err)
	}
	if err := ag.AddBullet("Versioning", "bump the patch version", false); err != nil {
		t.Fatalf("AddBullet: %v", err)
	}
	secs := ag.AgentsSections(false)
	if len(secs) != 2 {
		t.Fatalf("want 2 sections, got %+v", secs)
	}
	if secs[0].Heading != "Memory" || secs[1].Heading != "Versioning" {
		t.Errorf("section headings/order: %+v", secs)
	}
	if ag.MemoryBulletCount() != 2 {
		t.Errorf("bullet count = %d, want 2", ag.MemoryBulletCount())
	}
	if !strings.Contains(ag.system, "bump the patch version") {
		t.Error("added bullet not in the rebuilt system prompt")
	}
	// Edit in place: replace, no duplicate.
	if err := ag.UpdateBullet("Memory", "always run gofmt", "always run gofmt ./...", false); err != nil {
		t.Fatalf("UpdateBullet: %v", err)
	}
	got := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if strings.Contains(got, "- always run gofmt\n") || !strings.Contains(got, "- always run gofmt ./...") {
		t.Errorf("edit didn't replace in place:\n%s", got)
	}
	// Delete.
	if err := ag.DeleteBullet("bump the patch version", false); err != nil {
		t.Fatalf("DeleteBullet: %v", err)
	}
	if ag.MemoryBulletCount() != 1 {
		t.Errorf("after delete, count = %d, want 1", ag.MemoryBulletCount())
	}
}

// TestAgentsSectionsScopes verifies the project and global AGENTS.md are two
// independent "places": a global bullet lands in the global file (not the
// project one), shows up only under AgentsSections(true), and still counts
// toward the combined MemoryBulletCount.
func TestAgentsSectionsScopes(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	ag := New(&config.Config{Model: "test", WorkingDir: dir}, nil, tools.NewRegistry(dir), nil, nil)

	if err := ag.AddBullet("Memory", "project-only rule", false); err != nil {
		t.Fatalf("AddBullet project: %v", err)
	}
	if err := ag.AddBullet("Memory", "global rule for all repos", true); err != nil {
		t.Fatalf("AddBullet global: %v", err)
	}

	proj := ag.AgentsSections(false)
	glob := ag.AgentsSections(true)
	if len(proj) != 1 || len(proj[0].Bullets) != 1 || proj[0].Bullets[0] != "project-only rule" {
		t.Errorf("project scope = %+v, want only the project rule", proj)
	}
	if len(glob) != 1 || len(glob[0].Bullets) != 1 || glob[0].Bullets[0] != "global rule for all repos" {
		t.Errorf("global scope = %+v, want only the global rule", glob)
	}
	// The global note must NOT be written into the project's AGENTS.md.
	if got := readFile(t, filepath.Join(dir, "AGENTS.md")); strings.Contains(got, "global rule for all repos") {
		t.Errorf("global bullet leaked into project AGENTS.md:\n%s", got)
	}
	if ag.MemoryBulletCount() != 2 {
		t.Errorf("combined count = %d, want 2", ag.MemoryBulletCount())
	}
	// Deleting the global bullet leaves the project one intact.
	if err := ag.DeleteBullet("global rule for all repos", true); err != nil {
		t.Fatalf("DeleteBullet global: %v", err)
	}
	if len(ag.AgentsSections(true)) != 0 || len(ag.AgentsSections(false)) != 1 {
		t.Error("delete crossed scopes")
	}
}

func TestAppendMemoryBullet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	// First note creates the file + section.
	if err := appendMemoryBullet(path, "always run make vet"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, memoryHeading) || !strings.Contains(got, "- always run make vet") {
		t.Fatalf("first memory not written correctly:\n%s", got)
	}

	// Second note appends under the same section (only one heading).
	if err := appendMemoryBullet(path, "tests live next to code"); err != nil {
		t.Fatal(err)
	}
	got = readFile(t, path)
	if strings.Count(got, memoryHeading) != 1 {
		t.Errorf("expected exactly one %q heading, got:\n%s", memoryHeading, got)
	}
	bullets := readMemoryBullets(path)
	if len(bullets) != 2 || bullets[0] != "- always run make vet" || bullets[1] != "- tests live next to code" {
		t.Errorf("unexpected bullets: %#v", bullets)
	}

	// Appending to a file with pre-existing prose keeps that content.
	path2 := filepath.Join(dir, "WITHPROSE.md")
	os.WriteFile(path2, []byte("# Project\n\nSome docs.\n"), 0o644)
	if err := appendMemoryBullet(path2, "note one"); err != nil {
		t.Fatal(err)
	}
	got = readFile(t, path2)
	if !strings.Contains(got, "Some docs.") || !strings.Contains(got, "## Memory") || !strings.Contains(got, "- note one") {
		t.Errorf("prose or memory missing:\n%s", got)
	}
}

func TestRememberReloadsSystemPrompt(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	cfg := &config.Config{Model: "test", WorkingDir: dir}
	ag := New(cfg, nil, reg, nil, nil)

	if strings.Contains(ag.system, "REMEMBER_ME") {
		t.Fatal("precondition: memory should not be in the prompt yet")
	}
	msg, err := ag.Remember("REMEMBER_ME always", "", false)
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if !strings.Contains(msg, "project") {
		t.Errorf("confirmation should mention scope, got %q", msg)
	}
	// Live reload: the new memory must be in the rebuilt system prompt.
	if !strings.Contains(ag.system, "REMEMBER_ME always") {
		t.Errorf("system prompt was not reloaded with the new memory:\n%s", ag.system)
	}
	// And persisted to the project AGENTS.md.
	if got := readFile(t, filepath.Join(dir, "AGENTS.md")); !strings.Contains(got, "- REMEMBER_ME always") {
		t.Errorf("memory not persisted to AGENTS.md:\n%s", got)
	}
}

func TestRememberRejectsEmpty(t *testing.T) {
	isolateConfig(t)
	ag := New(&config.Config{Model: "t", WorkingDir: t.TempDir()}, nil, tools.NewRegistry(t.TempDir()), nil, nil)
	if _, err := ag.Remember("   #  ", "", false); err == nil {
		t.Error("expected an error for an empty memory")
	}
}

func TestRememberSafeIsSerial(t *testing.T) {
	ag := &Agent{tools: tools.NewRegistry(t.TempDir())}
	if ag.parallelSafe("remember") {
		t.Error("remember must not be parallel-safe (it writes shared state)")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestClassifyMemory(t *testing.T) {
	cases := map[string]string{
		"always run make vet":             "rule",
		"Never commit .DS_Store":          "rule",
		"must build before pushing":       "rule",
		"prefer tabs over spaces":         "preference",
		"I like concise commit messages":  "preference",
		"we use Go modules":               "convention",
		"follow the existing file layout": "convention",
		"the relay lives in cmd/relay":    "note",
	}
	for text, want := range cases {
		if got := classifyMemory(text); got != want {
			t.Errorf("classifyMemory(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestUpsertMemoryCategoriesAndDedup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	if err := upsertMemoryBullet(path, categoryHeading("rule"), "always run make vet"); err != nil {
		t.Fatal(err)
	}
	if err := upsertMemoryBullet(path, categoryHeading("preference"), "prefer tabs"); err != nil {
		t.Fatal(err)
	}
	// A second rule files under the existing Rules subheading.
	if err := upsertMemoryBullet(path, categoryHeading("rule"), "never push to main"); err != nil {
		t.Fatal(err)
	}
	// Exact duplicate anywhere in the section is a no-op.
	if err := upsertMemoryBullet(path, categoryHeading("rule"), "always run make vet"); err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if strings.Count(got, memoryHeading) != 1 {
		t.Errorf("expected one Memory heading:\n%s", got)
	}
	if strings.Count(got, "### Rules") != 1 {
		t.Errorf("expected one Rules subheading:\n%s", got)
	}
	if strings.Count(got, "- always run make vet") != 1 {
		t.Errorf("duplicate bullet was added:\n%s", got)
	}
	if !strings.Contains(got, "### Preferences") || !strings.Contains(got, "- prefer tabs") {
		t.Errorf("preference not filed correctly:\n%s", got)
	}

	// Rules subheading should precede Preferences (insertion order via append).
	if idxR, idxP := strings.Index(got, "### Rules"), strings.Index(got, "### Preferences"); idxR == -1 || idxP == -1 || idxR > idxP {
		t.Errorf("subheading order unexpected:\n%s", got)
	}

	groups := readMemoryGrouped(path)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %#v", len(groups), groups)
	}
	if groups[0].heading != "### Rules" || len(groups[0].bullets) != 2 {
		t.Errorf("unexpected first group: %#v", groups[0])
	}
}

func TestDetectStandaloneRule(t *testing.T) {
	yes := map[string]string{
		"always run make vet before committing": "rule",
		"never commit secrets":                  "rule",
		"prefer table-driven tests":             "preference",
		"we use conventional commits":           "convention",
	}
	for text, wantCat := range yes {
		cat, ok := detectStandaloneRule(text)
		if !ok || cat != wantCat {
			t.Errorf("detectStandaloneRule(%q) = (%q,%v), want (%q,true)", text, cat, ok, wantCat)
		}
	}
	no := []string{
		"",
		"can you always run make vet?",     // question
		"fix the build",                    // not a directive
		"always",                           // too short
		"please refactor this\nand that",   // multi-line
		strings.Repeat("always do x ", 30), // too long
	}
	for _, text := range no {
		if cat, ok := detectStandaloneRule(text); ok {
			t.Errorf("detectStandaloneRule(%q) = (%q,true), want false", text, cat)
		}
	}
}

func TestAutoCaptureRule(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	reg := tools.NewRegistry(dir)
	ag := New(&config.Config{Model: "t", WorkingDir: dir}, nil, reg, nil, nil)

	msg, ok := ag.AutoCaptureRule("always run make vet before committing")
	if !ok {
		t.Fatal("expected a standalone rule to be captured")
	}
	if !strings.Contains(strings.ToLower(msg), "auto") {
		t.Errorf("expected an auto-capture message, got %q", msg)
	}
	got := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(got, "### Rules") || !strings.Contains(got, "- always run make vet before committing") {
		t.Errorf("rule not persisted under Rules:\n%s", got)
	}

	// Re-capturing the same rule is a no-op (already remembered).
	if _, ok := ag.AutoCaptureRule("always run make vet before committing"); ok {
		t.Error("duplicate auto-capture should be skipped")
	}

	// A plain task is not captured.
	if _, ok := ag.AutoCaptureRule("fix the failing build"); ok {
		t.Error("non-directive prompt should not be captured")
	}

	// Opt-out via env disables capture.
	t.Setenv("MOONY_MEMORY_AUTOCAPTURE", "0")
	if _, ok := ag.AutoCaptureRule("never delete the database"); ok {
		t.Error("auto-capture should be disabled when MOONY_MEMORY_AUTOCAPTURE=0")
	}
}

// TestReplaceSection: the whole-section save path overwrites a section's entire
// bullet list from raw multi-line text (one bullet per line, "- " optional),
// preserves other sections, and removes the section when given empty text.
func TestReplaceSection(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	ag := New(&config.Config{Model: "test", WorkingDir: dir}, nil, tools.NewRegistry(dir), nil, nil)

	if err := ag.AddBullet("Memory", "old one", false); err != nil {
		t.Fatalf("AddBullet: %v", err)
	}
	if err := ag.AddBullet("Versioning", "bump patch", false); err != nil {
		t.Fatalf("AddBullet: %v", err)
	}

	// Replace the whole Memory section with three lines (mixed "- " prefixing).
	if err := ag.ReplaceSection("Memory", "first rule\n- second rule\n\n  third rule  ", false); err != nil {
		t.Fatalf("ReplaceSection: %v", err)
	}
	secs := ag.AgentsSections(false)
	var mem *MemorySection
	for i := range secs {
		if secs[i].Heading == "Memory" {
			mem = &secs[i]
		}
	}
	if mem == nil {
		t.Fatalf("Memory section gone: %+v", secs)
	}
	want := []string{"first rule", "second rule", "third rule"}
	if len(mem.Bullets) != 3 || mem.Bullets[0] != want[0] || mem.Bullets[1] != want[1] || mem.Bullets[2] != want[2] {
		t.Errorf("bullets = %v, want %v", mem.Bullets, want)
	}
	got := readFile(t, filepath.Join(dir, "AGENTS.md"))
	if strings.Contains(got, "old one") {
		t.Error("old bullet not replaced")
	}
	if !strings.Contains(got, "- bump patch") {
		t.Error("Versioning section was clobbered")
	}

	// Empty text removes the section entirely (heading included).
	if err := ag.ReplaceSection("Memory", "   \n\n", false); err != nil {
		t.Fatalf("ReplaceSection empty: %v", err)
	}
	for _, s := range ag.AgentsSections(false) {
		if s.Heading == "Memory" {
			t.Errorf("Memory section should be gone, still present: %+v", s)
		}
	}
}

func TestParseMemoryFlags(t *testing.T) {
	cases := []struct {
		arg      string
		wantNote string
		wantCat  string
		wantGl   bool
	}{
		{"just a note", "just a note", "", false},
		{"--global a shared note", "a shared note", "", true},
		{"--rule always run vet", "always run vet", "rule", false},
		{"-p prefer tabs", "prefer tabs", "preference", false},
		{"--convention -g we use modules", "we use modules", "convention", true},
		{"--note plain thing", "plain thing", "note", false},
	}
	for _, c := range cases {
		note, cat, gl := ParseMemoryFlags(c.arg)
		if note != c.wantNote || cat != c.wantCat || gl != c.wantGl {
			t.Errorf("ParseMemoryFlags(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.arg, note, cat, gl, c.wantNote, c.wantCat, c.wantGl)
		}
	}
}
