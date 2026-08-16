package agent

// Snapshots: a small undo log that records the prior contents of every file the
// agent is about to write or edit. Each entry is tagged with the user-turn id
// so /undo can revert just the most recent turn's changes.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pysty1995/moony/internal/tools"
)

// fileSnapshot is the prior state of a single file before a mutating tool ran.
// missing=true means the file did not exist before the change (so undo deletes it).
type fileSnapshot struct {
	path    string
	turn    int
	prior   []byte
	missing bool
}

// snapshotStore is an in-memory undo buffer. It dedupes per (turn, path) so
// repeated edits to the same file in one turn revert to the original, not to
// the last intermediate state.
type snapshotStore struct {
	mu      sync.Mutex
	entries []fileSnapshot
	turn    int
	// opaqueTurn is the turn in which a mutating tool ran whose targets could NOT be
	// enumerated (bash and friends: `sed -i`, a python heredoc, a code generator). Such
	// a turn's snapshot set is KNOWN-INCOMPLETE, so anything reasoning about "what
	// changed this turn" must not treat it as the whole story. Stored as a turn id
	// rather than a bool so it self-clears when the next turn starts.
	opaqueTurn int
}

// markOpaqueMutation records that a mutating tool ran this turn whose file targets
// can't be determined from its input, so the turn's snapshot set is incomplete.
func (s *snapshotStore) markOpaqueMutation() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opaqueTurn = s.turn
}

// hadOpaqueMutation reports whether the CURRENT turn ran a mutating tool whose
// targets couldn't be enumerated. When true, currentTurnChanges()/changedRelPaths()
// are a lower bound on what the turn actually changed, not the full set. The zero
// value is turn 0 while startTurn() begins at 1, so an idle store reports false.
func (s *snapshotStore) hadOpaqueMutation() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn > 0 && s.opaqueTurn == s.turn
}

// startTurn bumps the turn counter and returns the new id.
func (s *snapshotStore) startTurn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn++
	return s.turn
}

// mutatedThisTurn reports whether any file was snapshotted during the current
// turn — i.e. the agent created or edited a file this turn via the file tools
// (write/edit/multi_edit/apply_patch). Used to gate the post-turn verification
// loop so it runs only when code actually changed.
func (s *snapshotStore) mutatedThisTurn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.turn == s.turn {
			return true
		}
	}
	return false
}

// currentTurnChanges returns this turn's snapshot entries WITH their prior
// contents (unlike modified(), which strips them), so the self-review loop can
// build a before→after diff of just what the current turn changed. record()
// already dedupes per (turn, path) keeping the earliest state, so each changed
// file appears once with its original pre-turn content.
func (s *snapshotStore) currentTurnChanges() []fileSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fileSnapshot
	for _, e := range s.entries {
		if e.turn == s.turn {
			out = append(out, e)
		}
	}
	return out
}

// touchedPaths returns the DISTINCT absolute paths of every file this session
// snapshotted (created or edited via the file tools), across all turns. It's the
// per-session "what this session changed" set used for cross-session commit
// attribution — showing which sibling session touched each pending file.
func (s *snapshotStore) touchedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(s.entries))
	out := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		if _, ok := seen[e.path]; ok {
			continue
		}
		seen[e.path] = struct{}{}
		out = append(out, e.path)
	}
	sort.Strings(out)
	return out
}

// record snapshots a file's pre-change contents under the current turn. It is
// a no-op if the same (turn, path) was already recorded — only the earliest
// state of each file in a turn is kept.
func (s *snapshotStore) record(path string) {
	s.mu.Lock()
	turn := s.turn
	for _, e := range s.entries {
		if e.turn == turn && e.path == path {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	entry := fileSnapshot{path: path, turn: turn, prior: data, missing: os.IsNotExist(err)}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

// modified returns the distinct files currently tracked for undo — i.e. files
// changed this session that have not yet been reverted — ordered most-recently
// touched first. The returned snapshots carry only path and missing (where
// missing reflects whether the file was absent before its first recorded
// change, i.e. the agent created it); prior contents are omitted.
func (s *snapshotStore) modified() []fileSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	created := map[string]bool{}
	lastIdx := map[string]int{}
	var paths []string
	for i, e := range s.entries {
		if _, ok := lastIdx[e.path]; !ok {
			// First (earliest) occurrence determines created-ness.
			created[e.path] = e.missing
			paths = append(paths, e.path)
		}
		lastIdx[e.path] = i
	}
	// Most-recently-touched first, stable for ties.
	sort.SliceStable(paths, func(i, j int) bool { return lastIdx[paths[i]] > lastIdx[paths[j]] })

	out := make([]fileSnapshot, 0, len(paths))
	for _, p := range paths {
		out = append(out, fileSnapshot{path: p, missing: created[p]})
	}
	return out
}

// pop removes and returns the snapshots from the most recent turn that has any.
func (s *snapshotStore) pop() (turn int, entries []fileSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		return 0, nil
	}
	last := s.entries[len(s.entries)-1].turn
	cut := len(s.entries)
	for cut > 0 && s.entries[cut-1].turn == last {
		cut--
	}
	entries = append([]fileSnapshot(nil), s.entries[cut:]...)
	s.entries = s.entries[:cut]
	return last, entries
}

// turnCount returns the current (most recent) turn id.
func (s *snapshotStore) turnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

// popTurnsAfter removes and returns every snapshot entry whose turn id is
// greater than keepTurn (the most recent turns) and resets the turn counter to
// keepTurn so subsequent turns renumber consistently. Entries stay in
// chronological order. Used by Rewind to revert several turns at once.
func (s *snapshotStore) popTurnsAfter(keepTurn int) []fileSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := len(s.entries)
	for cut > 0 && s.entries[cut-1].turn > keepTurn {
		cut--
	}
	removed := append([]fileSnapshot(nil), s.entries[cut:]...)
	s.entries = s.entries[:cut]
	if s.turn > keepTurn {
		s.turn = keepTurn
	}
	return removed
}

// SnapshotPath records a file's prior state for undo. The path may be relative
// or absolute; it's resolved against the agent's working directory.
func (a *Agent) SnapshotPath(path string) {
	if a.snapshots == nil {
		return
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(a.cfg.WorkingDir, path)
	}
	a.snapshots.record(abs)
}

// ModifiedFile describes a file the agent has written or edited this session.
type ModifiedFile struct {
	Path    string // relative to the working directory when possible
	Created bool   // the file did not exist before the agent's first change
}

// ModifiedFiles returns the distinct files the agent has written or edited this
// session that have not been reverted via Undo, most-recently-touched first.
// Paths are relativized to the working directory. Returns nil when undo
// tracking is unavailable.
func (a *Agent) ModifiedFiles() []ModifiedFile {
	if a.snapshots == nil {
		return nil
	}
	entries := a.snapshots.modified()
	out := make([]ModifiedFile, 0, len(entries))
	for _, e := range entries {
		out = append(out, ModifiedFile{Path: mustRel(a.cfg.WorkingDir, e.path), Created: e.missing})
	}
	return out
}

// Undo reverts the most recent turn's file changes. Returns a one-line summary
// suitable for the TUI; the result is also persisted (the model's history is
// unchanged so the assistant still "remembers" having written things).
func (a *Agent) Undo(_ context.Context) (string, error) {
	if a.snapshots == nil {
		return "", fmt.Errorf("undo not available")
	}
	turn, entries := a.snapshots.pop()
	if len(entries) == 0 {
		return "Nothing to undo.", nil
	}
	restored, deleted, failed := a.applyRevert(entries)
	var b strings.Builder
	fmt.Fprintf(&b, "Undid turn %d:", turn)
	if len(restored) > 0 {
		fmt.Fprintf(&b, "\n  reverted: %s", strings.Join(restored, ", "))
	}
	if len(deleted) > 0 {
		fmt.Fprintf(&b, "\n  deleted (created by that turn): %s", strings.Join(deleted, ", "))
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\n  failed: %s", strings.Join(failed, ", "))
	}
	return b.String(), nil
}

// applyRevert restores the given snapshot entries on disk and returns the
// relative paths grouped by outcome. Entries may span several turns; a file
// touched in more than one of them is restored to the EARLIEST recorded prior
// state (entries are chronological, so the first occurrence per path wins).
func (a *Agent) applyRevert(entries []fileSnapshot) (restored, deleted, failed []string) {
	if len(entries) == 0 {
		return
	}
	seen := map[string]bool{}
	uniq := make([]fileSnapshot, 0, len(entries))
	for _, e := range entries {
		if seen[e.path] {
			continue
		}
		seen[e.path] = true
		uniq = append(uniq, e)
	}
	sort.SliceStable(uniq, func(i, j int) bool { return uniq[i].path < uniq[j].path })
	for _, e := range uniq {
		rel := mustRel(a.cfg.WorkingDir, e.path)
		if e.missing {
			if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
				failed = append(failed, fmt.Sprintf("%s: %v", rel, err))
				continue
			}
			deleted = append(deleted, rel)
			continue
		}
		if err := writeRestore(e.path, e.prior); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		restored = append(restored, rel)
	}
	return
}

// Rewind rolls the whole session — workspace files AND conversation history —
// back by n turns (n<1 means 1). Unlike Undo, which only reverts the most
// recent turn's files and deliberately leaves history intact, Rewind also trims
// the transcript so the model no longer "remembers" the rolled-back turns. The
// result is persisted.
func (a *Agent) Rewind(_ context.Context, n int) (string, error) {
	if a.snapshots == nil {
		return "", fmt.Errorf("rewind not available")
	}
	if n < 1 {
		n = 1
	}
	// Conversation turn count is authoritative for message boundaries; fall
	// back to the snapshot turn counter when there's no session.
	turns := a.snapshots.turnCount()
	useMetas := a.sess != nil && len(a.sess.TurnMetas) > 0
	if useMetas {
		turns = len(a.sess.TurnMetas)
	}
	if turns == 0 {
		return "Nothing to rewind.", nil
	}
	if n > turns {
		n = turns
	}
	keep := turns - n

	// 1) Revert files from the dropped turns. popTurnsAfter works in snapshot
	// turn-id space, which can drift AHEAD of the TurnMetas index when an earlier
	// turn errored or was cancelled (RunWith bumps the snapshot counter at turn
	// start but records no TurnMeta on the error path). So derive the keep
	// threshold from the kept turn's RECORDED snapshot id rather than reusing
	// `keep` (a TurnMetas count) as if the two spaces coincided. Without a session
	// there are no metas and the two spaces DO coincide, so keep is the id.
	keepSnapshotTurn := keep
	if useMetas {
		keepSnapshotTurn = 0 // keep==0 → rewinding everything
		if keep > 0 {
			keepSnapshotTurn = a.sess.TurnMetas[keep-1].SnapshotTurn
		}
	}
	entries := a.snapshots.popTurnsAfter(keepSnapshotTurn)
	restored, deleted, failed := a.applyRevert(entries)

	// 2) Trim conversation history to the end of turn `keep`.
	trimmedMsgs := 0
	if a.sess != nil && len(a.sess.TurnMetas) > 0 {
		cut := 0
		if keep > 0 && keep <= len(a.sess.TurnMetas) {
			cut = a.sess.TurnMetas[keep-1].MsgCount
		}
		if cut < 0 {
			cut = 0
		}
		if cut > len(a.messages) {
			cut = len(a.messages)
		}
		trimmedMsgs = len(a.messages) - cut
		a.messages = a.messages[:cut]
		a.trimSessionTo(keep, cut)
	}
	a.persist()

	if len(restored) == 0 && len(deleted) == 0 && trimmedMsgs == 0 {
		return "Nothing to rewind.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Rewound %d turn(s) (kept %d):", n, keep)
	if len(restored) > 0 {
		fmt.Fprintf(&b, "\n  reverted: %s", strings.Join(restored, ", "))
	}
	if len(deleted) > 0 {
		fmt.Fprintf(&b, "\n  deleted (created by those turns): %s", strings.Join(deleted, ", "))
	}
	if trimmedMsgs > 0 {
		fmt.Fprintf(&b, "\n  dropped %d message(s) from history", trimmedMsgs)
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "\n  failed: %s", strings.Join(failed, ", "))
	}
	return b.String(), nil
}

// trimSessionTo trims per-turn session metadata anchored beyond the rewind
// point: TurnMetas back to keepTurns entries, BoardMessages posted after the
// cut message index, and a checkpoint distilled past the cut. TaskMetas carry
// no message anchor and are left as-is (cosmetic replay footers).
func (a *Agent) trimSessionTo(keepTurns, cut int) {
	if a.sess == nil {
		return
	}
	if keepTurns >= 0 && keepTurns < len(a.sess.TurnMetas) {
		a.sess.TurnMetas = a.sess.TurnMetas[:keepTurns]
	}
	if len(a.sess.BoardMessages) > 0 {
		kept := a.sess.BoardMessages[:0]
		for _, m := range a.sess.BoardMessages {
			if m.MsgCount <= cut {
				kept = append(kept, m)
			}
		}
		a.sess.BoardMessages = kept
	}
	if a.sess.CheckpointMsgs > cut {
		a.sess.Checkpoint = ""
		a.sess.CheckpointMsgs = 0
	}
}

func mustRel(base, p string) string {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return rel
}

// writeRestore writes data to path, ensuring the parent dir exists.
func writeRestore(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// snapshotForCall records the prior contents of any file this tool call is about to
// mutate, so /undo can revert just this turn's writes. When the call mutates but its
// targets can't be enumerated from its input (bash: `sed -i`, a heredoc, a generator),
// nothing can be snapshotted — so the turn is flagged instead, because a change set that
// is silently PARTIAL is worse than one that is known to be partial. Extracted from
// executeApprovedCall so the snapshot-vs-flag decision is testable at its real call site.
func (a *Agent) snapshotForCall(name string, input json.RawMessage) {
	paths := snapshotPaths(name, input)
	for _, p := range paths {
		a.SnapshotPath(p)
	}
	if len(paths) > 0 || a.snapshots == nil {
		return
	}
	// No enumerable targets. Only mutating tools matter: a read_only tool that changes
	// nothing must not make the turn look unreviewable. Registry.Mutates fails safe
	// (unknown tool → true), which is the behaviour we want here.
	if a.tools != nil && a.tools.Mutates(name) {
		a.snapshots.markOpaqueMutation()
	}
}

// snapshotPaths inspects a tool call's input to find any file paths that the
// call is about to mutate, so the agent can record their prior state before
// the tool runs. We support write_file/edit_file/multi_edit (single-file) and
// apply_patch (multi-file); bash is opaque, so its changes can't be
// auto-snapshotted — see snapshotForCall, which flags such a turn instead.
func snapshotPaths(toolName string, input json.RawMessage) []string {
	switch toolName {
	case "write_file", "edit_file", "multi_edit":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &args); err == nil && args.Path != "" {
			return []string{args.Path}
		}
	case "apply_patch":
		return tools.ApplyPatchPaths(input)
	}
	return nil
}

// diagnosticTargets resolves the files a successful mutating tool call touched,
// reads each back from disk, and returns (absolutePath, content) pairs to feed
// the LSP manager. write_file/edit_file/multi_edit touch one file; apply_patch
// may touch several (deleted files are skipped — they no longer exist).
func diagnosticTargets(root, toolName string, input json.RawMessage) []diagnosticTarget {
	var rels []string
	switch toolName {
	case "write_file", "edit_file", "multi_edit":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &args); err != nil || args.Path == "" {
			return nil
		}
		rels = []string{args.Path}
	case "apply_patch":
		rels = tools.ApplyPatchPaths(input)
	default:
		return nil
	}

	var out []diagnosticTarget
	for _, rel := range rels {
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, rel)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue // e.g. a deleted file
		}
		out = append(out, diagnosticTarget{path: abs, content: string(data)})
	}
	return out
}

// diagnosticTarget is one file to request diagnostics for after a write.
type diagnosticTarget struct {
	path    string
	content string
}

// TouchedFiles returns the paths (relative to the agent's WorkingDir when
// possible) of every file this session has created or edited via the file tools.
// Used for cross-session commit attribution: showing which sibling session's work
// is present in the shared working tree. Absolute paths outside WorkingDir are
// returned as-is.
func (a *Agent) TouchedFiles() []string {
	abs := a.snapshots.touchedPaths()
	out := make([]string, 0, len(abs))
	wd := a.cfg.WorkingDir
	for _, p := range abs {
		if wd != "" {
			if rel, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(rel, "..") {
				out = append(out, rel)
				continue
			}
		}
		out = append(out, p)
	}
	return out
}
