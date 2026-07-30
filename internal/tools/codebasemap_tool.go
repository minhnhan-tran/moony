package tools

// codebasemap_tool.go exposes the generated codebase map as an on-demand tool.
//
// The system prompt only carries the COMPACT digest (a directory index), which
// keeps prompt cost flat no matter how large the repo is. This tool serves the
// FULL per-file symbol listing for a directory when the model actually needs it,
// so map detail is effectively unbounded instead of competing for prompt space.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type codebaseMapTool struct{ r *Registry }

func (t *codebaseMapTool) Name() string  { return "codebase_map" }
func (t *codebaseMapTool) Mutates() bool { return false }

func (t *codebaseMapTool) Description() string {
	return "Get the generated structural map of a DIRECTORY: every mapped source file in it with its key definitions, each with kind (func/type/const…), declaration line, and how many other files reference it. Built from a local symbol/caller graph — no LLM, always current, and far cheaper than reading the files. The system prompt already carries a compact directory index; call this to drill into one directory from that index (e.g. before editing an unfamiliar package, or to answer 'what's in internal/web?'). Pass no directory to get the index of all mapped directories. Use find_symbol to jump to one known symbol, grep for exact text, and this for a structural overview of an area."
}

func (t *codebaseMapTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"directory": {"type": "string", "description": "Repo-relative directory to describe, e.g. \"internal/web\" (\"\" or \".\" for the project root). Omit to list all mapped directories."}
		}
	}`)
}

type codebaseMapInput struct {
	Directory string `json:"directory"`
	// Accept a couple of natural aliases so a near-miss argument still works.
	Dir  string `json:"dir"`
	Path string `json:"path"`
}

func (t *codebaseMapTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in codebaseMapInput
	if err := decode(input, &in); err != nil {
		return "", err
	}
	dir := strings.TrimSpace(in.Directory)
	if dir == "" {
		dir = strings.TrimSpace(in.Dir)
	}
	if dir == "" {
		dir = strings.TrimSpace(in.Path)
	}

	dirs := t.r.CodebaseMapDirs()
	if len(dirs) == 0 {
		return "No codebase map available (no supported source files found).", nil
	}

	// No argument: return the compact index so the model can choose.
	if dir == "" {
		if d := t.r.CodebaseMapDigest(); d != "" {
			return d, nil
		}
		return "No codebase map available.", nil
	}

	norm := strings.Trim(strings.TrimSpace(dir), "/")
	if norm == "." {
		norm = ""
	}
	if out := t.r.CodebaseMapForDir(norm); out != "" {
		return out, nil
	}

	// Unknown directory — help the model recover rather than just failing.
	var suggestions []string
	lower := strings.ToLower(norm)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if strings.Contains(strings.ToLower(d), lower) || strings.Contains(lower, strings.ToLower(d)) {
			suggestions = append(suggestions, d)
		}
	}
	sort.Strings(suggestions)
	if len(suggestions) > 15 {
		suggestions = suggestions[:15]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "No mapped source files in %q.\n", dir)
	if len(suggestions) > 0 {
		b.WriteString("\nDid you mean:\n")
		for _, s := range suggestions {
			b.WriteString("- " + s + "\n")
		}
		return b.String(), nil
	}
	b.WriteString("\nMapped directories:\n")
	for i, d := range dirs {
		if i >= 40 {
			fmt.Fprintf(&b, "- … %d more\n", len(dirs)-i)
			break
		}
		label := d
		if label == "" {
			label = "(project root)"
		}
		b.WriteString("- " + label + "\n")
	}
	return b.String(), nil
}
