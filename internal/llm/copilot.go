package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// GitHub Copilot backend.
//
// Unlike every other provider (a plain HTTP API), Copilot is driven through the
// official github/copilot-sdk Go SDK, which spawns and talks to the local
// `copilot` CLI over JSON-RPC. The CLI is itself an autonomous agent: it plans,
// calls its OWN built-in tools (file read/write, shell, etc.), and streams
// assistant text back. So a moony "turn" here maps to one SDK session send:
//   - moony's own tools are NOT offered to the session (autonomous mode);
//   - the CLI's permission prompts are auto-approved non-interactively;
//   - we stream the assistant's text (and tool-activity/reasoning as deltas)
//     into moony's Handler, and return a plain end_turn Response with no
//     tool_use blocks — moony's agent loop therefore just renders the reply and
//     stops, rather than executing tools itself.
//
// This keeps Copilot's agent loop and moony's from fighting over tool execution,
// at the cost that moony's approval policy / sandbox / tool logging do not apply
// to what the Copilot CLI does on disk.

func (c *Client) usesCopilot() bool {
	return c.cfg != nil && provider.UsesCopilotSDK(c.cfg.LLMProvider)
}

// resolveCopilotModel picks the model id for the session: a per-call ctx
// override, then the session model, else "" (let the CLI use its own default).
func (c *Client) resolveCopilotModel(ctx context.Context) string {
	if m := modelFrom(ctx); m != "" {
		return m
	}
	if c.cfg != nil {
		return strings.TrimSpace(c.cfg.Model)
	}
	return ""
}

// newCopilotClient builds and starts an SDK client from the config (CLI path +
// GitHub token). The caller must Stop() it. Kept small so StreamMessages and
// the auth/model probes share identical connection setup.
func (c *Client) newCopilotClient(ctx context.Context) (*copilot.Client, error) {
	opts := &copilot.ClientOptions{}
	if c.cfg != nil {
		if p := strings.TrimSpace(c.cfg.CopilotCLIPath); p != "" {
			opts.Connection = copilot.StdioConnection{Path: p}
		}
		if tok := strings.TrimSpace(c.cfg.CopilotToken); tok != "" {
			opts.GitHubToken = tok
		}
		if c.cfg.WorkingDir != "" {
			opts.WorkingDirectory = c.cfg.WorkingDir
		}
	}
	cl := copilot.NewClient(opts)
	if err := cl.Start(ctx); err != nil {
		return nil, fmt.Errorf("start copilot CLI: %w (is the `copilot` CLI installed and on PATH, or MN_COPILOT_CLI_PATH set?)", err)
	}
	return cl, nil
}

// streamCopilot runs one autonomous Copilot turn. tools is ignored on purpose
// (autonomous mode). The last message's user text is sent as the prompt; prior
// turns are replayed into the session so it has conversational context.
func (c *Client) streamCopilot(ctx context.Context, system string, messages []Message, _ []Tool, h Handler) (*Response, error) {
	cl, err := c.newCopilotClient(ctx)
	if err != nil {
		return nil, err
	}
	defer cl.Stop()

	cfg := &copilot.SessionConfig{
		ClientName: "moony",
		// Auto-approve the CLI's own permission prompts so the turn runs
		// non-interactively; moony's approval gate does not cover Copilot's
		// built-in tools in autonomous mode.
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
	}
	if m := c.resolveCopilotModel(ctx); m != "" {
		cfg.Model = m
	}
	if system != "" {
		cfg.SystemMessage = &copilot.SystemMessageConfig{Mode: "append", Content: system}
	}
	if eff := copilotEffort(c.reasoningLevelFor(ctx)); eff != "" {
		cfg.ReasoningEffort = eff
	}

	session, err := cl.CreateSession(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create copilot session: %w", err)
	}

	// Replay conversation history (everything but the final user message) so
	// the session has context. The final user message becomes the prompt.
	prompt, history := splitCopilotHistory(messages)
	for _, turn := range history {
		if turn.role == RoleUser && turn.text != "" {
			if _, err := session.SendAndWait(ctx, copilot.MessageOptions{Prompt: turn.text}); err != nil {
				return nil, fmt.Errorf("replay copilot history: %w", err)
			}
		}
	}

	// Stream deltas from the live session into moony's Handler. A mutex guards
	// the accumulated text against the SDK's event goroutine.
	var (
		mu       sync.Mutex
		fullText strings.Builder
		usage    Usage
	)
	session.On(func(ev copilot.SessionEvent) {
		switch d := ev.Data.(type) {
		case *copilot.AssistantMessageDeltaData:
			if d.DeltaContent == "" {
				return
			}
			mu.Lock()
			fullText.WriteString(d.DeltaContent)
			mu.Unlock()
			if h.OnTextDelta != nil {
				h.OnTextDelta(d.DeltaContent)
			}
		case *copilot.AssistantReasoningDeltaData:
			if h.OnThinkingDelta != nil && d.DeltaContent != "" {
				h.OnThinkingDelta(d.DeltaContent)
			}
		case *copilot.ToolExecutionStartData:
			if h.OnToolUse != nil && d.ToolName != "" {
				h.OnToolUse(d.ToolName)
			}
		case *copilot.AssistantUsageData:
			mu.Lock()
			if d.InputTokens != nil {
				usage.InputTokens += int(*d.InputTokens)
			}
			if d.OutputTokens != nil {
				usage.OutputTokens += int(*d.OutputTokens)
			}
			mu.Unlock()
		}
	})

	reply, err := session.SendAndWait(ctx, copilot.MessageOptions{Prompt: prompt})
	if err != nil {
		return nil, fmt.Errorf("copilot turn: %w", err)
	}

	// Prefer the streamed text; fall back to the final assistant message if no
	// deltas arrived (e.g. a runtime that only emits the terminal message).
	mu.Lock()
	text := fullText.String()
	mu.Unlock()
	if text == "" && reply != nil {
		if d, ok := reply.Data.(*copilot.AssistantMessageData); ok {
			text = d.Content
		}
	}

	return &Response{
		Blocks:     []ContentBlock{{Type: BlockText, Text: text}},
		StopReason: "end_turn",
		Usage:      usage,
	}, nil
}

// reasoningLevelFor returns the effective reasoning level for a call: a ctx
// effort override, else the session's configured level.
func (c *Client) reasoningLevelFor(ctx context.Context) string {
	if lvl := effortFrom(ctx); lvl != "" {
		return lvl
	}
	if c.cfg != nil {
		return c.cfg.ReasoningLevel
	}
	return ""
}

// copilotEffort maps a moony reasoning level to a Copilot ReasoningEffort value.
// The SDK accepts "low", "medium", "high", "xhigh"; "off"/"auto"/unknown yield
// "" (let the CLI decide).
func copilotEffort(level string) string {
	switch config.NormalizeReasoning(level) {
	case config.ReasoningMinimal, config.ReasoningLow:
		return "low"
	case config.ReasoningMedium:
		return "medium"
	case config.ReasoningHigh:
		return "high"
	case config.ReasoningXHigh, config.ReasoningMax:
		return "xhigh"
	default: // off / auto
		return ""
	}
}

type copilotTurn struct {
	role string
	text string
}

// splitCopilotHistory returns the final user prompt and the preceding turns.
// Only plain text is carried (Copilot's CLI manages its own tool state), and
// tool_result/assistant tool_use blocks are flattened to their text.
func splitCopilotHistory(messages []Message) (prompt string, history []copilotTurn) {
	turns := make([]copilotTurn, 0, len(messages))
	for _, m := range messages {
		turns = append(turns, copilotTurn{role: m.Role, text: flattenText(m)})
	}
	// The final user turn is the prompt for this send.
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].role == RoleUser {
			prompt = turns[i].text
			history = append(turns[:i:i], turns[i+1:]...)
			return prompt, history
		}
	}
	return "", turns
}

// flattenText concatenates the textual content of a message's blocks.
func flattenText(m Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		switch blk.Type {
		case BlockText:
			b.WriteString(blk.Text)
		case BlockToolResult:
			if blk.Content != "" {
				b.WriteString(blk.Content)
			}
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// checkCopilotAuth verifies the CLI can be started (and thus that it's installed
// and a GitHub token/credentials are available).
func (c *Client) checkCopilotAuth(ctx context.Context) error {
	cl, err := c.newCopilotClient(ctx)
	if err != nil {
		return err
	}
	_ = cl.Stop()
	return nil
}

// listCopilotModels returns the models the CLI advertises for the account.
func (c *Client) listCopilotModels(ctx context.Context) ([]config.ModelInfo, error) {
	cl, err := c.newCopilotClient(ctx)
	if err != nil {
		return nil, err
	}
	defer cl.Stop()

	models, err := cl.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list copilot models: %w", err)
	}
	out := make([]config.ModelInfo, 0, len(models))
	for _, m := range models {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, config.ModelInfo{ID: m.ID, Name: name})
	}
	return out, nil
}
