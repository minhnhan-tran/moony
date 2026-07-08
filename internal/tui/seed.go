package tui

import (
	"strings"

	"github.com/pysty1995/moony/internal/llm"
)

// seedEntries reconstructs transcript entries from persisted conversation
// history so a resumed session shows its prior context. Tool calls are paired
// with their results by tool_use id.
func seedEntries(messages []llm.Message) []entry {
	// Collect tool results keyed by the tool_use id they answer.
	type result struct {
		output string
		isErr  bool
	}
	results := map[string]result{}
	for _, m := range messages {
		for _, c := range m.Content {
			if c.Type == llm.BlockToolResult {
				results[c.ToolUseID] = result{output: c.Content, isErr: c.IsError}
			}
		}
	}

	var entries []entry
	for _, m := range messages {
		switch m.Role {
		case llm.RoleUser:
			// Rebuild the prompt text plus an "[attached: …]" note for any
			// image/document/extracted-file blocks, mirroring the live display.
			var text strings.Builder
			var labels []string
			for _, c := range m.Content {
				switch c.Type {
				case llm.BlockText:
					if name, ok := extractedFileMarker(c.Text); ok {
						labels = append(labels, "file "+name)
					} else if c.Text != "" {
						if text.Len() > 0 {
							text.WriteString("\n")
						}
						text.WriteString(c.Text)
					}
				case llm.BlockImage:
					labels = append(labels, "image")
				case llm.BlockDocument:
					name := c.Title
					if name == "" {
						name = "document"
					}
					labels = append(labels, "PDF "+name)
				}
			}
			body := text.String()
			if len(labels) > 0 {
				note := "[attached: " + strings.Join(labels, ", ") + "]"
				if body == "" {
					body = note
				} else {
					body += "\n" + note
				}
			}
			if body != "" {
				entries = append(entries, entry{kind: entryUser, text: body})
			}
		case llm.RoleAssistant:
			for _, c := range m.Content {
				switch c.Type {
				case llm.BlockText:
					if c.Text != "" {
						entries = append(entries, entry{kind: entryAssistant, text: c.Text})
					}
				case llm.BlockThinking:
					if c.Thinking != "" {
						entries = append(entries, entry{kind: entryThinking, text: c.Thinking})
					}
				case llm.BlockRedactedThinking:
					entries = append(entries, entry{kind: entryThinking, text: "[redacted thinking]"})
				case llm.BlockToolUse:
					e := entry{
						kind:      entryTool,
						toolName:  c.Name,
						toolInput: string(c.Input),
						toolDone:  true,
					}
					if r, ok := results[c.ID]; ok {
						e.toolOutput = r.output
						e.toolErr = r.isErr
					}
					entries = append(entries, e)
				}
			}
		}
	}
	return entries
}

// extractedFileMarker recognizes the header attach.Attachment.Block() puts on
// text-extracted Office files ("--- attached file: NAME ---") and returns NAME,
// so a resumed session chips the file instead of replaying its extracted text.
func extractedFileMarker(text string) (string, bool) {
	const pre = "--- attached file: "
	if !strings.HasPrefix(text, pre) {
		return "", false
	}
	rest := text[len(pre):]
	if i := strings.Index(rest, " ---"); i >= 0 {
		return rest[:i], true
	}
	return "", false
}
