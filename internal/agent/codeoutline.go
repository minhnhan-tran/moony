package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Code-aware tool-output compression (Headroom "CodeCompressor" idea). When a
// read_file result on a source file is over budget, capToolOutput replaces the
// blunt byte-clip (which throws away the whole bottom of the file) with a
// structural OUTLINE: declaration/signature lines are kept across the WHOLE
// file with their line numbers, and runs of body lines collapse into a short
// "… (N lines elided)" marker. The model still sees the file's shape and exact
// line numbers, so it can read_file a specific range to pull a body back when
// it actually needs one — far cheaper than shipping every line verbatim.

// codeExts are the extensions outlineReadFile will compress. Anything else
// falls back to the generic byte cap.
var codeExts = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".rs": true, ".java": true, ".kt": true, ".kts": true, ".swift": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true,
	".cs": true, ".rb": true, ".php": true, ".scala": true, ".m": true, ".mm": true,
}

// numberedLine matches read_file's "<n>: <code>" output format (files.go).
var numberedLine = regexp.MustCompile(`^(\d+): (.*)$`)

// declKeyword matches lines that introduce a declaration across the common
// languages (optionally behind visibility/modifier words). Keyword-based, so it
// stays conservative — it never guesses a body line is structural.
var declKeyword = regexp.MustCompile(`^(?:export\s+|default\s+|public\s+|private\s+|protected\s+|internal\s+|static\s+|final\s+|abstract\s+|async\s+|open\s+|sealed\s+|override\s+|pub\s+|unsafe\s+)*(?:func|function|def|fn|class|struct|interface|enum|trait|impl|type|typedef|module|namespace|package|import|from|use|using|const|var|let|val|protocol|extension|object|record|companion)\b`)

// declDecorator matches a decorator/annotation/attribute line (Python @, Rust
// #[...], Java/Kotlin @Ann), which usually sits right above a declaration.
var declDecorator = regexp.MustCompile(`^[@#]`)

// ctrlKeyword guards the signature heuristic from matching control-flow blocks.
var ctrlKeyword = regexp.MustCompile(`^(if|for|while|switch|else|elif|catch|do|return|case|default|except|finally|with|match|when|guard)\b`)

// sigOpener matches a C-family / JS method or function signature that opens a
// block ("… ) {") for languages where definitions carry no leading keyword.
var sigOpener = regexp.MustCompile(`\)\s*\{$`)

// isStructural reports whether a (left-trimmed) code line is worth keeping in an
// outline.
func isStructural(code string) bool {
	t := strings.TrimSpace(code)
	if t == "" {
		return false
	}
	if declKeyword.MatchString(t) || declDecorator.MatchString(t) {
		return true
	}
	if !ctrlKeyword.MatchString(t) && sigOpener.MatchString(t) {
		return true
	}
	return false
}

const (
	outlineHeadLines = 15 // always keep the file header (package/imports/top docs)
	outlineTailLines = 8  // always keep the tail (often trailing decls)
)

// outlineReadFile turns numbered read_file output into a signatures-only
// outline. It returns the outline and true only when an extension-recognised
// source file actually shrank; otherwise ("", false) and the caller keeps the
// generic cap.
func outlineReadFile(path, numbered string) (string, bool) {
	if !codeExts[strings.ToLower(filepath.Ext(path))] {
		return "", false
	}
	lines := strings.Split(numbered, "\n")
	if len(lines) < outlineHeadLines+outlineTailLines+4 {
		return "", false // too small to be worth outlining
	}

	keep := make([]bool, len(lines))
	for i, ln := range lines {
		if i < outlineHeadLines || i >= len(lines)-outlineTailLines {
			keep[i] = true
			continue
		}
		if m := numberedLine.FindStringSubmatch(ln); m != nil {
			keep[i] = isStructural(m[2])
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "// [outline: %d-line file compressed to its declaration/signature lines; re-read with offset/limit for a full body]\n", len(lines))
	elidedFrom := -1
	flush := func(upto int) {
		if elidedFrom < 0 {
			return
		}
		n := upto - elidedFrom
		lo := lineNum(lines[elidedFrom])
		hi := lineNum(lines[upto-1])
		if lo > 0 && hi >= lo {
			fmt.Fprintf(&b, "    … (%d lines elided: %d–%d)\n", n, lo, hi)
		} else {
			fmt.Fprintf(&b, "    … (%d lines elided)\n", n)
		}
		elidedFrom = -1
	}
	for i, ln := range lines {
		if keep[i] {
			flush(i)
			b.WriteString(ln)
			b.WriteByte('\n')
		} else if elidedFrom < 0 {
			elidedFrom = i
		}
	}
	flush(len(lines))

	out := strings.TrimRight(b.String(), "\n")
	if len(out) >= len(numbered) {
		return "", false // no win (file was mostly declarations)
	}
	return out, true
}

// lineNum extracts the leading line number from a "<n>: …" line, or 0.
func lineNum(s string) int {
	if m := numberedLine.FindStringSubmatch(s); m != nil {
		n := 0
		for _, r := range m[1] {
			n = n*10 + int(r-'0')
		}
		return n
	}
	return 0
}

// capToolOutput caps a tool result for the model. For oversized read_file
// output on a source file it first tries a code outline (when enabled); failing
// that, and for every other tool, it falls back to the generic byte cap.
func (a *Agent) capToolOutput(name string, input json.RawMessage, output string, max int) string {
	if a.cfg.CodeOutline && name == "read_file" && max > 0 && len(output) > max {
		var in struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(input, &in) == nil {
			if outline, ok := outlineReadFile(in.Path, output); ok {
				return capOutput(outline, max)
			}
		}
	}
	return capOutput(output, max)
}
