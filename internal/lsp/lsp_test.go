package lsp

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathToURI(t *testing.T) {
	abs, _ := filepath.Abs(t.TempDir())
	uri := pathToURI(abs)
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("expected file:// URI, got %q", uri)
	}
}

func TestLanguageFor(t *testing.T) {
	cases := map[string]string{
		"main.go":   "go",
		"foo.py":    "python",
		"App.tsx":   "typescriptreact",
		"x.unknown": "plaintext",
		"core.cpp":  "cpp",
	}
	for path, want := range cases {
		if got := languageFor(path); got != want {
			t.Errorf("languageFor(%s) = %q, want %q", path, got, want)
		}
	}
}

func TestReadContentLength(t *testing.T) {
	in := "Content-Length: 17\r\n\r\n{\"jsonrpc\":\"2.0\"}"
	r := bufio.NewReader(strings.NewReader(in))
	n, err := readContentLength(r)
	if err != nil {
		t.Fatalf("readContentLength: %v", err)
	}
	if n != 17 {
		t.Errorf("length = %d, want 17", n)
	}
	body := make([]byte, n)
	if _, err := r.Read(body); err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"jsonrpc":"2.0"}` {
		t.Errorf("body = %q", body)
	}
}

func TestFormatDiagnostic(t *testing.T) {
	d := Diagnostic{Severity: 1, Source: "gopls", Message: "undefined: foo"}
	d.Range.Start = lspPosition{Line: 4, Character: 7}
	got := formatDiagnostic("/x/y/main.go", d)
	if !strings.Contains(got, "main.go:5:8") || !strings.Contains(got, "error") || !strings.Contains(got, "[gopls]") {
		t.Errorf("unexpected diagnostic format: %q", got)
	}
}

func TestLoadConfigsProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	gd := filepath.Join(home, ".config", "moony")
	if err := os.MkdirAll(gd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gd, "lsp.json"),
		[]byte(`{"servers":{"go":{"command":"global-bin","extensions":[".go"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	proj := t.TempDir()
	pd := filepath.Join(proj, ".sqe")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pd, "lsp.json"),
		[]byte(`{"servers":{"go":{"command":"project-bin","extensions":[".go"]},"py":{"command":"py","extensions":[".py"],"disabled":true}}}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	cfgs, err := LoadConfigs(proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Command != "project-bin" {
		t.Fatalf("expected project go server only, got %+v", cfgs)
	}
}
