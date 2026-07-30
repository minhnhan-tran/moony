package agent

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/llm"
)

// maybeAutoTitle generates a concise, model-written title for a freshly-started
// session and persists it, replacing the heuristic title derived from the first
// user message. It runs at most once per session, only when persistence is
// enabled and MN_AUTO_TITLE isn't disabled. Best effort: any failure leaves the
// heuristic title in place.
//
// It is called synchronously at the end of the first completed turn, so it never
// races the agent's single-threaded history/persistence (a new turn can't start
// until Run returns).
func (a *Agent) maybeAutoTitle(ctx context.Context, emit func(Event)) {
	if a.titled || a.store == nil || a.sess == nil || a.client == nil || !autoTitleEnabled() {
		return
	}
	a.titled = true // attempt only once per session, whatever the outcome
	title, err := a.generateTitle(ctx)
	if err != nil || title == "" {
		return
	}
	a.sess.Title = title
	_ = a.store.Save(a.sess)
	emit(TitleUpdate{Title: title})
}

// generateTitle asks the model (no tools) for a short title summarizing the
// session, from the first user message and the assistant's first reply.
func (a *Agent) generateTitle(ctx context.Context) (string, error) {
	user := firstRoleText(a.messages, llm.RoleUser)
	if strings.TrimSpace(user) == "" {
		return "", nil
	}
	const sys = "You write a short, specific title for a coding session. " +
		"Reply with ONLY the title: 3 to 6 words, Title Case, no surrounding quotes, " +
		"no trailing punctuation, and no markdown."

	var b strings.Builder
	b.WriteString("Write a title for this session.\n\nUser's first request:\n")
	b.WriteString(clipText(user, 1500))
	if asst := firstRoleText(a.messages, llm.RoleAssistant); strings.TrimSpace(asst) != "" {
		b.WriteString("\n\nAssistant's reply (for context):\n")
		b.WriteString(clipText(asst, 1000))
	}
	b.WriteString("\n\nTitle:")

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := a.streamAux(ctx, sys, []llm.Message{llm.UserText(b.String())})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, blk := range resp.Blocks {
		if blk.Type == llm.BlockText {
			out.WriteString(blk.Text)
		}
	}
	return sanitizeTitle(out.String()), nil
}

// autoTitleEnabled reports whether model-generated session titles are on (the
// default). Set MN_AUTO_TITLE=0/off/false/no to fall back to the first-message
// heuristic.
func autoTitleEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_AUTO_TITLE"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// firstRoleText returns the first non-empty text block for the given role.
func firstRoleText(messages []llm.Message, role string) string {
	for _, m := range messages {
		if m.Role != role {
			continue
		}
		for _, c := range m.Content {
			if c.Type == llm.BlockText && strings.TrimSpace(c.Text) != "" {
				return c.Text
			}
		}
	}
	return ""
}

// sanitizeTitle trims the model's reply into a clean one-line title: first line
// only, no "Title:" prefix, no surrounding quotes/markdown, no trailing
// punctuation, collapsed whitespace, and capped length.
func sanitizeTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) // first line only
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "Title:"))
	s = strings.Trim(s, "\"'`*_ ")
	s = strings.TrimRight(s, ".!,;:")
	s = strings.Join(strings.Fields(s), " ") // collapse internal whitespace
	const max = 72
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

// clipText truncates s to at most n bytes, dropping any partial trailing rune.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// clipTextTail truncates s to at most n bytes, keeping the most RECENT (trailing)
// portion and dropping any partial leading rune. Use it when the tail is the
// valuable part (e.g. the latest state of a session transcript).
func clipTextTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + strings.ToValidUTF8(s[len(s)-n:], "")
}

// SetTitle updates the active session's title.
func (a *Agent) SetTitle(title string) error {
	if a.sess == nil {
		return os.ErrNotExist
	}
	a.sess.Title = title
	return a.store.Rename(a.sess.ID, title)
}

// SetPinned pins or unpins the active session.
func (a *Agent) SetPinned(pinned bool) error {
	if a.sess == nil {
		return os.ErrNotExist
	}
	a.sess.Pinned = pinned
	return a.store.SetPinned(a.sess.ID, pinned)
}
