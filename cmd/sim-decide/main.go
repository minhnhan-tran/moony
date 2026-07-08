// Command sim-decide is the "brain" of the browser simulation bot: a one-shot
// decision maker that role-plays a human USER of moony. The Playwright
// harness (test/browser) drives the real web UI in a real browser, and whenever
// it needs a human decision — the opening message, an answer to an ask_user
// question, an approve/deny, or the next follow-up — it pipes the conversation
// state to this tool on stdin and reads back one JSON action on stdout.
//
// The persona brain lives here (Go) rather than in the TypeScript harness on
// purpose: it reuses internal/llm, so Claude access (Floodgate / SQE Proxy auth)
// is already solved by the same client the agent itself uses. Node never has to
// authenticate to the model.
//
// Protocol (stdin → stdout, both single JSON objects):
//
//	in : {"persona","goal","history":[{"role","text"}],"pending":{"kind","questions","tool","summary","diff"}}
//	out: {"action","text","labels","groups","reason"}
//
// action is one of: message | answer | answerGroup | approve | deny | stop.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// qspec is one ask_user question as seen by the persona (a flattened QuestionRequest).
type qspec struct {
	Question  string   `json:"question"`
	Options   []string `json:"options"`
	Multiple  bool     `json:"multiple"`
	AllowText bool     `json:"allowText"`
}

// pending describes what (if anything) the agent is currently blocked on.
type pending struct {
	Kind      string  `json:"kind"` // none | question | approval
	Questions []qspec `json:"questions,omitempty"`
	Tool      string  `json:"tool,omitempty"`
	Summary   string  `json:"summary,omitempty"`
	Diff      string  `json:"diff,omitempty"`
}

// turn is one line of the conversation transcript scraped from the UI.
type turn struct {
	Role string `json:"role"` // user | agent
	Text string `json:"text"`
}

// request is the decision input piped in on stdin.
type request struct {
	Persona string  `json:"persona"`
	Goal    string  `json:"goal"`
	History []turn  `json:"history"`
	Pending pending `json:"pending"`
}

// action is the decision written out on stdout.
type action struct {
	Action string     `json:"action"`           // message | answer | answerGroup | approve | deny | stop
	Text   string     `json:"text,omitempty"`   // message: the next user message; answer: optional free-text
	Labels []string   `json:"labels,omitempty"` // answer: chosen option labels for a single question
	Groups [][]string `json:"groups,omitempty"` // answerGroup: one label-slice per question
	Reason string     `json:"reason,omitempty"` // short rationale (logging)
}

// validActions is the closed set the harness knows how to execute.
var validActions = map[string]bool{
	"message": true, "answer": true, "answerGroup": true,
	"approve": true, "deny": true, "stop": true,
}

func main() {
	model := flag.String("model", "", "override the model id for persona decisions (default: config.Model; a cheap model like Haiku is fine)")
	timeout := flag.Duration("timeout", 2*time.Minute, "max time for one decision")
	flag.Parse()

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		die("read stdin: %v", err)
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		die("decode request: %v", err)
	}

	cfg := config.Load()
	if *model != "" {
		cfg.Model = *model
	}
	client := llm.NewClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	act, err := decide(ctx, client, req)
	if err != nil {
		die("decide: %v", err)
	}
	out, err := json.Marshal(act)
	if err != nil {
		die("encode action: %v", err)
	}
	fmt.Println(string(out))
}

// decide runs one persona LLM call and returns the parsed, validated action.
func decide(ctx context.Context, client *llm.Client, req request) (action, error) {
	msgs := []llm.Message{llm.UserText(buildUserPrompt(req))}
	// Thinking off: persona decisions are short and cheap; no tools — it can't act.
	resp, err := client.StreamMessagesNoThink(ctx, buildSystemPrompt(), msgs, nil, llm.Handler{})
	if err != nil {
		return action{}, err
	}
	var b strings.Builder
	for _, blk := range resp.Blocks {
		if blk.Type == llm.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return parseAction(b.String())
}

// buildSystemPrompt states the persona brain's contract: role-play a user and
// emit exactly one JSON action.
func buildSystemPrompt() string {
	return strings.Join([]string{
		"You are simulating a REAL HUMAN USER interacting with an AI coding agent through its web UI, for automated end-to-end testing.",
		"You are NOT the coding agent. You do not write code or call tools; you behave like the person giving instructions and making decisions.",
		"Stay in character per the provided persona and pursue the provided goal across multiple turns.",
		"",
		"On each call you receive the conversation so far and, optionally, something the agent is waiting on (a question or an approval).",
		"Respond with EXACTLY ONE JSON object and nothing else (no prose, no markdown fences). Schema:",
		`  {"action": "...", "text": "...", "labels": ["..."], "groups": [["..."]], "reason": "..."}`,
		"action MUST be one of:",
		`  "message"     - send the next user message (put it in "text"). Use when the agent is idle/finished and it's your turn to speak.`,
		`  "answer"      - answer a SINGLE ask_user question: put chosen option labels in "labels" (exact strings from the offered options) and/or a free-text reply in "text".`,
		`  "answerGroup" - answer a GROUPED ask_user (multiple questions): "groups" has one array per question, in order, each holding that question's chosen option labels and/or a free-text string.`,
		`  "approve"     - approve the pending tool action.`,
		`  "deny"        - reject the pending tool action.`,
		`  "stop"        - end the session (goal met, or you're stuck / going in circles). Put why in "reason".`,
		"",
		"Guidance: keep messages concise and human — it's fine to be slightly underspecified, like a real user.",
		"When answering a question, choose realistically for your goal; only free-text when it adds needed detail.",
		"Approve edits that clearly serve your goal and look safe; deny destructive or clearly-wrong actions.",
		"Call \"stop\" once the goal is achieved (say so in reason) or after the agent is clearly done — don't chat forever.",
	}, "\n")
}

// buildUserPrompt serializes the request into the decision prompt.
func buildUserPrompt(req request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PERSONA:\n%s\n\n", strings.TrimSpace(req.Persona))
	fmt.Fprintf(&b, "GOAL:\n%s\n\n", strings.TrimSpace(req.Goal))

	b.WriteString("CONVERSATION SO FAR:\n")
	if len(req.History) == 0 {
		b.WriteString("(none yet — the session just opened)\n")
	}
	for _, t := range req.History {
		who := "USER"
		if t.Role == "agent" {
			who = "AGENT"
		}
		fmt.Fprintf(&b, "[%s] %s\n", who, strings.TrimSpace(t.Text))
	}
	b.WriteString("\n")

	switch req.Pending.Kind {
	case "question":
		b.WriteString("THE AGENT IS ASKING YOU")
		if len(req.Pending.Questions) > 1 {
			fmt.Fprintf(&b, " %d QUESTIONS (answer with action \"answerGroup\", one array per question in order):\n", len(req.Pending.Questions))
		} else {
			b.WriteString(" A QUESTION (answer with action \"answer\"):\n")
		}
		for i, q := range req.Pending.Questions {
			fmt.Fprintf(&b, "  Q%d: %s\n", i+1, strings.TrimSpace(q.Question))
			if len(q.Options) > 0 {
				fmt.Fprintf(&b, "      options: %s\n", strings.Join(q.Options, " | "))
			}
			fmt.Fprintf(&b, "      multiple=%v allowText=%v\n", q.Multiple, q.AllowText)
		}
	case "approval":
		b.WriteString("THE AGENT WANTS APPROVAL FOR A TOOL ACTION (answer with action \"approve\" or \"deny\"):\n")
		if req.Pending.Tool != "" {
			fmt.Fprintf(&b, "  tool: %s\n", req.Pending.Tool)
		}
		if req.Pending.Summary != "" {
			fmt.Fprintf(&b, "  summary: %s\n", strings.TrimSpace(req.Pending.Summary))
		}
		if d := strings.TrimSpace(req.Pending.Diff); d != "" {
			fmt.Fprintf(&b, "  diff:\n%s\n", clip(d, 4000))
		}
	default:
		b.WriteString("THE AGENT IS IDLE — it's your turn. Send the next message (action \"message\"), or \"stop\" if the goal is met.\n")
	}

	b.WriteString("\nRespond with ONE JSON action object now.")
	return b.String()
}

// parseAction extracts the JSON object from the model's reply and validates it.
func parseAction(s string) (action, error) {
	obj := extractJSONObject(s)
	if obj == "" {
		return action{}, fmt.Errorf("no JSON object in model reply: %q", clip(strings.TrimSpace(s), 200))
	}
	var a action
	if err := json.Unmarshal([]byte(obj), &a); err != nil {
		return action{}, fmt.Errorf("bad JSON action %q: %w", clip(obj, 200), err)
	}
	a.Action = strings.TrimSpace(a.Action)
	if !validActions[a.Action] {
		return action{}, fmt.Errorf("unknown action %q", a.Action)
	}
	return a, nil
}

// extractJSONObject returns the substring from the first '{' to the last '}',
// tolerating any prose or markdown fences the model wraps around it.
func extractJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}

// clip truncates s to at most n bytes, adding an ellipsis when it cuts.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sim-decide: "+format+"\n", args...)
	os.Exit(1)
}
