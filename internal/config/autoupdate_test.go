package config

import (
	"testing"
)

// An always-on daemon has nobody watching to run /update, so it must default to
// keeping itself current.
func TestAutoUpdateDefaultsOn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MN_AUTO_UPDATE", "")
	if !Load().AutoUpdate {
		t.Error("AutoUpdate must default to true")
	}
}

// A locked-down or manually-managed host must be able to pin its build.
func TestAutoUpdateEnvOff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, v := range []string{"0", "off", "false", "no", "OFF", "False"} {
		t.Setenv("MN_AUTO_UPDATE", v)
		if Load().AutoUpdate {
			t.Errorf("MN_AUTO_UPDATE=%q must disable auto-update", v)
		}
	}
	for _, v := range []string{"1", "on", "true", "yes", "ON"} {
		t.Setenv("MN_AUTO_UPDATE", v)
		if !Load().AutoUpdate {
			t.Errorf("MN_AUTO_UPDATE=%q must enable auto-update", v)
		}
	}
	// An unrecognized value must not silently disable updates — fall back to the default.
	t.Setenv("MN_AUTO_UPDATE", "maybe")
	if !Load().AutoUpdate {
		t.Error("an unparseable value must leave the default (on) in place")
	}
}

// The choice must survive a restart, and must beat the env so a UI decision is not
// undone by a stale deployment variable.
func TestAutoUpdatePrefsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MN_AUTO_UPDATE", "1") // the env says on…

	off := false
	save := &Config{AutoUpdate: false, AutoUpdatePersona: &off}
	if err := save.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// …but the user's explicit choice was off.
	if Load().AutoUpdate {
		t.Error("the persona choice must win over the env")
	}

	on := true
	save2 := &Config{AutoUpdate: true, AutoUpdatePersona: &on}
	if err := save2.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !Load().AutoUpdate {
		t.Error("an explicit on must persist too")
	}
}

// VersionLess drives the "is there a newer build?" decision, so a wrong answer either
// stalls updates forever or downgrades the machine.
func TestVersionLessForUpdateDecision(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"2.10.0", "2.11.0", true},
		{"2.10.0", "2.10.1", true},
		{"2.9.0", "2.10.0", true}, // numeric, not lexical: "9" < "10"
		{"2.10.0", "2.10.0", false},
		{"2.11.0", "2.10.0", false},
		{"3.0.0", "2.99.99", false},
	} {
		if got := VersionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("VersionLess(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
