package tui

import "testing"

func TestRelayWebURL(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"relay.example:9000":         "https://relay.example",
		"code.sqeqct.apple.com:443":  "https://code.sqeqct.apple.com",
		"code.sqeqct.apple.com":      "https://code.sqeqct.apple.com",
		"code.sqeqct.apple.com:8771": "https://code.sqeqct.apple.com",
		"  host:8080  ":              "https://host",
	}
	for in, want := range cases {
		if got := relayWebURL(in); got != want {
			t.Errorf("relayWebURL(%q) = %q, want %q", in, got, want)
		}
	}
}
