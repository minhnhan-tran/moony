package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// detectVerifyCommand picks a sensible default "does the project still build/pass
// tests?" command for the verify/repair loop, based on the marker files present
// in dir. Returns "" when nothing recognizable is found (the caller then leaves
// the verify loop disabled). Detection is intentionally conservative and ordered
// most-specific first: a Makefile `test`/`check` target wins over a bare toolchain
// build, because a repo that ships one usually means it as the canonical check.
//
// The command is only ever USED when the user opts in (MN_VERIFY_AUTO=1) and has
// not set an explicit VerifyCommand, so this never runs behind the user's back.
func detectVerifyCommand(dir string) string {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	// 1. Makefile with a test/check target — the repo's own canonical check.
	if tgt := makefileCheckTarget(dir); tgt != "" {
		return "make " + tgt
	}

	// 2. Language toolchains, most-conventional command each.
	switch {
	case has("go.mod"):
		// Build is the cheapest signal that edits didn't break compilation; tests
		// can be slow/networked, so default to build (users can override).
		return "go build ./..."
	case has("Cargo.toml"):
		return "cargo build"
	case has("package.json"):
		if npmHasTestScript(dir) {
			return "npm test --silent"
		}
		// A JS/TS project without a test script: type-check if configured.
		if has("tsconfig.json") {
			return "npx --no-install tsc --noEmit"
		}
	case has("pyproject.toml") || has("setup.py") || has("setup.cfg"):
		if has("pyproject.toml") && fileContains(filepath.Join(dir, "pyproject.toml"), "pytest") {
			return "pytest -q"
		}
		if has("tox.ini") {
			return "tox -q"
		}
		return "python -m compileall -q ."
	case has("pom.xml"):
		return "mvn -q -e test-compile"
	case has("build.gradle") || has("build.gradle.kts"):
		return "./gradlew assemble -q"
	}

	// 3. pytest layout without a build system file.
	if has("pytest.ini") || has("tox.ini") {
		return "pytest -q"
	}
	return ""
}

var makeTargetRe = regexp.MustCompile(`(?m)^(test|check|lint|verify|ci)\s*:`)

// makefileCheckTarget returns the first check-like target (preferring `test`,
// then `check`) declared in the project Makefile, or "" if none.
func makefileCheckTarget(dir string) string {
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		found := map[string]bool{}
		for _, m := range makeTargetRe.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = true
		}
		// Preference order.
		for _, t := range []string{"test", "check", "verify", "ci", "lint"} {
			if found[t] {
				return t
			}
		}
	}
	return ""
}

// npmHasTestScript reports whether package.json declares a real `test` script
// (not the npm-init placeholder that just errors).
func npmHasTestScript(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return false
	}
	t := strings.TrimSpace(pkg.Scripts["test"])
	if t == "" {
		return false
	}
	// The default `npm init` placeholder — treat as "no test".
	if strings.Contains(t, "no test specified") {
		return false
	}
	return true
}

func fileContains(path, substr string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), substr)
}

// DetectVerifyCommand exposes verify-command auto-detection to other packages
// (e.g. the /verify "auto" subcommand). See detectVerifyCommand.
func DetectVerifyCommand(dir string) string { return detectVerifyCommand(dir) }
