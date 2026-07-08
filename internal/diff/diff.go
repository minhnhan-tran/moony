// Package diff produces compact, human-readable unified diffs for previewing
// file changes before they are applied.
package diff

import (
	"fmt"
	"strings"
)

// maxLines bounds the LCS computation. Beyond this we fall back to a coarse
// summary to avoid quadratic blowups on very large files.
const maxLines = 1500

// context is how many unchanged lines to show around each change.
const context = 3

// Stats summarizes the magnitude of a change.
type Stats struct {
	Added   int
	Removed int
}

// String renders stats like "+12 -3".
func (s Stats) String() string { return fmt.Sprintf("+%d -%d", s.Added, s.Removed) }

// op is a single line operation in the diff.
type opKind int

const (
	opEqual opKind = iota
	opDel
	opAdd
)

type op struct {
	kind opKind
	text string
}

// Unified compares oldText and newText line-by-line and returns a unified-style
// diff (without @@ line-number headers) plus change statistics. Long runs of
// unchanged lines are collapsed with an ellipsis marker.
func Unified(oldText, newText string) (string, Stats) {
	if oldText == newText {
		return "", Stats{}
	}
	a := splitLines(oldText)
	b := splitLines(newText)

	if len(a) > maxLines || len(b) > maxLines {
		return coarse(a, b), Stats{Added: len(b), Removed: len(a)}
	}

	ops := lcsDiff(a, b)
	var stats Stats
	for _, o := range ops {
		switch o.kind {
		case opAdd:
			stats.Added++
		case opDel:
			stats.Removed++
		}
	}
	return collapse(ops), stats
}

// splitLines splits text into lines, dropping a single trailing newline's empty
// element so files that end in "\n" don't show a spurious blank line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// lcsDiff computes a line diff via the classic LCS dynamic-programming table.
func lcsDiff(a, b []string) []op {
	m, n := len(a), len(b)
	// dp[i][j] = LCS length of a[i:] and b[j:].
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []op
	i, j := 0, 0
	for i < m && j < n {
		if a[i] == b[j] {
			ops = append(ops, op{opEqual, a[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, op{opDel, a[i]})
			i++
		} else {
			ops = append(ops, op{opAdd, b[j]})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, op{opDel, a[i]})
	}
	for ; j < n; j++ {
		ops = append(ops, op{opAdd, b[j]})
	}
	return ops
}

// collapse renders ops, showing changed lines with +/- and keeping only a few
// lines of surrounding context. Hidden runs are replaced by an ellipsis.
func collapse(ops []op) string {
	// Mark which indices are "near" a change (within context lines).
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind != opEqual {
			lo := i - context
			if lo < 0 {
				lo = 0
			}
			hi := i + context
			if hi >= len(ops) {
				hi = len(ops) - 1
			}
			for k := lo; k <= hi; k++ {
				keep[k] = true
			}
		}
	}

	var b strings.Builder
	hiddenRun := false
	for i, o := range ops {
		if !keep[i] {
			if !hiddenRun {
				b.WriteString(" ⋮\n")
				hiddenRun = true
			}
			continue
		}
		hiddenRun = false
		switch o.kind {
		case opEqual:
			b.WriteString("  " + o.text + "\n")
		case opDel:
			b.WriteString("- " + o.text + "\n")
		case opAdd:
			b.WriteString("+ " + o.text + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// coarse renders a minimal diff for files too large to diff line-by-line.
func coarse(a, b []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "(large file: replacing %d lines with %d lines)\n", len(a), len(b))
	head := b
	if len(head) > 12 {
		head = head[:12]
	}
	for _, l := range head {
		sb.WriteString("+ " + l + "\n")
	}
	if len(b) > 12 {
		fmt.Fprintf(&sb, " ⋮ (+%d more lines)\n", len(b)-12)
	}
	return strings.TrimRight(sb.String(), "\n")
}
