package tui

// Agent mentions: typing "@<name>" in a message, where <name> matches a
// configured sub-agent persona or an external worker, is a shortcut for "hand
// this to that agent". Like Claude Code's @-agent invocation, it doesn't run the
// agent directly — instead it appends a directive to the prompt that tells the
// model to dispatch the request via the `task` tool (personas) or the `delegate`
// tool (workers). The model still drives the call, so approval and streaming work
// exactly as if it had chosen the tool itself.
//
// File mentions (expandMentions / attachmentMentions) and agent mentions share
// the same "@token" surface but never collide: an agent mention only fires when
// the token matches a known agent name *and* doesn't resolve to a real file.

import (
	"sort"
	"strings"

	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/workflow"
)

// agentMentionNames returns the sets of currently-enabled persona and worker
// names that an @-mention may target. Used to expand @agent mentions in a
// prompt. Both maps may be empty.
func (m Model) agentMentionNames() (personas, workers map[string]bool) {
	personas = map[string]bool{}
	workers = map[string]bool{}

	mgmt := manage.Load(m.cfg.WorkingDir)
	for _, p := range manage.Filter(subagents.Load(m.cfg.WorkingDir),
		func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled) {
		if !p.SubagentCapable() {
			continue // primary-only profiles (build/plan) aren't delegation targets
		}
		personas[p.Name] = true
	}
	if mgr := m.agent.Workers(); mgr != nil {
		for _, c := range mgr.Configs() { // Configs() = enabled workers only
			workers[c.Name] = true
		}
	}
	return personas, workers
}

// mentionInfo is one @-mention candidate (an enabled persona or worker), with a
// short description, for the live suggestion dropdown / hints.
type mentionInfo struct {
	Name string
	Kind string // "agent" | "worker" | "skill" | "deep"
	Desc string
}

// agentMentionList returns the enabled persona + worker mention candidates,
// sorted by name. It mirrors agentMentionNames (the names mention-expansion will
// accept) but keeps descriptions for the live @-mention suggestion list and the
// web viewers' dropdown (published via the bridge).
func (m Model) agentMentionList() []mentionInfo {
	var out []mentionInfo
	wd := m.cfg.WorkingDir
	mgmt := manage.Load(wd)
	for _, p := range manage.Filter(subagents.Load(wd),
		func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled) {
		if !p.SubagentCapable() {
			continue // primary-only profiles (build/plan) aren't delegation targets
		}
		out = append(out, mentionInfo{Name: p.Name, Kind: "agent", Desc: p.Description})
	}
	if m.agent != nil {
		if mgr := m.agent.Workers(); mgr != nil {
			for _, c := range mgr.Configs() { // enabled only
				desc := "worker"
				if c.Backend != "" {
					desc = "worker · " + c.Backend
				}
				out = append(out, mentionInfo{Name: c.Name, Kind: "worker", Desc: desc})
			}
		}
	}
	// Skills are shown in the dropdown too (kind "skill") so they're discoverable;
	// the type tag colors them in the web viewer. (Mention expansion only dispatches
	// agents/workers; an @skill is left as a prompt hint the model acts on.)
	for _, sk := range manage.Filter(skills.Load(wd),
		func(sk skills.Skill) string { return sk.Name }, mgmt.SkillEnabled) {
		out = append(out, mentionInfo{Name: sk.Name, Kind: "skill", Desc: sk.Description})
	}
	// Saved Agent Builder workflows — dispatched via the run_workflow tool, the
	// @workflow analog of @agent/@worker.
	if wfs, err := workflow.Open(); err == nil {
		if list, err := wfs.List(); err == nil {
			for _, wf := range list {
				if n := strings.TrimSpace(wf.Name); n != "" {
					out = append(out, mentionInfo{Name: n, Kind: "workflow", Desc: wf.Description})
				}
			}
		}
	}
	// "@ultrathink" is a deep-dive MODE (kind "deep"), not an agent to dispatch:
	// it spawns parallel sub-agents to investigate, then synthesizes. Listed so
	// it's discoverable in the @-mention dropdown.
	out = append(out, mentionInfo{Name: "ultrathink", Kind: "deep", Desc: "Deep dive — spawn parallel sub-agents, then synthesize"})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// expandPrompt applies both file @-mentions and agent @-mentions to a user
// message, producing the prompt actually sent to the model. The transcript
// keeps showing the user's original text.
//
// Agent mentions are resolved against the *original* text (not the
// file-expanded prompt) so an inlined file's contents can't accidentally
// trigger a stray @agent match; the dispatch directive is then appended after
// any attached-files section.
func (m Model) expandPrompt(text string) string {
	// File mentions only. The @persona/@worker/@workflow dispatch directive is
	// now appended by the agent core (Agent.expandTurnMentions in RunWith), shared
	// with the web sessions — so the TUI no longer adds it here (doing so would
	// double the directive).
	return expandMentions(text, m.cfg.WorkingDir)
}

// completeAtMention completes a trailing "@<partial>" token in the input against
// the enabled mention names — personas, workers, workflows, and skills (Tab
// autocompletion, mirroring slash commands). Returns true when the input ended
// in an @-token, so the Tab handler consumes the key instead of inserting a tab.
func (m *Model) completeAtMention() bool {
	cands := m.agentMentionList() // personas + workers + workflows + skills + ultrathink
	if len(cands) == 0 {
		return false
	}
	names := make([]string, 0, len(cands))
	for _, c := range cands {
		names = append(names, c.Name)
	}
	newVal, handled := completeMention(m.input.Value(), names)
	if !handled {
		return false
	}
	if newVal != m.input.Value() {
		m.input.SetValue(newVal)
		m.input.CursorEnd()
		m.syncInputHeight()
	}
	return true
}

// completeMention completes a trailing "@<partial>" token in val against names
// (case-insensitive prefix). It returns the new value and whether val ended in a
// mention token at all (so the caller can consume Tab). A single match fills the
// full name + a space; multiple matches extend to their common prefix. Pure, for
// testing. Only a trailing token is completed (nothing after it), so callers can
// safely move the cursor to the end.
func completeMention(val string, names []string) (string, bool) {
	at, partial, ok := trailingMention(val)
	if !ok {
		return val, false
	}
	lp := strings.ToLower(partial)
	var matches []string
	for _, n := range names {
		if strings.HasPrefix(strings.ToLower(n), lp) {
			matches = append(matches, n)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return val, true // an @-token, but nothing matches: consume Tab, no change
	case 1:
		return val[:at+1] + matches[0] + " ", true
	default:
		if cp := commonStrPrefix(matches); len(cp) > len(partial) {
			return val[:at+1] + cp, true
		}
		return val, true
	}
}

// trailingMention reports whether val ends in an "@<partial>" mention token (the
// cursor sits at the end) and returns the index of the '@' plus the partial. It
// is the single source of truth for the boundary rules shared by Tab completion
// (completeMention) and the live suggestion list (mentionHints): the '@' must
// start the string or follow whitespace/"(["—so "email@x" isn't a mention—and
// the partial must not contain whitespace or a path separator.
func trailingMention(val string) (at int, partial string, ok bool) {
	at = strings.LastIndexByte(val, '@')
	if at < 0 {
		return -1, "", false
	}
	if at > 0 && !isMentionBoundary(val[at-1]) {
		return -1, "", false // "email@x" etc. — not a mention
	}
	partial = val[at+1:]
	if strings.ContainsAny(partial, " \t\r\n/\\") {
		return -1, "", false // token already finished (or path-like)
	}
	return at, partial, true
}

// isMentionBoundary reports whether b can precede an "@" that starts a mention,
// matching the mentionPattern's leading class ((^|[\s(\[])).
func isMentionBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '(', '[':
		return true
	}
	return false
}

// commonStrPrefix returns the longest common prefix of ss.
func commonStrPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}
