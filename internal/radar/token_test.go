package radar

import "testing"

func TestParseAccessToken(t *testing.T) {
	// Typical appleconnect output carries several lines; only the
	// oauth-access-token line is the value we want (not oauth-id-token).
	out := "Some banner\noauth-id-token: ID-TOKEN-VALUE\noauth-access-token: ACCESS-TOKEN-VALUE\n"
	if got := parseAccessToken(out); got != "ACCESS-TOKEN-VALUE" {
		t.Errorf("parseAccessToken = %q, want ACCESS-TOKEN-VALUE", got)
	}

	// Tolerates extra surrounding whitespace.
	if got := parseAccessToken("  oauth-access-token:   TOK  \n"); got != "TOK" {
		t.Errorf("parseAccessToken (whitespace) = %q, want TOK", got)
	}

	// Absent → empty (caller turns this into a clear error).
	if got := parseAccessToken("oauth-id-token: only-the-id\n"); got != "" {
		t.Errorf("parseAccessToken should miss when no access-token line is present, got %q", got)
	}
}
