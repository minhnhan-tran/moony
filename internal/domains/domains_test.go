package domains

import (
	"path/filepath"
	"testing"
)

func TestCheckDenyList(t *testing.T) {
	l := &List{Deny: []string{"evil.com"}}
	// Default-allow: anything not denied is permitted.
	if ok, _ := l.Check("apple.com"); !ok {
		t.Error("apple.com should be allowed when only a deny list exists")
	}
	// Denied host (and its subdomains) blocked.
	if ok, _ := l.Check("evil.com"); ok {
		t.Error("evil.com should be denied")
	}
	if ok, _ := l.Check("tracker.evil.com"); ok {
		t.Error("subdomain of a denied domain should be denied")
	}
}

func TestCheckAllowlistLockdown(t *testing.T) {
	l := &List{Allow: []string{"apple.com"}}
	if ok, _ := l.Check("docs.apple.com"); !ok {
		t.Error("subdomain of an allowed domain should be allowed")
	}
	if ok, _ := l.Check("apple.com"); !ok {
		t.Error("the allowed domain itself should be allowed")
	}
	if ok, _ := l.Check("example.com"); ok {
		t.Error("non-allowlisted host should be blocked in allowlist mode")
	}
}

func TestCheckDenyWinsOverAllow(t *testing.T) {
	l := &List{Allow: []string{"apple.com"}, Deny: []string{"internal.apple.com"}}
	if ok, _ := l.Check("docs.apple.com"); !ok {
		t.Error("docs.apple.com should be allowed")
	}
	if ok, _ := l.Check("internal.apple.com"); ok {
		t.Error("an explicit deny must win over an allow")
	}
}

func TestHostFromURL(t *testing.T) {
	cases := map[string]string{
		"https://Docs.Apple.com/path?q=1": "docs.apple.com",
		"http://example.com:8080/x":       "example.com",
		"https://user@host.example.org/":  "host.example.org",
		"not a url":                       "",
	}
	for in, want := range cases {
		if got := HostFromURL(in); got != want {
			t.Errorf("HostFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"  HTTPS://*.Apple.com:443/docs ": "apple.com",
		"apple.com":                       "apple.com",
		".apple.com.":                     "apple.com",
		"http://x.io":                     "x.io",
	}
	for in, want := range cases {
		if got := normalizeDomain(in); got != want {
			t.Errorf("normalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddRemovePersist(t *testing.T) {
	// Redirect the global config dir to a temp location.
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HOME", home)

	l := Load()
	if err := l.Add("evil.com", true); err != nil {
		t.Fatal(err)
	}
	if err := l.Add("apple.com", false); err != nil {
		t.Fatal(err)
	}
	// Adding to allow removes from deny and vice-versa.
	if err := l.Add("evil.com", false); err != nil {
		t.Fatal(err)
	}
	reloaded := Load()
	if contains(reloaded.Deny, "evil.com") {
		t.Error("evil.com should have moved out of deny when re-added as allow")
	}
	if !contains(reloaded.Allow, "evil.com") || !contains(reloaded.Allow, "apple.com") {
		t.Errorf("allow list not persisted: %+v", reloaded.Allow)
	}
	if err := reloaded.Remove("apple.com"); err != nil {
		t.Fatal(err)
	}
	if contains(Load().Allow, "apple.com") {
		t.Error("apple.com should be removed after Remove")
	}
}

func TestEnsureDefaultsSeedsAppleOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HOME", home)

	// No file yet → permits everything.
	if ok, _ := Load().Check("example.com"); !ok {
		t.Fatal("with no file, every host should be allowed")
	}

	// After seeding, only Apple hosts (and subdomains) are fetchable.
	EnsureDefaults()
	l := Load()
	if !contains(l.Allow, "apple.com") {
		t.Fatalf("EnsureDefaults should seed apple.com, got %+v", l.Allow)
	}
	if ok, _ := l.Check("docs.apple.com"); !ok {
		t.Error("apple.com subdomain should be allowed after seeding")
	}
	if ok, _ := l.Check("example.com"); ok {
		t.Error("non-Apple host should be blocked after seeding (lockdown)")
	}
}

func TestEnsureDefaultsNeverOverwrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HOME", home)

	// User clears the default to allow everything; a later EnsureDefaults must
	// not re-seed apple.com over their choice.
	if err := Load().Remove("apple.com"); err != nil {
		t.Fatal(err)
	}
	EnsureDefaults()
	if contains(Load().Allow, "apple.com") {
		t.Error("EnsureDefaults must not overwrite an existing (user-cleared) file")
	}
}
