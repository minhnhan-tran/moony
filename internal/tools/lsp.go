package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/lsp"
)

// RegisterLSP wires (or re-wires, on reload) the LSP-backed navigation tools
// — lsp_definition, lsp_references, lsp_hover — against mgr. Called from
// Agent.AttachLSP alongside the diagnostics-after-edit wiring, so it shares
// the same manager and doesn't need its own construction-time dependency.
// A nil/empty manager still registers the tools; they report "no language
// server" per call rather than being absent, since AttachLSP can be called
// again later (e.g. after `/lsp reload`) once servers exist.
func (r *Registry) RegisterLSP(mgr *lsp.Manager) {
	r.Register(&lspDefinitionTool{r: r, mgr: mgr})
	r.Register(&lspReferencesTool{r: r, mgr: mgr})
	r.Register(&lspHoverTool{r: r, mgr: mgr})
}

const lspPositionSchema = `
			"path": {"type": "string", "description": "File to look up the symbol in."},
			"line": {"type": "integer", "description": "1-indexed line number of the symbol."},
			"column": {"type": "integer", "description": "1-indexed column (character offset) of the symbol on that line."}`

// resolveLSPTarget validates {path,line,column} and resolves path within the
// workspace, returning 1-indexed line/column unchanged (the lsp.Manager
// methods do the 0-index conversion internally).
func resolveLSPTarget(r *Registry, path string, line, column int) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if line < 1 || column < 1 {
		return "", fmt.Errorf("line and column are 1-indexed and must be >= 1")
	}
	return r.resolve(path)
}

// noServerMsg is returned (as a result, not an error — same convention as an
// empty diagnostics/search result) when no running LSP server handles the
// target file's extension.
func noServerMsg(path string) string {
	ext := filepath.Ext(path)
	return fmt.Sprintf("No language server is configured/running for %q files; nothing to look up.", ext)
}

// formatLocations renders locations as "path:line:col: <line text>", 1-indexed,
// relative to the workspace root when possible.
func formatLocations(root string, locs []lsp.Location) string {
	if len(locs) == 0 {
		return "No results."
	}
	var b strings.Builder
	for _, l := range locs {
		rel := l.Path
		if r, err := filepath.Rel(root, l.Path); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
		line1 := l.Range.Start.Line + 1
		col1 := l.Range.Start.Character + 1
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", filepath.ToSlash(rel), line1, col1, snippetAt(l.Path, line1))
	}
	return strings.TrimSpace(b.String())
}

// snippetAt best-effort reads the 1-indexed line from path for context;
// returns "" (not an error) if the file can't be read.
func snippetAt(path string, line1 int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line1 < 1 || line1 > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line1-1])
}

// lspDefinitionTool wraps textDocument/definition.
type lspDefinitionTool struct {
	r   *Registry
	mgr *lsp.Manager
}

func (t *lspDefinitionTool) Name() string { return "lsp_definition" }
func (t *lspDefinitionTool) Description() string {
	return "Jump to where a symbol at a file:line:column is DEFINED, using the project's running language server " +
		"(exact semantic resolution — imports, interfaces, generated code — unlike grep). Prefer this over " +
		"grep/find_symbol when you already have a precise position (e.g. from a diagnostic or another lookup) " +
		"and need the real definition site, especially across files."
}
func (t *lspDefinitionTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {` + lspPositionSchema + `}, "required": ["path", "line", "column"]}`)
}
func (t *lspDefinitionTool) Mutates() bool { return false }

func (t *lspDefinitionTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	abs, err := resolveLSPTarget(t.r, in.Path, in.Line, in.Column)
	if err != nil {
		return "", err
	}
	locs, server, err := t.mgr.Definition(ctx, abs, in.Line, in.Column)
	if err != nil {
		return "", err
	}
	if server == "" {
		return noServerMsg(abs), nil
	}
	return formatLocations(t.r.root, locs), nil
}

// lspReferencesTool wraps textDocument/references.
type lspReferencesTool struct {
	r   *Registry
	mgr *lsp.Manager
}

func (t *lspReferencesTool) Name() string { return "lsp_references" }
func (t *lspReferencesTool) Description() string {
	return "Find every USAGE of the symbol at a file:line:column, using the project's running language server " +
		"(exact semantic references, not text matches — so it won't miss aliased imports or match unrelated " +
		"same-named symbols the way grep can). Prefer this for refactoring/impact-analysis once you have a " +
		"precise position; use find_references/grep for a name-based, no-position search."
}
func (t *lspReferencesTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {` + lspPositionSchema + `,
			"includeDeclaration": {"type": "boolean", "description": "Include the declaration/definition site itself among the results (default true)."}
		}, "required": ["path", "line", "column"]}`)
}
func (t *lspReferencesTool) Mutates() bool { return false }

func (t *lspReferencesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	in := struct {
		Path               string `json:"path"`
		Line               int    `json:"line"`
		Column             int    `json:"column"`
		IncludeDeclaration *bool  `json:"includeDeclaration"`
	}{}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	abs, err := resolveLSPTarget(t.r, in.Path, in.Line, in.Column)
	if err != nil {
		return "", err
	}
	includeDecl := in.IncludeDeclaration == nil || *in.IncludeDeclaration
	locs, server, err := t.mgr.References(ctx, abs, in.Line, in.Column, includeDecl)
	if err != nil {
		return "", err
	}
	if server == "" {
		return noServerMsg(abs), nil
	}
	return formatLocations(t.r.root, locs), nil
}

// lspHoverTool wraps textDocument/hover.
type lspHoverTool struct {
	r   *Registry
	mgr *lsp.Manager
}

func (t *lspHoverTool) Name() string { return "lsp_hover" }
func (t *lspHoverTool) Description() string {
	return "Get the language server's hover info (resolved type/signature and doc comment) for the symbol at a " +
		"file:line:column — the same info an IDE tooltip shows. Useful to check a function's real signature or " +
		"a variable's inferred type without reading its whole definition."
}
func (t *lspHoverTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {` + lspPositionSchema + `}, "required": ["path", "line", "column"]}`)
}
func (t *lspHoverTool) Mutates() bool { return false }

func (t *lspHoverTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	abs, err := resolveLSPTarget(t.r, in.Path, in.Line, in.Column)
	if err != nil {
		return "", err
	}
	text, server, err := t.mgr.Hover(ctx, abs, in.Line, in.Column)
	if err != nil {
		return "", err
	}
	if server == "" {
		return noServerMsg(abs), nil
	}
	if strings.TrimSpace(text) == "" {
		return "No hover info at that position.", nil
	}
	return text, nil
}
