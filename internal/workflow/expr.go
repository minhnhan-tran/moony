package workflow

// Safe, dependency-free expression evaluation for transform / if-else / while
// nodes. Go has no eval, which is a feature: instead of running arbitrary code
// (the mistake the JS clones made with new Function), we support two small,
// bounded languages:
//
//   - Interpolation: "{{input}}", "{{lastOutput}}", "{{node_id}}", "{{a.b.c}}"
//     resolve against the variable bus (values are JSON-walked for dotted paths).
//   - Conditions: a single comparison "LHS OP RHS" (==, !=, <, <=, >, >=,
//     contains) or the bare words true/false, where each side is an interpolation
//     token or a literal (quoted string, number, or true/false). Numbers compare
//     numerically; everything else compares as trimmed strings.
//
// This covers the common workflow needs (route on a value, loop while a counter
// is under a bound, reshape text) without an interpreter or a third-party lib.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var tokenRE = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// Interpolate replaces every {{ path }} in s with its value from vars. An
// unresolved path becomes the empty string. Non-string values are rendered as
// their JSON (objects/arrays) or plain form (numbers/bools).
func Interpolate(s string, vars map[string]any) string {
	return tokenRE.ReplaceAllStringFunc(s, func(m string) string {
		path := strings.TrimSpace(tokenRE.FindStringSubmatch(m)[1])
		return renderValue(resolvePath(path, vars))
	})
}

// resolvePath walks a dotted path ("a.b.c") through vars, descending into
// map[string]any and JSON-string values. Returns nil if any segment is missing.
func resolvePath(path string, vars map[string]any) any {
	parts := strings.Split(path, ".")
	var cur any = vars
	for _, p := range parts {
		p = strings.TrimSpace(p)
		m, ok := cur.(map[string]any)
		if !ok {
			// Try to parse a JSON-string value so "node.field" works when the
			// upstream node returned a JSON object as text.
			if s, isStr := cur.(string); isStr {
				var parsed map[string]any
				if json.Unmarshal([]byte(s), &parsed) == nil {
					m, ok = parsed, true
				}
			}
		}
		if !ok {
			return nil
		}
		cur = m[p]
		if cur == nil {
			return nil
		}
	}
	return cur
}

func renderValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// EvalCondition evaluates a bounded boolean expression against vars. Supported:
// "true"/"false"; "LHS OP RHS" with OP in == != < <= > >= contains. Each operand
// is interpolated, then compared numerically when both parse as numbers, else as
// trimmed strings. An empty/unparseable expression is false.
func EvalCondition(expr string, vars map[string]any) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	switch strings.ToLower(expr) {
	case "true":
		return true
	case "false":
		return false
	}
	// Longest operators first so "<=" isn't split as "<". Each entry is the
	// operator and the exact token searched for in the expression (the word
	// operator "contains" is space-padded so it isn't matched inside a word).
	ops := []struct{ op, token string }{
		{"==", "=="}, {"!=", "!="}, {"<=", "<="}, {">=", ">="},
		{"contains", " contains "}, {"<", "<"}, {">", ">"},
	}
	for _, o := range ops {
		idx := strings.Index(expr, o.token)
		if idx < 0 {
			continue
		}
		lhs := operand(expr[:idx], vars)
		rhs := operand(expr[idx+len(o.token):], vars)
		return compare(lhs, rhs, o.op)
	}
	// No operator: treat a non-empty interpolated value as truthy.
	v := strings.TrimSpace(Interpolate(expr, vars))
	return v != "" && strings.ToLower(v) != "false" && v != "0"
}

// findOp finds op as a whitespace- or boundary-delimited operator (so "contains"
// isn't matched inside a word, and "<" inside "<=" is handled by call order).
// operand interpolates and unquotes one side of a comparison.
func operand(s string, vars map[string]any) string {
	s = strings.TrimSpace(Interpolate(strings.TrimSpace(s), vars))
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = s[1 : len(s)-1]
	}
	return s
}

func compare(lhs, rhs, op string) bool {
	if op == "contains" {
		return strings.Contains(lhs, rhs)
	}
	// Numeric comparison when both sides are numbers.
	ln, lerr := strconv.ParseFloat(lhs, 64)
	rn, rerr := strconv.ParseFloat(rhs, 64)
	if lerr == nil && rerr == nil {
		switch op {
		case "==":
			return ln == rn
		case "!=":
			return ln != rn
		case "<":
			return ln < rn
		case "<=":
			return ln <= rn
		case ">":
			return ln > rn
		case ">=":
			return ln >= rn
		}
	}
	switch op {
	case "==":
		return lhs == rhs
	case "!=":
		return lhs != rhs
	case "<":
		return lhs < rhs
	case "<=":
		return lhs <= rhs
	case ">":
		return lhs > rhs
	case ">=":
		return lhs >= rhs
	}
	return false
}
