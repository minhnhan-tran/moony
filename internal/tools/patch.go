package tools

import (
	"fmt"
	"strings"
)

// This file implements a Codex-style `apply_patch` envelope: a single textual
// patch that can add, update, and delete several files atomically. The format
// is line-oriented and self-describing so a model can emit it reliably:
//
//	*** Begin Patch
//	*** Add File: path/to/new.txt
//	+first line
//	+second line
//	*** Update File: path/to/existing.go
//	@@
//	 unchanged context
//	-old line
//	+new line
//	*** Delete File: path/to/old.txt
//	*** End Patch
//
// Update hunks use exact-context matching rather than line numbers: each hunk
// (introduced by a "@@" marker, or the implicit first one) is the concatenation
// of its context lines (leading space), removals ("-"), and additions ("+").
// The "old" side (context + removals) must appear exactly once in the current
// file, and is replaced by the "new" side (context + additions). This mirrors
// the exact-match semantics of edit_file/multi_edit, so patches don't silently
// apply to the wrong place.

// patchOp is one file operation within a patch.
type patchOp struct {
	kind  patchKind
	path  string
	hunks []patchHunk // update only
	body  string      // add only: full new content
}

type patchKind int

const (
	patchAdd patchKind = iota
	patchUpdate
	patchDelete
)

// patchHunk is one contiguous edit within an Update File section.
type patchHunk struct {
	oldText string // context + removed lines (what must match)
	newText string // context + added lines (the replacement)
}

const (
	patchBegin    = "*** Begin Patch"
	patchEnd      = "*** End Patch"
	patchAddFile  = "*** Add File: "
	patchUpdate2  = "*** Update File: "
	patchDelete2  = "*** Delete File: "
	patchHunkMark = "@@"
)

// parsePatch parses an apply_patch envelope into an ordered list of operations.
func parsePatch(patch string) ([]patchOp, error) {
	// Normalize line endings; tolerate a trailing newline.
	patch = strings.ReplaceAll(patch, "\r\n", "\n")
	lines := strings.Split(patch, "\n")

	// Find the Begin/End markers, ignoring blank lines around them.
	start, end := -1, -1
	for i, ln := range lines {
		switch strings.TrimRight(ln, " \t") {
		case patchBegin:
			start = i
		case patchEnd:
			end = i
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("patch must start with %q", patchBegin)
	}
	if end < 0 {
		return nil, fmt.Errorf("patch must end with %q", patchEnd)
	}
	if end < start {
		return nil, fmt.Errorf("%q appears before %q", patchEnd, patchBegin)
	}
	body := lines[start+1 : end]

	var ops []patchOp
	i := 0
	for i < len(body) {
		ln := body[i]
		switch {
		case strings.HasPrefix(ln, patchAddFile):
			path := strings.TrimSpace(strings.TrimPrefix(ln, patchAddFile))
			if path == "" {
				return nil, fmt.Errorf("Add File: missing path")
			}
			i++
			var content []string
			for i < len(body) && !isPatchHeader(body[i]) {
				l := body[i]
				if !strings.HasPrefix(l, "+") {
					return nil, fmt.Errorf("Add File %s: every line must start with '+', got %q", path, l)
				}
				content = append(content, strings.TrimPrefix(l, "+"))
				i++
			}
			ops = append(ops, patchOp{kind: patchAdd, path: path, body: strings.Join(content, "\n")})

		case strings.HasPrefix(ln, patchDelete2):
			path := strings.TrimSpace(strings.TrimPrefix(ln, patchDelete2))
			if path == "" {
				return nil, fmt.Errorf("Delete File: missing path")
			}
			ops = append(ops, patchOp{kind: patchDelete, path: path})
			i++

		case strings.HasPrefix(ln, patchUpdate2):
			path := strings.TrimSpace(strings.TrimPrefix(ln, patchUpdate2))
			if path == "" {
				return nil, fmt.Errorf("Update File: missing path")
			}
			i++
			hunks, next, err := parseHunks(path, body, i)
			if err != nil {
				return nil, err
			}
			if len(hunks) == 0 {
				return nil, fmt.Errorf("Update File %s: no changes", path)
			}
			ops = append(ops, patchOp{kind: patchUpdate, path: path, hunks: hunks})
			i = next

		case strings.TrimSpace(ln) == "":
			i++ // tolerate blank lines between sections

		default:
			return nil, fmt.Errorf("unexpected line in patch: %q", ln)
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("patch contains no file operations")
	}
	return ops, nil
}

// isPatchHeader reports whether a line begins a new file section.
func isPatchHeader(ln string) bool {
	return strings.HasPrefix(ln, patchAddFile) ||
		strings.HasPrefix(ln, patchUpdate2) ||
		strings.HasPrefix(ln, patchDelete2)
}

// parseHunks reads the hunks of an Update File section starting at body[i],
// returning the hunks and the index of the next unconsumed line.
func parseHunks(path string, body []string, i int) ([]patchHunk, int, error) {
	var hunks []patchHunk
	var oldB, newB strings.Builder
	open := false

	flush := func() error {
		if !open {
			return nil
		}
		o := strings.TrimSuffix(oldB.String(), "\n")
		n := strings.TrimSuffix(newB.String(), "\n")
		if o == "" && n == "" {
			return fmt.Errorf("Update File %s: empty hunk", path)
		}
		hunks = append(hunks, patchHunk{oldText: o, newText: n})
		oldB.Reset()
		newB.Reset()
		open = false
		return nil
	}

	for i < len(body) {
		ln := body[i]
		if isPatchHeader(ln) {
			break
		}
		switch {
		case strings.HasPrefix(ln, patchHunkMark):
			if err := flush(); err != nil {
				return nil, 0, err
			}
			open = true
			i++
		case strings.HasPrefix(ln, "+"):
			open = true
			newB.WriteString(strings.TrimPrefix(ln, "+"))
			newB.WriteByte('\n')
			i++
		case strings.HasPrefix(ln, "-"):
			open = true
			oldB.WriteString(strings.TrimPrefix(ln, "-"))
			oldB.WriteByte('\n')
			i++
		case strings.HasPrefix(ln, " "):
			open = true
			ctx := strings.TrimPrefix(ln, " ")
			oldB.WriteString(ctx)
			oldB.WriteByte('\n')
			newB.WriteString(ctx)
			newB.WriteByte('\n')
			i++
		case strings.TrimSpace(ln) == "":
			// A blank line inside a hunk is treated as a shared (context) empty
			// line so surrounding context stays aligned.
			open = true
			oldB.WriteByte('\n')
			newB.WriteByte('\n')
			i++
		default:
			return nil, 0, fmt.Errorf("Update File %s: each line must start with ' ', '+', '-', or '@@', got %q", path, ln)
		}
	}
	if err := flush(); err != nil {
		return nil, 0, err
	}
	return hunks, i, nil
}

// applyUpdate applies a sequence of update hunks to content, in order, each
// against the result of the previous one. Each hunk's old side must match
// exactly once. Returns the new content.
func applyUpdate(path, content string, hunks []patchHunk) (string, error) {
	cur := content
	for k, h := range hunks {
		if h.oldText == "" {
			// Pure insertion with no context is ambiguous; require some anchor.
			return "", fmt.Errorf("update %s: hunk %d has no context or removed lines to locate the change", path, k+1)
		}
		count := strings.Count(cur, h.oldText)
		if count == 0 {
			return "", fmt.Errorf("update %s: hunk %d context not found", path, k+1)
		}
		if count > 1 {
			return "", fmt.Errorf("update %s: hunk %d context matches %d times; add more context lines", path, k+1, count)
		}
		cur = strings.Replace(cur, h.oldText, h.newText, 1)
	}
	return cur, nil
}
