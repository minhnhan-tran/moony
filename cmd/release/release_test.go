package main

import (
	"strings"
	"testing"
)

func TestBumpVersion(t *testing.T) {
	cases := []struct {
		cur, level, want string
		ok               bool
	}{
		{"0.2.0", "minor", "0.3.0", true},
		{"0.2.0", "patch", "0.2.1", true},
		{"0.2.3", "major", "1.0.0", true},
		{"1.4.9", "minor", "1.5.0", true},
		{"0.2.0", "bogus", "", false},
		{"0.2", "minor", "", false},
		{"vX", "minor", "", false},
	}
	for _, tc := range cases {
		got, err := bumpVersion(tc.cur, tc.level)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("bumpVersion(%q,%q) = (%q,%v), want %q", tc.cur, tc.level, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("bumpVersion(%q,%q) expected an error", tc.cur, tc.level)
		}
	}
}

func TestReplaceVersionConst(t *testing.T) {
	src := "package config\n\n// doc\nconst Version = \"0.2.0\"\n\nconst Other = \"0.2.0\"\n"
	out, err := replaceVersionConst(src, "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `const Version = "0.3.0"`) {
		t.Errorf("Version not bumped:\n%s", out)
	}
	// Only the Version const changes, not other identical literals.
	if !strings.Contains(out, `const Other = "0.2.0"`) {
		t.Errorf("an unrelated literal was changed:\n%s", out)
	}
	if _, err := replaceVersionConst("no version here", "1.0.0"); err == nil {
		t.Error("expected an error when the const is absent")
	}
}

func TestCategorize(t *testing.T) {
	added, changed, fixed := categorize([]string{
		"Add prompt caching",
		"Implement retry/backoff",
		"Fix mouse-scroll leak",
		"Render markdown live",
		"Merge branch 'main'", // dropped
		"Release v0.2.0",      // dropped
		"",                    // dropped
	})
	if len(added) != 2 || len(fixed) != 1 || len(changed) != 1 {
		t.Fatalf("buckets: added=%v changed=%v fixed=%v", added, changed, fixed)
	}
	if changed[0] != "Render markdown live" || fixed[0] != "Fix mouse-scroll leak" {
		t.Errorf("misclassified: changed=%v fixed=%v", changed, fixed)
	}
}

func TestChangelogEntry(t *testing.T) {
	entry := changelogEntry("0.3.0", "2026-06-11", []string{"Add X", "Fix Y", "Tweak Z"})
	for _, want := range []string{"## [0.3.0] — 2026-06-11", "### Added", "- Add X", "### Fixed", "- Fix Y", "### Changed", "- Tweak Z"} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry missing %q:\n%s", want, entry)
		}
	}
	// Empty section headers are omitted.
	if strings.Contains(changelogEntry("0.3.0", "d", []string{"Add only"}), "### Fixed") {
		t.Error("empty Fixed section should be omitted")
	}
}

func TestPrependChangelogEntry(t *testing.T) {
	content := "# Changelog\n\nPreamble.\n\n## [0.2.0] — 2026-06-10\n\n### Added\n- old thing\n"
	entry := "## [0.3.0] — 2026-06-11\n\n### Added\n- new thing"
	out, err := prependChangelogEntry(content, entry)
	if err != nil {
		t.Fatal(err)
	}
	i30 := strings.Index(out, "## [0.3.0]")
	i20 := strings.Index(out, "## [0.2.0]")
	if i30 < 0 || i20 < 0 || i30 > i20 {
		t.Errorf("0.3.0 should appear before 0.2.0:\n%s", out)
	}
	if !strings.HasPrefix(out, "# Changelog") {
		t.Errorf("preamble must be preserved:\n%s", out)
	}
	if _, err := prependChangelogEntry("no sections", entry); err == nil {
		t.Error("expected an error when there's no existing section")
	}
}
