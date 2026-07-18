package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// testRunTool runs a project's test suite and returns a STRUCTURED summary —
// pass/fail/skip counts, coverage, and the names+output of failing tests —
// instead of raw scrollback. For Go it drives `go test -json` and parses the
// test2json event stream; for other ecosystems it runs the conventional
// command and reports the exit status plus a tail of output. Read-only in the
// sense that a failing suite is a normal, successful result (not a tool error).
type testRunTool struct {
	r   *Registry
	max int
}

func (t *testRunTool) Name() string { return "test_run" }

func (t *testRunTool) Description() string {
	return "Run the project's tests and return a structured summary: total/passed/failed/skipped counts, " +
		"coverage when available, and the names and output of failing tests. Detects the ecosystem by marker " +
		"file (go.mod → `go test`, package.json → npm test, Cargo.toml → cargo test, pyproject.toml → pytest). " +
		"Optionally scope to a path and/or filter by a run pattern (Go: -run regexp). Prefer this over invoking " +
		"the test runner through bash — a non-zero (failing) result is reported, not treated as an error."
}

func (t *testRunTool) Schema() json.RawMessage {
	return schema(`{"type": "object", "properties": {
		"path": {"type": "string", "description": "File or directory to test (default: the whole project)."},
		"run": {"type": "string", "description": "Only run tests whose name matches this pattern (Go: -run regexp)."}
	}}`)
}

func (t *testRunTool) Mutates() bool { return false }

func (t *testRunTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
		Run  string `json:"run"`
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

	dir := target
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		dir = filepath.Dir(target)
	}
	isFile := dir != target
	run := strings.TrimSpace(in.Run)

	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if base, ok := findMarker(dir, t.r.root, "go.mod"); ok {
		return t.runGo(cctx, base, target, isFile, run)
	}
	if base, ok := findMarker(dir, t.r.root, "package.json"); ok {
		args := []string{"test"}
		if run != "" {
			args = append(args, "--", "-t", run)
		}
		return t.runGeneric(cctx, "npm/JS", base, "npm", args...)
	}
	if base, ok := findMarker(dir, t.r.root, "Cargo.toml"); ok {
		args := []string{"test"}
		if run != "" {
			args = append(args, run)
		}
		return t.runGeneric(cctx, "Rust", base, "cargo", args...)
	}
	for _, marker := range []string{"pyproject.toml", "pytest.ini", "setup.py", "requirements.txt"} {
		if base, ok := findMarker(dir, t.r.root, marker); ok {
			args := []string{}
			if run != "" {
				args = append(args, "-k", run)
			}
			return t.runGeneric(cctx, "Python", base, "pytest", args...)
		}
	}
	rel, _ := filepath.Rel(t.r.root, target)
	return fmt.Sprintf("No recognized project type (go.mod, package.json, Cargo.toml, or pyproject.toml) found for %s; nothing to test.", orDot(rel)), nil
}

func (t *testRunTool) runGo(ctx context.Context, base, target string, isFile bool, run string) (string, error) {
	pattern := "./..."
	if isFile {
		if rel, err := filepath.Rel(base, filepath.Dir(target)); err == nil {
			pattern = "./" + filepath.ToSlash(rel)
		}
	}
	args := []string{"test", "-json", "-cover", pattern}
	if run != "" {
		args = append(args, "-run", run)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = base
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return "test_run: `go test` timed out after 5m", nil
	}
	summary := parseGoTestJSON(stdout.Bytes())
	// A setup/compile failure produces little or no JSON; surface stderr then.
	if summary.total() == 0 && summary.coverage == "" {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if msg == "" {
			return "No tests found.", nil
		}
		return "No tests ran (build or setup failed):\n" + truncate(msg, t.max), nil
	}
	return summary.render(t.max), nil
}

func (t *testRunTool) runGeneric(ctx context.Context, lang, dir, bin string, args ...string) (string, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Sprintf("%s tests need %q on PATH, which isn't installed.", lang, bin), nil
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("test_run: `%s %s` timed out after 5m", bin, strings.Join(args, " ")), nil
	}
	status := "passed"
	if err != nil {
		status = "FAILED (" + err.Error() + ")"
	}
	body := truncate(strings.TrimSpace(string(out)), t.max)
	return fmt.Sprintf("## %s tests (%s %s): %s\n\n%s", lang, bin, strings.Join(args, " "), status, body), nil
}

// goTestEvent is one line of `go test -json` output (the test2json format).
type goTestEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

type goTestSummary struct {
	passed   int
	failed   int
	skipped  int
	coverage string // e.g. "72.4% of statements" (best/last package seen)
	failures []testFailure
	pkgFail  []string // packages that failed to build/setup (no per-test events)
}

type testFailure struct {
	name   string
	output string
}

func (s goTestSummary) total() int { return s.passed + s.failed + s.skipped }

var goCoverageRe = regexp.MustCompile(`coverage: ([0-9.]+% of statements)`)

// parseGoTestJSON folds a test2json event stream into a summary: per-test
// pass/fail/skip actions, accumulated output for failing tests, and coverage
// lines. Non-JSON lines (rare, but possible on toolchain errors) are ignored.
func parseGoTestJSON(data []byte) goTestSummary {
	var s goTestSummary
	outputs := map[string]*strings.Builder{} // package/test -> collected output
	key := func(pkg, test string) string { return pkg + "\x00" + test }

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev goTestEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Action {
		case "output":
			if m := goCoverageRe.FindStringSubmatch(ev.Output); m != nil {
				s.coverage = m[1]
			}
			if ev.Test != "" {
				k := key(ev.Package, ev.Test)
				b := outputs[k]
				if b == nil {
					b = &strings.Builder{}
					outputs[k] = b
				}
				b.WriteString(ev.Output)
			}
		case "pass":
			if ev.Test != "" {
				s.passed++
			}
		case "skip":
			if ev.Test != "" {
				s.skipped++
			}
		case "fail":
			if ev.Test != "" {
				out := ""
				if b := outputs[key(ev.Package, ev.Test)]; b != nil {
					out = b.String()
				}
				s.failures = append(s.failures, testFailure{name: ev.Package + "." + ev.Test, output: strings.TrimSpace(out)})
				s.failed++
			} else if ev.Test == "" {
				// package-level failure with no test events (e.g. build error)
				s.pkgFail = append(s.pkgFail, ev.Package)
			}
		}
	}
	return s
}

func (s goTestSummary) render(max int) string {
	var b strings.Builder
	verdict := "PASS"
	if s.failed > 0 || len(s.pkgFail) > 0 {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "%s — %d passed, %d failed, %d skipped (%d total)",
		verdict, s.passed, s.failed, s.skipped, s.total())
	if s.coverage != "" {
		fmt.Fprintf(&b, "; coverage %s", s.coverage)
	}
	b.WriteByte('\n')

	if len(s.pkgFail) > 0 {
		pkgs := append([]string(nil), s.pkgFail...)
		sort.Strings(pkgs)
		fmt.Fprintf(&b, "\nPackages that failed to build/run: %s\n", strings.Join(pkgs, ", "))
	}
	if len(s.failures) > 0 {
		sort.SliceStable(s.failures, func(i, j int) bool { return s.failures[i].name < s.failures[j].name })
		b.WriteString("\nFailing tests:\n")
		for _, f := range s.failures {
			fmt.Fprintf(&b, "\n### %s\n", f.name)
			if f.output != "" {
				b.WriteString(f.output)
				b.WriteByte('\n')
			}
		}
	}
	return truncate(strings.TrimRight(b.String(), "\n"), max)
}
