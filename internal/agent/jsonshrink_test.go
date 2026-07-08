package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCompactJSONLossless(t *testing.T) {
	pretty := "{\n  \"a\": 1,\n  \"b\": [1, 2, 3]\n}"
	got, ok := compactJSON(pretty)
	if !ok {
		t.Fatal("compactJSON should accept valid JSON")
	}
	if got != `{"a":1,"b":[1,2,3]}` {
		t.Errorf("compactJSON = %q", got)
	}
	// Non-JSON is rejected.
	if _, ok := compactJSON("just some log output\n"); ok {
		t.Error("compactJSON should reject non-JSON")
	}
}

// pruneEmpty drops null/empty fields but must keep numeric zero and boolean
// false (they carry signal), and must not corrupt large integers.
func TestPruneJSON(t *testing.T) {
	in := `{"keep":1,"zero":0,"flag":false,"empty":"","nul":null,"arr":[],"obj":{},"big":10000000000}`
	got, ok := pruneJSON(in)
	if !ok {
		t.Fatal("pruneJSON should accept valid JSON")
	}
	// json.Marshal sorts object keys, so this is deterministic.
	want := `{"big":10000000000,"flag":false,"keep":1,"zero":0}`
	if got != want {
		t.Errorf("pruneJSON = %q, want %q", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Error("pruned output must stay valid JSON")
	}
}

// pruneJSON must not HTML-escape <, >, & in string values (keeps them readable
// and avoids 1→6 byte inflation), and must reject trailing data after the value.
func TestPruneJSONNoHTMLEscapeAndTrailing(t *testing.T) {
	got, ok := pruneJSON(`{"cmd":"make && test > log","drop":null}`)
	if !ok {
		t.Fatal("pruneJSON should accept valid JSON")
	}
	if !strings.Contains(got, "make && test > log") {
		t.Errorf("pruneJSON must not HTML-escape <>&; got %q", got)
	}
	if strings.Contains(got, `\u0026`) || strings.Contains(got, `\u003e`) || strings.Contains(got, `\u003c`) {
		t.Errorf("unexpected HTML escaping in %q", got)
	}
	if !json.Valid([]byte(got)) {
		t.Error("output must stay valid JSON")
	}
	// Trailing data after the first value is rejected (not silently dropped).
	if _, ok := pruneJSON(`{"a":1}{"b":2}`); ok {
		t.Error("pruneJSON should reject trailing data after the first value")
	}
}

func TestCapOutputUnderBudgetUnchanged(t *testing.T) {
	s := "short output"
	if got := capOutput(s, 100); got != s {
		t.Errorf("under-budget output should be unchanged, got %q", got)
	}
	if got := capOutput(s, 0); got != s {
		t.Errorf("max<=0 should disable capping, got %q", got)
	}
}

// A pretty JSON payload that fits once whitespace is stripped is returned in full
// (lossless) with no truncation marker.
func TestCapOutputJSONLosslessFit(t *testing.T) {
	pretty := "{\n    \"a\": 1,\n    \"b\": 2\n}"
	compact := `{"a":1,"b":2}`
	got := capOutput(pretty, len(compact)) // budget exactly fits the compact form
	if got != compact {
		t.Errorf("expected lossless compaction %q, got %q", compact, got)
	}
	if strings.Contains(got, "truncated") || strings.Contains(got, "omitted") {
		t.Errorf("lossless fit should carry no marker, got %q", got)
	}
}

// When compaction alone doesn't fit but dropping empty fields does, capOutput
// returns the pruned JSON plus an "omitted" note.
func TestCapOutputJSONPruneFit(t *testing.T) {
	in := `{"keep":1,"empty":"","gone":null,"arr":[]}`
	got := capOutput(in, 20) // compact (~42B) > 20 >= pruned ({"keep":1} = 10B)
	if !strings.HasPrefix(got, `{"keep":1}`) {
		t.Errorf("expected pruned JSON prefix, got %q", got)
	}
	if !strings.Contains(got, "empty/null JSON fields omitted") {
		t.Errorf("expected prune marker, got %q", got)
	}
}

// When even the pruned form is over budget, capOutput clips and marks it.
func TestCapOutputJSONClipFallback(t *testing.T) {
	in := `{"keep":1,"empty":"","gone":null,"arr":[]}`
	got := capOutput(in, 5)
	if !strings.Contains(got, "tool output truncated to 5 of") {
		t.Errorf("expected truncation marker, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Error("clipped output must be valid UTF-8")
	}
}

// Plain (non-JSON) output is clipped on a line boundary, never mid-line.
func TestCapOutputClipsAtNewline(t *testing.T) {
	s := "line1\nline2\nline3\nline4\n"
	got := capOutput(s, 9) // "line1\nlin" → backs up to "line1\n"
	body, _, found := strings.Cut(got, "\n... [tool output truncated")
	if !found {
		t.Fatalf("expected a truncation marker, got %q", got)
	}
	if body != "line1\n" {
		t.Errorf("expected clip at the line boundary (%q), got %q", "line1\n", body)
	}
}

// A multi-byte rune straddling the cut (no newline) must not produce invalid UTF-8.
func TestCapOutputUTF8Safe(t *testing.T) {
	s := strings.Repeat("a", 4) + "世界" // 世 starts at byte 4 (3 bytes each)
	got := capOutput(s, 6)             // cut lands inside 世
	if !utf8.ValidString(got) {
		t.Errorf("clipped output must be valid UTF-8, got %q", got)
	}
}

// Over a real byte budget, capOutput keeps BOTH ends and elides the middle, so
// the tail (where command/test output puts the outcome) survives — a plain head
// clip would drop it. The result still fits the budget.
func TestCapOutputHeadTailKeepsBothEnds(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&b, "line-%04d filler filler filler\n", i)
	}
	s := b.String()
	const max = 4096
	got := capOutput(s, max)

	if len(got) > max {
		t.Fatalf("head+tail clip exceeded budget: %d > %d", len(got), max)
	}
	if !strings.Contains(got, "line-0000") {
		t.Error("head lost: first line missing")
	}
	if !strings.Contains(got, "line-3999") {
		t.Error("tail lost: last line missing (a head-only clip would have dropped it)")
	}
	if !strings.Contains(got, "elided from the middle") {
		t.Errorf("missing head+tail elision marker in: %q", got)
	}
}

// Below minHeadTailBytes, capOutput keeps the legacy head-only clip (the tail is
// dropped, marker says "truncated"), so tiny budgets stay cheap.
func TestCapOutputSmallBudgetStaysHeadOnly(t *testing.T) {
	s := "line1\nline2\nline3\nline4\nline5\n"
	got := capOutput(s, 12) // < minHeadTailBytes
	if strings.Contains(got, "elided from the middle") {
		t.Errorf("small budget should use the head-only clip, got %q", got)
	}
	if !strings.Contains(got, "tool output truncated") {
		t.Errorf("expected head-only truncation marker, got %q", got)
	}
}

// clipToTail keeps the last n bytes, snapped forward to a line boundary so it
// never begins mid-line, and stays UTF-8-safe on a no-newline cut.
func TestClipToTail(t *testing.T) {
	if got := clipToTail("line1\nline2\nline3\n", 8); got != "line3\n" {
		t.Errorf("clipToTail line-snap = %q, want %q", got, "line3\n")
	}
	if got := clipToTail("whatever", 0); got != "" {
		t.Errorf("clipToTail(_, 0) = %q, want empty", got)
	}
	if got := clipToTail("abc", 10); got != "abc" {
		t.Errorf("clipToTail under-budget = %q, want abc", got)
	}
	// "aaaa世界": cut at the last 4 bytes lands inside 世; advance to 界.
	got := clipToTail("aaaa世界", 4)
	if !utf8.ValidString(got) {
		t.Errorf("clipToTail must stay UTF-8-safe, got %q", got)
	}
	if got != "界" {
		t.Errorf("clipToTail utf8 = %q, want 界", got)
	}
}
