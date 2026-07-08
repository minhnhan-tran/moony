package plugins

import (
	"path/filepath"
	"testing"

	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
)

// TestCatalogReferencesAreReal guards against typos: every skill/agent/mcp a
// plugin bundles must exist in the corresponding marketplace catalog, and every
// plugin must have at least one component.
func TestCatalogReferencesAreReal(t *testing.T) {
	for _, e := range Catalog() {
		for _, s := range e.Skills {
			if _, ok := skills.CatalogEntryByName(s); !ok {
				t.Errorf("plugin %q references unknown skill %q", e.Name, s)
			}
		}
		for _, a := range e.Agents {
			if _, ok := subagents.CatalogEntryByName(a); !ok {
				t.Errorf("plugin %q references unknown agent %q", e.Name, a)
			}
		}
		for _, m := range e.MCP {
			if _, ok := mcp.CatalogEntryByName(m); !ok {
				t.Errorf("plugin %q references unknown mcp %q", e.Name, m)
			}
		}
		if e.componentCount() == 0 {
			t.Errorf("plugin %q has no components", e.Name)
		}
	}
}

func TestInstallFanOutAndStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	wd := t.TempDir()

	// review-suite bundles skills + an agent + a command (no MCP), so install is
	// pure file writes — deterministic without any package manager.
	const plugin = "review-suite"
	e, ok := CatalogEntryByName(plugin)
	if !ok {
		t.Fatalf("catalog missing %q", plugin)
	}

	if st := statusFor(t, wd, plugin); st.Installed || st.Present != 0 {
		t.Fatalf("pre-install status = %+v, want none present", st)
	}

	if _, err := Install(plugin, true, wd); err != nil {
		t.Fatalf("Install: %v", err)
	}

	st := statusFor(t, wd, plugin)
	if !st.Installed || st.Present != st.Components {
		t.Fatalf("post-install status = %+v, want all present", st)
	}
	// Spot-check the fan-out wrote each kind of component.
	if !subset(nameSet(skillNames(skills.Load(wd))), e.Skills) {
		t.Error("skills not installed")
	}
	if !subset(nameSet(agentNames(subagents.Load(wd))), e.Agents) {
		t.Error("agents not installed")
	}
	for _, c := range e.Commands {
		if !commandExists(c.Name, wd) {
			t.Errorf("command /%s not installed", c.Name)
		}
	}

	// Idempotent: a second install adds nothing.
	if summary, err := Install(plugin, true, wd); err != nil || summary != "already installed" {
		t.Fatalf("re-Install = (%q, %v), want (already installed, nil)", summary, err)
	}

	// Remove takes the parts back out.
	if _, err := Remove(plugin, wd); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if st := statusFor(t, wd, plugin); st.Present != 0 {
		t.Fatalf("post-remove status = %+v, want none present", st)
	}

	// Unknown plugin errors.
	if _, err := Install("nope", true, wd); err == nil {
		t.Error("installing an unknown plugin should error")
	}
}

func statusFor(t *testing.T, wd, name string) Status {
	t.Helper()
	for _, st := range StatusAll(wd) {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("status missing %q", name)
	return Status{}
}

func skillNames(ss []skills.Skill) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}
func agentNames(ps []subagents.Persona) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}
func nameSet(names []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}
func subset(have map[string]bool, want []string) bool {
	for _, n := range want {
		if !have[n] {
			return false
		}
	}
	return true
}
