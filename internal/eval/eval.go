// Package eval is a small regression-evaluation harness for the agent: run a set
// of fixture tasks (each a starting repo state + a prompt + a check command that
// must pass) and score them on success-rate, cost, and effort. It turns "did
// this prompt/model change help or hurt?" from a guess into a measurement.
//
// The harness is decoupled from the (heavy, network-bound) agent wiring via a
// pluggable RunFunc: the core — fixture setup, the check command, and scoring —
// is fully unit-testable offline, while cmd/eval supplies a RunFunc that drives
// the real agent for live measurement. Scores are sourced from the same
// session.TurnMeta data the agent already persists (cost, model-call steps,
// tokens), so the numbers match what the UI footers show.
package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/session"
)

// Task is one evaluation case: the agent is given Prompt in a fresh repo seeded
// with Files, and succeeds if Check (a shell command run in that repo) exits 0.
// An empty Check scores success purely on the run completing without error.
type Task struct {
	Name   string
	Prompt string
	// Files seeds the task's temp repo before the run (path → contents, relative
	// to the repo root; parent dirs are created).
	Files map[string]string
	// Check is a shell command run in the repo after the agent finishes; exit 0
	// means the task passed (e.g. "go test ./...", "go build ./...").
	Check string
}

// RunMetrics is one run's cost/effort, aggregated from the session's TurnMetas.
type RunMetrics struct {
	DurationMS int64
	CostUSD    float64
	Steps      int // total model calls (loop steps) across the run's turns
	InTokens   int
	OutTokens  int
}

// MetricsFromTurnMetas aggregates a finished session's persisted per-turn footers
// into a single RunMetrics — the same data the web UI shows per turn, summed
// across the whole run. This is the bridge the user asked for: scoring off the
// TurnMeta data the agent already records.
func MetricsFromTurnMetas(metas []session.TurnMeta) RunMetrics {
	var m RunMetrics
	for _, t := range metas {
		m.DurationMS += t.DurationMS
		m.CostUSD += t.CostUSD
		m.Steps += t.Calls
		m.InTokens += t.InTokens
		m.OutTokens += t.OutTokens
	}
	return m
}

// RunFunc executes one task's prompt against an agent rooted at repoDir and
// returns the run's metrics. It's pluggable so the harness is testable offline
// (inject a fake) and runnable live (cmd/eval drives the real agent).
type RunFunc func(ctx context.Context, task Task, repoDir string) (RunMetrics, error)

// Result is one task's outcome.
type Result struct {
	Task        string
	Passed      bool
	CheckOutput string // trimmed output of the Check command (on failure especially)
	Metrics     RunMetrics
	Err         error // non-nil when the run itself errored (agent failure, setup error)
}

// Summary is the scored outcome of a whole eval run.
type Summary struct {
	Results []Result
}

// Run executes every task in isolation: it creates a temp repo, seeds Files,
// invokes run, then runs the Check command to decide pass/fail, and returns a
// scored Summary. Tasks are independent; one failure never aborts the rest.
func Run(ctx context.Context, tasks []Task, run RunFunc) Summary {
	var sum Summary
	for _, task := range tasks {
		sum.Results = append(sum.Results, runOne(ctx, task, run))
	}
	return sum
}

func runOne(ctx context.Context, task Task, run RunFunc) Result {
	res := Result{Task: task.Name}
	dir, err := os.MkdirTemp("", "sqe-eval-")
	if err != nil {
		res.Err = fmt.Errorf("create temp repo: %w", err)
		return res
	}
	defer os.RemoveAll(dir)

	if err := seedFiles(dir, task.Files); err != nil {
		res.Err = fmt.Errorf("seed fixture: %w", err)
		return res
	}

	metrics, runErr := run(ctx, task, dir)
	res.Metrics = metrics
	if runErr != nil {
		res.Err = runErr
		return res // a run error is a failure; skip the check
	}

	ok, out := runCheck(ctx, task.Check, dir)
	res.Passed = ok
	res.CheckOutput = out
	return res
}

// seedFiles writes the fixture files into dir, creating parent directories.
func seedFiles(dir string, files map[string]string) error {
	// Deterministic order so a failure is reproducible.
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, []byte(files[rel]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runCheck runs the task's check command in the repo. An empty command passes
// (success = the run completed without error). ok is true on exit 0.
func runCheck(ctx context.Context, check, dir string) (ok bool, output string) {
	if strings.TrimSpace(check) == "" {
		return true, ""
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", check)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return err == nil, strings.TrimSpace(string(out))
}

// Passed counts the tasks that succeeded.
func (s Summary) Passed() int {
	n := 0
	for _, r := range s.Results {
		if r.Passed {
			n++
		}
	}
	return n
}

// Total is the number of tasks evaluated.
func (s Summary) Total() int { return len(s.Results) }

// TotalCostUSD sums the estimated dollar cost across all runs.
func (s Summary) TotalCostUSD() float64 {
	var c float64
	for _, r := range s.Results {
		c += r.Metrics.CostUSD
	}
	return c
}

// TotalSteps sums the model-call steps across all runs.
func (s Summary) TotalSteps() int {
	n := 0
	for _, r := range s.Results {
		n += r.Metrics.Steps
	}
	return n
}

// Scorecard renders a human-readable table: one row per task (pass/fail, steps,
// cost) followed by aggregate success-rate, total cost, and total steps. Stable
// and plain-text so it's diffable across runs in CI logs.
func (s Summary) Scorecard() string {
	var b strings.Builder
	b.WriteString("Eval scorecard\n")
	b.WriteString("──────────────\n")
	for _, r := range s.Results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "%-4s  %-28s  steps=%-3d  cost=$%.4f  in=%d out=%d\n",
			status, truncate(r.Task, 28), r.Metrics.Steps, r.Metrics.CostUSD, r.Metrics.InTokens, r.Metrics.OutTokens)
		if r.Err != nil {
			fmt.Fprintf(&b, "        error: %s\n", r.Err)
		} else if !r.Passed && r.CheckOutput != "" {
			fmt.Fprintf(&b, "        check: %s\n", firstLine(r.CheckOutput))
		}
	}
	b.WriteString("──────────────\n")
	rate := 0.0
	if s.Total() > 0 {
		rate = float64(s.Passed()) / float64(s.Total()) * 100
	}
	fmt.Fprintf(&b, "passed %d/%d (%.0f%%)  total cost $%.4f  total steps %d\n",
		s.Passed(), s.Total(), rate, s.TotalCostUSD(), s.TotalSteps())
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
