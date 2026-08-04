package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The quick-action icon picker offers a curated catalog (QA_ICONS in app-manage.js).
// <app-icon> resolves a name through SF_TO_MATERIAL (app-state.js) and falls back to a
// generic "help" glyph when the name is unmapped — silently, with no console error. So
// an unmapped entry in the catalog is a picker offering a choice that renders as a
// broken icon both in the dropdown and on the composer button.
//
// This guards the invariant in the only place it can be checked cheaply: the assets
// themselves. It is a static-asset test, not a DOM test, so it needs no browser.
func TestQuickActionIconsAreMapped(t *testing.T) {
	catalog := readAsset(t, "static/app-manage.js")
	state := readAsset(t, "static/app-state.js")

	mapped := sfToMaterialKeys(t, state)
	if len(mapped) < 50 {
		t.Fatalf("only %d mapped icon names parsed from SF_TO_MATERIAL — parser is out of date", len(mapped))
	}

	names := qaIconNames(t, catalog)
	if len(names) < 20 {
		t.Fatalf("only %d catalog names parsed from QA_ICONS — parser is out of date", len(names))
	}

	seen := map[string]bool{}
	for _, n := range names {
		if !mapped[n] {
			t.Errorf("QA_ICONS offers %q, which is not in SF_TO_MATERIAL — it renders as the fallback \"help\" glyph", n)
		}
		if seen[n] {
			t.Errorf("QA_ICONS lists %q more than once", n)
		}
		seen[n] = true
	}
}

func readAsset(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

var (
	sfMapRe   = regexp.MustCompile(`(?s)const SF_TO_MATERIAL = \{(.*?)\n\};`)
	qaIconsRe = regexp.MustCompile(`(?s)const QA_ICONS = \[(.*?)\n\];`)
	keyRe     = regexp.MustCompile(`"([^"]+)"\s*:`)
	groupRe   = regexp.MustCompile(`(?s)names:\s*\[([^\]]*)\]`)
	strRe     = regexp.MustCompile(`"([^"]+)"`)
)

func sfToMaterialKeys(t *testing.T, src string) map[string]bool {
	t.Helper()
	m := sfMapRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("SF_TO_MATERIAL not found in app-state.js")
	}
	out := map[string]bool{}
	for _, k := range keyRe.FindAllStringSubmatch(m[1], -1) {
		out[k[1]] = true
	}
	return out
}

func qaIconNames(t *testing.T, src string) []string {
	t.Helper()
	m := qaIconsRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("QA_ICONS not found in app-manage.js")
	}
	var out []string
	for _, g := range groupRe.FindAllStringSubmatch(m[1], -1) {
		for _, n := range strRe.FindAllStringSubmatch(g[1], -1) {
			if s := strings.TrimSpace(n[1]); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// Every CSS class the picker builds must actually have a rule, or the dropdown renders
// unstyled (and, being portaled to <body> with position:fixed, effectively invisible).
func TestQuickActionPickerClassesAreStyled(t *testing.T) {
	css := readAsset(t, "static/app.css")
	for _, cls := range []string{
		"qa-icon-picker", "qa-icon-btn", "qa-icon-btn-glyph", "qa-icon-btn-text",
		"qa-icon-caret", "qa-icon-none", "qa-icon-list", "qa-icon-group",
		"qa-icon-item", "qa-icon-glyph", "qa-icon-name",
		"qa-field", "qa-field-label", "qa-field-hint",
		"qa-form-head", "qa-form-head-icon", "qa-form-head-text", "qa-form-head-target",
	} {
		if !strings.Contains(css, "."+cls) {
			t.Errorf("class %q is used by the quick-action form but has no CSS rule", cls)
		}
	}
}
