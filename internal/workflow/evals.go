package workflow

// Evals for the Agent Builder: run a workflow over a dataset of inputs, capture
// each run's final output, and (optionally) LLM-judge it against an expectation —
// returning per-case pass/fail + an aggregate score. Modeled on OpenAI Agent
// Builder's evals. Reuses the same execution engine (Run) and judge helper, so
// it's consistent with Preview/Run and unit-testable with fakes.

import (
	"context"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// EvalCase is one dataset row: an input and an optional expectation. When Expect
// is empty, the case passes if the run completes without error (a smoke test);
// otherwise an LLM-judge grades the output against Expect.
type EvalCase struct {
	Input  string `json:"input"`
	Expect string `json:"expect,omitempty"`
}

// EvalCaseResult is the graded outcome of one case.
type EvalCaseResult struct {
	Input   string  `json:"input"`
	Expect  string  `json:"expect,omitempty"`
	Output  string  `json:"output"`
	Passed  bool    `json:"passed"`
	Score   float64 `json:"score"`            // 0..1 (LLM-judge); 1/0 for smoke cases
	Reason  string  `json:"reason,omitempty"` // judge rationale
	Error   string  `json:"error,omitempty"`  // run error, if any
	CostUSD float64 `json:"costUSD,omitempty"`
}

// EvalSummary aggregates a run over the dataset.
type EvalSummary struct {
	Total    int              `json:"total"`
	Passed   int              `json:"passed"`
	AvgScore float64          `json:"avgScore"`
	CostUSD  float64          `json:"costUSD"`
	Cases    []EvalCaseResult `json:"cases"`
}

// passThreshold is the LLM-judge score at/above which a case counts as passed.
const passThreshold = 0.7

// RunEvals executes wf once per case via run (the same NodeFunc used by
// Preview/Run), then grades each output. judgeClient may be nil to skip LLM
// grading (cases then pass on a clean run). emitCase, when non-nil, is called as
// each case finishes (for streaming progress). ctx cancellation stops the sweep.
func RunEvals(ctx context.Context, wf *Workflow, cases []EvalCase, run NodeFunc, judgeClient llm.Streamer, base *config.Config, emitCase func(EvalCaseResult)) EvalSummary {
	sum := EvalSummary{Total: len(cases)}
	for _, c := range cases {
		if err := ctx.Err(); err != nil {
			break
		}
		cr := runOneEval(ctx, wf, c, run, judgeClient, base)
		sum.Cases = append(sum.Cases, cr)
		if cr.Passed {
			sum.Passed++
		}
		sum.AvgScore += cr.Score
		sum.CostUSD += cr.CostUSD
		if emitCase != nil {
			emitCase(cr)
		}
	}
	if len(sum.Cases) > 0 {
		sum.AvgScore /= float64(len(sum.Cases))
	}
	return sum
}

func runOneEval(ctx context.Context, wf *Workflow, c EvalCase, run NodeFunc, judgeClient llm.Streamer, base *config.Config) EvalCaseResult {
	cr := EvalCaseResult{Input: c.Input, Expect: c.Expect}

	// Capture the final output + summed cost from the trace.
	var finalOut string
	var cost float64
	emit := func(ev RunEvent) {
		switch ev.Type {
		case "node_completed":
			if ev.Usage != nil {
				cost += ev.Usage.CostUSD
			}
		case "workflow_completed":
			finalOut = ev.Output
		}
	}
	err := Run(ctx, wf, c.Input, run, emit, nil)
	cr.Output = finalOut
	cr.CostUSD = cost
	if err != nil {
		cr.Error = err.Error()
		cr.Passed = false
		cr.Score = 0
		return cr
	}

	// No expectation → smoke test: a clean run passes.
	if strings.TrimSpace(c.Expect) == "" {
		cr.Passed = true
		cr.Score = 1
		cr.Reason = "ran without error"
		return cr
	}

	// LLM-judge the output against the expectation.
	if judgeClient == nil {
		// No judge available: fall back to a substring/equality heuristic.
		ok := strings.Contains(strings.ToLower(finalOut), strings.ToLower(strings.TrimSpace(c.Expect)))
		cr.Passed = ok
		if ok {
			cr.Score = 1
		}
		cr.Reason = "heuristic match (no judge)"
		return cr
	}

	sys := "You are a strict evaluator. Given an EXPECTATION and an ACTUAL output, score how well ACTUAL satisfies EXPECTATION " +
		"from 0.0 (not at all) to 1.0 (fully). Reply with ONLY JSON: {\"score\":0.0,\"reason\":\"...\"}. No prose."
	user := "EXPECTATION:\n" + c.Expect + "\n\nACTUAL:\n" + finalOut
	var verdict struct {
		Score  float64 `json:"score"`
		Reason string  `json:"reason"`
	}
	if _, jerr := judge(ctx, judgeClient, base, sys, user, &verdict); jerr != nil {
		cr.Error = "judge: " + jerr.Error()
		return cr
	}
	cr.Score = verdict.Score
	cr.Reason = verdict.Reason
	cr.Passed = verdict.Score >= passThreshold
	return cr
}
