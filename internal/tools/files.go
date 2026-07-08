package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/diff"
)

// readTool reads a UTF-8 text file, optionally a line range.
type readTool struct{ r *Registry }

// defaultReadLines is the line cap a read_file with no explicit limit returns.
// Lower values keep individual reads cheaper (smaller re-sent context) at the
// cost of more paged follow-up reads on large files. Set via SetDefaultReadLines
// (driven by config.MaxReadLines); 2000 preserves the historical default when
// unset.
var defaultReadLines = 2000

// SetDefaultReadLines overrides the read_file default line cap (a positive n).
// Safe to call at startup before serving; read tools read the value per-call.
func SetDefaultReadLines(n int) {
	if n > 0 {
		defaultReadLines = n
	}
}

func (t *readTool) Name() string { return "read_file" }

func (t *readTool) Description() string {
	return "Read the contents of a text file. Returns the file with 1-based line " +
		"number prefixes. Use offset and limit to page through large files. " +
		"Paths are relative to the working directory."
}

func (t *readTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"path":   {"type": "string", "description": "File path to read, relative to the working directory."},
			"offset": {"type": "integer", "description": "1-based line number to start from. Optional."},
			"limit":  {"type": "integer", "description": "Maximum number of lines to return. Optional; defaults to a bounded window — page with offset for more."}
		},
		"required": ["path"]
	}`)
}

func (t *readTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path   string  `json:"path"`
		Offset flexInt `json:"offset"`
		Limit  flexInt `json:"limit"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			// Don't just echo the bare "stat … no such file": help the model
			// self-correct by pointing at near-name matches, the parent dir's
			// contents, or the nearest existing ancestor when the parent is gone.
			return "", fmt.Errorf("read %s: no such file%s", in.Path, t.notFoundHint(abs))
		}
		return "", fmt.Errorf("read %s: %v", in.Path, err)
	}
	// Pointed at a directory: return its listing with a hint rather than the
	// opaque "is a directory" error os.ReadFile would give, so the model can
	// recover in one step (read a file inside, or switch to list_dir).
	if info.IsDir() {
		listing, lerr := t.r.dirListing(abs)
		if lerr != nil {
			return "", fmt.Errorf("read %s: %v", in.Path, lerr)
		}
		return fmt.Sprintf("%q is a directory, not a file — use read_file on a specific file inside it, or list_dir to explore. Contents:\n%s", in.Path, listing), nil
	}
	// Dedup: if this exact (path, offset, limit) window was already returned to
	// the model and the file is unchanged (same size+mtime), don't re-send the
	// body — it's still in the conversation. Return a compact stub instead. This
	// is keyed on the raw requested offset/limit so only an identical call hits.
	size, mtime := info.Size(), info.ModTime().UnixNano()
	if e, ok := t.r.readCacheLookup(abs, int(in.Offset), int(in.Limit), size, mtime); ok {
		return t.cacheStub(in.Path, e), nil
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %v", in.Path, err)
	}
	if len(data) == 0 {
		return "(file is empty)", nil
	}

	lines := strings.Split(string(data), "\n")
	start := 1
	if in.Offset > 0 {
		start = int(in.Offset)
	}
	limit := defaultReadLines
	if in.Limit > 0 {
		limit = int(in.Limit)
	}

	var b strings.Builder
	shown := 0
	var raw strings.Builder // window content without line-number prefixes, for diffing
	for i := start - 1; i < len(lines) && shown < limit; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i+1, lines[i])
		raw.WriteString(lines[i])
		raw.WriteByte('\n')
		shown++
	}
	if start-1+shown < len(lines) {
		fmt.Fprintf(&b, "... (%d more lines; use offset=%d to continue)\n", len(lines)-(start-1+shown), start+shown)
	}
	full, rawBody := b.String(), raw.String()
	store := func() {
		// Record this window (with its raw content) so a later identical read can
		// stub it, and an edited re-read can diff against it.
		t.r.readCacheStore(abs, int(in.Offset), int(in.Limit), readCacheEntry{
			size: size, mtime: mtime, start: start, lines: shown, body: rawBody,
		})
	}
	// Diff-on-re-read: if this exact window was shown before but the file has
	// since changed (an edit dirtied the cache, or size/mtime differ), don't
	// resend the whole window — the prior version is still above in the
	// conversation. Return a unified diff when the window actually changed (and
	// the diff is smaller), or a stub when only the rest of the file moved.
	if prev, ok := t.r.readCachePeek(abs, int(in.Offset), int(in.Limit)); ok && prev.body != "" {
		if prev.body == rawBody {
			store()
			return t.cacheStub(in.Path, readCacheEntry{start: start, lines: shown}), nil
		}
		if d, stats := diff.Unified(prev.body, rawBody); d != "" && len(d) < len(full) {
			store()
			return fmt.Sprintf("(%s changed since you last read it — unified diff for lines %d-%d, %s; the previous version is above. Re-read with an explicit offset for full fresh context.)\n%s",
				in.Path, start, start+shown-1, stats, d), nil
		}
	}
	store()
	return full, nil
}

// cacheStub is the compact reply for a repeat read of an unchanged window: it
// tells the model the content is already in the conversation rather than
// re-sending it, naming the line range so the model can locate it.
func (t *readTool) cacheStub(path string, e readCacheEntry) string {
	if e.lines <= 0 {
		return fmt.Sprintf("(%s is unchanged since you last read it — its contents are already above in this conversation)", path)
	}
	return fmt.Sprintf("(%s lines %d-%d are unchanged since you last read them — those contents are already above in this conversation; no need to re-read)", path, e.start, e.start+e.lines-1)
}

func (t *readTool) Mutates() bool { return false }

// notFoundHint builds a short recovery suffix for a missing read_file target:
// close-name matches in the (existing) parent directory, else a capped listing
// of it, else the nearest existing ancestor when the parent itself is missing.
// Best-effort and quiet: returns "" when it can't add anything useful.
func (t *readTool) notFoundHint(abs string) string {
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Parent dir doesn't exist either — point at where the path diverges.
		anc := nearestExistingDir(filepath.Dir(dir))
		if anc == "" {
			return ""
		}
		return fmt.Sprintf(" — directory %q doesn't exist (nearest existing path: %q); check the path", t.relDisplay(dir), t.relDisplay(anc))
	}
	// Parent exists: prefer close-name matches (same name ignoring case, or the
	// requested stem as a substring), else show what's actually there.
	stem := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	var matches, names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
		low := strings.ToLower(e.Name())
		if strings.EqualFold(e.Name(), base) || (stem != "" && strings.Contains(low, stem)) {
			matches = append(matches, name)
		}
	}
	if len(matches) > 0 {
		if len(matches) > 8 {
			matches = matches[:8]
		}
		return fmt.Sprintf(" — did you mean: %s?", strings.Join(matches, ", "))
	}
	if len(names) == 0 {
		return ""
	}
	const cap = 40
	more := 0
	if len(names) > cap {
		more, names = len(names)-cap, names[:cap]
	}
	hint := fmt.Sprintf(" — %q contains: %s", t.relDisplay(dir), strings.Join(names, ", "))
	if more > 0 {
		hint += fmt.Sprintf(", …+%d more", more)
	}
	return hint
}

// relDisplay renders abs relative to the workspace root when it's inside it (so
// hints stay short), falling back to the absolute path otherwise.
func (t *readTool) relDisplay(abs string) string {
	if rel, err := filepath.Rel(t.r.root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return abs
}

// nearestExistingDir walks up from dir to the first path that exists, or "" if
// none do (shouldn't happen — the root always exists).
func nearestExistingDir(dir string) string {
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// writeTool creates or overwrites a file.
type writeTool struct{ r *Registry }

func (t *writeTool) Name() string { return "write_file" }

func (t *writeTool) Description() string {
	return "Create a new file or overwrite an existing file with the given " +
		"content. Creates parent directories as needed. Prefer edit_file for " +
		"small changes to existing files."
}

func (t *writeTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"path":    {"type": "string", "description": "File path to write, relative to the working directory."},
			"content": {"type": "string", "description": "Full file content to write."}
		},
		"required": ["path", "content"]
	}`)
}

func (t *writeTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return "", err
	}
	if dir := parentDir(abs); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create parent dir: %v", err)
		}
	}
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %v", in.Path, err)
	}
	t.r.invalidateRead(abs)
	t.r.invalidateSearches()
	n := strings.Count(in.Content, "\n") + 1
	return fmt.Sprintf("Wrote %d bytes (%d lines) to %s", len(in.Content), n, in.Path), nil
}

func (t *writeTool) Mutates() bool { return true }

// Preview shows a diff of the file as it will look after the write.
func (t *writeTool) Preview(_ context.Context, input json.RawMessage) (Preview, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(input, &in); err != nil {
		return Preview{}, err
	}
	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return Preview{}, err
	}
	existing := ""
	verb := "create"
	if data, err := os.ReadFile(abs); err == nil {
		existing = string(data)
		verb = "overwrite"
	}
	d, stats := diff.Unified(existing, in.Content)
	if d == "" {
		d = "(no change)"
	}
	return Preview{
		Path:    in.Path,
		Diff:    d,
		Summary: fmt.Sprintf("%s %s (%s)", verb, in.Path, stats),
	}, nil
}

// editTool performs an exact-match string replacement in a file.
type editTool struct{ r *Registry }

func (t *editTool) Name() string { return "edit_file" }

func (t *editTool) Description() string {
	return "Replace an exact string in a file with new text. old_string must " +
		"match exactly once (include surrounding context to disambiguate). Set " +
		"replace_all to change every occurrence."
}

func (t *editTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"path":        {"type": "string", "description": "File to edit, relative to the working directory."},
			"old_string":  {"type": "string", "description": "Exact text to find."},
			"new_string":  {"type": "string", "description": "Replacement text."},
			"replace_all": {"type": "boolean", "description": "Replace all occurrences instead of requiring a unique match."}
		},
		"required": ["path", "old_string", "new_string"]
	}`)
}

func (t *editTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	in, _, after, count, err := t.apply(input)
	if err != nil {
		return "", err
	}
	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(after), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %v", in.Path, err)
	}
	t.r.invalidateRead(abs)
	t.r.invalidateSearches()
	return fmt.Sprintf("Replaced %d occurrence(s) in %s", count, in.Path), nil
}

func (t *editTool) Mutates() bool { return true }

// editInput is the shared shape of edit_file input.
type editInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// apply computes the edited content and match count without writing to disk.
func (t *editTool) apply(input json.RawMessage) (in editInput, before, after string, count int, err error) {
	if err = decode(input, &in); err != nil {
		return
	}
	if in.OldString == "" {
		err = fmt.Errorf("old_string must not be empty")
		return
	}
	if in.OldString == in.NewString {
		err = fmt.Errorf("old_string and new_string are identical — this edit changes nothing. Set new_string to the intended replacement, then retry")
		return
	}
	abs, rerr := t.r.resolve(in.Path)
	if rerr != nil {
		err = rerr
		return
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		err = fmt.Errorf("read %s: %v", in.Path, rerr)
		return
	}
	before = string(data)
	count = strings.Count(before, in.OldString)
	if count == 0 {
		err = fmt.Errorf("old_string not found in %s", in.Path)
		return
	}
	if count > 1 && !in.ReplaceAll {
		err = fmt.Errorf("old_string matches %d times in %s; add more context or set replace_all", count, in.Path)
		return
	}
	if in.ReplaceAll {
		after = strings.ReplaceAll(before, in.OldString, in.NewString)
	} else {
		after = strings.Replace(before, in.OldString, in.NewString, 1)
	}
	return
}

// Preview shows a diff of the pending edit.
func (t *editTool) Preview(_ context.Context, input json.RawMessage) (Preview, error) {
	in, before, after, count, err := t.apply(input)
	if err != nil {
		return Preview{}, err
	}
	d, stats := diff.Unified(before, after)
	if d == "" {
		d = "(no change)"
	}
	return Preview{
		Path:    in.Path,
		Diff:    d,
		Summary: fmt.Sprintf("edit %s — %d match(es) (%s)", in.Path, count, stats),
	}, nil
}

// multiEditTool applies a sequence of exact-match string replacements to a
// single file, atomically: every edit must succeed or none are written.
type multiEditTool struct{ r *Registry }

func (t *multiEditTool) Name() string { return "multi_edit" }

func (t *multiEditTool) Description() string {
	return "Apply several exact-string replacements to one file in a single " +
		"atomic operation. Edits are applied in order, each against the result " +
		"of the previous one. Every edit must match (each old_string exactly " +
		"once unless replace_all is set) or the whole operation fails and the " +
		"file is left unchanged. Prefer this over multiple edit_file calls when " +
		"changing several places in the same file."
}

func (t *multiEditTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File to edit, relative to the working directory."},
			"edits": {
				"type": "array",
				"description": "Ordered list of replacements to apply.",
				"items": {
					"type": "object",
					"properties": {
						"old_string":  {"type": "string", "description": "Exact text to find."},
						"new_string":  {"type": "string", "description": "Replacement text."},
						"replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."}
					},
					"required": ["old_string", "new_string"]
				}
			}
		},
		"required": ["path", "edits"]
	}`)
}

// multiEditInput is the decoded shape of multi_edit input.
type multiEditInput struct {
	Path  string `json:"path"`
	Edits []struct {
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	} `json:"edits"`
}

// apply computes the fully-edited content without writing to disk, returning
// the original text, the edited text, and the total number of replacements.
func (t *multiEditTool) apply(input json.RawMessage) (in multiEditInput, before, after string, total int, err error) {
	if err = decode(input, &in); err != nil {
		return
	}
	if len(in.Edits) == 0 {
		err = fmt.Errorf("edits is required and must not be empty")
		return
	}
	abs, rerr := t.r.resolve(in.Path)
	if rerr != nil {
		err = rerr
		return
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		err = fmt.Errorf("read %s: %v", in.Path, rerr)
		return
	}
	before = string(data)
	cur := before
	for i, e := range in.Edits {
		if e.OldString == "" {
			err = fmt.Errorf("edit %d: old_string must not be empty", i+1)
			return
		}
		if e.OldString == e.NewString {
			err = fmt.Errorf("edit %d: old_string and new_string are identical — this edit changes nothing. Remove it, or set new_string to the intended replacement, then retry", i+1)
			return
		}
		count := strings.Count(cur, e.OldString)
		if count == 0 {
			err = fmt.Errorf("edit %d: old_string not found in %s", i+1, in.Path)
			return
		}
		if count > 1 && !e.ReplaceAll {
			err = fmt.Errorf("edit %d: old_string matches %d times in %s; add more context or set replace_all", i+1, count, in.Path)
			return
		}
		if e.ReplaceAll {
			cur = strings.ReplaceAll(cur, e.OldString, e.NewString)
			total += count
		} else {
			cur = strings.Replace(cur, e.OldString, e.NewString, 1)
			total++
		}
	}
	after = cur
	return
}

func (t *multiEditTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	in, _, after, total, err := t.apply(input)
	if err != nil {
		return "", err
	}
	abs, err := t.r.resolve(in.Path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(after), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %v", in.Path, err)
	}
	t.r.invalidateRead(abs)
	t.r.invalidateSearches()
	return fmt.Sprintf("Applied %d edit(s) (%d replacement(s)) to %s", len(in.Edits), total, in.Path), nil
}

func (t *multiEditTool) Mutates() bool { return true }

// Preview shows a diff of the combined pending edits.
func (t *multiEditTool) Preview(_ context.Context, input json.RawMessage) (Preview, error) {
	in, before, after, total, err := t.apply(input)
	if err != nil {
		return Preview{}, err
	}
	d, stats := diff.Unified(before, after)
	if d == "" {
		d = "(no change)"
	}
	return Preview{
		Path:    in.Path,
		Diff:    d,
		Summary: fmt.Sprintf("multi_edit %s — %d edit(s), %d replacement(s) (%s)", in.Path, len(in.Edits), total, stats),
	}, nil
}

// applyPatchTool applies a Codex-style multi-file patch envelope: add, update,
// and delete several files in one atomic operation.
type applyPatchTool struct{ r *Registry }

func (t *applyPatchTool) Name() string { return "apply_patch" }

func (t *applyPatchTool) Description() string {
	return "Apply a single patch that adds, updates, and/or deletes multiple " +
		"files atomically. The patch is one string in this envelope:\n" +
		"*** Begin Patch\n" +
		"*** Add File: path/new.txt\n+line one\n+line two\n" +
		"*** Update File: path/existing.go\n@@\n unchanged context\n-old line\n+new line\n" +
		"*** Delete File: path/old.txt\n" +
		"*** End Patch\n" +
		"Update hunks match by exact context, not line numbers: each hunk's " +
		"context+removed lines must appear exactly once in the file (add more " +
		"context to disambiguate). Either every file change applies or none do. " +
		"Prefer this over several edit_file calls when changing multiple files."
}

func (t *applyPatchTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"patch": {"type": "string", "description": "The full *** Begin Patch / *** End Patch envelope."}
		},
		"required": ["patch"]
	}`)
}

// patchResult is the computed effect of a patch on one file.
type patchResult struct {
	op      patchKind
	relPath string
	before  string
	after   string
	existed bool
}

// plan parses the patch and computes the resulting content for every affected
// file, validating against disk, without writing anything.
func (t *applyPatchTool) plan(input json.RawMessage) ([]patchResult, error) {
	var in struct {
		Patch string `json:"patch"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Patch) == "" {
		return nil, fmt.Errorf("patch is required")
	}
	ops, err := parsePatch(in.Patch)
	if err != nil {
		return nil, err
	}

	// Guard against a single patch touching the same file twice (order/intent
	// would be ambiguous), and resolve+sandbox every path up front.
	seen := map[string]bool{}
	results := make([]patchResult, 0, len(ops))
	for _, op := range ops {
		abs, err := t.r.resolve(op.path)
		if err != nil {
			return nil, err
		}
		if seen[abs] {
			return nil, fmt.Errorf("patch references %s more than once", op.path)
		}
		seen[abs] = true

		data, readErr := os.ReadFile(abs)
		existed := readErr == nil

		switch op.kind {
		case patchAdd:
			if existed {
				return nil, fmt.Errorf("Add File %s: already exists (use Update File)", op.path)
			}
			results = append(results, patchResult{op: patchAdd, relPath: op.path, before: "", after: op.body, existed: false})

		case patchDelete:
			if !existed {
				return nil, fmt.Errorf("Delete File %s: does not exist", op.path)
			}
			results = append(results, patchResult{op: patchDelete, relPath: op.path, before: string(data), after: "", existed: true})

		case patchUpdate:
			if !existed {
				return nil, fmt.Errorf("Update File %s: does not exist (use Add File)", op.path)
			}
			after, err := applyUpdate(op.path, string(data), op.hunks)
			if err != nil {
				return nil, err
			}
			results = append(results, patchResult{op: patchUpdate, relPath: op.path, before: string(data), after: after, existed: true})
		}
	}
	return results, nil
}

func (t *applyPatchTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	results, err := t.plan(input)
	if err != nil {
		return "", err
	}
	// Apply in order; validation already happened in plan(), so failures here
	// are unexpected I/O errors. We report which file failed.
	var added, updated, deleted int
	for _, r := range results {
		abs, err := t.r.resolve(r.relPath)
		if err != nil {
			return "", err
		}
		t.r.invalidateRead(abs)
		t.r.invalidateSearches()
		switch r.op {
		case patchAdd:
			if dir := parentDir(abs); dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return "", fmt.Errorf("create parent dir for %s: %v", r.relPath, err)
				}
			}
			if err := os.WriteFile(abs, []byte(r.after), 0o644); err != nil {
				return "", fmt.Errorf("add %s: %v", r.relPath, err)
			}
			added++
		case patchUpdate:
			if err := os.WriteFile(abs, []byte(r.after), 0o644); err != nil {
				return "", fmt.Errorf("update %s: %v", r.relPath, err)
			}
			updated++
		case patchDelete:
			if err := os.Remove(abs); err != nil {
				return "", fmt.Errorf("delete %s: %v", r.relPath, err)
			}
			deleted++
		}
	}
	return fmt.Sprintf("Applied patch: %d added, %d updated, %d deleted", added, updated, deleted), nil
}

func (t *applyPatchTool) Mutates() bool { return true }

// Preview shows a combined diff across every file the patch touches.
func (t *applyPatchTool) Preview(_ context.Context, input json.RawMessage) (Preview, error) {
	results, err := t.plan(input)
	if err != nil {
		return Preview{}, err
	}
	var b strings.Builder
	var totalAdd, totalRem int
	for i, r := range results {
		if i > 0 {
			b.WriteByte('\n')
		}
		var verb string
		switch r.op {
		case patchAdd:
			verb = "add"
		case patchUpdate:
			verb = "update"
		case patchDelete:
			verb = "delete"
		}
		fmt.Fprintf(&b, "%s %s\n", verb, r.relPath)
		d, stats := diff.Unified(r.before, r.after)
		totalAdd += stats.Added
		totalRem += stats.Removed
		if d == "" {
			d = "(no change)"
		}
		b.WriteString(d)
		if !strings.HasSuffix(d, "\n") {
			b.WriteByte('\n')
		}
	}
	path := ""
	if len(results) == 1 {
		path = results[0].relPath
	}
	stats := diff.Stats{Added: totalAdd, Removed: totalRem}
	return Preview{
		Path:    path,
		Diff:    strings.TrimRight(b.String(), "\n"),
		Summary: fmt.Sprintf("apply_patch — %d file(s) (%s)", len(results), stats),
	}, nil
}

// patchPaths returns the working-directory-relative paths a patch will touch,
// for snapshotting and diagnostics. Parse/validation failures yield nil so the
// caller falls back gracefully.
func (t *applyPatchTool) patchPaths(input json.RawMessage) []string {
	var in struct {
		Patch string `json:"patch"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	ops, err := parsePatch(in.Patch)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(ops))
	for _, op := range ops {
		paths = append(paths, op.path)
	}
	return paths
}

// ApplyPatchPaths returns the working-directory-relative paths an apply_patch
// envelope will touch (added, updated, or deleted), for the agent's undo
// snapshots and LSP diagnostics. It returns nil if the input can't be parsed.
func ApplyPatchPaths(input json.RawMessage) []string {
	return (&applyPatchTool{}).patchPaths(input)
}

// parentDir returns the directory portion of a path, or "" if none.
func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return ""
}
