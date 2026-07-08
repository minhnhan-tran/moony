package workflow

import "testing"

func TestInterpolate(t *testing.T) {
	vars := map[string]any{
		"input":      "hello",
		"lastOutput": "world",
		"n1":         `{"price": 42, "name": "widget"}`, // JSON-string from an upstream node
		"obj":        map[string]any{"a": map[string]any{"b": "deep"}},
	}
	cases := map[string]string{
		"{{input}}":             "hello",
		"x={{lastOutput}}!":     "x=world!",
		"{{n1.price}}":          "42",
		"{{n1.name}}":           "widget",
		"{{obj.a.b}}":           "deep",
		"{{missing}}":           "",
		"{{n1.nope}}":           "",
		"no tokens":             "no tokens",
		"{{input}}-{{n1.name}}": "hello-widget",
	}
	for in, want := range cases {
		if got := Interpolate(in, vars); got != want {
			t.Errorf("Interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEvalCondition(t *testing.T) {
	vars := map[string]any{
		"input":      "yes",
		"count":      "3",
		"lastOutput": "the answer is 42",
		"n1":         `{"ok": true, "score": 0.9}`,
	}
	truthy := []string{
		"true",
		"{{count}} < 5",
		"{{count}} >= 3",
		"{{input}} == yes",
		`{{input}} == "yes"`,
		"{{lastOutput}} contains answer",
		"{{n1.score}} > 0.5",
		"{{input}}", // non-empty bare value
	}
	falsy := []string{
		"false",
		"{{count}} > 10",
		"{{input}} == no",
		"{{lastOutput}} contains banana",
		"{{n1.score}} < 0.5",
		"{{missing}}",
		"", // empty
	}
	for _, e := range truthy {
		if !EvalCondition(e, vars) {
			t.Errorf("EvalCondition(%q) = false, want true", e)
		}
	}
	for _, e := range falsy {
		if EvalCondition(e, vars) {
			t.Errorf("EvalCondition(%q) = true, want false", e)
		}
	}
}

func TestEvalConditionNumericVsString(t *testing.T) {
	vars := map[string]any{"a": "10", "b": "9"}
	// Numeric: 10 > 9 is true (string compare would say "10" < "9").
	if !EvalCondition("{{a}} > {{b}}", vars) {
		t.Error("expected numeric 10 > 9 to be true")
	}
}
