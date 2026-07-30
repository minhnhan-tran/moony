// Package quickactions implements the user-editable preset prompts behind the
// web UI's quick-actions split button (the pill beside the turn pager).
//
// An action is just a labelled prompt: picking it sends Prompt to the agent as
// if the user had typed it. Actions live in two scopes, both named
// quickactions.json:
//
//   - global:  ~/.config/sqeqct-coding-agent/quickactions.json
//   - project: ./.sqe/quickactions.json
//
// The project scope is what the UI edits (presets are usually repo-specific,
// e.g. "rio build"), and it's committable so a team shares the same buttons.
// The global scope is a fallback used only when a project defines none, so a
// fresh checkout still gets the built-in defaults rather than an empty menu.
//
// Ordering is meaningful: the first action is the split button's default, so
// Move lets the UI reorder without a full rewrite.
package quickactions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"fmt"
	"github.com/pysty1995/moony/internal/config"

	"github.com/pysty1995/moony/internal/provider"
)

// Scope selects which file an edit is written to.
type Scope string

const (
	Global  Scope = "global"
	Project Scope = "project"
)

// maxActions bounds the list so a runaway client can't grow the file without
// limit (the menu is a dropdown; hundreds of entries would be unusable anyway).
const maxActions = 50

// Field limits keep one pathological entry from bloating the file or the menu.
const (
	maxLabelLen  = 60
	maxPromptLen = 2000
	maxSubLen    = 120
	maxIconLen   = 60
	maxModelLen  = 120
)

// Action is one preset in the menu.
type Action struct {
	// Prompt is the text sent to the agent — the only required field.
	Prompt string `json:"prompt"`
	// Label is the menu row's title and the split button's text. Defaults to
	// Prompt when empty.
	Label string `json:"label,omitempty"`
	// Sub is the optional grey subtitle under the label.
	Sub string `json:"sub,omitempty"`
	// Icon is an optional SF Symbol name for the row.
	Icon string `json:"icon,omitempty"`

	// Model, Provider and Reasoning optionally override the session's settings for
	// the single turn this action fires, then revert. Empty means "leave as is",
	// so existing files and hand-written actions keep working untouched.
	//
	// Per-turn rather than sticky on purpose: a quick action is a one-shot
	// ("review this diff on a stronger model"), and silently leaving the session on
	// a different model afterwards would make every later message behave oddly with
	// no visible cause. Use the /model, /provider and /reasoning commands for a
	// durable change.
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

// file is the on-disk shape. A struct (rather than a bare array) leaves room to
// add fields later without breaking older files.
type file struct {
	Actions []Action `json:"actions"`
}

// Defaults are the built-in presets seeded into the global scope on first run,
// so the button is useful before anyone configures anything.
func Defaults() []Action {
	return []Action{
		{Prompt: "rio build", Label: "Rio build", Sub: "Signed — sign, commit, push, unsign", Icon: "checkmark.seal"},
		{Prompt: "rio unsigned build", Label: "Rio unsigned build", Sub: "Unsigned — no ac-sign toggle", Icon: "hammer"},
	}
}

// Store is the quick-action state for one working directory.
type Store struct {
	workingDir string
}

// Load returns a store rooted at workingDir. Reads are done per call so edits
// made outside the process (or by another session) are picked up.
func Load(workingDir string) *Store { return &Store{workingDir: workingDir} }

// EnsureGlobalDefaults seeds the global file with the built-in presets the first
// time the agent runs. It never overwrites an existing file, so user edits (and
// a deliberately emptied list) are preserved. Best effort.
func EnsureGlobalDefaults() {
	path := globalPath()
	if path == "" {
		return
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return // exists, or stat failed for another reason — leave it alone
	}
	_ = write(path, file{Actions: Defaults()})
}

// List returns the effective actions: the project list when it has any,
// otherwise the global list. Never returns nil.
func (s *Store) List() []Action {
	if s == nil {
		return []Action{}
	}
	if a := read(projectPath(s.workingDir)).Actions; len(a) > 0 {
		return a
	}
	if a := read(globalPath()).Actions; len(a) > 0 {
		return a
	}
	return []Action{}
}

// EffectiveScope reports which scope List() is currently reading from, so the
// UI can tell the user whether they're looking at project or global presets.
func (s *Store) EffectiveScope() Scope {
	if s != nil && len(read(projectPath(s.workingDir)).Actions) > 0 {
		return Project
	}
	return Global
}

// ListScope returns the raw actions stored in one scope (no fallback).
func (s *Store) ListScope(scope Scope) []Action {
	if s == nil {
		return []Action{}
	}
	a := read(s.pathFor(scope)).Actions
	if a == nil {
		return []Action{}
	}
	return a
}

// Add appends an action to the scope. Prompt is required; a duplicate prompt is
// rejected so the menu can't grow two rows that do the same thing.
func (s *Store) Add(a Action, scope Scope) error {
	a, err := normalize(a)
	if err != nil {
		return err
	}
	list := s.ListScope(scope)
	if len(list) >= maxActions {
		return errors.New("too many quick actions")
	}
	for _, x := range list {
		if x.Prompt == a.Prompt {
			return errors.New("a quick action with that prompt already exists")
		}
	}
	return s.save(append(list, a), scope)
}

// Update replaces the action whose prompt is oldPrompt. Renaming the prompt to
// one that already exists elsewhere in the list is rejected.
func (s *Store) Update(oldPrompt string, a Action, scope Scope) error {
	a, err := normalize(a)
	if err != nil {
		return err
	}
	oldPrompt = strings.TrimSpace(oldPrompt)
	list := s.ListScope(scope)
	idx := indexOf(list, oldPrompt)
	if idx < 0 {
		return errors.New("quick action not found")
	}
	for i, x := range list {
		if i != idx && x.Prompt == a.Prompt {
			return errors.New("a quick action with that prompt already exists")
		}
	}
	list[idx] = a
	return s.save(list, scope)
}

// Remove deletes the action with the given prompt. Removing something that
// isn't there is not an error (the UI may be a refresh behind).
func (s *Store) Remove(prompt string, scope Scope) error {
	prompt = strings.TrimSpace(prompt)
	list := s.ListScope(scope)
	out := make([]Action, 0, len(list))
	for _, x := range list {
		if x.Prompt != prompt {
			out = append(out, x)
		}
	}
	return s.save(out, scope)
}

// Move shifts an action by delta positions (negative = earlier), clamped to the
// ends. Order matters because the first action is the button's default.
func (s *Store) Move(prompt string, delta int, scope Scope) error {
	prompt = strings.TrimSpace(prompt)
	list := s.ListScope(scope)
	i := indexOf(list, prompt)
	if i < 0 {
		return errors.New("quick action not found")
	}
	j := i + delta
	if j < 0 {
		j = 0
	}
	if j > len(list)-1 {
		j = len(list) - 1
	}
	if i == j {
		return nil
	}
	a := list[i]
	list = append(list[:i], list[i+1:]...)
	rest := append([]Action{}, list[j:]...)
	list = append(append(list[:j], a), rest...)
	return s.save(list, scope)
}

// ResetDefaults restores the built-in presets in the given scope.
func (s *Store) ResetDefaults(scope Scope) error { return s.save(Defaults(), scope) }

func (s *Store) save(list []Action, scope Scope) error {
	path := s.pathFor(scope)
	if path == "" {
		return os.ErrInvalid
	}
	return write(path, file{Actions: list})
}

func (s *Store) pathFor(scope Scope) string {
	if scope == Global {
		return globalPath()
	}
	return projectPath(s.workingDir)
}

func indexOf(list []Action, prompt string) int {
	for i, x := range list {
		if x.Prompt == prompt {
			return i
		}
	}
	return -1
}

// normalize trims and validates one action, filling Label from Prompt when the
// user didn't supply one.
func normalize(a Action) (Action, error) {
	a.Prompt = strings.TrimSpace(a.Prompt)
	a.Label = strings.TrimSpace(a.Label)
	a.Sub = strings.TrimSpace(a.Sub)
	a.Icon = strings.TrimSpace(a.Icon)
	a.Model = strings.TrimSpace(a.Model)
	a.Provider = strings.TrimSpace(a.Provider)
	a.Reasoning = strings.TrimSpace(a.Reasoning)
	if a.Prompt == "" {
		return a, errors.New("prompt is required")
	}
	if a.Label == "" {
		a.Label = a.Prompt
	}
	if len(a.Prompt) > maxPromptLen {
		return a, errors.New("prompt is too long")
	}
	a.Label = clip(a.Label, maxLabelLen)
	a.Sub = clip(a.Sub, maxSubLen)
	a.Icon = clip(a.Icon, maxIconLen)
	// Validate the overrides now, while the user is looking at the editor. A bad
	// provider or reasoning level caught here is a clear message; caught mid-turn it
	// is a failed request after the action already appeared to fire.
	prov, reasoning, err := NormalizeOverrides(a.Provider, a.Reasoning)
	if err != nil {
		return a, err
	}
	a.Provider, a.Reasoning = prov, reasoning
	a.Model = clip(a.Model, maxModelLen)
	return a, nil
}

// NormalizeOverrides validates and canonicalizes a provider id and reasoning
// level, returning the stored spellings. Empty inputs stay empty ("inherit the
// session's setting").
//
// Exported because the same values arrive by two routes: saved on an action here,
// and sent per-message on POST /api/sessions/{id}/message. Validating in only one
// of them is how a typo'd level reached the turn logic and silently disabled
// thinking, so both call this.
func NormalizeOverrides(prov, reasoning string) (string, string, error) {
	prov = strings.TrimSpace(prov)
	reasoning = strings.TrimSpace(reasoning)
	if prov != "" {
		prov = strings.ToLower(prov)
		if _, ok := provider.Lookup(prov); !ok {
			return "", "", fmt.Errorf("unknown provider %q", prov)
		}
	}
	if reasoning != "" {
		reasoning = strings.ToLower(reasoning)
		if !validReasoning(reasoning) {
			return "", "", fmt.Errorf("unknown reasoning level %q", reasoning)
		}
		// Canonical spelling so the UI and the turn logic see one form.
		if reasoning == "none" {
			reasoning = config.ReasoningOff
		} else {
			reasoning = config.NormalizeReasoning(reasoning)
		}
	}
	return prov, reasoning, nil
}

// validReasoning reports whether v names a reasoning level.
//
// config.NormalizeReasoning cannot be used as the validator on its own: it
// returns ReasoningOff for anything it does not recognise, so a typo like
// "hgih" would silently become "off" — thinking quietly disabled, no error. So
// accept "off" explicitly and otherwise require that normalization produced a
// level other than off, which is true exactly for the recognised names and their
// aliases ("min", "med", "x-high", "ultrathink", "auto", ...).
func validReasoning(v string) bool {
	if v == config.ReasoningOff || v == "none" {
		return true // "none" is a natural spelling of off
	}
	return config.NormalizeReasoning(v) != config.ReasoningOff
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func globalPath() string {
	p, err := config.GlobalConfigPath("quickactions.json")
	if err != nil {
		return ""
	}
	return p
}

func projectPath(workingDir string) string {
	if strings.TrimSpace(workingDir) == "" {
		return ""
	}
	return filepath.Join(workingDir, ".sqe", "quickactions.json")
}

func read(path string) file {
	var f file
	if path == "" {
		return f
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return f
	}
	_ = json.Unmarshal(data, &f) // malformed file reads as empty, matching permissions
	// Drop entries that can't be rendered/sent so a hand-edited file can't put
	// a blank row in the menu.
	out := f.Actions[:0]
	for _, a := range f.Actions {
		if n, err := normalize(a); err == nil {
			out = append(out, n)
		}
	}
	f.Actions = out
	return f
}

func write(path string, f file) error {
	if path == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if f.Actions == nil {
		f.Actions = []Action{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
