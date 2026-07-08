package agent

// @-mention dispatch: typing "@<name>" in a turn, where <name> matches an enabled
// sub-agent persona, an external worker, or a saved workflow, is a shortcut for
// "hand this to that". It doesn't run anything directly — it appends a directive
// telling the model to dispatch via the `task` (persona), `delegate` (worker), or
// `run_workflow` (workflow) tool. The model still drives the call, so approval +
// streaming work exactly as if it chose the tool itself. This lives in the agent
// core so BOTH the web sessions and the TUI get the same behavior.
//
// Skills are intentionally NOT dispatched here — an @skill is left in the text as
// a hint the model acts on via the `skill` tool (it already advertises them).
// File mentions win: a token that resolves to a real file is never a mention.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pysty1995/moony/internal/attach"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/subagents"
)

// mentionKind classifies how a matched @name should dispatch.
type mentionKind int

const (
	mentionPersona mentionKind = iota
	mentionWorker
	mentionWorkflow
)

type resolvedMention struct {
	name string
	kind mentionKind
}

// expandTurnMentions augments userInput for any @-mentions it contains. It (1)
// inlines the content of @<path> mentions that resolve to a real file — so the
// model gets a file the user pointed at without spending a read_file round-trip —
// and (2) appends a dispatch directive for @persona / @worker / @workflow tokens.
// The original text (including the @ tokens) is preserved at the top so the
// transcript reads naturally. The dispatch directive is computed from the
// ORIGINAL prose, so a mention that happens to appear inside an inlined file
// can't trigger a spurious dispatch. Returns userInput unchanged when there's
// nothing to expand. planMode skips persona mentions (the `task` tool is hidden);
// workers/workflows/files still resolve. Lives in the agent core so BOTH the web
// sessions and the TUI get identical behavior.
func (a *Agent) expandTurnMentions(userInput string) string {
	if strings.IndexByte(userInput, '@') < 0 {
		return userInput
	}
	wd := ""
	if a.cfg != nil {
		wd = a.cfg.WorkingDir
	}
	// Dispatch directive, derived from the ORIGINAL prose (before file contents
	// are appended below).
	directive := ""
	personas, workers, workflows := a.mentionTargets()
	if len(personas)+len(workers)+len(workflows) > 0 {
		if dispatched := expandMentionDirective(userInput, personas, workers, workflows, a.PlanMode(), wd); dispatched != userInput {
			directive = strings.TrimPrefix(dispatched, userInput)
		}
	}
	// Inline @file contents, then append the dispatch directive after them.
	return a.expandFileMentions(userInput) + directive
}

// mentionStartBoundary reports whether the byte before an '@' lets it begin a
// mention (start of string, whitespace, or an opening bracket).
func mentionStartBoundary(prev byte) bool {
	switch prev {
	case ' ', '\t', '\n', '\r', '(', '[':
		return true
	}
	return false
}

// mentionEndsAt reports whether the rune AFTER a candidate name is a legitimate
// mention terminator — end of string (r==0) or a non-name, non-path character.
// So "@triage" matches "triage" but "@triageX" doesn't; "@Superpower Workflow."
// ends at the period. A following "/" or "\" means the @token is a PATH prefix
// (e.g. "@cmd/main.go"), so it's NOT a mention — file mentions win there.
func mentionEndsAt(r rune) bool {
	if r == 0 {
		return true
	}
	if r == '/' || r == '\\' {
		return false // a path like "@cmd/main.go" — not an agent mention
	}
	return !(unicode.IsLetter(r) || unicode.IsDigit(r))
}

// expandMentionDirective is the pure core of @-mention dispatch (no *Agent), so
// it's directly testable. It appends a dispatch directive for any
// @persona/@worker/@workflow mention in text, matched against the known name
// sets — LONGEST name first, so a multi-word name (e.g. "Superpower Workflow")
// wins over a same-prefixed shorter one and isn't truncated at a space. planMode
// skips persona mentions (the `task` tool is hidden then). A single-token name
// that resolves to a real file under wd is treated as a file mention, not a
// mention (file mentions win); a name containing a slash is never a mention.
func expandMentionDirective(text string, personas, workers, workflows map[string]bool, planMode bool, wd string) string {
	// All candidates, longest name first so the most specific match wins.
	type cand struct {
		name string
		kind mentionKind
	}
	var cands []cand
	for n := range personas {
		cands = append(cands, cand{n, mentionPersona})
	}
	for n := range workers {
		cands = append(cands, cand{n, mentionWorker})
	}
	for n := range workflows {
		cands = append(cands, cand{n, mentionWorkflow})
	}
	if len(cands) == 0 {
		return text
	}
	sort.Slice(cands, func(i, j int) bool {
		if len(cands[i].name) != len(cands[j].name) {
			return len(cands[i].name) > len(cands[j].name) // longest first
		}
		return cands[i].name < cands[j].name // stable
	})

	seen := map[string]bool{}
	var found []resolvedMention
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 && !mentionStartBoundary(text[i-1]) {
			continue
		}
		rest := text[i+1:]
		for _, c := range cands {
			if c.name == "" || !strings.HasPrefix(rest, c.name) {
				continue
			}
			r, _ := utf8.DecodeRuneInString(rest[len(c.name):]) // 0 at end of string
			if !mentionEndsAt(r) {
				continue // e.g. "@triageX" must not match "triage"
			}
			if c.kind == mentionPersona && planMode {
				continue // task tool hidden in plan mode → not a dispatch target
			}
			// A slash means it's a path; a single-token name that's a real file is a
			// file mention. (Multi-word names can't be a path token, so this only
			// affects bare identifiers.)
			if strings.ContainsAny(c.name, "/\\") || mentionResolvesToFile(c.name, wd) {
				continue
			}
			if !seen[c.name] {
				seen[c.name] = true
				found = append(found, resolvedMention{name: c.name, kind: c.kind})
			}
			i += len(c.name) // skip past this match so a later @ inside it isn't re-scanned
			break            // longest match at this @ wins
		}
	}
	if len(found) == 0 {
		return text
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].name < found[j].name })

	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\n\n--- agent dispatch (from @-mentions) ---\n")
	for _, f := range found {
		switch f.kind {
		case mentionWorker:
			fmt.Fprintf(&b, "Use the `delegate` tool with worker %q to carry out this request. ", f.name)
		case mentionWorkflow:
			fmt.Fprintf(&b, "Use the `run_workflow` tool with workflow %q to carry out this request. ", f.name)
		default:
			fmt.Fprintf(&b, "Use the `task` tool with agent %q to carry out this request. ", f.name)
		}
	}
	b.WriteString("Pass it the full context it needs (it does not see this conversation), then summarize what it returned.")
	return b.String()
}

// mentionTargets returns the enabled persona, worker, and workflow names an
// @-mention may dispatch to (each as a set).
func (a *Agent) mentionTargets() (personas, workers, workflows map[string]bool) {
	personas, workers, workflows = map[string]bool{}, map[string]bool{}, map[string]bool{}
	wd := ""
	if a.cfg != nil {
		wd = a.cfg.WorkingDir
	}
	mgmt := manage.Load(wd)
	for _, p := range manage.Filter(subagents.Load(wd), func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled) {
		if p.SubagentCapable() { // primary-only profiles (build/plan) aren't delegation targets
			personas[p.Name] = true
		}
	}
	if a.workers != nil {
		for _, c := range a.workers.Configs() { // enabled only
			workers[c.Name] = true
		}
	}
	if a.workflowNames != nil {
		for _, n := range a.workflowNames() {
			if n = strings.TrimSpace(n); n != "" {
				workflows[n] = true
			}
		}
	}
	return personas, workers, workflows
}

// mentionResolvesToFile reports whether ref points at an existing regular file
// (or attachable file) under wd — so a filename is never hijacked as a mention.
func mentionResolvesToFile(ref, wd string) bool {
	full := ref
	if !filepath.IsAbs(full) && wd != "" {
		full = filepath.Join(wd, ref)
	}
	if attach.Attachable(full) {
		return true
	}
	fi, err := os.Stat(full)
	return err == nil && !fi.IsDir()
}

// --- @file mentions: inline a mentioned file's content so the model needn't
// spend a read_file round-trip on a file the user explicitly pointed at. ---

// maxFileMentions bounds how many @file mentions are inlined per turn, so a
// prompt full of @paths can't blow up the context.
const maxFileMentions = 10

// isPathTokenByte reports whether b can appear in an @file path token. It's
// deliberately conservative: identifier characters plus the path punctuation
// (/, \, ., -, _), so it stops at whitespace, quotes, brackets, and the like.
func isPathTokenByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '/', '\\', '.', '-', '_':
		return true
	}
	return false
}

// scanFileMentions returns the @<path> tokens in text, in order, at mention
// boundaries (start of string / after whitespace or an opening bracket). It does
// NOT touch the filesystem — expandFileMentions resolves each. Trailing sentence
// punctuation is trimmed, so "@cmd/main.go." yields "cmd/main.go".
func scanFileMentions(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 && !mentionStartBoundary(text[i-1]) {
			continue
		}
		j := i + 1
		for j < len(text) && isPathTokenByte(text[j]) {
			j++
		}
		tok := strings.TrimRight(text[i+1:j], ".,;:!?")
		i = j - 1 // resume after this token (the loop's i++ moves past the last byte)
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// expandFileMentions appends the content of any @<path> mention that resolves to
// a readable text file under the workspace, so the model has it without a
// read_file call. The prose (including the @path token) is preserved; contents
// are added in an "attached files" section, each bounded by capToolOutput (so a
// large source file is outlined, a large text file head+tailed). Non-file tokens
// (e.g. an @persona name or an email's domain), binary/oversize files, and
// out-of-workspace paths are skipped, leaving the prose untouched.
func (a *Agent) expandFileMentions(userInput string) string {
	if a.tools == nil || strings.IndexByte(userInput, '@') < 0 {
		return userInput
	}
	max := 0
	if a.cfg != nil {
		max = a.cfg.MaxToolOutputBytes
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, p := range scanFileMentions(userInput) {
		if seen[p] {
			continue
		}
		content, ok := a.tools.ReadForMention(p)
		if !ok {
			continue
		}
		seen[p] = true
		input, _ := json.Marshal(struct {
			Path string `json:"path"`
		}{p})
		capped := a.capToolOutput("read_file", input, content, max)
		if b.Len() == 0 {
			b.WriteString("\n\n--- attached files (from @-mentions; inlined below — no need to read them again) ---\n")
		}
		fmt.Fprintf(&b, "\n===== %s =====\n%s\n", p, capped)
		if len(seen) >= maxFileMentions {
			break
		}
	}
	return userInput + b.String()
}
