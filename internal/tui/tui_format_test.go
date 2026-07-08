package tui

import "testing"

func TestRelayWebURL(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"relay.example:9000":      "https://relay.example",
		"code.relay.example:443":  "https://code.relay.example",
		"code.relay.example":      "https://code.relay.example",
		"code.relay.example:8771": "https://code.relay.example",
		"  host:8080  ":           "https://host",
	}
	for in, want := range cases {
		if got := relayWebURL(in); got != want {
			t.Errorf("relayWebURL(%q) = %q, want %q", in, got, want)
		}
	}
}
