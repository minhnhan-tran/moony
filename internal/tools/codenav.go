package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/pysty1995/moony/internal/lsp"
)

// lspManager is the slice of *lsp.Manager that code_nav needs. Declaring it as
// an interface here keeps tool.go from importing internal/lsp for the field.
type lspManager interface {
	Empty() bool
	Handles(path string) bool
	Definition(ctx context.Context, path string, line, char int) ([]lsp.Location, string, error)
	References(ctx context.Context, path string, line, char int, includeDecl bool) ([]lsp.Location, string, error)
	Hover(ctx context.Context, path string, line, char int) (string, string, error)
}

// SetLSP wires a running LSP manager into the registry so the code_nav tool can
// answer definition/references/hover with compiler-grade precision. Passing a
// nil or empty manager leaves code_nav registered but degrading gracefully.
func (r *Registry) SetLSP(m lspManager) { r.lsp = m }

type codeNavTool struct{ r *Registry }

func (t *codeNavTool) Name() string { return "code_nav" }

func (t *codeNavTool) Description() string {
	return "Precisely navigate code using a running Language Server (LSP), e.g. " +
		"gopls for Go. Unlike find_symbol/find_references (which are regex/ripgrep " +
		"heuristics), this resolves the ACTUAL symbol under the cursor via the " +
		"compiler, so it disambiguates overloads, methods, and shadowed names. " +
		"action=definition jumps to where a symbol is defined; action=references " +
		"lists real call sites/usages; action=hover returns the type signature and " +
		"doc comment. Give the file 'path' and the 'symbol' name; optionally 'line' " +
		"(1-based) to disambiguate which occurrence, and 'occurrence' (1-based) to " +
		"pick among several on that line. Requires a language server for the file's " +
		"type to be running (managed via the LSP menu); otherwise it says so."
}

func (t *codeNavTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"action":     {"type": "string", "enum": ["definition", "references", "hover"], "description": "What to resolve for the symbol: its definition, its references (usages), or hover (signature/docs)."},
			"path":       {"type": "string", "description": "File containing the symbol occurrence to resolve from (workspace-relative)."},
			"symbol":     {"type": "string", "description": "The identifier to resolve (bare name, no parentheses)."},
			"line":       {"type": "integer", "description": "Optional 1-based line to disambiguate which occurrence of the symbol to resolve from."},
			"occurrence": {"type": "integer", "description": "Optional 1-based index to pick among multiple matches (of the symbol on the chosen line, else file-wide). Defaults to 1."}
		},
		"required": ["action", "path", "symbol"]
	}`)
}

func (t *codeNavTool) Mutates() bool { return false }

type codeNavInput struct {
	Action     string  `json:"action"`
	Path       string  `json:"path"`
	Symbol     string  `json:"symbol"`
	Line       flexInt `json:"line"`
	Occurrence flexInt `json:"occurrence"`
}

func (t *codeNavTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in codeNavInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	in.Action = strings.TrimSpace(strings.ToLower(in.Action))
	in.Symbol = strings.TrimSpace(in.Symbol)
	if in.Action == "" || in.Path == "" || in.Symbol == "" {
		return "", fmt.Errorf("action, path, and symbol are required")
	}
	switch in.Action {
	case "definition", "references", "hover":
	default:
		return "", fmt.Errorf("unknown action %q (want definition, references, or hover)", in.Action)
	}

	if t.r.lsp == nil || t.r.lsp.Empty() {
		return "No language server is running, so code_nav can't resolve symbols precisely. " +
			"Start/install one for this file type via the LSP menu (e.g. gopls for Go), " +
			"or fall back to find_symbol / find_references / grep.", nil
	}

	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return "", err
	}
	if !t.r.lsp.Handles(abs) {
		return fmt.Sprintf("No running language server handles %s files. code_nav is unavailable for this file; "+
			"use find_symbol / find_references / grep instead.", filepath.Ext(abs)), nil
	}

	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	line, char, ok := locateSymbol(string(b), in.Symbol, int(in.Occurrence), int(in.Line))
	if !ok {
		return "", fmt.Errorf("symbol %q not found in %s%s", in.Symbol, in.Path, lineHintSuffix(int(in.Line)))
	}

	switch in.Action {
	case "definition":
		locs, _, err := t.r.lsp.Definition(ctx, abs, line, char)
		if err != nil {
			return navErr(err)
		}
		return formatNavLocations("definition", in.Symbol, locs, t.r.Root()), nil
	case "references":
		locs, _, err := t.r.lsp.References(ctx, abs, line, char, true)
		if err != nil {
			return navErr(err)
		}
		return formatNavLocations("references", in.Symbol, locs, t.r.Root()), nil
	default: // hover
		text, _, err := t.r.lsp.Hover(ctx, abs, line, char)
		if err != nil {
			return navErr(err)
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Sprintf("hover: no information for %q at %s:%d.", in.Symbol, in.Path, line+1), nil
		}
		return fmt.Sprintf("hover for %q:\n\n%s", in.Symbol, strings.TrimSpace(text)), nil
	}
}

func navErr(err error) (string, error) {
	// moony's lsp.Manager reports "no server" as a plain error; treat it as a
	// graceful degrade (a usable answer) rather than a tool failure.
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no language server") {
		return "No running language server handles this file type; use find_symbol / find_references / grep instead.", nil
	}
	return "", err
}

// relOrAbs renders an absolute location path relative to root when it's inside
// it, else leaves it absolute.
func relOrAbs(p, root string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

func lineHintSuffix(line int) string {
	if line > 0 {
		return fmt.Sprintf(" on line %d", line)
	}
	return ""
}

// locateSymbol finds the identifier `symbol` in content as a whole word and
// returns its 0-based line and UTF-16 character offset (LSP positions are
// UTF-16). occurrence (1-based, default 1) picks among matches; when lineHint
// (1-based, 0 = none) is set, only matches on that line are considered.
func locateSymbol(content, symbol string, occurrence, lineHint int) (line, char int, ok bool) {
	if occurrence < 1 {
		occurrence = 1
	}
	lines := strings.Split(content, "\n")
	seen := 0
	for i, ln := range lines {
		if lineHint > 0 && i != lineHint-1 {
			continue
		}
		for _, col := range wholeWordCols(ln, symbol) {
			seen++
			if seen == occurrence {
				return i, utf16Len(ln[:col]), true
			}
		}
	}
	// If a line hint was given but produced nothing, retry file-wide so a
	// slightly-off hint still resolves.
	if lineHint > 0 {
		return locateSymbol(content, symbol, occurrence, 0)
	}
	return 0, 0, false
}

// wholeWordCols returns the byte columns where symbol occurs as a whole word.
func wholeWordCols(line, symbol string) []int {
	var cols []int
	from := 0
	for {
		idx := strings.Index(line[from:], symbol)
		if idx < 0 {
			return cols
		}
		at := from + idx
		if !isIdentByte(prevByte(line, at)) && !isIdentByte(byteAt(line, at+len(symbol))) {
			cols = append(cols, at)
		}
		from = at + len(symbol)
		if from > len(line) {
			return cols
		}
	}
}

func prevByte(s string, i int) byte {
	if i-1 < 0 || i-1 >= len(s) {
		return ' '
	}
	return s[i-1]
}

func byteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return ' '
	}
	return s[i]
}

func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// utf16Len returns the number of UTF-16 code units in s (LSP character offset).
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// formatLocations renders navigation results as 'path:line:col' lines, with the
// workspace-relative path when possible, sorted and de-duplicated.
func formatNavLocations(action, symbol string, locs []lsp.Location, root string) string {
	if len(locs) == 0 {
		return fmt.Sprintf("%s: no results for %q.", action, symbol)
	}
	type row struct {
		path       string
		line, char int
	}
	seen := map[string]bool{}
	rows := make([]row, 0, len(locs))
	for _, l := range locs {
		p := relOrAbs(l.Path, root)
		key := fmt.Sprintf("%s:%d:%d", p, l.Range.Start.Line, l.Range.Start.Character)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, row{p, l.Range.Start.Line + 1, l.Range.Start.Character + 1})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].path != rows[j].path {
			return rows[i].path < rows[j].path
		}
		if rows[i].line != rows[j].line {
			return rows[i].line < rows[j].line
		}
		return rows[i].char < rows[j].char
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s of %q — %d result(s):\n", action, symbol, len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "%s:%d:%d\n", r.path, r.line, r.char)
	}
	return strings.TrimRight(b.String(), "\n")
}
