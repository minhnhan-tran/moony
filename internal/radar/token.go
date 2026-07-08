// Package radar mints the short-lived AppleConnect access token that the Radar
// MCP server (https://radar-mcp.corp.apple.com/mcp) requires. This is a
// different token from the model gateway's ID token — a different client id, resource
// URL, and output key ("oauth-access-token" vs "oauth-id-token").
//
// The token authorizes the bundled `radar` MCP catalog entry, whose
// Authorization header references ${RADAR_MCP_TOKEN}. Typical use:
//
//	export RADAR_MCP_TOKEN=$(moony radar-token)
//
// The token is short-lived (~1h); re-run when calls start returning 401.
package radar

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const (
	clientID       = "41rih2rtlg4zyax6eztzug6ftnhkun"
	installationID = "900731"
	resourceURL    = "https://radar-webservices.apple.com"
	acEnv          = "PROD"

	// TokenEnv is the environment variable the bundled radar MCP entry reads its
	// bearer token from.
	TokenEnv = "RADAR_MCP_TOKEN"
	// MCPURL is the Radar MCP server endpoint.
	MCPURL = "https://radar-mcp.corp.apple.com/mcp"
)

// Token mints an OIDC access token scoped to radar-webservices.apple.com via the
// appleconnect CLI (macOS). Interactivity is allowed so the CLI can prompt
// (Touch ID / browser) when a fresh login is required. Returns an error if
// appleconnect is unavailable or authentication fails.
func Token(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "appleconnect", "getToken",
		"-E", acEnv,
		"-I", installationID,
		"-t", "oauth",
		"-G", "pkce",
		"-C", clientID,
		"-u", resourceURL,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("appleconnect getToken failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	tok := parseAccessToken(string(out))
	if tok == "" {
		return "", fmt.Errorf("could not parse an oauth-access-token line from appleconnect output:\n%s", truncate(out, 500))
	}
	return tok, nil
}

// parseAccessToken extracts the value of the "oauth-access-token:" line from
// appleconnect output. Returns "" when absent.
func parseAccessToken(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "oauth-access-token:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
