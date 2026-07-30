package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodebaseMapStructural(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "pkg", "auth.go"), `package pkg
func Login(u string) error { return nil }
type Session struct{}
`)
	mustWrite(t, filepath.Join(dir, "main.go"), `package main
func main() { _ = 0 }
`)
	reg := NewRegistry(dir)
	// Force a synchronous build so the map is populated.
	_ = reg.SymbolMap()

	out := reg.CodebaseMapStructural()
	if out == "" {
		t.Fatal("expected non-empty structural map")
	}
	for _, want := range []string{"auth.go", "Login", "pkg/"} {
		if !strings.Contains(out, want) {
			t.Errorf("structural map missing %q in:\n%s", want, out)
		}
	}
	// Deterministic: two runs identical.
	if out2 := reg.CodebaseMapStructural(); out2 != out {
		t.Error("structural map is not deterministic across calls")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCodebaseMapExcludesTestsAndBuildOutput pins the render-time filters: test
// files and generated/compiled output must not consume map budget, while the
// symbols themselves stay resolvable via the symbol map.
func TestCodebaseMapExcludesTestsAndBuildOutput(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "pkg", "auth.go"), `package pkg
func Login(u string) error { return nil }
`)
	mustWrite(t, filepath.Join(dir, "pkg", "auth_test.go"), `package pkg
func TestLoginHelperThing(t *T) {}
`)
	mustWrite(t, filepath.Join(dir, "web", "out", "bundle.js"), `
function compiledArtifactFunc(a) { return a; }
`)
	mustWrite(t, filepath.Join(dir, "web", "app.min.js"), `
function minifiedThing(a){return a}
`)
	reg := NewRegistry(dir)
	_ = reg.SymbolMap()
	out := reg.CodebaseMapStructural()

	if !strings.Contains(out, "auth.go") {
		t.Errorf("expected source file in map:\n%s", out)
	}
	for _, unwanted := range []string{"auth_test.go", "TestLoginHelperThing", "bundle.js", "compiledArtifactFunc", "app.min.js"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("map should exclude %q:\n%s", unwanted, out)
		}
	}
}

// TestCodebaseMapRendersKindAndLine ensures each symbol carries its declaration
// kind and line, which is what makes the map actionable (jump straight to it).
func TestCodebaseMapRendersKindAndLine(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "pkg", "auth.go"), `package pkg

func LoginHandler(u string) error { return nil }

type SessionRecord struct{}
`)
	mustWrite(t, filepath.Join(dir, "pkg", "use.go"), `package pkg
func caller() { _ = LoginHandler("x") }
`)
	reg := NewRegistry(dir)
	_ = reg.SymbolMap()
	out := reg.CodebaseMapStructural()

	if !strings.Contains(out, "LoginHandler (func:3") {
		t.Errorf("expected kind:line for LoginHandler in:\n%s", out)
	}
	if !strings.Contains(out, "SessionRecord (type:5") {
		t.Errorf("expected kind:line for SessionRecord in:\n%s", out)
	}
}

// TestCodebaseMapFilesSortedAlphabetically guards diff stability: the file is
// committed and auto-refreshed, so ordering must not depend on scores.
func TestCodebaseMapFilesSortedAlphabetically(t *testing.T) {
	dir := t.TempDir()
	// zebra is referenced far more than alpha, so score order would invert these.
	mustWrite(t, filepath.Join(dir, "pkg", "zebra.go"), `package pkg
func ZebraThing() {}
`)
	mustWrite(t, filepath.Join(dir, "pkg", "alpha.go"), `package pkg
func AlphaThing() {}
`)
	for i := 0; i < 5; i++ {
		mustWrite(t, filepath.Join(dir, "pkg", "user"+string(rune('a'+i))+".go"), `package pkg
func u`+string(rune('a'+i))+`() { ZebraThing(); ZebraThing() }
`)
	}
	reg := NewRegistry(dir)
	_ = reg.SymbolMap()
	out := reg.CodebaseMapStructural()

	ia, iz := strings.Index(out, "`alpha.go`"), strings.Index(out, "`zebra.go`")
	if ia < 0 || iz < 0 {
		t.Fatalf("both files should be listed:\n%s", out)
	}
	if ia > iz {
		t.Errorf("files must be alphabetical (alpha before zebra):\n%s", out)
	}
}

// TestCodebaseMapRespectsBudget ensures the section degrades gracefully instead
// of being truncated mid-line by the prompt injector.
func TestCodebaseMapRespectsBudget(t *testing.T) {
	dir := t.TempDir()
	for d := 0; d < 30; d++ {
		for f := 0; f < 10; f++ {
			var b strings.Builder
			b.WriteString("package p\n")
			for s := 0; s < 30; s++ {
				fmt.Fprintf(&b, "func VeryLongExportedSymbolName%d_%d_%d() {}\n", d, f, s)
			}
			mustWrite(t, filepath.Join(dir, fmt.Sprintf("dir%02d", d), fmt.Sprintf("file%02d.go", f)), b.String())
		}
	}
	reg := NewRegistry(dir)
	_ = reg.SymbolMap()
	out := reg.CodebaseMapStructural()
	if out == "" {
		t.Fatal("expected output")
	}
	// Allow the floor case (symCap=2) to exceed, but it must be far below the
	// pathological unbudgeted size.
	if len(out) > codebaseMapBudgetBytes*2 {
		t.Errorf("output %d bytes far exceeds budget %d", len(out), codebaseMapBudgetBytes)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("output should end cleanly with a newline, not mid-line")
	}
}

// TestCodebaseMapTiers pins the two-tier contract: the digest is a compact
// directory index (no per-file symbol detail), while the full listing and the
// per-directory view carry symbols. Keeping the prompt on the digest is what lets
// the on-disk map be arbitrarily detailed.
func TestCodebaseMapTiers(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "pkg", "auth.go"), `package pkg
func LoginHandler(u string) error { return nil }
type SessionRecord struct{}
`)
	mustWrite(t, filepath.Join(dir, "pkg", "use.go"), `package pkg
func caller() { _ = LoginHandler("x") }
`)
	mustWrite(t, filepath.Join(dir, "other", "thing.go"), `package other
func OtherThing() {}
`)
	reg := NewRegistry(dir)
	_ = reg.SymbolMap()

	digest := reg.CodebaseMapDigest()
	full := reg.CodebaseMapStructural()
	if digest == "" || full == "" {
		t.Fatal("expected both tiers to render")
	}
	if len(digest) >= len(full) {
		t.Errorf("digest (%d) should be smaller than full listing (%d)", len(digest), len(full))
	}
	if !strings.Contains(digest, "pkg") || !strings.Contains(digest, "other") {
		t.Errorf("digest should index every directory:\n%s", digest)
	}
	if strings.Contains(digest, "func:") {
		t.Errorf("digest must not carry per-symbol detail:\n%s", digest)
	}
	if !strings.Contains(full, "LoginHandler (func:") {
		t.Errorf("full listing should carry symbol detail:\n%s", full)
	}

	// Per-directory view: scoped to the requested dir, with full detail.
	one := reg.CodebaseMapForDir("pkg")
	if !strings.Contains(one, "LoginHandler (func:") {
		t.Errorf("per-directory view should carry symbol detail:\n%s", one)
	}
	if strings.Contains(one, "OtherThing") {
		t.Errorf("per-directory view leaked another directory:\n%s", one)
	}
	if reg.CodebaseMapForDir("does/not/exist") != "" {
		t.Error("unknown directory should render empty")
	}
}

// TestCodebaseMapToolDrillsDown covers the tool wrapper: no argument yields the
// index, a directory yields its detail, and an unknown one recovers with
// suggestions rather than an error.
func TestCodebaseMapToolDrillsDown(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "internal", "web", "server.go"), `package web
func Handler() {}
`)
	mustWrite(t, filepath.Join(dir, "internal", "web", "use.go"), `package web
func c() { Handler() }
`)
	reg := NewRegistry(dir)
	_ = reg.SymbolMap()
	tool := &codebaseMapTool{r: reg}

	index, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "internal/web") {
		t.Errorf("no-arg call should return the index:\n%s", index)
	}

	detail, err := tool.Execute(context.Background(), json.RawMessage(`{"directory":"internal/web"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "Handler (func:") {
		t.Errorf("directory call should return symbol detail:\n%s", detail)
	}

	miss, err := tool.Execute(context.Background(), json.RawMessage(`{"directory":"web"}`))
	if err != nil {
		t.Fatalf("unknown dir should not error: %v", err)
	}
	if !strings.Contains(miss, "internal/web") {
		t.Errorf("unknown dir should suggest close matches:\n%s", miss)
	}
}
