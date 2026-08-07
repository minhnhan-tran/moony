package config

import "testing"

func TestParseCustomHeaders(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []CustomHeader
	}{
		{"empty", "", nil},
		{"blank", "   \n  ", nil},
		{"single", "X-Token: abc", []CustomHeader{{"X-Token", "abc"}}},
		{"no space after colon", "X-Token:abc", []CustomHeader{{"X-Token", "abc"}}},
		{
			"newline separated",
			"X-A: 1\nX-B: 2",
			[]CustomHeader{{"X-A", "1"}, {"X-B", "2"}},
		},
		{
			"crlf separated",
			"X-A: 1\r\nX-B: 2",
			[]CustomHeader{{"X-A", "1"}, {"X-B", "2"}},
		},
		{
			"semicolon separated",
			"X-A: 1; X-B: 2",
			[]CustomHeader{{"X-A", "1"}, {"X-B", "2"}},
		},
		{
			// The shape people actually paste from a shell export.
			"run together with a spaced value",
			"Authorization: Basic Zmxvb2RnYXRlOnMzY3JldA== ngrok-skip-browser-warning: 1",
			[]CustomHeader{
				{"Authorization", "Basic Zmxvb2RnYXRlOnMzY3JldA=="},
				{"ngrok-skip-browser-warning", "1"},
			},
		},
		{
			// A colon inside a value must not start a new header when it is not
			// preceded by whitespace.
			"colon inside value",
			"User-Agent: moony/1.0 (note:hi)",
			[]CustomHeader{{"User-Agent", "moony/1.0 (note:hi)"}},
		},
		{
			"url value keeps its colon",
			"X-Origin: https://example.com:8443/x",
			[]CustomHeader{{"X-Origin", "https://example.com:8443/x"}},
		},
		{"empty value is kept", "X-Empty:", []CustomHeader{{"X-Empty", ""}}},
		{"garbage without colon is dropped", "nonsense", nil},
		{"empty name is dropped", ": value", nil},
		{"invalid name is dropped", "bad name: value", nil},
		{"crlf injection attempt is dropped", "X-A: 1\r\nEvil Header: 2", []CustomHeader{{"X-A", "1"}}},
		{
			"mixed separators and padding",
			"  X-A: 1 ;\n  X-B: two words \n",
			[]CustomHeader{{"X-A", "1"}, {"X-B", "two words"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCustomHeaders(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseCustomHeaders(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("header %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
