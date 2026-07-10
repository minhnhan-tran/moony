package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// lintTool runs whichever static-analysis/lint tooling applies to the target
// (detected from marker files: go.mod, package.json, pyproject.toml/
// requirements.txt/setup.py, Cargo.toml) and are installed, reporting their
// findings. It is read-only: a linter's non-zero exit means issues were
// found, which is a normal, successful result, not a tool error.
type lintTool struct {
	r   *Registry
	max int
}

func (t *lintTool) Name() string { return "lint" }
func (t *lintTool) Description() string {
	return "Run static-analysis/lint tooling over the project or a path and report findings: " +
		"golangci-lint or go vet for Go, eslint for JS/TS, ruff or flake8 for Python, cargo clippy for Rust " +
		"— whichever apply (by marker file) and are installed on PATH. Read-only; a non-zero linter exit just " +
		"means issues were found, not a tool failure. Prefer this over shelling out to a linter directly."
}
func (t *lintTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File or directory to lint (default: the whole project)."}
		}
	}`)
}
func (t *lintTool) Mutates() bool { return false }

func (t *lintTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	target := t.r.root
	if p := strings.TrimSpace(in.Path); p != "" {
		abs, err := t.r.resolve(p)
		if err != nil {
			return "", err
		}
		target = abs
	}
	if _, ok := t.r.searchCacheLookup(t.Name(), input); ok {
		return searchDedupStub(t.Name()), nil
	}

	runs := planLintRuns(t.r.root, target)
	if len(runs) == 0 {
		rel, _ := filepath.Rel(t.r.root, target)
		return fmt.Sprintf("No recognized project type (go.mod, package.json, pyproject.toml/requirements.txt/setup.py, or Cargo.toml) found for %s; nothing to lint.", orDot(rel)), nil
	}

	var b strings.Builder
	ranAny := false
	for _, run := range runs {
		if _, err := exec.LookPath(run.bin); err != nil {
			fmt.Fprintf(&b, "## %s: %s not installed (skipped)\n\n", run.lang, run.bin)
			continue
		}
		ranAny = true
		out := runLinter(ctx, run.dir, t.max, run.bin, run.args...)
		fmt.Fprintf(&b, "## %s (%s %s)\n%s\n\n", run.lang, run.bin, strings.Join(run.args, " "), out)
	}
	if !ranAny {
		b.WriteString("No linter for the detected project type(s) is installed. Install golangci-lint, eslint, ruff, or cargo as appropriate.")
	}
	result := strings.TrimSpace(b.String())
	t.r.searchCacheStore(t.Name(), input, result)
	return result, nil
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

// linterRun is one candidate lint invocation: run `bin args...` from `dir`.
type linterRun struct {
	lang string
	bin  string
	args []string
	dir  string
}

// planLintRuns detects which project type(s) target belongs to (checking for
// marker files at target's own directory and, failing that, at root) and
// returns the linter invocation to try for each. Go prefers golangci-lint,
// falling back to `go vet` (always available wherever go.mod is, so a Go
// target always gets checked by something). Other languages use one
// well-established linter each; if it isn't installed the caller reports a
// skip rather than trying to guess an alternative.
func planLintRuns(root, target string) []linterRun {
	dir := target
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		dir = filepath.Dir(target)
	}
	isFile := dir != target

	var runs []linterRun
	if base, ok := findMarker(dir, root, "go.mod"); ok {
		pattern := "./..."
		if isFile {
			if rel, err := filepath.Rel(base, target); err == nil {
				pattern = "./" + filepath.ToSlash(rel)
			}
		}
		if _, err := exec.LookPath("golangci-lint"); err == nil {
			runs = append(runs, linterRun{"Go", "golangci-lint", []string{"run", pattern}, base})
		} else {
			runs = append(runs, linterRun{"Go (go vet — golangci-lint not installed)", "go", []string{"vet", pattern}, base})
		}
	}
	if base, ok := findMarker(dir, root, "package.json"); ok {
		arg := "."
		if isFile {
			arg = target
		}
		runs = append(runs, linterRun{"JS/TS", "eslint", []string{arg}, base})
	}
	for _, marker := range []string{"pyproject.toml", "requirements.txt", "setup.py"} {
		if base, ok := findMarker(dir, root, marker); ok {
			arg := "."
			if isFile {
				arg = target
			}
			runs = append(runs, linterRun{"Python", "ruff", []string{"check", arg}, base})
			break
		}
	}
	if base, ok := findMarker(dir, root, "Cargo.toml"); ok {
		runs = append(runs, linterRun{"Rust", "cargo", []string{"clippy", "--quiet"}, base})
	}
	return dedupLinterRuns(runs)
}

// findMarker checks dir, then root (if different), for the given marker file,
// returning the directory it was found in.
func findMarker(dir, root, marker string) (string, bool) {
	if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
		return dir, true
	}
	if root != dir {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			return root, true
		}
	}
	return "", false
}

// dedupLinterRuns drops exact-duplicate invocations (e.g. a marker matched at
// both dir and root resolving to the identical command).
func dedupLinterRuns(runs []linterRun) []linterRun {
	seen := map[string]bool{}
	out := make([]linterRun, 0, len(runs))
	for _, r := range runs {
		key := r.bin + "|" + r.dir + "|" + strings.Join(r.args, " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// runLinter runs bin(args...) from dir and returns its combined output
// (capped to max bytes), treating any exit code as a valid result — a
// non-zero exit from a linter means "issues found", not an execution error.
// Only a timeout or truly empty output on failure gets a synthetic message.
func runLinter(ctx context.Context, dir string, max int, bin string, args ...string) string {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if max > 0 {
		text = truncate(text, max)
	}
	if cctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("%s timed out", bin)
	}
	if text == "" {
		if err != nil {
			return "no issues found (exit: " + err.Error() + ")"
		}
		return "no issues found"
	}
	return text
}
