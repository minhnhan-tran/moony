package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// truncate keeps a head and a tail with a marker, and both cuts land on line
// boundaries so the model never sees a half-line.
func TestTruncateLineAligned(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("line-")
		b.WriteByte(byte('a' + i%26))
		b.WriteByte('\n')
	}
	s := b.String()
	got := truncate(s, 120)
	if len(got) >= len(s) {
		t.Fatalf("expected truncation, got len %d >= %d", len(got), len(s))
	}
	if !strings.Contains(got, "[truncated") {
		t.Errorf("expected a truncation marker, got:\n%s", got)
	}
	head, rest, ok := strings.Cut(got, "\n... [truncated")
	if !ok {
		t.Fatalf("marker not found in:\n%s", got)
	}
	// Head ends on a line boundary (the original used \n line endings).
	if head != "" && !strings.HasSuffix(head, "\n") {
		t.Errorf("head should end at a line boundary, got tail %q", lastN(head, 12))
	}
	// Tail (after the marker line) begins at a line boundary: every line is intact.
	_, tail, ok := strings.Cut(rest, "] ...\n")
	if !ok {
		t.Fatalf("marker tail not found in:\n%s", got)
	}
	for _, ln := range strings.Split(strings.TrimRight(tail, "\n"), "\n") {
		if ln != "" && !strings.HasPrefix(ln, "line-") {
			t.Errorf("tail contains a broken line: %q", ln)
		}
	}
}

// A single over-long line with no newlines and a multi-byte rune across each cut
// must still yield valid UTF-8 on both head and tail.
func TestTruncateUTF8SafeNoNewline(t *testing.T) {
	s := strings.Repeat("世", 2000) // 3 bytes each, no newlines
	got := truncate(s, 301)
	if !utf8.ValidString(got) {
		t.Errorf("truncated output must be valid UTF-8")
	}
	if !strings.Contains(got, "[truncated") {
		t.Errorf("expected a truncation marker")
	}
}

// Under-budget and disabled cases pass through unchanged.
func TestTruncatePassthrough(t *testing.T) {
	if got := truncate("small", 100); got != "small" {
		t.Errorf("under-budget should be unchanged, got %q", got)
	}
	if got := truncate("anything", 0); got != "anything" {
		t.Errorf("max<=0 should disable truncation, got %q", got)
	}
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// stripRedundantLeadingCD drops a redundant "cd <workdir>" the model prepends
// (which breaks when the working dir has a space and is unquoted), but leaves a
// cd to any other directory alone.
func TestStripRedundantLeadingCD(t *testing.T) {
	const wd = "/work space/proj"
	cases := []struct{ in, want string }{
		// The reported breakage: unquoted path with a space.
		{"cd /work space/proj && git log --oneline", "git log --oneline"},
		{`cd "/work space/proj" && git log`, "git log"}, // double-quoted
		{"cd '/work space/proj' && ls", "ls"},           // single-quoted
		{"cd /work space/proj/ && ls -a", "ls -a"},      // trailing slash
		{"cd /work space/proj; pwd", "pwd"},             // ; separator
		{"  cd /work space/proj && pwd", "pwd"},         // leading whitespace
		{"cd /work space/proj", "true"},                 // bare cd → no-op
		{"cd /other/dir && ls", "cd /other/dir && ls"},  // different dir: untouched
		{"cd subdir && ls", "cd subdir && ls"},          // relative subdir: untouched
		{"git log --oneline", "git log --oneline"},      // no cd: untouched
		{"cdxyz foo", "cdxyz foo"},                      // not a cd command
	}
	for _, c := range cases {
		if got := stripRedundantLeadingCD(c.in, wd); got != c.want {
			t.Errorf("stripRedundantLeadingCD(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// An empty working dir disables the rewrite entirely.
	if got := stripRedundantLeadingCD("cd /x && ls", ""); got != "cd /x && ls" {
		t.Errorf("empty workdir should pass through, got %q", got)
	}
}

// TestBashWorksInDirWithSpace runs the exact failing pattern (an unquoted
// "cd <dir with space> && cmd") end to end and confirms it now succeeds, with
// the command running in the working directory.
func TestBashWorksInDirWithSpace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Source Code", "moony")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(dir)
	bash := &bashTool{r: reg, maxOutputBytes: 1 << 20}

	// The model's unquoted "cd <workdir> && ls" used to fail on the space.
	out, err := bash.Execute(context.Background(), json.RawMessage(`{"command":"cd `+dir+` && ls"}`))
	if err != nil {
		t.Fatalf("bash in a space dir errored: %v", err)
	}
	if strings.Contains(out, "No such file or directory") || strings.Contains(out, "exit error") {
		t.Fatalf("command broke on the space: %q", out)
	}
	if !strings.Contains(out, "marker.txt") {
		t.Errorf("expected the command to run in the working dir (see marker.txt), got: %q", out)
	}
}
