package config

import "strings"

// CustomHeader is one extra HTTP header to attach to backend requests.
type CustomHeader struct {
	Name  string
	Value string
}

// isHeaderNameChar reports whether r may appear in an RFC 7230 field-name.
func isHeaderNameChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

// ParseCustomHeaders turns a user-supplied header string into ordered headers.
//
// Three spellings are accepted, because people copy these between tools that
// each chose a different one:
//
//	"X-A: 1\nX-B: 2"   newline separated (Claude Code's documented form)
//	"X-A: 1; X-B: 2"   semicolon separated
//	"X-A: 1 X-B: 2"    run together on one line
//
// The third is why this is not a simple split: a value may itself contain
// spaces ("Authorization: Basic aGk="), so a new header only starts where a
// valid field-name is followed by ":" AND is preceded by whitespace. That keeps
// colons *inside* values intact — "User-Agent: x (note: hi)" stays one header,
// since "note" is preceded by "(" rather than a space.
//
// Blank segments are skipped and names/values are trimmed. A segment with no
// colon, or with an empty name, is dropped rather than guessed at.
func ParseCustomHeaders(s string) []CustomHeader {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var out []CustomHeader
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ';' }) {
		// Every piece of a segment must parse. Half-parsing is dangerous: splitting
		// "bad name: value" would otherwise drop the "bad" and silently send a
		// header the user never wrote ("name: value"), so a malformed segment is
		// discarded whole. Invalid field-names are rejected by net/http at write
		// time anyway, which would kill the entire request.
		pieces := splitRunTogether(seg)
		parsed := make([]CustomHeader, 0, len(pieces))
		valid := len(pieces) > 0
		for _, h := range pieces {
			name, value, found := strings.Cut(h, ":")
			name = strings.TrimSpace(name)
			if !found || !isHeaderName(name) {
				valid = false
				break
			}
			parsed = append(parsed, CustomHeader{Name: name, Value: strings.TrimSpace(value)})
		}
		if valid {
			out = append(out, parsed...)
		}
	}
	return out
}

// splitRunTogether breaks one segment at each point where a field-name follows
// whitespace and is itself followed by a colon — the "X-A: 1 X-B: 2" case.
func splitRunTogether(seg string) []string {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return nil
	}

	var cuts []int // byte offsets where a subsequent header starts
	for i := 1; i < len(seg); i++ {
		// Candidate start: first char after whitespace.
		if !isSpace(seg[i-1]) || isSpace(seg[i]) {
			continue
		}
		j := i
		for j < len(seg) && isHeaderNameChar(rune(seg[j])) {
			j++
		}
		if j == i || j >= len(seg) || seg[j] != ':' {
			continue
		}
		// "https://x" also reads as name + colon; a URI scheme is not a header.
		if strings.HasPrefix(seg[j+1:], "//") {
			continue
		}
		cuts = append(cuts, i)
	}
	if len(cuts) == 0 {
		return []string{seg}
	}

	// The first cut must not swallow the segment's own name: keep [0,cut0).
	out := make([]string, 0, len(cuts)+1)
	prev := 0
	for _, c := range cuts {
		if part := strings.TrimSpace(seg[prev:c]); part != "" {
			out = append(out, part)
		}
		prev = c
	}
	if part := strings.TrimSpace(seg[prev:]); part != "" {
		out = append(out, part)
	}
	return out
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' }

// isHeaderName reports whether s is a non-empty RFC 7230 field-name.
func isHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isHeaderNameChar(r) {
			return false
		}
	}
	return true
}
