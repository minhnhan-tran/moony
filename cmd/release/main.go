// Command release cuts a new moony version: it bumps config.Version,
// prepends a CHANGELOG.md section built from the git commit subjects since the
// last release tag, and (unless told otherwise) commits the two files and
// creates a vX.Y.Z tag. The changelog top entry therefore always matches
// config.Version, keeping the sync test (root main_test.go) green.
//
// Usage (normally via `make release`):
//
//	go run ./cmd/release                 # bump the minor version
//	go run ./cmd/release -bump=patch     # bump the patch version
//	go run ./cmd/release -version=1.0.0  # set an explicit version
//	go run ./cmd/release -dry-run        # print what would change
//	go run ./cmd/release -no-tag         # edit files but don't commit/tag
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

func main() {
	var (
		bump    = stringFlag("-bump", "minor")
		version = stringFlag("-version", "")
		dryRun  = boolFlag("-dry-run")
		noTag   = boolFlag("-no-tag")
	)

	if err := run(bump, version, dryRun, noTag); err != nil {
		fmt.Fprintln(os.Stderr, "release: "+err.Error())
		os.Exit(1)
	}
}

func run(bump, explicit string, dryRun, noTag bool) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	current := config.Version
	next := explicit
	if next == "" {
		next, err = bumpVersion(current, bump)
		if err != nil {
			return err
		}
	}
	if !semverRe.MatchString(next) {
		return fmt.Errorf("invalid version %q (want X.Y.Z)", next)
	}

	subjects := commitSubjects(root)
	date := time.Now().Format("2006-01-02")
	entry := changelogEntry(next, date, subjects)

	if dryRun {
		fmt.Printf("Would bump %s -> %s\n\n%s\n", current, next, entry)
		return nil
	}

	// Update config.Version.
	cfgPath := filepath.Join(root, "internal", "config", "config.go")
	cfgSrc, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	newCfg, err := replaceVersionConst(string(cfgSrc), next)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, []byte(newCfg), 0o644); err != nil {
		return err
	}

	// Prepend the CHANGELOG entry.
	clPath := filepath.Join(root, "CHANGELOG.md")
	clSrc, err := os.ReadFile(clPath)
	if err != nil {
		return err
	}
	newCL, err := prependChangelogEntry(string(clSrc), entry)
	if err != nil {
		return err
	}
	if err := os.WriteFile(clPath, []byte(newCL), 0o644); err != nil {
		return err
	}

	fmt.Printf("Bumped %s -> %s and updated CHANGELOG.md (%d commit(s)).\n", current, next, len(subjects))

	tag := "v" + next
	if noTag {
		fmt.Printf("Edited files only. Review, then: git commit + git tag %s\n", tag)
		return nil
	}

	// Commit only the two release files (leaves any other WIP untouched), tag,
	// but never push — that stays an explicit manual step.
	if err := git(root, "add", "internal/config/config.go", "CHANGELOG.md"); err != nil {
		return err
	}
	if err := git(root, "commit", "-m", "Release "+tag); err != nil {
		return err
	}
	if err := git(root, "tag", tag); err != nil {
		return err
	}
	fmt.Printf("Committed and tagged %s. Push with: git push origin main --tags\n", tag)
	return nil
}

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// bumpVersion increments cur ("X.Y.Z") at the given level (major|minor|patch).
func bumpVersion(cur, level string) (string, error) {
	if !semverRe.MatchString(cur) {
		return "", fmt.Errorf("current version %q is not X.Y.Z", cur)
	}
	parts := strings.Split(cur, ".")
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch, _ := strconv.Atoi(parts[2])
	switch level {
	case "major":
		major, minor, patch = major+1, 0, 0
	case "minor":
		minor, patch = minor+1, 0
	case "patch":
		patch++
	default:
		return "", fmt.Errorf("unknown bump level %q (want major|minor|patch)", level)
	}
	return fmt.Sprintf("%d.%d.%d", major, minor, patch), nil
}

var versionConstRe = regexp.MustCompile(`(const Version = ")[^"]*(")`)

// replaceVersionConst rewrites the `const Version = "..."` literal in config.go.
func replaceVersionConst(src, next string) (string, error) {
	if !versionConstRe.MatchString(src) {
		return "", fmt.Errorf("could not find `const Version = \"...\"` in config.go")
	}
	return versionConstRe.ReplaceAllString(src, `${1}`+next+`${2}`), nil
}

// changelogEntry renders a Keep-a-Changelog section, grouping commit subjects
// into Added / Changed / Fixed by their leading verb.
func changelogEntry(version, date string, subjects []string) string {
	added, changed, fixed := categorize(subjects)

	var b strings.Builder
	fmt.Fprintf(&b, "## [%s] — %s\n", version, date)
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n### %s\n\n", title)
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
	}
	section("Added", added)
	section("Changed", changed)
	section("Fixed", fixed)
	if len(added)+len(changed)+len(fixed) == 0 {
		b.WriteString("\n_No changes recorded._\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// categorize buckets commit subjects by their leading verb.
func categorize(subjects []string) (added, changed, fixed []string) {
	for _, s := range subjects {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "Merge ") || strings.HasPrefix(strings.ToLower(s), "release v") {
			continue
		}
		switch {
		case hasPrefixWord(s, "Add", "Implement", "Introduce", "Support"):
			added = append(added, s)
		case hasPrefixWord(s, "Fix"):
			fixed = append(fixed, s)
		default:
			changed = append(changed, s)
		}
	}
	return added, changed, fixed
}

// hasPrefixWord reports whether s starts with any of words followed by a space.
func hasPrefixWord(s string, words ...string) bool {
	for _, w := range words {
		if strings.HasPrefix(s, w+" ") {
			return true
		}
	}
	return false
}

// prependChangelogEntry inserts entry (plus a blank line) immediately before the
// first existing "## [" release section.
func prependChangelogEntry(content, entry string) (string, error) {
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## [") {
			out := append([]string{}, lines[:i]...)
			out = append(out, strings.Split(entry, "\n")...)
			out = append(out, "") // blank line between the new and prior sections
			out = append(out, lines[i:]...)
			return strings.Join(out, "\n"), nil
		}
	}
	return "", fmt.Errorf("no existing '## [' release section found in CHANGELOG.md")
}

// commitSubjects returns the commit subject lines since the last release tag
// (or all commits when there is no tag), excluding merges.
func commitSubjects(root string) []string {
	args := []string{"log", "--no-merges", "--pretty=format:%s"}
	if tag, err := output(root, "describe", "--tags", "--abbrev=0"); err == nil && strings.TrimSpace(tag) != "" {
		args = append(args, strings.TrimSpace(tag)+"..HEAD")
	}
	out, err := output(root, args...)
	if err != nil {
		return nil
	}
	var subjects []string
	for _, ln := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			subjects = append(subjects, s)
		}
	}
	return subjects
}

// repoRoot walks up from the working directory to the module root (go.mod).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from the working directory")
		}
		dir = parent
	}
}

func git(root string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func output(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

// stringFlag / boolFlag are tiny argv parsers (avoids the flag package's global
// state so the helpers above stay easy to unit-test).
func stringFlag(name, def string) string {
	for i, a := range os.Args[1:] {
		if a == name && i+2 <= len(os.Args)-1 {
			return os.Args[i+2]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return def
}

func boolFlag(name string) bool {
	for _, a := range os.Args[1:] {
		if a == name || a == name+"=true" {
			return true
		}
	}
	return false
}
