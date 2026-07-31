package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// MemorySink receives memories the model asks to persist. The agent wires its
// Save callback (which appends to AGENTS.md and reloads the system prompt) via
// SetSave; until then the remember tool reports that memory is unavailable.
type MemorySink struct {
	save func(text, category string) (string, error)
	list func() []MemoryNote
}

// MemoryNote is one persisted memory bullet plus the section heading it lives
// under (e.g. "Rules", "Conventions"), used by the recall tool for ranking and
// display.
type MemoryNote struct {
	Section string
	Text    string
}

// NewMemorySink returns an unwired sink. Call SetSave to connect persistence.
func NewMemorySink() *MemorySink { return &MemorySink{} }

// SetSave wires the persistence callback (typically the agent's Remember). The
// callback receives the note text and an optional category key.
func (s *MemorySink) SetSave(fn func(text, category string) (string, error)) { s.save = fn }

// SetList wires the callback returning all persisted memory notes (project +
// global), used by the recall tool. Until set, recall reports memory is empty.
func (s *MemorySink) SetList(fn func() []MemoryNote) { s.list = fn }

// rememberTool lets the model persist a durable fact. It is non-mutating (no
// approval prompt) since it only ever appends a note to a markdown memory file —
// the whole point is frictionless, automatic memory. One implementation backs two
// tools: "remember" (project/repo memory → AGENTS.md) and "remember_user" (the
// global user profile → USER.md), differing only in name, description, and which
// sink they write to.
type rememberTool struct {
	name string
	desc string
	sink *MemorySink
}

func (t *rememberTool) Name() string { return t.name }

func (t *rememberTool) Description() string { return t.desc }

const rememberDesc = "Save a durable fact or preference to project memory so it persists " +
	"across sessions and appears in future context. Use for stable, reusable " +
	"information — project conventions, build/test commands, architecture " +
	"notes, and any rules the user states about THIS project (e.g. \"always …\", " +
	"\"never …\", \"prefer …\", \"we use …\") — not transient task state. " +
	"For facts about the USER themselves (their general coding style, preferred " +
	"output format, workflow habits, standing requests that apply everywhere) use " +
	"remember_user instead. " +
	"Set category to rule/preference/convention/note to file the memory " +
	"appropriately (it is inferred from the text when omitted). Keep each note " +
	"to a concise single line."

const rememberUserDesc = "Save a durable fact about the USER to their global profile so it applies " +
	"to EVERY session across ALL projects (persisted to USER.md and injected into " +
	"every future system prompt). Use for who-the-user-is / how-they-like-to-work: " +
	"their coding style, preferred output or response format, tone, workflow habits, " +
	"tools they favor, and standing personal requests that are not tied to any one " +
	"repository. Do NOT use this for project-specific conventions or build/test " +
	"commands — those belong in `remember`. " +
	"Set category to style/preference/format/note (inferred when omitted). Keep " +
	"each note to a concise single line."

func (t *rememberTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"text": {"type": "string", "description": "The fact to remember, as a concise one-line note."},
			"category": {"type": "string", "description": "Optional kind of memory (e.g. rule/preference/convention/note, or style/format for the user profile). Inferred from the text when omitted."}
		},
		"required": ["text"]
	}`)
}

func (t *rememberTool) Mutates() bool { return false }

func (t *rememberTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Text     string `json:"text"`
		Category string `json:"category"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if t.sink == nil || t.sink.save == nil {
		return "", fmt.Errorf("memory is not available")
	}
	return t.sink.save(in.Text, in.Category)
}

// RegisterRemember adds the remember and recall tools, backed by sink. recall
// also gets a Registry reference so it can reuse whatever embedder SetEmbedder
// installs for search_code's semantic rerank (set after registration, so the
// tool must read r.embedder lazily rather than capturing it here).
func (r *Registry) RegisterRemember(sink *MemorySink) {
	r.Register(&rememberTool{name: "remember", desc: rememberDesc, sink: sink})
	r.Register(&recallTool{sink: sink, r: r})
}

// RegisterRememberUser adds the remember_user tool, writing to the global user
// profile (USER.md) rather than project memory. Separate from RegisterRemember so
// a caller with no user-profile store simply doesn't register it.
func (r *Registry) RegisterRememberUser(sink *MemorySink) {
	r.Register(&rememberTool{name: "remember_user", desc: rememberUserDesc, sink: sink})
}
