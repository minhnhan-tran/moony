package config

import (
	"os"
	"path/filepath"
	"strings"
)

// CODEBASE_MAP.md is a per-project, committable codebase map the agent reads
// (instead of re-exploring the tree every turn). It has three clearly delimited
// sections between HTML-comment markers:
//
//   <!-- BEGIN PINNED --> … <!-- END PINNED -->
//       Owned by the human. Durable, hand-authored knowledge that no generator
//       may rewrite: architecture decisions, gotchas, invariants, conventions.
//       PRESERVED across BOTH structural refreshes and @map narrative rewrites.
//
//   <!-- BEGIN AUTO:STRUCTURE --> … <!-- END AUTO:STRUCTURE -->
//       Owned by the generator; regenerated (overwritten) whenever the codebase
//       changes. Pure-Go structural view — never hand-edit, it will be replaced.
//
//   <!-- BEGIN NARRATIVE --> … <!-- END NARRATIVE -->
//       Owned by the model / user; a feature → files narrative (re)written on
//       demand via @map. PRESERVED across structural refreshes.
//
// The helpers below let each owner rewrite only its own section without ever
// clobbering the others. Anything placed OUTSIDE these markers is dropped on the
// next write, since the file is reassembled from its sections.

const (
	// CodebaseMapFile is the map's filename at the project working-dir root.
	CodebaseMapFile = "CODEBASE_MAP.md"

	cmStructStart = "<!-- BEGIN AUTO:STRUCTURE -->"
	cmStructEnd   = "<!-- END AUTO:STRUCTURE -->"
	cmNarrStart   = "<!-- BEGIN NARRATIVE -->"
	cmNarrEnd     = "<!-- END NARRATIVE -->"
	cmPinStart    = "<!-- BEGIN PINNED -->"
	cmPinEnd      = "<!-- END PINNED -->"

	cmHeader = "# Codebase map\n\n" +
		"> Maintained map of this project. **Pinned notes** are hand-authored and never overwritten; the **structure** section is generated from the code and auto-refreshed on change; the **narrative** section (features → files) is written on demand via `@map`. The agent reads this before exploring.\n>\n" +
		"> Only text INSIDE the `<!-- BEGIN … -->` / `<!-- END … -->` markers is preserved — content added outside them is dropped on the next refresh.\n\n"
)

// CodebaseMapPath returns the map's path for a project working directory.
func CodebaseMapPath(workingDir string) string {
	return filepath.Join(workingDir, CodebaseMapFile)
}

// ReadCodebaseMap returns the full file contents ("" if it does not exist).
func ReadCodebaseMap(workingDir string) (string, bool) {
	b, err := os.ReadFile(CodebaseMapPath(workingDir))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// section extracts the text between start and end markers (exclusive), trimmed.
func section(content, start, end string) string {
	i := strings.Index(content, start)
	if i < 0 {
		return ""
	}
	i += len(start)
	j := strings.Index(content[i:], end)
	if j < 0 {
		return strings.TrimSpace(content[i:])
	}
	return strings.TrimSpace(content[i : i+j])
}

// CodebaseMapStructure / CodebaseMapNarrative return the current contents of each
// section (empty when the file or section is absent).
func CodebaseMapStructure(workingDir string) string {
	c, ok := ReadCodebaseMap(workingDir)
	if !ok {
		return ""
	}
	return section(c, cmStructStart, cmStructEnd)
}

func CodebaseMapNarrative(workingDir string) string {
	c, ok := ReadCodebaseMap(workingDir)
	if !ok {
		return ""
	}
	return section(c, cmNarrStart, cmNarrEnd)
}

// CodebaseMapPinned returns the hand-authored pinned section ("" when absent).
func CodebaseMapPinned(workingDir string) string {
	c, ok := ReadCodebaseMap(workingDir)
	if !ok {
		return ""
	}
	return section(c, cmPinStart, cmPinEnd)
}

// assembleCodebaseMap rebuilds the whole file from its two sections + header.
func assembleCodebaseMap(structure, narrative, pinned string) string {
	var b strings.Builder
	b.WriteString(cmHeader)
	// Pinned first: it is the human-authored, highest-authority material.
	b.WriteString("## Pinned notes\n\n")
	b.WriteString(cmPinStart)
	b.WriteByte('\n')
	if p := strings.TrimSpace(pinned); p != "" {
		b.WriteString(p)
		b.WriteByte('\n')
	} else {
		b.WriteString("_Hand-authored notes that no generator may overwrite: architecture decisions, gotchas, invariants, conventions. Edit this section directly._\n")
	}
	b.WriteString(cmPinEnd)
	b.WriteString("\n\n")
	b.WriteString("## Structure\n\n")
	b.WriteString(cmStructStart)
	b.WriteByte('\n')
	if s := strings.TrimSpace(structure); s != "" {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	b.WriteString(cmStructEnd)
	b.WriteString("\n\n## Narrative — features → files\n\n")
	b.WriteString(cmNarrStart)
	b.WriteByte('\n')
	if n := strings.TrimSpace(narrative); n != "" {
		b.WriteString(n)
		b.WriteByte('\n')
	} else {
		b.WriteString("_Not written yet. Use `@map` to have the agent describe each feature and the files that implement it._\n")
	}
	b.WriteString(cmNarrEnd)
	b.WriteByte('\n')
	return b.String()
}

// WriteCodebaseMapStructure replaces ONLY the structural section, preserving any
// existing narrative and pinned notes. Creates the file if missing. A no-op write (identical
// content) is skipped so it does not churn mtimes / git status.
func WriteCodebaseMapStructure(workingDir, structure string) error {
	return writeCodebaseMap(workingDir, structure, CodebaseMapNarrative(workingDir), CodebaseMapPinned(workingDir))
}

// WriteCodebaseMapNarrative replaces ONLY the narrative section, preserving the
// current structural section and pinned notes. Creates the file if missing.
func WriteCodebaseMapNarrative(workingDir, narrative string) error {
	return writeCodebaseMap(workingDir, CodebaseMapStructure(workingDir), narrative, CodebaseMapPinned(workingDir))
}

// WriteCodebaseMapPinned replaces ONLY the pinned section, preserving structure
// and narrative. Creates the file if missing.
func WriteCodebaseMapPinned(workingDir, pinned string) error {
	return writeCodebaseMap(workingDir, CodebaseMapStructure(workingDir), CodebaseMapNarrative(workingDir), pinned)
}

func writeCodebaseMap(workingDir, structure, narrative, pinned string) error {
	next := assembleCodebaseMap(structure, narrative, pinned)
	p := CodebaseMapPath(workingDir)
	if cur, err := os.ReadFile(p); err == nil && string(cur) == next {
		return nil // unchanged — avoid needless writes
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(next), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
