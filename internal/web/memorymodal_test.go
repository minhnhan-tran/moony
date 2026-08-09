package web

// The Session-memory modal groups the three things the agent carries into a turn
// as project knowledge: AGENTS.md memory, the project notes backlog, and the
// codebase map. These tests pin that wiring — a renamed renderer or a moved tab
// would otherwise break the modal silently, since none of it is compiled.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func staticAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("static", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMemoryModalHasCodebaseMapTab(t *testing.T) {
	mg := staticAsset(t, "app-manage.js")

	block := regexp.MustCompile(`(?s)const MEMORY_TABS = \[(.*?)\];`).FindStringSubmatch(mg)
	if block == nil {
		t.Fatal("MEMORY_TABS not found in app-manage.js")
	}
	tabs := block[1]

	for _, want := range []struct{ key, label string }{
		{"memory", "Memory"},
		{"notes", "Notes"},
		{"map", "Codebase map"},
	} {
		if !strings.Contains(tabs, `key: "`+want.key+`"`) {
			t.Errorf("the memory modal should have a %q tab", want.key)
		}
		if !strings.Contains(tabs, `label: "`+want.label+`"`) {
			t.Errorf("the %q tab should be labelled %q", want.key, want.label)
		}
	}

	// The modal must actually open as a tabbed modal on those tabs.
	if !regexp.MustCompile(`openTabbedModal\("Session memory", "brain", MEMORY_TABS`).MatchString(mg) {
		t.Error("openMemoryEditor should open the tabbed Session memory modal with MEMORY_TABS")
	}
}

// Every renderer named by a tab must exist, or clicking the tab throws.
func TestMemoryModalRenderersExist(t *testing.T) {
	all := staticAsset(t, "app-manage.js") + "\n" +
		staticAsset(t, "app-core.js") + "\n" +
		staticAsset(t, "app-render.js")

	block := regexp.MustCompile(`(?s)const MEMORY_TABS = \[(.*?)\];`).FindStringSubmatch(all)
	if block == nil {
		t.Fatal("MEMORY_TABS not found")
	}
	renderers := regexp.MustCompile(`render: \(\) => (\w+)\(`).FindAllStringSubmatch(block[1], -1)
	if len(renderers) == 0 {
		t.Fatal("no renderers found on the memory tabs")
	}
	for _, m := range renderers {
		name := m[1]
		def := regexp.MustCompile(`(?:async\s+)?(?:function\s+` + name + `\b|const\s+` + name + `\s*=)`)
		if !def.MatchString(all) {
			t.Errorf("tab renderer %q is referenced but never defined", name)
		}
	}

	// Helpers the map and notes tabs depend on.
	for _, dep := range []string{"getActiveProject", "renderProjectNotesInto", "renderMD", "openTabbedModal"} {
		def := regexp.MustCompile(`(?:async\s+)?(?:function\s+` + dep + `\b|const\s+` + dep + `\s*=)`)
		if !def.MatchString(all) {
			t.Errorf("%q is used by the memory modal but not defined", dep)
		}
	}
}

// The panel must talk to the route the server actually registers, for all three
// methods it uses (load, generate, save pinned).
func TestCodebaseMapPanelMatchesServerRoutes(t *testing.T) {
	mg := staticAsset(t, "app-manage.js")
	if !regexp.MustCompile(`/api/sessions/\$\{[^}]+\}/codebasemap`).MatchString(mg) {
		t.Error("the codebase map panel should call /api/sessions/{id}/codebasemap")
	}
	srv, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST", "PUT"} {
		route := `"` + method + ` /api/sessions/{id}/codebasemap"`
		if !strings.Contains(string(srv), route) {
			t.Errorf("server.go should register %s", route)
		}
	}
}

// The map lives in exactly one modal. It used to sit under Config, which is why
// it was hard to find; keeping both would give two divergent copies of the same
// panel state.
func TestCodebaseMapTabIsNotDuplicatedInConfig(t *testing.T) {
	core := staticAsset(t, "app-core.js")
	block := regexp.MustCompile(`(?s)const CONFIG_TABS = \[(.*?)\];`).FindStringSubmatch(core)
	if block == nil {
		t.Fatal("CONFIG_TABS not found in app-core.js")
	}
	if strings.Contains(block[1], "renderCodebaseMapPanel") {
		t.Error("the codebase map should live only in the Session memory modal, not also in Config")
	}
}
