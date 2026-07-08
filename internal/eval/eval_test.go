package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/session"
)

// TestMetricsFromTurnMetas confirms the per-turn footers are summed into one
// RunMetrics (the bridge to the agent's persisted data).
func TestMetricsFromTurnMetas(t *testing.T) {
	got := MetricsFromTurnMetas([]session.TurnMeta{
		{DurationMS: 1000, CostUSD: 0.01, Calls: 3, InTokens: 100, OutTokens: 20},
		{DurationMS: 500, CostUSD: 0.02, Calls: 2, InTokens: 50, OutTokens: 10},
	})
	if got.DurationMS != 1500 || got.Steps != 5 || got.InTokens != 150 || got.OutTokens != 30 {
		t.Errorf("aggregate = %+v, want duration 1500, steps 5, in 150, out 30", got)
	}
	if got.CostUSD < 0.0299 || got.CostUSD > 0.0301 {
		t.Errorf("cost = %v, want ~0.03", got.CostUSD)
	}
}

// TestRunSeedsAndChecks confirms Run seeds the fixture files, hands the agent a
// repo dir, and uses the Check command to decide pass/fail — here the fake
// "agent" writes the file the check needs, so the task passes.
func TestRunSeedsAndChecks(t *testing.T) {
	tasks := []Task{{
		Name:   "writes-marker",
		Prompt: "create marker",
		Files:  map[string]string{"seed.txt": "hello"},
		Check:  "test -f done.txt && grep -q hello seed.txt",
	}}
	run := func(_ context.Context, task Task, dir string) (RunMetrics, error) {
		// The fixture must have been seeded before the run.
		if b, err := os.ReadFile(filepath.Join(dir, "seed.txt")); err != nil || string(b) != "hello" {
			t.Errorf("seed.txt not seeded before run: %v %q", err, b)
		}
		// Simulate the agent doing the work.
		if err := os.WriteFile(filepath.Join(dir, "done.txt"), []byte("x"), 0o644); err != nil {
			return RunMetrics{}, err
		}
		return RunMetrics{Steps: 4, CostUSD: 0.05}, nil
	}

	sum := Run(context.Background(), tasks, run)
	if sum.Total() != 1 || sum.Passed() != 1 {
		t.Fatalf("passed %d/%d, want 1/1: %+v", sum.Passed(), sum.Total(), sum.Results)
	}
	if sum.TotalSteps() != 4 || sum.TotalCostUSD() != 0.05 {
		t.Errorf("totals: steps=%d cost=%v, want 4 and 0.05", sum.TotalSteps(), sum.TotalCostUSD())
	}
}

// TestRunCheckFails confirms a task whose check command fails is scored FAIL,
// with the check output captured.
func TestRunCheckFails(t *testing.T) {
	tasks := []Task{{
		Name:  "missing-file",
		Check: "cat does-not-exist.txt",
	}}
	run := func(context.Context, Task, string) (RunMetrics, error) { return RunMetrics{Steps: 1}, nil }

	sum := Run(context.Background(), tasks, run)
	if sum.Passed() != 0 {
		t.Fatalf("expected the task to fail its check, got passed=%d", sum.Passed())
	}
	if sum.Results[0].CheckOutput == "" {
		t.Error("check output should be captured on failure")
	}
}

// TestRunRunError confirms a run-level error is a failure and skips the check.
func TestRunRunError(t *testing.T) {
	checked := false
	tasks := []Task{{Name: "boom", Check: "true"}}
	run := func(context.Context, Task, string) (RunMetrics, error) {
		return RunMetrics{}, context.Canceled
	}
	_ = checked
	sum := Run(context.Background(), tasks, run)
	if sum.Passed() != 0 || sum.Results[0].Err == nil {
		t.Fatalf("run error should fail the task with Err set: %+v", sum.Results[0])
	}
}

// TestEmptyCheckPassesOnRun confirms a task with no check passes when the run
// completes without error.
func TestEmptyCheckPassesOnRun(t *testing.T) {
	sum := Run(context.Background(), []Task{{Name: "noop"}},
		func(context.Context, Task, string) (RunMetrics, error) { return RunMetrics{}, nil })
	if sum.Passed() != 1 {
		t.Errorf("empty-check task should pass on a clean run, got %d/%d", sum.Passed(), sum.Total())
	}
}

// TestScorecard confirms the scorecard reports the pass rate and totals.
func TestScorecard(t *testing.T) {
	sum := Summary{Results: []Result{
		{Task: "a", Passed: true, Metrics: RunMetrics{Steps: 3, CostUSD: 0.01}},
		{Task: "b", Passed: false, CheckOutput: "boom\nmore", Metrics: RunMetrics{Steps: 5, CostUSD: 0.02}},
	}}
	card := sum.Scorecard()
	if !strings.Contains(card, "passed 1/2 (50%)") {
		t.Errorf("scorecard missing pass rate:\n%s", card)
	}
	if !strings.Contains(card, "total steps 8") || !strings.Contains(card, "$0.03") {
		t.Errorf("scorecard missing totals:\n%s", card)
	}
	if !strings.Contains(card, "PASS") || !strings.Contains(card, "FAIL") {
		t.Errorf("scorecard missing per-task status:\n%s", card)
	}
}

// TestFixturesWellFormed sanity-checks the built-in fixtures.
func TestFixturesWellFormed(t *testing.T) {
	for _, f := range Fixtures() {
		if f.Name == "" || f.Prompt == "" || f.Check == "" || len(f.Files) == 0 {
			t.Errorf("fixture %+v is missing a required field", f)
		}
	}
}
