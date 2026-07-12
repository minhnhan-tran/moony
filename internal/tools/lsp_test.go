package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/lsp"
)

func TestLSPToolsReadOnly(t *testing.T) {
	if (&lspDefinitionTool{}).Mutates() || (&lspReferencesTool{}).Mutates() || (&lspHoverTool{}).Mutates() || (&lspDiagnosticsTool{}).Mutates() {
		t.Error("lsp_* tools should never be reported as mutating")
	}
}

func TestLSPToolsNamesAndSchemas(t *testing.T) {
	for _, tool := range []Tool{&lspDefinitionTool{}, &lspReferencesTool{}, &lspHoverTool{}} {
		if !strings.HasPrefix(tool.Name(), "lsp_") {
			t.Errorf("tool name %q should start with lsp_", tool.Name())
		}
		req := schemaRequired(tool.Schema())
		for _, want := range []string{"path", "line", "column"} {
			found := false
			for _, r := range req {
				if r == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: schema required list %v missing %q", tool.Name(), req, want)
			}
		}
	}
}

func TestResolveLSPTargetValidation(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(root)
	if _, err := resolveLSPTarget(reg, "", 1, 1); err == nil {
		t.Error("empty path should be rejected")
	}
	if _, err := resolveLSPTarget(reg, "a.go", 0, 1); err == nil {
		t.Error("line 0 (not 1-indexed) should be rejected")
	}
	if _, err := resolveLSPTarget(reg, "a.go", 1, 0); err == nil {
		t.Error("column 0 (not 1-indexed) should be rejected")
	}
}

func TestLSPToolsNoServerConfigured(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(root)
	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("a.go", "package a\n")
	mgr := &lsp.Manager{} // configured but with zero running servers, same as no lsp.json

	for _, tool := range []Tool{
		&lspDefinitionTool{r: reg, mgr: mgr},
		&lspReferencesTool{r: reg, mgr: mgr},
		&lspHoverTool{r: reg, mgr: mgr},
	} {
		in, _ := json.Marshal(map[string]any{"path": "a.go", "line": 1, "column": 1})
		out, err := tool.Execute(context.Background(), in)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tool.Name(), err)
		}
		if !strings.Contains(out, "No language server") {
			t.Errorf("%s: expected a no-server message, got: %s", tool.Name(), out)
		}
	}

	// lsp_diagnostics takes only a path; with no server it should also report the
	// no-server message rather than erroring.
	diag := &lspDiagnosticsTool{r: reg, mgr: mgr}
	in, _ := json.Marshal(map[string]any{"path": "a.go"})
	out, err := diag.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("lsp_diagnostics: unexpected error: %v", err)
	}
	if !strings.Contains(out, "No language server") {
		t.Errorf("lsp_diagnostics: expected a no-server message, got: %s", out)
	}
	if _, err := diag.Execute(context.Background(), mustJSON(map[string]any{"path": ""})); err == nil {
		t.Error("lsp_diagnostics: empty path should be rejected")
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestFormatLocationsEmpty(t *testing.T) {
	if got := formatLocations("/root", nil); got != "No results." {
		t.Errorf("formatLocations(nil) = %q, want a no-results message", got)
	}
}

func TestRegisterLSPReplacesExistingTools(t *testing.T) {
	root := t.TempDir()
	reg := NewRegistry(root)
	reg.RegisterLSP(&lsp.Manager{})
	first := reg.tools["lsp_definition"]
	reg.RegisterLSP(&lsp.Manager{}) // simulate a reload with a fresh manager
	second := reg.tools["lsp_definition"]
	if first == second {
		t.Error("RegisterLSP should install a fresh tool instance (bound to the new manager) on each call")
	}
	if len(reg.order) != len(uniqueStrings(reg.order)) {
		t.Errorf("RegisterLSP called twice should not duplicate registry order entries: %v", reg.order)
	}
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
