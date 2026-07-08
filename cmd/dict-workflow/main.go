package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/workflow"
)

func main() {
	delayFlag := flag.Duration("delay", 2*time.Millisecond, "delay multiplier for API simulation")
	flag.Parse()

	word := "learning"
	args := flag.Args()
	if len(args) > 0 {
		word = args[0]
	}

	fmt.Printf("========================================================================\n")
	fmt.Printf("🚀 STARTING PARALLEL DICTIONARY WORKFLOW DEMO\n")
	fmt.Printf("========================================================================\n")
	fmt.Printf("Word to Translate: %q\n", word)
	fmt.Printf("Delay Multiplier:  %v\n", *delayFlag)
	fmt.Printf("Workflow Nodes:    [start] -> [fork] -> [translate_es, translate_fr, translate_de] -> [join] -> [aggregator] -> [end]\n")
	fmt.Printf("========================================================================\n\n")

	wf := workflow.NewDictionaryWorkflow()
	nodeFunc := workflow.GetDictionaryNodeFunc(*delayFlag)

	// Capture and format events as they arrive in real-time
	emit := func(ev workflow.RunEvent) {
		// Format prefix depending on which concurrent branch the event belongs to
		prefix := "       "
		if ev.GroupID == "fork" {
			switch ev.Branch {
			case 0:
				prefix = " [ES]  "
			case 1:
				prefix = "      [FR]  "
			case 2:
				prefix = "           [DE]  "
			default:
				prefix = fmt.Sprintf(" [B%d] ", ev.Branch)
			}
		}

		switch ev.Type {
		case "node_started":
			fmt.Printf("%s► Node [%s] started\n", prefix, ev.NodeID)
		case "node_delta":
			// Replace trailing or intermediate newlines to preserve indentation
			cleanDelta := strings.ReplaceAll(ev.Delta, "\n", "\n"+prefix+"  ")
			if strings.HasSuffix(cleanDelta, "\n"+prefix+"  ") {
				cleanDelta = strings.TrimSuffix(cleanDelta, "\n"+prefix+"  ")
			}
			fmt.Printf("%s  %s", prefix, cleanDelta)
		case "node_completed":
			if ev.NodeID == "aggregator" {
				fmt.Printf("\n%s✔ Node [%s] completed with result:\n\n%s\n\n", prefix, ev.NodeID, ev.Output)
			} else {
				fmt.Printf("%s✔ Node [%s] completed -> %q\n", prefix, ev.NodeID, ev.Output)
			}
		case "node_failed":
			fmt.Printf("%s❌ Node [%s] FAILED: %s\n", prefix, ev.NodeID, ev.Error)
		}
	}

	ctx := context.Background()
	start := time.Now()
	err := workflow.Run(ctx, wf, word, nodeFunc, emit, nil)
	duration := time.Since(start)

	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Workflow execution failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("========================================================================\n")
	fmt.Printf("🎉 WORKFLOW EXECUTION COMPLETED SUCCESSFULLY!\n")
	fmt.Printf("Total Elapsed Time: %v\n", duration)
	fmt.Printf("========================================================================\n")
}
