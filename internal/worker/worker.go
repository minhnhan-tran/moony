// Package worker lets moony dispatch focused tasks to other coding
// agents running as out-of-process "workers" — Claude Code, an ACP agent, and
// Gemini. Unlike the in-process `task` sub-agents, a worker is
// a separate agent binary with its own context, model, and tools; the agent
// drives it over the binary's headless interface and gets back a final answer.
//
// Three backends, two transports, one Session interface:
//
//   - "claude":  one-shot `claude -p … --output-format json`, made stateful by
//     threading the returned session_id through `--resume`. Read-only via
//     `--permission-mode plan`.
//   - "optimus": a long-lived ACP process driven over the Agent Client
//     Protocol (JSON-RPC on stdio). Read-only by rejecting any edit/execute
//     permission request from the client side.
//
// Workers are configured in workers.json (global + per-project) and reused
// across dispatches so a worker keeps its context warm.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// Result is the outcome of one dispatch to a worker.
type Result struct {
	Text      string // the worker's final answer
	SessionID string // backend session id, threaded into the next dispatch
	OK        bool
	Err       string // non-empty when the worker reported an error
}

// Event is a streaming update from a worker mid-dispatch (assistant text,
// tool activity, …). Backends emit these; callers may ignore them.
type Event struct {
	Kind string // "text" | "tool" | "info"
	Text string
}

// Options configure a worker session at open time.
type Options struct {
	Cwd      string        // working directory the worker operates in
	Model    string        // provider/model (backend-specific; optional)
	Agent    string        // named agent/persona within the worker (optional)
	ReadOnly bool          // forbid edits/exec (default true)
	Timeout  time.Duration // per-dispatch timeout
	Command  string        // override the backend binary (default: claude/optimus)
}

// Session is a stateful conversation with one worker. Prompt may be called
// repeatedly to continue the worker's context; Close releases it.
type Session interface {
	Prompt(ctx context.Context, task string, onEvent func(Event)) (Result, error)
	Close() error
}

// Backend identifiers.
const (
	BackendClaude  = "claude"
	BackendOptimus = "optimus"
	BackendGemini  = "gemini"
)

// isKnownBackend reports whether b is a backend this build can drive. Used to
// reject unsupported configs in Load and Manager.Add.
func isKnownBackend(b string) bool {
	switch b {
	case BackendClaude, BackendOptimus, BackendGemini:
		return true
	default:
		return false
	}
}

const defaultTimeout = 10 * time.Minute

// Config is one worker definition from workers.json.
type Config struct {
	Name       string `json:"name"`
	Backend    string `json:"backend"`              // "claude" | "optimus"
	Model      string `json:"model,omitempty"`      // provider/model
	Agent      string `json:"agent,omitempty"`      // named agent within the worker
	Cwd        string `json:"cwd,omitempty"`        // default: the agent's working dir
	ReadOnly   *bool  `json:"readOnly,omitempty"`   // default true
	TimeoutSec int    `json:"timeoutSec,omitempty"` // default 600
	Command    string `json:"command,omitempty"`    // override the binary
	Disabled   bool   `json:"disabled,omitempty"`
	Desc       string `json:"description,omitempty"`

	// srcPath is the workers.json this config was loaded from (global or
	// project). Not serialized; used to persist /workers edits back in place.
	srcPath string `json:"-"`
}

// Enabled reports whether the worker is active (not disabled).
func (c Config) Enabled() bool { return !c.Disabled }

// Configured reports whether this worker came from a workers.json file (vs being
// auto-detected), so callers can offer to remove only the ones that persist.
func (c Config) Configured() bool { return c.srcPath != "" }

// readOnly resolves the default (true) when unset.
func (c Config) readOnly() bool { return c.ReadOnly == nil || *c.ReadOnly }

// file is the on-disk schema: {"workers": [ … ]}.
type file struct {
	Workers []Config `json:"workers"`
}

func globalPath() string {
	p, err := config.GlobalConfigPath("workers.json")
	if err != nil {
		return ""
	}
	return p
}

func projectPath(workingDir string) string {
	return config.ProjectPath(workingDir, "workers.json")
}

// Load reads global (~/.config/moony/workers.json) then project
// (<workingDir>/.moony/workers.json) configs; project entries override global ones
// with the same name. Disabled workers are included (the Manager decides what's
// active), tagged with their source file so edits persist in place. Invalid
// entries are skipped; missing files are not an error.
func Load(workingDir string) ([]Config, error) {
	var firstErr error
	merged := map[string]Config{}
	order := []string{}

	add := func(cfgs []Config, path string) {
		for _, c := range cfgs {
			if c.Name == "" || c.Backend == "" {
				continue
			}
			if !isKnownBackend(c.Backend) {
				continue
			}
			c.srcPath = path
			if _, seen := merged[c.Name]; !seen {
				order = append(order, c.Name)
			}
			merged[c.Name] = c
		}
	}

	gp := globalPath()
	if gp != "" {
		cfgs, err := readFile(gp)
		if err != nil {
			firstErr = err
		}
		add(cfgs, gp)
	}
	pp := projectPath(workingDir)
	cfgs, err := readFile(pp)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	add(cfgs, pp)

	out := make([]Config, 0, len(order))
	for _, n := range order {
		out = append(out, merged[n])
	}
	return out, firstErr
}

func readFile(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f.Workers, nil
}

// writeFile persists a workers file atomically.
func writeFile(path string, f file) error {
	if path == "" {
		return fmt.Errorf("no workers.json path to write")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// editConfig rewrites the entry named `name` in the workers file at `path`,
// applying mutate. It re-reads the file so other entries are preserved.
func editConfig(path, name string, mutate func(*Config)) error {
	if path == "" {
		return fmt.Errorf("worker %q has no source file to edit", name)
	}
	cfgs, err := readFile(path)
	if err != nil {
		return err
	}
	found := false
	for i := range cfgs {
		if cfgs[i].Name == name {
			mutate(&cfgs[i])
			found = true
		}
	}
	if !found {
		return fmt.Errorf("worker %q not found in %s", name, path)
	}
	return writeFile(path, file{Workers: cfgs})
}

// upsertConfig adds cfg to the workers file at path, or replaces an existing
// entry with the same name. Other entries are preserved.
func upsertConfig(path string, cfg Config) error {
	cfgs, err := readFile(path)
	if err != nil {
		return err
	}
	found := false
	for i := range cfgs {
		if cfgs[i].Name == cfg.Name {
			cfgs[i] = cfg
			found = true
		}
	}
	if !found {
		cfgs = append(cfgs, cfg)
	}
	return writeFile(path, file{Workers: cfgs})
}

// Manager owns the configured workers and their open sessions.
type Manager struct {
	workingDir string
	byName     map[string]Config
	order      []string

	mu       sync.Mutex
	sessions map[string]Session
	// dispatchLocks serialize dispatches to the SAME worker: a worker is a
	// single stateful session (one process / one --resume chain), so two
	// concurrent prompts on it would interleave and corrupt its context.
	// Different workers get different locks and run in parallel. Guarded by mu.
	dispatchLocks map[string]*sync.Mutex

	readyMu sync.Mutex
	ready   map[string]bool // engine command -> last --version probe result
}

// New builds a manager for the given working directory and worker configs.
func New(workingDir string, cfgs []Config) *Manager {
	m := &Manager{
		workingDir:    workingDir,
		byName:        make(map[string]Config, len(cfgs)),
		sessions:      map[string]Session{},
		dispatchLocks: map[string]*sync.Mutex{},
		ready:         map[string]bool{},
	}
	for _, c := range cfgs {
		if _, ok := m.byName[c.Name]; !ok {
			m.order = append(m.order, c.Name)
		}
		m.byName[c.Name] = c
	}
	return m
}

// Empty reports whether any workers are configured (enabled or not).
func (m *Manager) Empty() bool { return m == nil || len(m.byName) == 0 }

// Configs returns the enabled workers in declared order — the set the model can
// dispatch to (used for the delegate tool's description/schema).
func (m *Manager) Configs() []Config {
	if m == nil {
		return nil
	}
	out := make([]Config, 0, len(m.order))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.order {
		if c := m.byName[n]; c.Enabled() {
			out = append(out, c)
		}
	}
	return out
}

// All returns every configured worker (enabled or disabled), in declared order —
// for management/listing (the /workers command).
func (m *Manager) All() []Config {
	if m == nil {
		return nil
	}
	out := make([]Config, 0, len(m.order))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.order {
		out = append(out, m.byName[n])
	}
	return out
}

// optsFor builds the open Options for a worker, defaulting cwd and timeout.
func (m *Manager) optsFor(c Config) Options {
	cwd := c.Cwd
	if cwd == "" {
		cwd = m.workingDir
	}
	timeout := defaultTimeout
	if c.TimeoutSec > 0 {
		timeout = time.Duration(c.TimeoutSec) * time.Second
	}
	return Options{
		Cwd:      cwd,
		Model:    c.Model,
		Agent:    c.Agent,
		ReadOnly: c.readOnly(),
		Timeout:  timeout,
		Command:  c.Command,
	}
}

// open constructs a Session for a backend. Exposed as a var so tests can stub it.
var open = func(ctx context.Context, c Config, opts Options) (Session, error) {
	switch c.Backend {
	case BackendClaude:
		return openClaude(opts), nil
	case BackendOptimus:
		return openACP(ctx, opts)
	case BackendGemini:
		return openGemini(opts), nil
	default:
		return nil, fmt.Errorf("unknown worker backend %q", c.Backend)
	}
}

// lockFor returns the per-worker dispatch mutex, creating it on first use.
// Caller must hold m.mu. Lock ordering is always per-worker-lock first, then
// m.mu (briefly), never the reverse, so there is no deadlock.
func (m *Manager) lockFor(name string) *sync.Mutex {
	l := m.dispatchLocks[name]
	if l == nil {
		l = &sync.Mutex{}
		m.dispatchLocks[name] = l
	}
	return l
}

// Dispatch sends task to the named worker, opening (and caching) its session on
// first use so subsequent dispatches continue the same context. Dispatches to
// the same worker are serialized (a worker has one session); dispatches to
// different workers run concurrently.
func (m *Manager) Dispatch(ctx context.Context, name, task string, onEvent func(Event)) (Result, error) {
	if m == nil {
		return Result{}, fmt.Errorf("no workers configured")
	}
	m.mu.Lock()
	c, ok := m.byName[name]
	if !ok {
		m.mu.Unlock()
		return Result{}, fmt.Errorf("unknown worker %q", name)
	}
	if c.Disabled {
		m.mu.Unlock()
		return Result{}, fmt.Errorf("worker %q is disabled (enable it with /workers enable %s)", name, name)
	}
	lock := m.lockFor(name)
	m.mu.Unlock()

	// Hold the per-worker lock across open+Prompt so two concurrent dispatches
	// to this worker can't talk to its single session at once (different
	// workers use different locks and stay parallel).
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	sess := m.sessions[name]
	m.mu.Unlock()

	if sess == nil {
		s, err := open(ctx, c, m.optsFor(c))
		if err != nil {
			return Result{}, fmt.Errorf("open worker %q: %w", name, err)
		}
		m.mu.Lock()
		m.sessions[name] = s
		m.mu.Unlock()
		sess = s
	}

	if onEvent == nil {
		onEvent = func(Event) {}
	}
	return sess.Prompt(ctx, task, onEvent)
}

// Reset closes and forgets a worker's session, so the next dispatch starts fresh.
func (m *Manager) Reset(name string) {
	if m == nil {
		return
	}
	// Take the per-worker lock first so we don't close a session out from under
	// an in-flight dispatch to the same worker.
	m.mu.Lock()
	lock := m.lockFor(name)
	m.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	sess := m.sessions[name]
	delete(m.sessions, name)
	m.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

// CloseAll releases every open worker session. Safe on a nil manager; call on
// process exit so held-open worker processes (ACP) don't outlive the agent.
func (m *Manager) CloseAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]Session{}
	m.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
}

// SetEnabled enables/disables a worker, persisting the change to its workers.json
// and applying it live (the delegate tool reflects enabled workers immediately;
// disabling also closes any open session). Returns an error for an unknown
// worker or if persistence fails.
func (m *Manager) SetEnabled(name string, enabled bool) error {
	if m == nil {
		return fmt.Errorf("no workers configured")
	}
	m.mu.Lock()
	c, ok := m.byName[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown worker %q", name)
	}
	c.Disabled = !enabled
	m.byName[name] = c
	var sess Session
	if !enabled {
		sess = m.sessions[name]
		delete(m.sessions, name)
	}
	path := c.srcPath
	m.mu.Unlock()

	if sess != nil {
		_ = sess.Close()
	}
	if path == "" {
		// Auto-detected worker: materialize it so the toggle persists.
		return m.materialize(name, c)
	}
	return editConfig(path, name, func(cc *Config) { cc.Disabled = !enabled })
}

// SetModel changes a worker's model, persisting it and resetting any open session
// so the next dispatch reopens with the new model. An empty model clears the
// override (the worker falls back to its CLI default).
func (m *Manager) SetModel(name, model string) error {
	if m == nil {
		return fmt.Errorf("no workers configured")
	}
	m.mu.Lock()
	c, ok := m.byName[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown worker %q", name)
	}
	c.Model = model
	m.byName[name] = c
	sess := m.sessions[name]
	delete(m.sessions, name) // reopen with the new model on the next dispatch
	path := c.srcPath
	m.mu.Unlock()

	if sess != nil {
		_ = sess.Close()
	}
	if path == "" {
		return m.materialize(name, c)
	}
	return editConfig(path, name, func(cc *Config) { cc.Model = model })
}

// materialize writes an auto-detected worker's current config into the project
// workers.json (creating the entry) so edits to it persist, and records the new
// source path in memory.
func (m *Manager) materialize(name string, cfg Config) error {
	path := projectPath(m.workingDir)
	cfg.srcPath = "" // never serialized
	if err := upsertConfig(path, cfg); err != nil {
		return err
	}
	m.mu.Lock()
	cc := m.byName[name]
	cc.srcPath = path
	m.byName[name] = cc
	m.mu.Unlock()
	return nil
}

// Add registers a new worker (backend "claude", "optimus", or "gemini")
// and persists it to
// the global (~/.config/moony/workers.json) or project workers.json,
// making it available to the delegate tool immediately. Returns the file written.
// Errors on a blank name, an unsupported backend, or a name that already exists.
func (m *Manager) Add(name, backend, model, command string, global bool) (string, error) {
	if m == nil {
		return "", fmt.Errorf("workers are unavailable in this session")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("a worker name is required")
	}
	if !isKnownBackend(backend) {
		return "", fmt.Errorf("backend must be %q, %q, or %q", BackendClaude, BackendOptimus, BackendGemini)
	}
	path := globalPath()
	if !global {
		path = projectPath(m.workingDir)
	}
	if path == "" {
		return "", fmt.Errorf("could not resolve a workers.json path")
	}
	cfg := Config{Name: name, Backend: backend, Model: strings.TrimSpace(model), Command: strings.TrimSpace(command)}

	m.mu.Lock()
	if _, exists := m.byName[name]; exists {
		m.mu.Unlock()
		return "", fmt.Errorf("worker %q already exists", name)
	}
	stored := cfg
	stored.srcPath = path
	m.byName[name] = stored
	m.order = append(m.order, name)
	m.mu.Unlock()

	if err := upsertConfig(path, cfg); err != nil {
		m.mu.Lock() // roll back the in-memory add on persist failure
		delete(m.byName, name)
		m.order = removeStr(m.order, name)
		m.mu.Unlock()
		return "", err
	}
	return path, nil
}

// Remove deletes a worker from memory (closing any open session) and from its
// workers.json. An auto-detected worker (no source file) is removed from memory
// only and will reappear next launch unless its engine is uninstalled.
func (m *Manager) Remove(name string) error {
	if m == nil {
		return fmt.Errorf("no workers configured")
	}
	m.mu.Lock()
	c, ok := m.byName[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown worker %q", name)
	}
	delete(m.byName, name)
	m.order = removeStr(m.order, name)
	sess := m.sessions[name]
	delete(m.sessions, name)
	path := c.srcPath
	m.mu.Unlock()

	if sess != nil {
		_ = sess.Close()
	}
	if path != "" {
		return removeConfig(path, name)
	}
	return nil
}

// removeStr returns s without v (new slice).
func removeStr(s []string, v string) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// removeConfig drops the named worker from the workers.json at path (no-op if
// it isn't there).
func removeConfig(path, name string) error {
	cfgs, err := readFile(path)
	if err != nil {
		return err
	}
	out := make([]Config, 0, len(cfgs))
	changed := false
	for _, c := range cfgs {
		if c.Name == name {
			changed = true
			continue
		}
		out = append(out, c)
	}
	if !changed {
		return nil
	}
	return writeFile(path, file{Workers: out})
}
