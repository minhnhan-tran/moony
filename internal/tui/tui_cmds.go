// tui_cmds.go — tea.Cmd factories and their result-message plumbing.

package tui

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pysty1995/moony/internal/attach"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/doctor"
	"github.com/pysty1995/moony/internal/selfupdate"
	"github.com/pysty1995/moony/internal/tools"
)

// fetchModelsCmd asynchronously queries Floodgate for the available models
// (merged across both endpoints), delivering a modelsLoadedMsg when done.
func (m Model) fetchModelsCmd() tea.Cmd {
	ag := m.agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, err := ag.ListModels(ctx)
		return modelsLoadedMsg{models: models, err: err}
	}
}

// compactCmd asynchronously runs Compact on the agent (it makes a model call to
// summarize older messages), delivering a compactDoneMsg when finished.
// usageCmd asynchronously fetches the Floodgate quota+spend snapshot and
// delivers a usageDoneMsg. The personal endpoint is used unless a project
// token is configured; the agent already picks the right one.
func (m Model) usageCmd() tea.Cmd {
	ag := m.agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := ag.Usage(ctx)
		return usageDoneMsg{resp: resp, err: err}
	}
}

// stageAttachmentCmd resolves an attachment off the event loop (so a slow iWork
// conversion via the converter service doesn't freeze the terminal) and delivers
// an attachStagedMsg. display is the path as the user typed it, for messages.
func (m Model) stageAttachmentCmd(full, display string) tea.Cmd {
	return func() tea.Msg {
		a, err := attach.FromPath(full)
		if err != nil {
			return attachStagedMsg{line: "Could not attach " + display + ": " + err.Error()}
		}
		return attachStagedMsg{att: a, ok: true}
	}
}

// doctorCmd asynchronously runs the environment/config diagnostics and delivers
// a doctorDoneMsg with the rendered report. The live auth check goes through the
// agent (which mints a Floodgate token via AppleConnect).
func (m Model) doctorCmd() tea.Cmd {
	cfg := m.cfg
	ag := m.agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		report := doctor.Run(ctx, cfg, ag.CheckAuth)
		return doctorDoneMsg{report: report.String()}
	}
}

// updateCmd checks for a newer release and, if this build is behind, downloads
// and installs it (replacing the running binary). On success the handler quits
// and main re-execs the new binary. Runs async so the UI stays responsive.
func (m Model) updateCmd() tea.Cmd {
	return func() tea.Msg {
		// The version check is a tiny GET — give it a short timeout so an
		// unreachable/slow relay fails fast with a clear message instead of
		// burning the whole budget.
		vctx, vcancel := context.WithTimeout(context.Background(), 30*time.Second)
		latest, err := selfupdate.LatestVersion(vctx)
		vcancel()
		if err != nil {
			return updateDoneMsg{err: err}
		}
		if !config.VersionLess(config.Version, latest) {
			return updateDoneMsg{already: true, version: latest}
		}
		// The binary is ~30 MB and the relay may sit behind a slow internal VIP;
		// give the download a generous budget so a slow-but-progressing transfer
		// isn't killed with "context deadline exceeded" (a 3-minute cap was too
		// tight on slow links).
		actx, acancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer acancel()
		if _, err := selfupdate.Apply(actx); err != nil {
			return updateDoneMsg{err: err}
		}
		return updateDoneMsg{updated: true, version: latest}
	}
}

// refreshQuotaCmd is the silent counterpart to usageCmd: it updates the
// persistent status-bar quota without echoing anything to the transcript. We
// kick this off from Init() and after every TurnDone so the displayed
// quota is always close to current. Failures are swallowed (quotaErr is
// recorded for diagnostics in /tokens) so we never spam the transcript.
func (m Model) refreshQuotaCmd() tea.Cmd {
	ag := m.agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resp, err := ag.Usage(ctx)
		return quotaRefreshMsg{resp: resp, err: err}
	}
}

// gitInfoMsg carries the current branch + dirty count for the status panel.
type gitInfoMsg struct {
	branch   string
	dirty    int
	repo     bool
	ahead    int
	behind   int
	upstream string
}

// refreshGitCmd fetches the working-tree branch and dirty count off the UI
// goroutine (git status is fast but still a subprocess).
func (m Model) refreshGitCmd() tea.Cmd {
	dir := m.cfg.WorkingDir
	return func() tea.Msg {
		branch, dirty, ok := tools.GitInfo(dir)
		ahead, behind, upstream, _ := tools.GitAheadBehind(dir)
		return gitInfoMsg{branch: branch, dirty: dirty, repo: ok, ahead: ahead, behind: behind, upstream: upstream}
	}
}

func (m Model) compactCmd() tea.Cmd {
	ag := m.agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		dropped, err := ag.Compact(ctx)
		return compactDoneMsg{dropped: dropped, err: err}
	}
}

// checkpointCmd asynchronously refreshes the session checkpoint (a model call),
// delivering a checkpointDoneMsg when finished.
func (m Model) checkpointCmd() tea.Cmd {
	ag := m.agent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		status, err := ag.WriteCheckpoint(ctx)
		return checkpointDoneMsg{status: status, err: err}
	}
}

// fetchMCPPromptCmd asynchronously fetches a server-rendered prompt template
// (passing no arguments since the slash-arg shape varies per server) and then
// submits the rendered text — with the user's args appended — as a turn.
func (m Model) fetchMCPPromptCmd(idx int, args string) tea.Cmd {
	prompt := m.mcpPrompts[idx]
	display := prompt.SlashCommand()
	if args != "" {
		display += " " + args
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		body, err := prompt.Client.GetPrompt(ctx, prompt.Prompt.Name, nil)
		if err != nil {
			return mcpPromptMsg{display: display, err: err}
		}
		if args != "" {
			body += "\n\n" + args
		}
		return mcpPromptMsg{display: display, body: body}
	}
}

// playDoneSoundCmd returns a command that plays a short completion chime, or nil
// when sound is disabled (or unsupported on this OS). It runs as a background
// Bubble Tea command so it never blocks the UI.
func playDoneSoundCmd() tea.Cmd {
	if !soundEnabled() || runtime.GOOS != "darwin" {
		return nil
	}
	return func() tea.Msg {
		// afplay ships with macOS; Glass is a soft "ding". Best effort.
		_ = exec.Command("afplay", "/System/Library/Sounds/Glass.aiff").Run()
		return nil
	}
}

// waitForStream blocks on the agent channel and surfaces the next event.
func waitForStream(ch <-chan streamMsg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}
