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

// rememberTool lets the model persist a durable fact to project memory. It is
// non-mutating (no approval prompt) since it only ever appends a note to the
// project's AGENTS.md — the whole point is frictionless, automatic memory.
type rememberTool struct{ sink *MemorySink }

func (t *rememberTool) Name() string { return "remember" }

func (t *rememberTool) Description() string {
	return "Save a durable fact or preference to project memory so it persists " +
		"across sessions and appears in future context. Use for stable, reusable " +
		"information — project conventions, build/test commands, architecture " +
		"notes, user preferences, and any rules the user states (e.g. \"always …\", " +
		"\"never …\", \"prefer …\", \"we use …\") — not transient task state. " +
		"Set category to rule/preference/convention/note to file the memory " +
		"appropriately (it is inferred from the text when omitted). Keep each note " +
		"to a concise single line."
}

func (t *rememberTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"text": {"type": "string", "description": "The fact to remember, as a concise one-line note."},
			"category": {"type": "string", "enum": ["rule", "preference", "convention", "note"], "description": "Optional kind of memory. Inferred from the text when omitted."}
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

// RegisterRemember adds the remember and recall tools, backed by sink.
func (r *Registry) RegisterRemember(sink *MemorySink) {
	r.Register(&rememberTool{sink: sink})
	r.Register(&recallTool{sink: sink})
}
