package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// numberedGoSource builds a read_file-style numbered Go file: header, a function
// with a long body, a type decl, then trailing filler — large enough to outline.
func numberedGoSource() string {
	var b strings.Builder
	n := 0
	emit := func(s string) { n++; fmt.Fprintf(&b, "%d: %s\n", n, s) }
	emit("package foo")
	emit("")
	emit("import \"fmt\"")
	for i := 0; i < 5; i++ {
		emit("// padding")
	}
	emit("func Alpha(x int) int {")
	for i := 0; i < 40; i++ {
		emit(fmt.Sprintf("\tx += %d // body", i))
	}
	emit("}")
	emit("type Bar struct {")
	emit("\tName string")
	emit("}")
	for i := 0; i < 20; i++ {
		emit("\t// trailing filler")
	}
	return strings.TrimRight(b.String(), "\n")
}

func TestOutlineReadFile(t *testing.T) {
	src := numberedGoSource()
	out, ok := outlineReadFile("foo.go", src)
	if !ok {
		t.Fatal("expected outline to apply to a large .go file")
	}
	if len(out) >= len(src) {
		t.Fatalf("outline (%d) should be smaller than source (%d)", len(out), len(src))
	}
	if !strings.Contains(out, "func Alpha(x int) int {") {
		t.Error("outline should keep the function signature")
	}
	if !strings.Contains(out, "type Bar struct {") {
		t.Error("outline should keep the type declaration")
	}
	if !strings.Contains(out, "elided") {
		t.Error("outline should mark elided bodies")
	}
	if strings.Contains(out, "x += 30 // body") {
		t.Error("outline should drop deep body lines")
	}

	// Non-code extension is left to the generic cap.
	if _, ok := outlineReadFile("notes.txt", src); ok {
		t.Error("outline should not apply to .txt")
	}
}

// TestOutlineElisionNoZeroBound guards against the elision marker printing a 0
// line number when a boundary line lacks the "<n>: " prefix.
func TestOutlineElisionNoZeroBound(t *testing.T) {
	out, ok := outlineReadFile("foo.go", numberedGoSource())
	if !ok {
		t.Fatal("expected outline")
	}
	if strings.Contains(out, "–0)") || strings.Contains(out, ": 0–") {
		t.Errorf("elision marker should not contain a 0 line number:\n%s", out)
	}
}

// TestCapToolOutput covers the read_file→outline wiring: oversized source output
// is outlined when enabled, and falls back to the generic byte cap when the flag
// is off or the tool isn't read_file.
func TestCapToolOutput(t *testing.T) {
	src := numberedGoSource()
	outline, ok := outlineReadFile("foo.go", src)
	if !ok {
		t.Fatal("expected the fixture to outline")
	}
	max := len(src) - 1 // forces capToolOutput to engage; the smaller outline still fits
	in := json.RawMessage(`{"path":"foo.go"}`)
	a := &Agent{cfg: &config.Config{CodeOutline: true, MaxToolOutputBytes: max}}

	// read_file on a source file → code outline.
	got := a.capToolOutput("read_file", in, src, max)
	if got != outline {
		t.Error("read_file source over budget should be outlined")
	}
	if !strings.Contains(got, "elided") {
		t.Errorf("outlined output should mark elided bodies, got:\n%s", got)
	}

	// Disabled → generic byte cap, no outline.
	a.cfg.CodeOutline = false
	if got := a.capToolOutput("read_file", in, src, max); strings.Contains(got, "outline") {
		t.Error("CodeOutline=false should not outline")
	}

	// Non-read_file tool → generic cap even when enabled.
	a.cfg.CodeOutline = true
	if got := a.capToolOutput("bash", json.RawMessage(`{}`), src, max); strings.Contains(got, "outline") {
		t.Error("non-read_file output should not be outlined")
	}
}
