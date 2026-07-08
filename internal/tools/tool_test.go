package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestFlexIntUnmarshal covers the shapes models actually emit for "integer"
// tool args: bare numbers, numeric strings (the offender that broke read_file),
// float-ish forms, and absent/empty/null (→ 0).
func TestFlexIntUnmarshal(t *testing.T) {
	type holder struct {
		N flexInt `json:"n"`
	}
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{`{"n":10}`, 10, false},
		{`{"n":"10"}`, 10, false},       // numeric string — the reported bug
		{`{"n":" 12 "}`, 12, false},     // padded numeric string
		{`{"n":10.0}`, 10, false},       // float number
		{`{"n":"5.0"}`, 5, false},       // float-ish string (truncated)
		{`{"n":"1,"}`, 1, false},        // trailing comma — the reported bug
		{`{"n":"1,000"}`, 1000, false},  // thousands separator
		{`{"n":"10 lines"}`, 10, false}, // leading numeric run salvaged
		{`{"n":0}`, 0, false},
		{`{"n":""}`, 0, false},   // empty string → 0
		{`{"n":null}`, 0, false}, // null → 0
		{`{}`, 0, false},         // absent → 0
		{`{"n":"abc"}`, 0, true}, // non-numeric string → error
		{`{"n":true}`, 0, true},  // bool → error
	}
	for _, c := range cases {
		var h holder
		err := json.Unmarshal([]byte(c.in), &h)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error, got n=%d", c.in, int(h.N))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.in, err)
			continue
		}
		if int(h.N) != c.want {
			t.Errorf("%s: got %d, want %d", c.in, int(h.N), c.want)
		}
	}
}

// TestMissingArgsError covers the self-correcting message for incomplete tool
// calls: a required field that's absent or blank is named (so the model retries
// with it), while a complete call, an argument-less tool, or non-object input
// passes through to the tool's own handling.
func TestMissingArgsError(t *testing.T) {
	const editSchema = `{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["path","old_string","new_string"]}`
	const noReqSchema = `{"type":"object","properties":{}}` // e.g. git_status

	cases := []struct {
		name   string
		schema string
		input  string
		want   string // substring the error must contain; "" = expect no error
	}{
		{"empty", editSchema, ``, "path, old_string, new_string"},
		{"empty object", editSchema, `{}`, "path, old_string, new_string"},
		{"missing path", editSchema, `{"old_string":"a","new_string":"b"}`, "missing/empty required argument(s): path"},
		{"blank path", editSchema, `{"path":"","old_string":"a","new_string":"b"}`, "path"},
		{"complete", editSchema, `{"path":"f.go","old_string":"a","new_string":"b"}`, ""},
		{"no required fields", noReqSchema, ``, ""},
		{"non-object input", editSchema, `"oops"`, ""}, // let the tool's decode report it
	}
	for _, c := range cases {
		err := missingArgsError("edit_file", json.RawMessage(c.schema), json.RawMessage(c.input))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: expected no error, got %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.want)
		}
	}
}

// TestExecuteSelfCorrectsMissingArgs drives the dispatch path the agent uses: an
// edit_file call with no arguments returns the self-correcting error (naming the
// required fields) from both Execute and ExecuteImages, and the Preview surfaces
// it as the summary instead of "preview unavailable: path is required".
func TestExecuteSelfCorrectsMissingArgs(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	reg.Register(&editTool{r: reg})

	_, err := reg.Execute(context.Background(), "edit_file", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "path, old_string, new_string") {
		t.Errorf("Execute: error = %v, want it to name the required fields", err)
	}
	if _, _, err := reg.ExecuteImages(context.Background(), "edit_file", json.RawMessage(``)); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("ExecuteImages: error = %v, want a self-correcting message", err)
	}
	prev, ok := reg.Preview(context.Background(), "edit_file", json.RawMessage(`{}`))
	if !ok || !strings.Contains(prev.Summary, "required") || strings.Contains(prev.Summary, "preview unavailable") {
		t.Errorf("Preview summary = %q, want the self-correcting message (not 'preview unavailable')", prev.Summary)
	}
}
