package worker

import (
	"errors"
	"testing"
)

// resetDetect clears the install cache and stubs engine detection; the cleanup
// restores the real probes.
func resetDetect(t *testing.T, installed map[string]bool, ready map[string]bool) {
	t.Helper()
	installedMu.Lock()
	installedCache = map[string]bool{}
	installedMu.Unlock()

	origLook, origProbe := lookPath, probeVersion
	lookPath = func(cmd string) (string, error) {
		if installed[cmd] {
			return "/usr/local/bin/" + cmd, nil
		}
		return "", errors.New("not found")
	}
	probeVersion = func(cmd string) bool { return ready[cmd] }
	t.Cleanup(func() {
		lookPath, probeVersion = origLook, origProbe
		installedMu.Lock()
		installedCache = map[string]bool{}
		installedMu.Unlock()
	})
}

func TestDetectAddsInstalledEngines(t *testing.T) {
	resetDetect(t, map[string]bool{"optimus": true}, nil) // claude not installed

	m := New(t.TempDir(), nil)
	m.Detect()

	all := m.All()
	if len(all) != 1 || all[0].Name != "optimus" || all[0].Backend != BackendOptimus {
		t.Fatalf("Detect should auto-add only the installed engine, got %+v", all)
	}
	// Auto worker is enabled and read-only by default, and offered to delegate.
	if len(m.Configs()) != 1 {
		t.Fatalf("auto worker should be enabled, Configs()=%d", len(m.Configs()))
	}
}

func TestDetectSkipsConfiguredName(t *testing.T) {
	resetDetect(t, map[string]bool{"optimus": true, "claude": true}, nil)

	// A configured worker already named "optimus" must not be duplicated.
	m := New(t.TempDir(), []Config{{Name: "optimus", Backend: BackendOptimus, Model: "custom/model", srcPath: "/x"}})
	m.Detect()

	count := 0
	for _, c := range m.All() {
		if c.Name == "optimus" {
			count++
			if c.Model != "custom/model" {
				t.Fatalf("configured optimus should be preserved, got model %q", c.Model)
			}
		}
	}
	if count != 1 {
		t.Fatalf("optimus should appear once, got %d", count)
	}
	// claude is installed and unconfigured, so it's auto-added.
	if _, ok := byNameLookup(m, "claude"); !ok {
		t.Fatal("claude should be auto-added")
	}
}

func TestReadinessStates(t *testing.T) {
	resetDetect(t, map[string]bool{"optimus": true, "claude": true},
		map[string]bool{"optimus": true, "claude": false})

	m := New(t.TempDir(), nil)
	m.Detect()

	// Before probing, engines read as "checking…".
	r := m.Readiness()
	if r["optimus"] != "checking…" {
		t.Fatalf("pre-probe optimus = %q, want checking…", r["optimus"])
	}

	m.ProbeReadiness()
	r = m.Readiness()
	if r["optimus"] != "ready" {
		t.Fatalf("optimus = %q, want ready", r["optimus"])
	}
	if r["claude"] != "not ready" {
		t.Fatalf("claude = %q, want not ready", r["claude"])
	}
}

func TestReadinessMissing(t *testing.T) {
	resetDetect(t, map[string]bool{}, nil) // nothing installed
	m := New(t.TempDir(), []Config{{Name: "reviewer", Backend: BackendClaude, srcPath: "/x"}})
	if got := m.Readiness()["reviewer"]; got != "missing" {
		t.Fatalf("uninstalled engine should be 'missing', got %q", got)
	}
}

func TestMaterializeAutoWorkerOnToggle(t *testing.T) {
	resetDetect(t, map[string]bool{"optimus": true}, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	m := New(dir, nil)
	m.Detect() // adds auto "optimus" (srcPath "")

	// Disabling an auto worker should materialize it into the project workers.json.
	if err := m.SetEnabled("optimus", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	reloaded, _ := Load(dir)
	found := false
	for _, c := range reloaded {
		if c.Name == "optimus" {
			found = true
			if !c.Disabled {
				t.Fatal("materialized optimus should be disabled")
			}
		}
	}
	if !found {
		t.Fatal("auto worker was not materialized into workers.json")
	}
}

func byNameLookup(m *Manager, name string) (Config, bool) {
	for _, c := range m.All() {
		if c.Name == name {
			return c, true
		}
	}
	return Config{}, false
}

func TestInstallCommand(t *testing.T) {
	for _, b := range []string{BackendClaude, BackendOptimus, BackendAppleGemini} {
		if InstallCommand(b) == "" {
			t.Errorf("backend %q should have an install command", b)
		}
	}
	if InstallCommand("gpt") != "" {
		t.Error("unknown backend should have no install command")
	}
}

func TestInstallPlanFor(t *testing.T) {
	resetDetect(t, map[string]bool{}, nil)
	m := New(t.TempDir(), []Config{{Name: "rev", Backend: BackendClaude, srcPath: "/x"}})

	engine, command, ok := m.InstallPlanFor("rev")
	if !ok || engine != "claude" || command == "" {
		t.Fatalf("InstallPlanFor(rev) = (%q, %q, %v)", engine, command, ok)
	}
	// An engine-command override is honored.
	m2 := New(t.TempDir(), []Config{{Name: "c", Backend: BackendClaude, Command: "claude-canary", srcPath: "/x"}})
	if engine, _, ok := m2.InstallPlanFor("c"); !ok || engine != "claude-canary" {
		t.Fatalf("override engine = %q ok=%v", engine, ok)
	}
	// Unknown worker → not ok.
	if _, _, ok := m.InstallPlanFor("nope"); ok {
		t.Error("unknown worker should not resolve an install plan")
	}
}

func TestRefreshReadinessFlipsAfterInstall(t *testing.T) {
	// Engine starts missing; Readiness caches that. After it "appears" on PATH,
	// RefreshReadiness must bust the cache + re-probe so it flips to ready.
	installedState := map[string]bool{"claude": false}
	readyState := map[string]bool{"claude": false}
	resetDetect(t, installedState, readyState)

	m := New(t.TempDir(), []Config{{Name: "rev", Backend: BackendClaude, srcPath: "/x"}})
	if got := m.Readiness()["rev"]; got != "missing" {
		t.Fatalf("pre-install = %q, want missing", got)
	}

	// Simulate a successful install: the binary is now on PATH and `--version` ok.
	installedState["claude"] = true
	readyState["claude"] = true

	// Without a refresh the cached "missing" would persist.
	m.RefreshReadiness()
	if got := m.Readiness()["rev"]; got != "ready" {
		t.Fatalf("post-install = %q, want ready", got)
	}
}
