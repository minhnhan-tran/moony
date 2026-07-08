package tools

import (
	"strings"
	"testing"
)

// #5: a single noisy file's matches past the per-file cap are collapsed into a
// summary line instead of flooding the result.
func TestGrepPerFileCollapse(t *testing.T) {
	var sb strings.Builder
	total := maxGrepMatchesPerFile + 10
	for i := 0; i < total; i++ {
		sb.WriteString("needle here\n")
	}
	g := grepReg(t, map[string]string{"noisy.txt": sb.String()})
	out := runGrep(t, g, map[string]any{"pattern": "needle"})

	shown := strings.Count(out, "noisy.txt:")
	if shown != maxGrepMatchesPerFile {
		t.Errorf("expected %d shown match lines, got %d:\n%s", maxGrepMatchesPerFile, shown, out)
	}
	if !strings.Contains(out, "10 more match(es) in noisy.txt") {
		t.Errorf("expected collapse summary for the remaining matches, got:\n%s", out)
	}
}

// A file with few matches is not collapsed.
func TestGrepNoCollapseUnderCap(t *testing.T) {
	g := grepReg(t, map[string]string{"a.txt": "needle\nneedle\nneedle\n"})
	out := runGrep(t, g, map[string]any{"pattern": "needle"})
	if strings.Contains(out, "more match(es)") {
		t.Errorf("small file should not be collapsed, got:\n%s", out)
	}
	if strings.Count(out, "a.txt:") != 3 {
		t.Errorf("expected 3 matches, got:\n%s", out)
	}
}
