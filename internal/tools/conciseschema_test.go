package tools

import "testing"

func TestFirstSentence(t *testing.T) {
	cases := map[string]string{
		"Read a file. Use offset for paging.": "Read a file.",
		"One line only":                       "One line only",
		"Summary here.\nMore details below.":  "Summary here.",
		"First\nSecond. Third.":               "First",
		"  leading space. rest":               "leading space.",
	}
	for in, want := range cases {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// #6: SetConciseSchemas trims descriptions in the specs sent to the model.
func TestConciseSchemas(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.RegisterDefaults(200000)

	full := map[string]string{}
	for _, s := range r.Schemas() {
		full[s.Name] = s.Description
	}

	r.SetConciseSchemas(true)
	for _, s := range r.Schemas() {
		if len(s.Description) > len(full[s.Name]) {
			t.Errorf("%s: concise description longer than full", s.Name)
		}
		if s.Description != firstSentence(full[s.Name]) {
			t.Errorf("%s: concise = %q, want first sentence %q", s.Name, s.Description, firstSentence(full[s.Name]))
		}
	}
}
