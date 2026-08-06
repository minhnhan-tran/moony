package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pysty1995/moony/internal/config"
)

// TestPersonaFieldTypesRenderable guards the contract between personaFields and
// the browser's makeField(): the web UI only knows how to render "bool",
// "select", "secret" and plain text. Any other type silently falls through to a
// TEXT input, which then posts a STRING for what the wire declares as a *bool —
// failing json.Unmarshal for the WHOLE payload and 400-ing every other edit with
// it (this is exactly how a stray "toggle" type broke saving provider keys).
func TestPersonaFieldTypesRenderable(t *testing.T) {
	renderable := map[string]bool{"bool": true, "select": true, "secret": true, "text": true, "": true}
	for _, f := range personaFields(&config.Config{}, nil, nil, nil) {
		if !renderable[f.Type] {
			t.Errorf("field %q has type %q, which app-core.js makeField() renders as a text input; use \"bool\" for booleans", f.Key, f.Type)
		}
	}
}

// TestPersonaBoolFieldsPostAsBool feeds every bool-typed field back through the
// wire the way the browser does and asserts the payload still unmarshals: a bool
// field must map to a *bool on personaEdit, or one bad field rejects them all.
func TestPersonaBoolFieldsPostAsBool(t *testing.T) {
	payload := map[string]any{}
	for _, f := range personaFields(&config.Config{}, nil, nil, nil) {
		if f.Type == "bool" {
			payload[f.Key] = true // checkbox reader returns a real boolean
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var edit personaEdit
	if err := json.Unmarshal(body, &edit); err != nil {
		t.Fatalf("bool fields must unmarshal into personaEdit, got %v", err)
	}
}

// TestPersonaSaveRejectsNothingOnFullPayload posts a payload shaped like the one
// the browser sends for an untouched form — every field at its rendered value —
// and requires it to unmarshal. A single type mismatch here is a 400 that loses
// the user's provider credentials, so it must be caught in the wire, not the UI.
func TestPersonaSaveRejectsNothingOnFullPayload(t *testing.T) {
	cfg := &config.Config{AutoUpdate: true}
	payload := map[string]any{}
	for _, f := range personaFields(cfg, nil, nil, nil) {
		switch f.Type {
		case "bool":
			payload[f.Key] = f.Value
		case "secret":
			// blank secrets are omitted by the browser (blank => keep existing)
		default:
			if f.Value == nil {
				payload[f.Key] = ""
			} else {
				payload[f.Key] = strings.TrimSpace(anyToString(f.Value))
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var edit personaEdit
	if err := json.Unmarshal(body, &edit); err != nil {
		t.Fatalf("a full untouched-form submit must unmarshal (else the save 400s and drops provider keys): %v", err)
	}
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(t)
		return strings.Trim(string(b), `"`)
	}
}
