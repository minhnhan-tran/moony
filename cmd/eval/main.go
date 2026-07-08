// Command eval runs the agent regression-evaluation harness: it drives the real
// agent through a set of fixture tasks and prints a scorecard (success-rate,
// cost, steps). It's the LIVE counterpart to the offline-tested internal/eval
// core — run it manually or in a dedicated, credentialed CI job to catch
// quality/cost regressions from prompt or model changes. It is not part of
// `go test ./...` (which only compiles it and unit-tests the harness logic).
//
// Usage:
//
//	go run ./cmd/eval            # run the built-in fixtures against the default model
//	SQE_MODEL=... go run ./cmd/eval
//
// Exit status is non-zero if any task fails, so CI can gate on it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/eval"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/tools"
)

func main() {
	perTask := flag.Duration("timeout", 8*time.Minute, "per-task timeout")
	flag.Parse()

	cfg := config.Load()
	client := llm.NewClient(cfg)
	fmt.Printf("eval: model=%s  tasks=%d\n\n", cfg.Model, len(eval.Fixtures()))

	// The live RunFunc builds a fresh agent rooted at each task's temp repo, runs
	// the prompt unattended, and reports the run's metrics from the session's
	// in-memory TurnMetas (no disk store needed). Verification is disabled so we
	// measure the agent's own output, and approval is automatic so it never
	// blocks waiting for a human.
	run := func(ctx context.Context, task eval.Task, dir string) (eval.RunMetrics, error) {
		c := *cfg
		c.WorkingDir = dir
		c.VerifyCommand = ""
		c.ApprovalPolicy = config.ApprovalAuto
		registry := tools.Default(dir, c.MaxToolOutputBytes)
		sess := &session.Session{}
		ag := agent.New(&c, client, registry, nil, sess)

		cctx, cancel := context.WithTimeout(ctx, *perTask)
		defer cancel()
		err := ag.Run(cctx, task.Prompt, func(agent.Event) {})
		return eval.MetricsFromTurnMetas(sess.TurnMetas), err
	}

	sum := eval.Run(context.Background(), eval.Fixtures(), run)
	fmt.Print(sum.Scorecard())

	if sum.Passed() < sum.Total() {
		os.Exit(1)
	}
}
