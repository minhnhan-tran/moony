// Package tools defines the agent's callable tools and a registry to manage them.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pysty1995/moony/internal/llm"
)

// Tool is a single capability the model can invoke.
type Tool interface {
	// Name is the unique tool identifier exposed to the model.
	Name() string
	// Description tells the model when and how to use the tool.
	Description() string
	// Schema is the JSON Schema for the tool's input object.
	Schema() json.RawMessage
	// Execute runs the tool with decoded input and returns text output.
	Execute(ctx context.Context, input json.RawMessage) (string, error)
	// Mutates reports whether the tool changes state (filesystem, shell) and
	// therefore should be gated behind user approval.
	Mutates() bool
}

// Preview describes the effect a mutating tool will have, for display in an
// approval prompt before the change is applied.
type Preview struct {
	// Path is the file the tool will affect, if any.
	Path string
	// Diff is a unified diff of the pending change, if computable.
	Diff string
	// Summary is a short human description of the action.
	Summary string
}

// Previewer is an optional capability for tools that can describe their effect
// before executing (e.g. file writes and edits show a diff).
type Previewer interface {
	Preview(ctx context.Context, input json.RawMessage) (Preview, error)
}

// Image is a binary image a tool returns to the model (e.g. a browser
// screenshot). The agent forwards each one as an image block in the tool_result,
// so the model can actually see it.
type Image struct {
	MediaType string // e.g. "image/png"
	Data      []byte // raw image bytes
}

// ImageProducer is an optional capability for a tool that returns images in
// addition to text — e.g. the browser tool's screenshot action. Tools that don't
// produce images just implement Execute and go through the text-only path.
type ImageProducer interface {
	ExecuteImages(ctx context.Context, input json.RawMessage) (text string, images []Image, err error)
}

// Registry holds the available tools and resolves paths against a root dir.
type Registry struct {
	root string
	// extraRoots are additional workspace roots that file tools may read and
	// write, beyond the primary working dir (multi-root workspaces). Relative
	// tool paths resolve against root; extra roots are reached by absolute path.
	extraRoots []string
	// tempRoots are system scratch dirs (the OS temp dir + /tmp) that file tools
	// may read/write by absolute path even though they're outside the workspace.
	// Populated by NewRegistry (see tempDirs); tests may clear it to assert the
	// strict workspace-only boundary.
	tempRoots []string
	tools     map[string]Tool
	order     []string
	ignore    *IgnoreSet
	// sandbox, when non-nil, is the explicit bash-sandbox setting; nil falls
	// back to the SQE_SANDBOX/SQE_SANDBOX_NET environment variables.
	sandbox *sandboxSetting
	// jobs tracks background bash commands (the bash `background` flag plus the
	// bash_output/bash_kill tools). KillAll reaps survivors on shutdown.
	jobs *JobStore
	// closers are teardown funcs (e.g. the browser tool's Chrome) run by Close
	// on shutdown, alongside reaping background jobs.
	closeMu sync.Mutex
	closers []func()

	// repoMapOnce memoizes RepoMap so its whole-tree walk runs at most once per
	// registry, not on every system-prompt rebuild (remember/MCP-connect/attach).
	// The map is a coarse, session-stable orientation snapshot — mild count drift
	// as the agent edits files is acceptable.
	repoMapOnce sync.Once
	repoMapStr  string

	// readCache dedups identical read_file calls within a session. Keyed by
	// absolute path + offset + limit, it records the file's size+mtime at the
	// time the body was last returned to the model. On a repeat read of the
	// same window with the file unchanged, the read tool returns a compact stub
	// instead of re-sending the (already-in-history) body — the single biggest
	// token saving in long multi-turn sessions. Invalidated by a size/mtime
	// change (external edits) or explicitly via invalidateRead when the agent
	// writes/edits the file. Guarded by readCacheMu since tools can run
	// concurrently.
	readCacheMu sync.Mutex
	readCache   map[string]readCacheEntry

	// searchCache dedups identical content/tree searches (grep, glob, list_dir)
	// within a session: a byte-identical repeat returns a compact stub instead of
	// re-sending a large result set. Unlike readCache (one file, validated by
	// size+mtime) a search result depends on many files, so it's invalidated
	// WHOLESALE whenever the agent writes (any edit could change a match) via
	// invalidateSearches, and on compaction (the prior result is no longer
	// "above"). Keyed on tool name + raw args, so only an exact repeat hits.
	// Guarded by searchCacheMu since tools can run concurrently.
	searchCacheMu sync.Mutex
	searchCache   map[string]string

	// conciseSchemas, when set via SetConciseSchemas, trims each tool's
	// Description to its first sentence in Schemas() (the tool specs sent to the
	// model). Opt-in: prompt caching amortizes full descriptions on cached
	// routes, so this only helps uncached/token-tight setups.
	conciseSchemas bool

	// embedder, when set via SetEmbedder, enables hybrid semantic reranking in
	// the search_code tool: the top BM25 candidates are re-scored by embedding
	// cosine similarity to the query and blended with their lexical score. nil
	// means pure BM25 (the default).
	embedder Embedder
}

// Embedder produces an embedding vector for each input string (a query and
// candidate code chunks). Implemented by the LLM client and injected by the
// agent when an embeddings model is configured.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// readCacheEntry records what was last returned for one (path, offset, limit)
// read window, so a repeat read can be answered with a stub when the file is
// unchanged. lines/start describe the window so the stub can name it precisely.
type readCacheEntry struct {
	size  int64
	mtime int64  // UnixNano
	start int    // 1-based first line returned
	lines int    // number of body lines returned
	turn  int    // sequence number for the stub message
	body  string // raw window content last shown (no line numbers), for diff-on-re-read
	dirty bool   // file edited since; a re-read should diff/refresh, not stub
}

// SetSandbox configures the OS-level bash sandbox explicitly, overriding the
// environment-variable fallback.
func (r *Registry) SetSandbox(enabled, network bool) {
	r.sandbox = &sandboxSetting{enabled: enabled, network: network}
}

// readCacheKey builds the cache key for a read window.
func readCacheKey(abs string, offset, limit int) string {
	return abs + "\x00" + strconv.Itoa(offset) + "\x00" + strconv.Itoa(limit)
}

// readCacheLookup reports whether (abs, offset, limit) was already returned to
// the model for a file still at size/mtime, and if so the entry describing it.
func (r *Registry) readCacheLookup(abs string, offset, limit int, size, mtime int64) (readCacheEntry, bool) {
	r.readCacheMu.Lock()
	defer r.readCacheMu.Unlock()
	e, ok := r.readCache[readCacheKey(abs, offset, limit)]
	if !ok || e.dirty || e.size != size || e.mtime != mtime {
		return readCacheEntry{}, false
	}
	return e, true
}

// readCachePeek returns the last recorded entry for a read window regardless of
// the file's current size/mtime or dirty flag — used by diff-on-re-read to
// recover the previously-shown content as a diff base.
func (r *Registry) readCachePeek(abs string, offset, limit int) (readCacheEntry, bool) {
	r.readCacheMu.Lock()
	defer r.readCacheMu.Unlock()
	e, ok := r.readCache[readCacheKey(abs, offset, limit)]
	return e, ok
}

// readCacheStore records that a read window's body was returned to the model.
func (r *Registry) readCacheStore(abs string, offset, limit int, e readCacheEntry) {
	r.readCacheMu.Lock()
	defer r.readCacheMu.Unlock()
	if r.readCache == nil {
		r.readCache = make(map[string]readCacheEntry)
	}
	r.readCache[readCacheKey(abs, offset, limit)] = e
}

// invalidateRead marks every cached read window for abs dirty (retaining the
// last-shown body). Called when the agent writes/edits/patches the file: the
// next read misses the "unchanged" stub and, because the prior body is kept,
// can return just a unified diff of what changed instead of the whole file.
func (r *Registry) invalidateRead(abs string) {
	r.readCacheMu.Lock()
	defer r.readCacheMu.Unlock()
	prefix := abs + "\x00"
	for k, e := range r.readCache {
		if strings.HasPrefix(k, prefix) {
			e.dirty = true
			r.readCache[k] = e
		}
	}
}

// dropRead deletes every cached read window for abs (no diff base retained).
func (r *Registry) dropRead(abs string) {
	r.readCacheMu.Lock()
	defer r.readCacheMu.Unlock()
	prefix := abs + "\x00"
	for k := range r.readCache {
		if strings.HasPrefix(k, prefix) {
			delete(r.readCache, k)
		}
	}
}

// DropRead invalidates the read-dedup cache for the file named in a read_file
// tool's input. The agent calls this when pruneStaleToolResults elides a
// read_file result from history: the cached "those contents are already above"
// stub would otherwise point at content that's just been replaced by an elision
// marker (the prune/dedup treadmill — the read-cache analog of DropSearch). No-op
// when the input has no path or it can't be resolved.
func (r *Registry) DropRead(input json.RawMessage) {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Path) == "" {
		return
	}
	if abs, err := r.resolve(in.Path); err == nil {
		r.dropRead(abs) // history elided: no diff base, drop outright
	}
}

// ClearReadCache drops the entire read-dedup cache. The cache's premise is that
// a previously-read window is still "above in this conversation"; compaction
// summarizes/removes older messages and breaks that, so the agent calls this
// after compacting — otherwise a repeat read would return an "already above"
// stub pointing at content that's no longer there, leaving the model unable to
// see it or re-read it.
func (r *Registry) ClearReadCache() {
	r.readCacheMu.Lock()
	defer r.readCacheMu.Unlock()
	r.readCache = nil
}

// searchCacheMinBytes is the smallest search result worth deduping — below it the
// stub would be about as long as the result, so it's not worth caching.
const searchCacheMinBytes = 400

// searchCacheKey keys a search by tool name + raw args; only a byte-identical
// repeat hits (different whitespace/key-order simply misses → re-runs, never a
// false hit).
func searchCacheKey(tool string, input json.RawMessage) string {
	return tool + "\x00" + strings.TrimSpace(string(input))
}

// searchCacheLookup returns the cached result for an identical earlier search, if
// any (and nothing has invalidated it since).
func (r *Registry) searchCacheLookup(tool string, input json.RawMessage) (string, bool) {
	r.searchCacheMu.Lock()
	defer r.searchCacheMu.Unlock()
	res, ok := r.searchCache[searchCacheKey(tool, input)]
	return res, ok
}

// searchCacheStore records a search's rendered result (skipping trivially small
// ones — not worth a stub).
func (r *Registry) searchCacheStore(tool string, input json.RawMessage, result string) {
	if len(result) < searchCacheMinBytes {
		return
	}
	r.searchCacheMu.Lock()
	defer r.searchCacheMu.Unlock()
	if r.searchCache == nil {
		r.searchCache = make(map[string]string)
	}
	r.searchCache[searchCacheKey(tool, input)] = result
}

// invalidateSearches drops the whole search cache. Called on any agent write (a
// single edit can change what grep/glob/list_dir would return anywhere) so a
// post-edit search re-runs against the current tree.
func (r *Registry) invalidateSearches() {
	r.searchCacheMu.Lock()
	defer r.searchCacheMu.Unlock()
	r.searchCache = nil
}

// DropSearch removes a single search's cached result, keyed exactly as
// searchCacheStore recorded it. The agent calls this when pruneStaleToolResults
// elides a grep/glob/list_dir result out of history: the cached "results are
// already above" stub would otherwise point at output that's just been replaced
// by an elision marker (the prune/dedup re-search treadmill). Surgical, so
// searches still inside the keep window keep their entries. Deleting an absent
// key (or from a nil map) is a safe no-op, so it's harmless to call for any
// elided read-style tool.
func (r *Registry) DropSearch(tool string, input json.RawMessage) {
	r.searchCacheMu.Lock()
	defer r.searchCacheMu.Unlock()
	delete(r.searchCache, searchCacheKey(tool, input))
}

// ClearSearchCache drops the search-result cache. Exported for the agent to call
// on compaction (alongside ClearReadCache): the prior results are no longer
// "above in this conversation" once older messages are summarized away.
func (r *Registry) ClearSearchCache() { r.invalidateSearches() }

// searchDedupStub is the compact reply for an identical repeat search whose
// result is already in the conversation and unchanged since (no edits + no
// compaction since it was cached).
func searchDedupStub(tool string) string {
	return fmt.Sprintf("(this %s call is identical to an earlier one this session and nothing has been edited since — its results are already above in this conversation; no need to repeat it)", tool)
}

// sandboxConfig resolves the active bash-sandbox setting: the explicit one set
// via SetSandbox, or the environment fallback.
func (r *Registry) sandboxConfig() sandboxSetting {
	if r.sandbox != nil {
		return *r.sandbox
	}
	return sandboxSetting{enabled: sandboxEnabledEnv(), network: sandboxNetworkEnv()}
}

// NewRegistry creates a registry rooted at workingDir.
func NewRegistry(workingDir string) *Registry {
	return &Registry{
		root:      workingDir,
		tools:     map[string]Tool{},
		ignore:    LoadIgnore(workingDir),
		jobs:      newJobStore(),
		tempRoots: tempDirs(),
	}
}

// Jobs returns the background-job store so hosts can reap running jobs on
// shutdown via Jobs().KillAll().
func (r *Registry) Jobs() *JobStore { return r.jobs }

// addCloser registers a teardown func run by Close (e.g. the browser tool's
// Chrome). Safe for concurrent use.
func (r *Registry) addCloser(f func()) {
	r.closeMu.Lock()
	r.closers = append(r.closers, f)
	r.closeMu.Unlock()
}

// Close reaps everything this registry started: registered closers (e.g. a
// headless browser) and any background bash jobs. Hosts should defer it on
// shutdown. Safe to call on a registry that started nothing.
func (r *Registry) Close() {
	r.closeMu.Lock()
	cs := r.closers
	r.closers = nil
	r.closeMu.Unlock()
	for _, f := range cs {
		f()
	}
	r.jobs.KillAll()
}

// Root returns the registry's working-directory root (the path-sandbox boundary).
func (r *Registry) Root() string { return r.root }

// SetRoot re-roots the registry at dir IN PLACE: it changes the path-sandbox
// boundary, reloads the ignore set for the new tree, and clears the root-relative
// caches (repo map + read/search dedup), while KEEPING every registered tool —
// including MCP tools the host added — so a live session can switch its working
// dir (the /cd command) without losing its toolset. Relative tool paths now
// resolve against dir; extra/temp roots are unchanged. Not safe to call during an
// in-flight tool call (callers gate it on the session being idle).
func (r *Registry) SetRoot(dir string) {
	r.root = dir
	r.ignore = LoadIgnore(dir)
	// Reset the orientation/dedup caches: they're keyed/derived against the old
	// root and would otherwise leak across the switch.
	r.repoMapOnce = sync.Once{}
	r.repoMapStr = ""
	r.readCacheMu.Lock()
	r.readCache = nil
	r.readCacheMu.Unlock()
	r.searchCacheMu.Lock()
	r.searchCache = nil
	r.searchCacheMu.Unlock()
}

// allRoots returns the primary working dir followed by any extra allowed roots.
func (r *Registry) allRoots() []string {
	if len(r.extraRoots) == 0 {
		return []string{r.root}
	}
	return append([]string{r.root}, r.extraRoots...)
}

// Roots returns all workspace roots (primary first). Used by the bash sandbox to
// allow writes under every root, and by callers that surface the active roots.
func (r *Registry) Roots() []string { return r.allRoots() }

// AddRoots whitelists additional workspace roots that file tools may read and
// write, beyond the primary working dir (multi-root, scope A). Paths are made
// absolute and cleaned; the primary root and duplicates are skipped. Relative
// tool paths still resolve against the primary root — extra roots are reached by
// absolute path.
func (r *Registry) AddRoots(dirs ...string) {
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		abs := d
		if !filepath.IsAbs(abs) {
			if a, err := filepath.Abs(abs); err == nil {
				abs = a
			}
		}
		abs = filepath.Clean(abs)
		if abs == r.root {
			continue
		}
		dup := false
		for _, e := range r.extraRoots {
			if e == abs {
				dup = true
				break
			}
		}
		if !dup {
			r.extraRoots = append(r.extraRoots, abs)
		}
	}
}

// Ignore returns the ignore set for read-side tools (glob/grep/list_dir).
func (r *Registry) Ignore() *IgnoreSet { return r.ignore }

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Unregister removes a tool by name (no-op if absent). Used when a tool's
// backing set becomes empty — e.g. all skills disabled — or on MCP reload.
func (r *Registry) Unregister(name string) {
	if _, ok := r.tools[name]; !ok {
		return
	}
	delete(r.tools, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// Schemas returns the tool definitions to advertise to the model.
func (r *Registry) Schemas() []llm.Tool {
	out := make([]llm.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		desc := t.Description()
		if r.conciseSchemas {
			desc = firstSentence(desc)
		}
		out = append(out, llm.Tool{
			Name:        t.Name(),
			Description: desc,
			InputSchema: t.Schema(),
		})
	}
	return out
}

// SetConciseSchemas toggles concise tool descriptions in Schemas(): when on,
// each tool's Description is trimmed to its first sentence in the specs sent to
// the model. Opt-in (prompt caching already amortizes full descriptions on
// cached routes); useful on uncached routes or token-tight setups.
func (r *Registry) SetConciseSchemas(v bool) { r.conciseSchemas = v }

// SetEmbedder installs the embedder used for optional semantic reranking in
// search_code. Pass nil to disable (pure BM25).
// flusher is the optional capability an embedder implements to persist buffered
// state (e.g. EmbedCache's on-disk cache) when the registry shuts down.
type flusher interface{ Flush() }

// SetEmbedder installs the embedding backend for search_code's semantic rerank.
// If the embedder can Flush (the persistent EmbedCache), its flush is registered
// as a Close closer, so embeds buffered since the last periodic save are
// persisted when the registry closes on exit (main defers registry.Close).
func (r *Registry) SetEmbedder(e Embedder) {
	r.embedder = e
	if f, ok := e.(flusher); ok {
		r.addCloser(f.Flush)
	}
}

// firstSentence returns the leading sentence of s: text up to the first newline
// or the first period followed by a space/end, whichever comes first.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	end := len(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < end {
		end = i
	}
	for i := 0; i < end; i++ {
		if s[i] == '.' && (i+1 >= len(s) || s[i+1] == ' ') {
			end = i + 1
			break
		}
	}
	return strings.TrimSpace(s[:end])
}

// Names returns the registered tool names in registration order.
func (r *Registry) Names() []string {
	names := append([]string(nil), r.order...)
	return names
}

// Has reports whether a tool with the given name is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Get returns the registered tool by name, or nil. Used by callers that need
// the live instance (not just its schema) to introspect it — e.g. the context
// meter sizing the per-persona / per-skill detail of the task/skill tools.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// schemaRequired returns the "required" field names declared by a tool's JSON
// schema (nil when there are none or the schema can't be parsed).
func schemaRequired(s json.RawMessage) []string {
	var sc struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(s, &sc)
	return sc.Required
}

// isBlankJSON reports whether a JSON value is effectively empty: null, "", {}, [],
// or whitespace — i.e. a required field that's present but carries no value.
func isBlankJSON(v json.RawMessage) bool {
	switch strings.TrimSpace(string(v)) {
	case "", "null", `""`, "{}", "[]":
		return true
	}
	return false
}

// missingArgsError returns a self-correcting error when a tool that declares
// required fields was called with some of them absent or blank — the usual shape
// of an incomplete tool call a model emits (e.g. edit_file with no path). It
// names exactly what to supply so the model retries correctly, rather than the
// tool's terse downstream complaint ("path is required"). Returns nil when every
// required field is present and non-blank, when the tool declares no required
// fields (so argument-less tools like git_status are unaffected), or when the
// input isn't a JSON object (the tool's own decode reports that parse error).
func missingArgsError(name string, schema, input json.RawMessage) error {
	req := schemaRequired(schema)
	if len(req) == 0 {
		return nil
	}
	got := map[string]json.RawMessage{}
	if strings.TrimSpace(string(input)) != "" {
		if json.Unmarshal(input, &got) != nil {
			return nil // not an object — let the tool's decode surface the parse error
		}
	}
	var missing []string
	for _, f := range req {
		if v, ok := got[f]; !ok || isBlankJSON(v) {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s was called with missing/empty required argument(s): %s — retry the call with all required fields: %s",
		name, strings.Join(missing, ", "), strings.Join(req, ", "))
}

// Execute dispatches a tool call by name.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q (available: %s)", name, strings.Join(sortedNames(r.tools), ", "))
	}
	if err := missingArgsError(name, t.Schema(), input); err != nil {
		return "", err
	}
	return t.Execute(ctx, input)
}

// ExecuteImages dispatches a tool call, returning any images the tool produced
// (for tools implementing ImageProducer, e.g. the browser screenshot) alongside
// the text output. Tools that don't produce images go through the normal text
// Execute path with a nil image slice.
func (r *Registry) ExecuteImages(ctx context.Context, name string, input json.RawMessage) (string, []Image, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", nil, fmt.Errorf("unknown tool %q (available: %s)", name, strings.Join(sortedNames(r.tools), ", "))
	}
	if err := missingArgsError(name, t.Schema(), input); err != nil {
		return "", nil, err
	}
	if ip, ok := t.(ImageProducer); ok {
		return ip.ExecuteImages(ctx, input)
	}
	out, err := t.Execute(ctx, input)
	return out, nil, err
}

// Mutates reports whether the named tool changes state and should be gated
// behind approval. Unknown tools are treated as mutating (fail safe).
func (r *Registry) Mutates(name string) bool {
	t, ok := r.tools[name]
	if !ok {
		return true
	}
	return t.Mutates()
}

// Preview returns a pending-change preview for the named tool when it supports
// previewing. ok is false when the tool is not a Previewer.
func (r *Registry) Preview(ctx context.Context, name string, input json.RawMessage) (Preview, bool) {
	t, exists := r.tools[name]
	if !exists {
		return Preview{}, false
	}
	p, ok := t.(Previewer)
	if !ok {
		return Preview{}, false
	}
	if err := missingArgsError(name, t.Schema(), input); err != nil {
		// Incomplete tool call — surface the self-correcting message as the
		// preview summary instead of attempting (and failing) a diff.
		return Preview{Summary: err.Error()}, true
	}
	prev, err := p.Preview(ctx, input)
	if err != nil {
		return Preview{Summary: "preview unavailable: " + err.Error()}, true
	}
	return prev, true
}

func sortedNames(m map[string]Tool) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolve turns a possibly-relative path into an absolute path under one of the
// workspace roots and guards against escaping all of them via traversal.
// Relative paths are taken against the primary root; absolute paths are accepted
// when they fall under the primary root, any extra allowed root, or a system
// temp directory (scratch space — see tempDirs).
func (r *Registry) resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.root, p)
	}
	p = filepath.Clean(p)

	for _, root := range r.allRoots() {
		if underRoot(root, p) {
			return p, nil
		}
	}
	// Escape hatch: allow the OS temp dir (and the conventional /tmp) as scratch
	// space. Models routinely target absolute /tmp paths for throwaway artifacts
	// (e.g. an audit summary), and rejecting them is surprising; this keeps those
	// writes working without exposing the rest of the filesystem.
	for _, root := range r.tempRoots {
		if underRoot(root, p) {
			return p, nil
		}
	}
	if len(r.extraRoots) > 0 {
		return "", fmt.Errorf("path %q is outside the workspace roots (and not under a system temp dir)", path)
	}
	return "", fmt.Errorf("path %q escapes the working directory (and is not under a system temp dir)", path)
}

// readForMentionCap bounds the file size ReadForMention will inline; larger
// files are left for a paged read_file rather than loaded whole.
const readForMentionCap = 1 << 20 // 1 MiB

// ReadForMention resolves path within the workspace roots and returns its
// content as read_file-style numbered lines ("<n>: <line>") for inlining an
// @file mention, with ok=false when the path escapes the roots, isn't a regular
// file, is too large, is binary, is empty, or can't be read. The caller applies
// the model-facing byte budget (e.g. via capToolOutput). It reuses resolve, so
// it enforces the same workspace boundary as every other file tool.
func (r *Registry) ReadForMention(path string) (string, bool) {
	abs, err := r.resolve(path)
	if err != nil {
		return "", false
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() > readForMentionCap {
		return "", false
	}
	data, err := os.ReadFile(abs)
	if err != nil || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", false // unreadable or binary
	}
	body := strings.TrimRight(string(data), "\n")
	if body == "" {
		return "", false // nothing useful to inline
	}
	lines := strings.Split(body, "\n")
	var b strings.Builder
	for i, ln := range lines {
		fmt.Fprintf(&b, "%d: %s\n", i+1, ln)
	}
	return b.String(), true
}

// underRoot reports whether cleaned absolute path p is root itself or a
// descendant of it (i.e. not reachable only via ".." traversal).
func underRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// tempDirs returns the system scratch locations file tools may read/write
// outside the workspace roots: the OS temp dir (TMPDIR, e.g. /var/folders/...
// on macOS) and the conventional /tmp. Both their literal and symlink-resolved
// forms are included (/tmp is a symlink to /private/tmp on macOS) so a path
// written either way still matches. Empty/duplicate entries are dropped.
func tempDirs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
		if real, err := filepath.EvalSymlinks(d); err == nil {
			real = filepath.Clean(real)
			if !seen[real] {
				seen[real] = true
				out = append(out, real)
			}
		}
	}
	add(os.TempDir())
	add("/tmp")
	return out
}

// decode unmarshals tool input JSON into v with a helpful error.
func decode(input json.RawMessage, v any) error {
	if len(input) == 0 {
		return nil
	}
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("invalid tool input: %v", err)
	}
	return nil
}

// schema is a small helper to build an input schema literal.
func schema(s string) json.RawMessage { return json.RawMessage(s) }

// flexInt is an int for tool-input fields that tolerates the shapes models
// actually emit for "integer" arguments: a real JSON number (10), a numeric
// string ("10" — the common offender that broke read_file), a float-ish value
// (10.0 / "10.0", truncated), and loosely-formatted numeric strings with stray
// commas ("1," / "1,000") or a trailing non-numeric tail ("10 lines"). null /
// "" / absent decode to 0. Use it for any model-supplied numeric field so a
// quoted or comma-laden number can't fail the whole call.
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	// Numeric string ("10" / "10.0" / "1," / "1,000"): unwrap the quotes, then
	// parse tolerantly.
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		if str == "" {
			return nil
		}
		if v, ok := parseFlexNumber(str); ok {
			*n = flexInt(v)
			return nil
		}
		return fmt.Errorf("invalid integer %q", str)
	}
	// Bare JSON number — via float64 so 10.0 doesn't fail an int unmarshal.
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = flexInt(int(f))
	return nil
}

// parseFlexNumber extracts an int from a loosely-formatted numeric string the
// models sometimes emit: it strips stray commas (a trailing "1," or thousands
// separators "1,000"), then parses; failing that, it salvages the leading
// signed numeric run (so "10 lines" or "10abc" → 10). Returns false when no
// number is present (e.g. "abc"), so the caller can still reject it.
func parseFlexNumber(s string) (int, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f), true
	}
	// Salvage the leading numeric run: optional sign, digits, optional decimal.
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	start := i
	seenDot := false
	for i < len(s) {
		c := s[i]
		if c >= '0' && c <= '9' {
			i++
			continue
		}
		if c == '.' && !seenDot {
			seenDot = true
			i++
			continue
		}
		break
	}
	if i == start { // no digits after the optional sign
		return 0, false
	}
	if f, err := strconv.ParseFloat(s[:i], 64); err == nil {
		return int(f), true
	}
	return 0, false
}
