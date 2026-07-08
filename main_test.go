package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// TestChangelogMatchesVersion keeps CHANGELOG.md's latest entry in sync with the
// version baked into the binary.
func TestChangelogMatchesVersion(t *testing.T) {
	data, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	m := regexp.MustCompile(`(?m)^## \[([0-9]+\.[0-9]+\.[0-9]+)\]`).FindSubmatch(data)
	if m == nil {
		t.Fatal("CHANGELOG.md has no '## [x.y.z]' version heading")
	}
	if got := string(m[1]); got != config.Version {
		t.Errorf("CHANGELOG top version %q != config.Version %q — update one to match", got, config.Version)
	}
}
