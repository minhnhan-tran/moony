package web

// Round-trip tests for the notes checklist parser in app-core.js.
//
// The checklist UI keeps the notes STRING as the source of truth and re-encodes it
// on every edit, so a parse/serialize asymmetry silently rewrites the user's file:
// a lost trailer or a bullet reflowed into a heading is data loss, not a cosmetic
// bug. These run the real functions out of the shipped asset under node, so the
// assertions can't drift from what the browser actually loads.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// notesJSHarness extracts the notes parser/serializer from app-core.js and wraps it
// in a standalone script. The functions are pure string→string, so they need no DOM.
func notesJSHarness(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("static", "app-core.js"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	// Slice out the two regexes plus parseNotesItems/serializeNotesItems.
	start := strings.Index(text, "const NOTES_ITEM_RE")
	if start < 0 {
		t.Fatal("NOTES_ITEM_RE not found in app-core.js — was the notes UI renamed?")
	}
	endMarker := "// renderProjectNotesInto"
	end := strings.Index(text[start:], endMarker)
	if end < 0 {
		t.Fatal("could not find the end of the notes helpers in app-core.js")
	}
	return text[start : start+end]
}

// runNotesJS evaluates body with the notes helpers in scope and returns stdout.
func runNotesJS(t *testing.T, body string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping the JS round-trip tests")
	}
	script := notesJSHarness(t) + "\n" + body
	f := filepath.Join(t.TempDir(), "notes_test.mjs")
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// The critical invariant: text already in checklist form must survive a
// parse→serialize cycle byte for byte, since the UI rewrites the file on every edit.
func TestNotesParseSerializeRoundTrip(t *testing.T) {
	cases := []string{
		"- [ ] one\n- [x] two",
		"- [x] done\n- [ ] todo\n- [ ] another",
		"- [ ] single",
		// Prose must keep its position, not be swept to the end of the file.
		"# Heading first\n- [ ] task one",
		"- [ ] a\n## Section\n- [ ] b",
		"- [ ] task\n\n## Notes at end\nprose",
		"> quoted context\n- [ ] after the quote",
	}
	payload, _ := json.Marshal(cases)
	out := runNotesJS(t, `
const cases = `+string(payload)+`;
for (const c of cases) {
  const parsed = parseNotesItems(c);
  const back = serializeNotesItems(parsed.items, parsed.trailer);
  if (back !== c) { console.log("MISMATCH\ninput:  " + JSON.stringify(c) + "\noutput: " + JSON.stringify(back)); process.exit(0); }
}
console.log("OK");`)
	if out != "OK" {
		t.Errorf("checklist text must round-trip unchanged:\n%s", out)
	}
}

// Notes written before this UI existed are plain bullets or bare lines. They must
// upgrade to unchecked items with their text intact — never be dropped.
func TestNotesLegacyPlainTextUpgrades(t *testing.T) {
	out := runNotesJS(t, `
const legacy = "fix the parser\n- add tests\n* ship it";
const { items } = parseNotesItems(legacy);
console.log(JSON.stringify(items));`)

	var items []struct {
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("bad JSON from harness: %v (%s)", err, out)
	}
	want := []string{"fix the parser", "add tests", "ship it"}
	if len(items) != len(want) {
		t.Fatalf("want %d items, got %d: %s", len(want), len(items), out)
	}
	for i, w := range want {
		if items[i].Text != w {
			t.Errorf("item %d: want %q, got %q", i, w, items[i].Text)
		}
		if items[i].Done {
			t.Errorf("legacy item %d should start unchecked", i)
		}
	}
}

// Prose that isn't a checklist item (headings, indented continuations) must be
// preserved verbatim in the trailer instead of being reshaped into items.
func TestNotesNonItemProseIsPreserved(t *testing.T) {
	out := runNotesJS(t, `
const src = "- [ ] task one\n\n## Context\n  indented detail\nmore prose";
const parsed = parseNotesItems(src);
const back = serializeNotesItems(parsed.items, parsed.trailer);
console.log(JSON.stringify({ trailer: parsed.trailer, back: back }));`)

	var got struct {
		Trailer string `json:"trailer"`
		Back    string `json:"back"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad JSON from harness: %v (%s)", err, out)
	}
	for _, want := range []string{"## Context", "indented detail", "more prose"} {
		if !strings.Contains(got.Trailer, want) {
			t.Errorf("trailer should preserve %q, got %q", want, got.Trailer)
		}
		if !strings.Contains(got.Back, want) {
			t.Errorf("round-trip lost %q:\n%s", want, got.Back)
		}
	}
	if !strings.Contains(got.Back, "- [ ] task one") {
		t.Errorf("the checklist item should survive too:\n%s", got.Back)
	}
}

// Toggling an item must change only that item's checkbox.
func TestNotesToggleOnlyAffectsOneItem(t *testing.T) {
	out := runNotesJS(t, `
const parsed = parseNotesItems("- [ ] a\n- [ ] b\n- [ ] c");
parsed.items[1].done = true;
console.log(serializeNotesItems(parsed.items, parsed.trailer));`)
	want := "- [ ] a\n- [x] b\n- [ ] c"
	if out != want {
		t.Errorf("want:\n%s\ngot:\n%s", want, out)
	}
}

// Empty input must not produce a stray bullet, which would resurrect notes the
// user just cleared (and re-badge the project as having a backlog).
func TestNotesEmptyStaysEmpty(t *testing.T) {
	out := runNotesJS(t, `
const parsed = parseNotesItems("");
console.log(JSON.stringify({ n: parsed.items.length, s: serializeNotesItems(parsed.items, parsed.trailer) }));`)
	if !strings.Contains(out, `"n":0`) || !strings.Contains(out, `"s":""`) {
		t.Errorf("empty notes should stay empty, got %s", out)
	}
}

// The UI posts to the endpoint the server actually registers; a rename on either
// side would break saving silently in the browser.
func TestNotesUIPostsToRegisteredRoute(t *testing.T) {
	js, err := os.ReadFile(filepath.Join("static", "app-core.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`/api/projects/\$\{[^}]+\}/notes`).Match(js) {
		t.Error("app-core.js should POST to /api/projects/{key}/notes")
	}
	srv, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(srv), `"POST /api/projects/{key}/notes"`) {
		t.Error("the notes route should be registered in server.go")
	}
}

// Regression: an earlier version of the parser collected ALL prose into a single
// trailer, so a heading written above a task silently jumped below it and a
// mid-list section header moved to the bottom. Interleaved prose must stay put.
func TestNotesProseKeepsItsPosition(t *testing.T) {
	out := runNotesJS(t, `
const src = "# Top\n- [ ] a\n## Middle\n- [ ] b";
const p = parseNotesItems(src);
console.log(serializeNotesItems(p.items, p.trailer));`)
	const want = "# Top\n- [ ] a\n## Middle\n- [ ] b"
	if out != want {
		t.Errorf("prose moved:\n want %q\n got  %q", want, out)
	}
}

// Toggling an item must not disturb surrounding prose either.
func TestNotesToggleKeepsProseInPlace(t *testing.T) {
	out := runNotesJS(t, `
const p = parseNotesItems("## Section\n- [ ] a\n- [ ] b");
p.items[0].done = true;
console.log(serializeNotesItems(p.items, p.trailer));`)
	const want = "## Section\n- [x] a\n- [ ] b"
	if out != want {
		t.Errorf("want %q, got %q", want, out)
	}
}
