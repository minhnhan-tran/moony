package tui

// Maintainer announcement banners in the TUI. On startup we fetch the relay's
// public announcement feed (GET /api/relay) and surface the current
// announcements as a notice in the transcript. We deliberately re-show them on
// each launch (rather than suppressing after one view) so a live notice isn't
// missed — matching the web banner, which stays until dismissed.

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pysty1995/moony/internal/selfupdate"
)

type announceMsg struct{ items []selfupdate.Announcement }

// announceTickMsg fires on an interval to live-refresh announcements.
type announceTickMsg struct{}

// announceRefreshInterval is how often the TUI re-polls the relay for new
// maintainer announcements. Low-frequency, and only unseen ids are surfaced, so
// a poll never re-prints a notice already shown this session.
const announceRefreshInterval = 2 * time.Minute

// announceCmd fetches announcements from the relay in the background (best
// effort; a failure or no relay simply yields no message).
func (m Model) announceCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		items, err := selfupdate.Announcements(ctx)
		if err != nil || len(items) == 0 {
			return nil
		}
		return announceMsg{items: items}
	}
}

// announceRefreshCmd schedules the next announcement poll; the announceTickMsg
// handler re-fetches (announceCmd) and re-arms this, so it loops for the session.
func announceRefreshCmd() tea.Cmd {
	return tea.Tick(announceRefreshInterval, func(time.Time) tea.Msg { return announceTickMsg{} })
}

// applyAnnouncements adds any NEW announcements to the transcript, deduped by id
// so the live-refresh poll only surfaces notices not shown yet this session.
// Oldest first so the newest lands nearest the prompt; the body renders as
// terminal markdown. Seen state is per-process, so every launch re-shows the
// current set (a live maintainer notice is never missed).
func (m *Model) applyAnnouncements(items []selfupdate.Announcement) {
	if len(items) == 0 {
		return
	}
	if m.seenAnnounce == nil {
		m.seenAnnounce = map[string]bool{}
	}
	added := false
	for i := len(items) - 1; i >= 0; i-- {
		a := items[i]
		if a.ID == "" || m.seenAnnounce[a.ID] {
			continue
		}
		m.seenAnnounce[a.ID] = true
		m.trans.add(entry{kind: entryRaw, text: m.renderAnnouncement(a)})
		added = true
	}
	if added {
		m.refresh()
	}
}

// renderAnnouncement builds one announcement block for the transcript: a styled
// header (level emoji + title) followed by the body rendered as terminal
// markdown. Emitted as entryRaw so the pre-applied header style and Glamour's
// ANSI both survive (entryInfo would re-style the whole block and clobber them).
func (m *Model) renderAnnouncement(a selfupdate.Announcement) string {
	tag := "📣 Announcement"
	switch a.Level {
	case "warn":
		tag = "⚠️  Announcement"
	case "critical":
		tag = "🚨 Announcement"
	}
	header := tag
	if a.Title != "" {
		header += ": " + a.Title
	}
	out := infoStyle.Render(header)
	if a.Body != "" {
		out += "\n" + m.trans.markdown(a.Body)
	}
	return out
}
