package lsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestUriToPath(t *testing.T) {
	if got := uriToPath("not-a-uri"); got != "not-a-uri" {
		t.Errorf("non-file uri should pass through, got %q", got)
	}
	// Use a real OS-native absolute path (from t.TempDir(), not a hardcoded
	// Unix-style literal) so this round-trips correctly on Windows too, where
	// pathToURI's drive letter ends up in the URI's host component.
	abs := filepath.Join(t.TempDir(), "some dir", "main.go")
	uri := pathToURI(abs)
	back := uriToPath(uri)
	if back != abs {
		t.Errorf("uriToPath(pathToURI(%q)) = %q, want round-trip", abs, back)
	}
}

func TestParseLocationsShapes(t *testing.T) {
	if locs := parseLocations(json.RawMessage(`null`)); locs != nil {
		t.Errorf("null should yield nil, got %v", locs)
	}
	want1 := filepath.FromSlash("/a/b.go")
	single := json.RawMessage(`{"uri":"file:///a/b.go","range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}}}`)
	locs := parseLocations(single)
	if len(locs) != 1 || locs[0].Path != want1 || locs[0].Range.Start.Line != 1 || locs[0].Range.Start.Character != 2 {
		t.Errorf("single location parsed wrong: %+v, want path %q", locs, want1)
	}
	want2 := filepath.FromSlash("/c/d.go")
	arr := json.RawMessage(`[{"uri":"file:///a/b.go","range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}}},{"uri":"file:///c/d.go","range":{"start":{"line":9,"character":0},"end":{"line":9,"character":3}}}]`)
	locs = parseLocations(arr)
	if len(locs) != 2 || locs[1].Path != want2 || locs[1].Range.Start.Line != 9 {
		t.Errorf("array of locations parsed wrong: %+v, want path %q", locs, want2)
	}
}

func TestHoverTextShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain string", `"hello"`, "hello"},
		{"markup content", `{"kind":"markdown","value":"**Foo**"}`, "**Foo**"},
		{"marked string object", `{"language":"go","value":"func Foo()"}`, "func Foo()"},
		{"array of marked strings", `["a","b"]`, "a\n\nb"},
		{"empty", ``, ""},
		{"null", `null`, ""},
	}
	for _, c := range cases {
		var raw json.RawMessage
		if c.raw != "" {
			raw = json.RawMessage(c.raw)
		}
		got := hoverText(raw)
		if got != c.want {
			t.Errorf("%s: hoverText(%s) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

func TestManagerNavOnEmptyManager(t *testing.T) {
	var m *Manager // nil manager, same as an agent with no LSP configured
	if locs, srv, err := m.Definition(context.Background(), "x.go", 1, 1); locs != nil || srv != "" || err != nil {
		t.Errorf("nil manager Definition should be a silent no-op, got %v %q %v", locs, srv, err)
	}
	m2 := &Manager{}
	if locs, srv, err := m2.References(context.Background(), "x.go", 1, 1, true); locs != nil || srv != "" || err != nil {
		t.Errorf("empty manager References should be a silent no-op, got %v %q %v", locs, srv, err)
	}
	if text, srv, err := m2.Hover(context.Background(), "x.go", 1, 1); text != "" || srv != "" || err != nil {
		t.Errorf("empty manager Hover should be a silent no-op, got %q %q %v", text, srv, err)
	}
}

func TestHoverTextIgnoresUnrelatedFields(t *testing.T) {
	// A MarkupContent-shaped object with an empty value must not be mistaken
	// for a match; falls through to "".
	got := hoverText(json.RawMessage(`{"kind":"plaintext","value":""}`))
	if got != "" {
		t.Errorf("empty value should yield empty string, got %q", got)
	}
	// Sanity: nested arrays of mixed shapes still join correctly.
	got = hoverText(json.RawMessage(`[{"value":"sig"},"doc"]`))
	if !strings.Contains(got, "sig") || !strings.Contains(got, "doc") {
		t.Errorf("mixed array hover text = %q, want both parts", got)
	}
}
