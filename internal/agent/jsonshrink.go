package agent

// jsonshrink.go reduces oversized JSON tool output before it reaches the model.
// A lot of tool/MCP output is pretty-printed JSON padded with insignificant
// whitespace and empty/null fields; blindly byte-truncating it both wastes
// tokens and hands the model a syntactically broken tail. Instead capOutput
// (agent.go) first tries to shrink JSON losslessly (whitespace), then by dropping
// empty/null fields, and only clips on a line boundary as a last resort.

import (
	"bytes"
	"encoding/json"
	"strings"
)

// looksLikeJSON reports whether s (after trimming) plausibly starts a JSON object
// or array — the only shapes worth trying to shrink. Bare scalars/log lines are
// left to the byte clipper.
func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	return t != "" && (t[0] == '{' || t[0] == '[')
}

// compactJSON removes insignificant whitespace from a JSON document. It is
// strictly lossless (json.Compact only drops formatting), so the model sees the
// exact same data, just smaller. ok=false when s isn't a valid JSON object/array.
func compactJSON(s string) (string, bool) {
	if !looksLikeJSON(s) {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(strings.TrimSpace(s))); err != nil {
		return "", false
	}
	return buf.String(), true
}

// pruneJSON parses s and re-marshals it with empty/null fields removed (see
// pruneEmpty). Numbers are decoded with UseNumber so large integers and exact
// decimals round-trip unchanged (never through float64), and HTML escaping is
// disabled so '<', '>', '&' in string values stay readable (and don't inflate
// 1→6 bytes, which could make the pruned form larger than the input). ok=false
// when s isn't a single valid JSON value. The result is compact, valid JSON.
func pruneJSON(s string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if dec.More() { // trailing data after the first value: not a single JSON document
		return "", false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(pruneEmpty(v)); err != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true // Encode appends a trailing newline
}

// pruneEmpty recursively drops null and empty (""/[]/{}) values from JSON
// objects, and prunes the contents of arrays. It deliberately keeps numeric zero
// and boolean false — those carry meaning, only absent/empty values are noise.
// Array length/order is preserved (only element contents are pruned).
func pruneEmpty(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			pv := pruneEmpty(val)
			if isEmptyJSON(pv) {
				continue
			}
			out[k] = pv
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, val := range t {
			out = append(out, pruneEmpty(val))
		}
		return out
	default:
		return v
	}
}

// isEmptyJSON reports whether a decoded JSON value carries no signal: null, the
// empty string, an empty object, or an empty array. Numbers (incl. 0) and
// booleans (incl. false) are never considered empty.
func isEmptyJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	default:
		return false
	}
}
