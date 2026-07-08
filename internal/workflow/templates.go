package workflow

// Built-in node/workflow templates the Agent Builder offers as starting points.
// Each is a complete (nodes+edges) starter graph the UI clones into a new
// workflow, so users don't begin from a blank canvas. Positions are pre-laid-out
// left→right; ids are template-local (the client regenerates real ids on import,
// but these are valid as-is for a quick instantiate).

// Template is one starter graph.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
}

func n(id, typ string, x, y float64, data NodeData) Node {
	return Node{ID: id, Type: typ, Position: Position{X: x, Y: y}, Data: data}
}
func e(id, src, tgt, handle string) Edge {
	return Edge{ID: id, Source: src, Target: tgt, SourceHandle: handle}
}

// Templates returns the built-in starter graphs, in display order.
func Templates() []Template {
	return []Template{
		{
			ID: "single-agent", Name: "Single agent",
			Description: "Start → Agent → End. The simplest workflow: one LLM step.",
			Nodes: []Node{
				n("start", NodeStart, 80, 160, NodeData{Label: "Start"}),
				n("agent", NodeAgent, 320, 160, NodeData{Label: "Agent", Instructions: "You are a helpful assistant. Answer the user's request."}),
				n("end", NodeEnd, 560, 160, NodeData{Label: "End"}),
			},
			Edges: []Edge{e("e1", "start", "agent", ""), e("e2", "agent", "end", "")},
		},
		{
			ID: "guarded-agent", Name: "Guarded agent",
			Description: "Guardrails screen the input, then an agent answers. Blocks on PII/jailbreak.",
			Nodes: []Node{
				n("start", NodeStart, 80, 160, NodeData{Label: "Start"}),
				n("guard", NodeGuardrails, 300, 160, NodeData{Label: "Guardrails", GuardPII: true, GuardJailbreak: true, ActionOnViolation: "block"}),
				n("agent", NodeAgent, 540, 160, NodeData{Label: "Agent", Instructions: "Answer the request."}),
				n("end", NodeEnd, 780, 160, NodeData{Label: "End"}),
			},
			Edges: []Edge{e("e1", "start", "guard", ""), e("e2", "guard", "agent", ""), e("e3", "agent", "end", "")},
		},
		{
			ID: "router", Name: "Classify + route",
			Description: "An agent classifies the input, then an If/Else routes to one of two handler agents.",
			Nodes: []Node{
				n("start", NodeStart, 60, 200, NodeData{Label: "Start"}),
				n("classify", NodeAgent, 280, 200, NodeData{Label: "Classify", Instructions: "Classify the request as either \"billing\" or \"other\". Reply with just that one word.", OutputSchema: ""}),
				n("route", NodeIfElse, 520, 200, NodeData{Label: "Billing?", Condition: "{{lastOutput}} contains billing"}),
				n("billing", NodeAgent, 760, 120, NodeData{Label: "Billing agent", Instructions: "Handle the billing question."}),
				n("other", NodeAgent, 760, 300, NodeData{Label: "General agent", Instructions: "Handle the general question."}),
				n("end", NodeEnd, 1000, 200, NodeData{Label: "End"}),
			},
			Edges: []Edge{
				e("e1", "start", "classify", ""), e("e2", "classify", "route", ""),
				e("e3", "route", "billing", "if"), e("e4", "route", "other", "else"),
				e("e5", "billing", "end", ""), e("e6", "other", "end", ""),
			},
		},
		{
			ID: "approval-flow", Name: "Draft → approve → finalize",
			Description: "An agent drafts, a human approves, then an agent finalizes. Human-in-the-loop.",
			Nodes: []Node{
				n("start", NodeStart, 60, 160, NodeData{Label: "Start"}),
				n("draft", NodeAgent, 280, 160, NodeData{Label: "Draft", Instructions: "Draft a response to the request."}),
				n("approve", NodeApproval, 520, 160, NodeData{Label: "Review", Message: "Approve this draft?\n\n{{lastOutput}}"}),
				n("final", NodeAgent, 760, 160, NodeData{Label: "Finalize", Instructions: "Polish the approved draft into a final version."}),
				n("end", NodeEnd, 1000, 160, NodeData{Label: "End"}),
			},
			Edges: []Edge{
				e("e1", "start", "draft", ""), e("e2", "draft", "approve", ""),
				e("e3", "approve", "final", ""), e("e4", "final", "end", ""),
			},
		},
		{
			ID: "scrape-summarize", Name: "Scrape + summarize",
			Description: "A Firecrawl MCP tool scrapes a URL, then an agent summarizes it. (Requires the Firecrawl MCP server.)",
			Nodes: []Node{
				n("start", NodeStart, 60, 160, NodeData{Label: "Start (input = URL)"}),
				n("scrape", NodeMCP, 300, 160, NodeData{Label: "Scrape", Tool: "mcp.firecrawl.scrape", ToolArgs: "{\"url\":\"{{input}}\"}"}),
				n("sum", NodeAgent, 560, 160, NodeData{Label: "Summarize", Instructions: "Summarize the scraped page content in 5 bullet points."}),
				n("end", NodeEnd, 800, 160, NodeData{Label: "End"}),
			},
			Edges: []Edge{e("e1", "start", "scrape", ""), e("e2", "scrape", "sum", ""), e("e3", "sum", "end", "")},
		},
		{
			ID: "prompt-chaining", Name: "Prompt chaining",
			Description: "Decompose a task into a sequence of agent steps, each building on the last, with a quality gate after the first step. Outline → (on-topic?) → Draft → Polish.",
			Nodes: []Node{
				n("start", NodeStart, 60, 220, NodeData{Label: "Start (input = topic)"}),
				n("outline", NodeAgent, 280, 220, NodeData{Label: "Outline", Instructions: "You are an expert writer. If the requested topic is clear enough to outline, begin your reply with the line OUTLINE: and then give a concise, well-structured outline as short bullet points. If the topic is too vague or off-limits, reply with only the line CANNOT and a one-sentence reason.\n\nTopic: {{input}}"}),
				n("gate", NodeIfElse, 520, 220, NodeData{Label: "Outline OK?", Condition: "{{outline}} contains OUTLINE"}),
				n("draft", NodeAgent, 760, 140, NodeData{Label: "Draft", Instructions: "Write a full first draft from this outline. Cover every section; clear, engaging prose.\n\nOutline:\n{{outline}}"}),
				n("polish", NodeAgent, 1000, 140, NodeData{Label: "Polish", Instructions: "Polish this draft: tighten the prose, fix grammar, and improve flow and tone. Return the final version only.\n\nDraft:\n{{lastOutput}}"}),
				n("reject", NodeEnd, 760, 320, NodeData{Label: "Rejected"}),
				n("end", NodeEnd, 1240, 140, NodeData{Label: "End"}),
			},
			Edges: []Edge{
				e("e1", "start", "outline", ""), e("e2", "outline", "gate", ""),
				e("e3", "gate", "draft", "if"), e("e4", "gate", "reject", "else"),
				e("e5", "draft", "polish", ""), e("e6", "polish", "end", ""),
			},
		},
		{
			ID: "evaluator-optimizer", Name: "Evaluator–optimizer",
			Description: "Generate, then critique and revise in a loop until the work passes (or the loop bound is hit). An agent drafts, an evaluator agent grades it PASS/REVISE, and a Loop sends REVISE back for another pass.",
			Nodes: []Node{
				n("start", NodeStart, 60, 240, NodeData{Label: "Start (input = task)"}),
				n("draft", NodeAgent, 280, 240, NodeData{Label: "Draft / revise", Instructions: "Produce the best response to the task. If an evaluator's feedback is provided below, address every point and improve the previous attempt.\n\nTask:\n{{input}}\n\nEvaluator feedback (if any):\n{{evaluate}}"}),
				n("evaluate", NodeAgent, 540, 240, NodeData{Label: "Evaluate", Instructions: "You are a strict reviewer. Assess the attempt below against the task: {{input}}.\n\nReply on the FIRST line with exactly PASS (it fully meets the task) or REVISE (it does not). On the following lines, give specific, actionable feedback.\n\nAttempt:\n{{lastOutput}}"}),
				n("loop", NodeWhile, 800, 240, NodeData{Label: "Needs work?", Condition: "{{evaluate}} contains REVISE", MaxIterations: 3}),
				n("end", NodeEnd, 1060, 320, NodeData{Label: "End"}),
			},
			Edges: []Edge{
				e("e1", "start", "draft", ""), e("e2", "draft", "evaluate", ""),
				e("e3", "evaluate", "loop", ""),
				e("e4", "loop", "draft", "continue"), e("e5", "loop", "end", "break"),
			},
		},
		{
			ID: "parallel-reduce", Name: "Fan-out / reduce",
			Description: "Run several agents on the same input concurrently, then synthesize their outputs. A Parallel node fans out to three perspectives, a Join waits for all three and concatenates them, and a final agent reduces them into one answer.",
			Nodes: []Node{
				n("start", NodeStart, 60, 260, NodeData{Label: "Start (input = task)"}),
				n("fork", NodeParallel, 280, 260, NodeData{Label: "Fan out"}),
				n("p1", NodeAgent, 520, 120, NodeData{Label: "Pro", Instructions: "Argue in FAVOR of the proposal. Give the strongest case for it.\n\nProposal:\n{{input}}"}),
				n("p2", NodeAgent, 520, 260, NodeData{Label: "Con", Instructions: "Argue AGAINST the proposal. Give the strongest case against it.\n\nProposal:\n{{input}}"}),
				n("p3", NodeAgent, 520, 400, NodeData{Label: "Risks", Instructions: "List the key risks and unknowns of the proposal as short bullets.\n\nProposal:\n{{input}}"}),
				n("join", NodeJoin, 780, 260, NodeData{Label: "Gather", JoinMode: "concat"}),
				n("reduce", NodeAgent, 1000, 260, NodeData{Label: "Synthesize", Instructions: "Below are three independent analyses (for, against, risks) of the same proposal, separated by ---. Weigh them and give a balanced recommendation with a clear verdict.\n\n{{lastOutput}}"}),
				n("end", NodeEnd, 1240, 260, NodeData{Label: "End"}),
			},
			Edges: []Edge{
				e("e1", "start", "fork", ""),
				e("e2", "fork", "p1", ""), e("e3", "fork", "p2", ""), e("e4", "fork", "p3", ""),
				e("e5", "p1", "join", ""), e("e6", "p2", "join", ""), e("e7", "p3", "join", ""),
				e("e8", "join", "reduce", ""), e("e9", "reduce", "end", ""),
			},
		},
	}
}
