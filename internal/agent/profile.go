package agent

// Cross-session project profile: a model-distilled, persisted understanding of
// the CODEBASE — its purpose, layout, entry points, conventions, and gotchas —
// built incrementally from real sessions and reused across them, so a fresh
// session orients without re-discovering the repo. It is distinct from:
//   - the RepoMap (a mechanical, no-model file-tree map), and
//   - AGENTS.md/CLAUDE.md (user-authored instructions).
//
// It's stored in the GLOBAL config dir keyed by the working-dir path (never
// written into the repo, so it can't be accidentally committed or clobber a
// user's files), injected into the system prompt as cacheable orientation, and
// refreshed at most ~weekly per repo from the session transcript (merged with the
// prior profile). OPT-IN via config.ProjectProfile (MOONY_PROJECT_PROFILE): it adds
// auto-generated content to every turn's prompt, which can mislead if it goes
// stale, so it's off until the user turns it on.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

const (
	// profileRefreshDays throttles how often a repo's profile is re-distilled, so
	// the extra aux call is rare (most sessions just read the existing profile).
	profileRefreshDays = 7
	// profileMinMessages skips profiling trivial sessions that taught nothing
	// durable about the codebase.
	profileMinMessages = 8
	// maxProfileBytes caps the stored profile so it never bloats the system prompt.
	maxProfileBytes = 6000
	// maxProfileTranscriptBytes bounds the transcript fed to the distillation so a
	// long session's refresh stays cheap.
	maxProfileTranscriptBytes = 24000
)

// profileSlug derives a stable, filesystem-safe filename stem for a working dir:
// the basename (for human readability) plus a hash of the absolute path (for
// uniqueness across same-named dirs and to bound the length).
func profileSlug(workingDir string) string {
	abs, err := filepath.Abs(workingDir)
	if err != nil || abs == "" {
		abs = workingDir
	}
	sum := sha256.Sum256([]byte(abs))
	base := strings.Map(func(r rune) rune {
		if r == os.PathSeparator || r == '/' || r == ' ' || r == '.' {
			return '-'
		}
		return r
	}, filepath.Base(abs))
	base = strings.Trim(base, "-")
	if base == "" {
		base = "repo"
	}
	return base + "-" + hex.EncodeToString(sum[:])[:16]
}

// projectProfilePath returns the on-disk path of a working dir's profile.
func projectProfilePath(workingDir string) (string, error) {
	return config.GlobalConfigPath("profiles", profileSlug(workingDir)+".md")
}

// loadProjectProfile returns the persisted profile body for workingDir, or "" if
// none (or unreadable). A stale profile is still returned — it's better than
// nothing, and the refresh path updates it.
func loadProjectProfile(workingDir string) string {
	path, err := projectProfilePath(workingDir)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// projectProfileSection renders the profile as a system-prompt section, framed as
// fallible orientation (not ground truth) so the model verifies before relying on
// a specific detail. "" when there's no profile.
func projectProfileSection(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return "# Project profile (learned across sessions)\n\n" +
		"A distilled, durable orientation to THIS codebase, accrued from previous " +
		"sessions. Treat it as a helpful starting point, NOT ground truth — it can be " +
		"out of date, so verify against the live code before relying on a specific " +
		"detail.\n\n" + body + "\n\n"
}

// maybeRefreshProfile re-distills the repo's profile when it's missing or stale
// (older than profileRefreshDays), gated to non-trivial sessions. Best effort and
// throttled — any failure leaves the previous profile in place. Runs at a
// completed turn (after persist), like maybeCheckpoint. The refreshed file is
// picked up by the NEXT session (the current session's system prompt is left
// byte-stable so its prompt cache isn't disturbed mid-session).
func (a *Agent) maybeRefreshProfile(ctx context.Context, emit func(Event)) {
	if !a.cfg.ProjectProfile || a.client == nil {
		return
	}
	if len(a.messages) < profileMinMessages {
		return
	}
	path, err := projectProfilePath(a.cfg.WorkingDir)
	if err != nil {
		return
	}
	if fi, statErr := os.Stat(path); statErr == nil && time.Since(fi.ModTime()) < profileRefreshDays*24*time.Hour {
		return // still fresh; refresh at most ~weekly per repo
	}
	prof, err := a.writeProjectProfile(ctx, loadProjectProfile(a.cfg.WorkingDir))
	if err != nil || strings.TrimSpace(prof) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(prof)+"\n"), 0o644); err != nil {
		return
	}
	emit(Info{Text: "Updated the project profile (learned codebase notes for future sessions)."})
}

// writeProjectProfile asks the model (cheap, no-tools aux call) to distill durable
// codebase facts from the session transcript, merged with the existing profile.
func (a *Agent) writeProjectProfile(ctx context.Context, existing string) (string, error) {
	const sys = "You maintain a durable PROJECT PROFILE of a codebase: a compact, cross-session " +
		"orientation a new engineer (or coding agent) can read to get up to speed fast. From the " +
		"existing profile (if any) and a transcript of recent work in this repo, produce an UPDATED " +
		"profile capturing only DURABLE facts about the CODEBASE — never task-specific state:\n" +
		"- Purpose: what the project is, in a sentence or two.\n" +
		"- Layout: key directories/packages and what each is for.\n" +
		"- Entry points: main binaries/servers/commands and where they live.\n" +
		"- Conventions: build/test/lint commands, code style, patterns the project follows.\n" +
		"- Gotchas: non-obvious constraints, footguns, or rules learned the hard way.\n" +
		"Merge with the existing profile: keep facts that still hold, add newly-learned ones, drop " +
		"the obsolete. Be terse and concrete — short bullets with file/dir paths. Omit a section if " +
		"you have nothing solid for it. No preamble or closing remarks."

	transcript := clipTextTail(serialize(a.messages), maxProfileTranscriptBytes)
	var b strings.Builder
	if strings.TrimSpace(existing) != "" {
		b.WriteString("Existing project profile:\n\n")
		b.WriteString(existing)
		b.WriteString("\n\n---\n\n")
	}
	fmt.Fprintf(&b, "Recent work in repo %q (transcript):\n\n%s", a.cfg.WorkingDir, transcript)

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := a.streamAux(cctx, sys, []llm.Message{llm.UserText(b.String())})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, blk := range resp.Blocks {
		if blk.Type == llm.BlockText {
			out.WriteString(blk.Text)
		}
	}
	prof := strings.TrimSpace(out.String())
	if len(prof) > maxProfileBytes {
		prof = clipText(prof, maxProfileBytes)
	}
	return prof, nil
}
