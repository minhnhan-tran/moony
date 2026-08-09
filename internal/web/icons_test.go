package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// <app-icon> resolves its name through SF_TO_MATERIAL (app-state.js) and falls back to a
// generic "help" glyph when the name is unmapped — silently, with no console error. So an
// icon name that was never added to the map ships as a question mark in the live UI and
// nothing complains: that is exactly how #jobsBtn's "bolt.horizontal.circle" survived.
//
// TestQuickActionIconsAreMapped guards the quick-action catalog. These guard the other two
// places icon names are written by hand — markup in index.html, and the icon: fields of
// the panel/palette tables in the JS — so an unmapped name fails the build instead.
var (
	appIconNameRe = regexp.MustCompile(`<app-icon\b[^>]*\bname="([^"]*)"`)
	iconFieldRe   = regexp.MustCompile(`\bicon:\s*"([^"]*)"`)
)

func TestMarkupIconNamesAreMapped(t *testing.T) {
	mapped := mappedIconNames(t)

	html := readAsset(t, "static/index.html")
	found := appIconNameRe.FindAllStringSubmatch(html, -1)
	if len(found) < 20 {
		t.Fatalf("only %d <app-icon> names parsed from index.html — parser is out of date", len(found))
	}
	for _, m := range found {
		name := m[1]
		if strings.Contains(name, "${") { // interpolated at render time; not checkable here
			continue
		}
		if !mapped[name] {
			t.Errorf("index.html renders <app-icon name=%q>, which is not in SF_TO_MATERIAL — it shows the fallback \"help\" glyph", name)
		}
	}
}

func TestPanelIconNamesAreMapped(t *testing.T) {
	mapped := mappedIconNames(t)

	entries, err := filepath.Glob(filepath.Join("static", "*.js"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("glob static/*.js: %v (%d files)", err, len(entries))
	}

	total := 0
	for _, path := range entries {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(b)
		// SF_TO_MATERIAL is a name table, not a consumer of one — don't scan it.
		src = sfMapRe.ReplaceAllString(src, "")
		for _, m := range iconFieldRe.FindAllStringSubmatch(src, -1) {
			name := m[1]
			if name == "" || strings.Contains(name, "${") {
				continue
			}
			total++
			if !mapped[name] {
				t.Errorf("%s declares icon: %q, which is not in SF_TO_MATERIAL — it shows the fallback \"help\" glyph", filepath.Base(path), name)
			}
		}
	}
	if total < 20 {
		t.Fatalf("only %d icon: fields parsed from static/*.js — parser is out of date", total)
	}
}

func mappedIconNames(t *testing.T) map[string]bool {
	t.Helper()
	mapped := sfToMaterialKeys(t, readAsset(t, "static/app-state.js"))
	if len(mapped) < 50 {
		t.Fatalf("only %d mapped icon names parsed from SF_TO_MATERIAL — parser is out of date", len(mapped))
	}
	return mapped
}

// Every Material name SF_TO_MATERIAL points at must have a codepoint in MATERIAL_CP: the
// bundled font is subset with its ligature tables stripped, so <app-icon> renders by
// codepoint and a name missing from MATERIAL_CP draws nothing at all.
func TestMappedIconsHaveCodepoints(t *testing.T) {
	state := readAsset(t, "static/app-state.js")

	m := sfMapRe.FindStringSubmatch(state)
	if m == nil {
		t.Fatal("SF_TO_MATERIAL not found in app-state.js")
	}
	cp := materialCodepoints(t, state)

	vals := regexp.MustCompile(`"[^"]+"\s*:\s*"([^"]+)"`).FindAllStringSubmatch(m[1], -1)
	if len(vals) < 50 {
		t.Fatalf("only %d SF_TO_MATERIAL values parsed — parser is out of date", len(vals))
	}
	for _, v := range vals {
		if !cp[v[1]] {
			t.Errorf("SF_TO_MATERIAL maps to Material icon %q, which has no MATERIAL_CP codepoint — it renders as a blank box", v[1])
		}
	}
}

var materialCPRe = regexp.MustCompile(`(?s)const MATERIAL_CP = \{(.*?)\};`)

func materialCodepoints(t *testing.T, src string) map[string]bool {
	t.Helper()
	m := materialCPRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("MATERIAL_CP not found in app-state.js")
	}
	out := map[string]bool{}
	for _, k := range keyRe.FindAllStringSubmatch(m[1], -1) {
		out[k[1]] = true
	}
	if len(out) < 50 {
		t.Fatalf("only %d codepoints parsed from MATERIAL_CP — parser is out of date", len(out))
	}
	return out
}

// sym("name") call sites are the third place icon names are hand-written, and they were
// the untested one: the Deploy button's sym("arrow.up.circle") shipped unmapped and drew
// the fallback "help" glyph, which looks exactly like a missing button.
var symCallRe = regexp.MustCompile(`\bsym\(\s*"([^"]*)"`)

func TestSymCallIconNamesAreMapped(t *testing.T) {
	mapped := mappedIconNames(t)

	entries, err := filepath.Glob(filepath.Join("static", "*.js"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("glob static/*.js: %v (%d files)", err, len(entries))
	}

	total := 0
	for _, path := range entries {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range symCallRe.FindAllStringSubmatch(string(b), -1) {
			name := m[1]
			if name == "" || strings.Contains(name, "${") {
				continue
			}
			total++
			if !mapped[name] {
				t.Errorf("%s calls sym(%q), which is not in SF_TO_MATERIAL — it renders the fallback \"help\" glyph", filepath.Base(path), name)
			}
		}
	}
	if total < 20 {
		t.Fatalf("only %d sym() names parsed from static/*.js — parser is out of date", total)
	}
}
