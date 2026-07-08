package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRepoMap covers the structural overview: detected stack, key root files, a
// depth-2 tree with recursive file counts, and exclusion of ignored paths.
func TestRepoMap(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{
		"go.mod":                  "module x\n",
		"Makefile":                "build:\n",
		"main.go":                 "package main\n",
		"internal/agent/agent.go": "package agent\n",
		"internal/agent/run.go":   "package agent\n",
		"internal/tools/tool.go":  "package tools\n",
		"cmd/eval/main.go":        "package main\n",
		"node_modules/dep/big.js": "// ignored\n", // default-ignored
	})
	m := NewRegistry(dir).RepoMap()

	for _, want := range []string{
		"Repository map", "(Go)", "key files:", "go.mod", "Makefile", "main.go",
		"internal/ (3)", "agent/ (2)", "tools/ (1)", "cmd/ (1)", "eval/ (1)",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("RepoMap missing %q; got:\n%s", want, m)
		}
	}
	// Ignored directories are excluded (both the dir and its files).
	if strings.Contains(m, "node_modules") || strings.Contains(m, "big.js") {
		t.Errorf("RepoMap should exclude ignored paths; got:\n%s", m)
	}
}

// TestRepoMapEmpty: a tree with no (non-ignored) files yields an empty map.
func TestRepoMapEmpty(t *testing.T) {
	if m := NewRegistry(t.TempDir()).RepoMap(); m != "" {
		t.Errorf("empty repo should yield an empty map, got %q", m)
	}
}

// TestRepoMapCached: RepoMap memoizes per registry, so a file added after the
// first call doesn't trigger a re-walk (the map stays the session-start snapshot).
func TestRepoMapCached(t *testing.T) {
	dir := writeRepoFiles(t, map[string]string{"go.mod": "module x\n", "a.go": "package x\n"})
	r := NewRegistry(dir)
	first := r.RepoMap()
	if first == "" {
		t.Fatal("expected a non-empty map for a non-empty repo")
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if second := r.RepoMap(); second != first {
		t.Errorf("RepoMap should be memoized (stable) within a registry:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestDetectStack(t *testing.T) {
	cases := []struct{ marker, want string }{
		{"go.mod", "Go"},
		{"Cargo.toml", "Rust"},
		{"pyproject.toml", "Python"},
		{"package.json", "Node/TypeScript"},
		{"pom.xml", "Java/JVM"},
		{"Gemfile", "Ruby"},
	}
	for _, c := range cases {
		if got := detectStack(map[string]bool{c.marker: true}); got != c.want {
			t.Errorf("detectStack(%s) = %q, want %q", c.marker, got, c.want)
		}
	}
	if got := detectStack(map[string]bool{"README.md": true}); got != "" {
		t.Errorf("no manifest should yield empty stack, got %q", got)
	}
}
