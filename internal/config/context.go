package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Per-model context-window sizes (in tokens). These are the maximum input
// sizes the underlying provider accepts, used to render the ctx N% indicator
// in the TUI status bar so the user knows when they're approaching the limit
// (and should /compact or /reset).
//
// Conservative defaults: Anthropic Claude all use 200K; Gemini ~1M. Some
// providers offer larger context windows behind beta flags — we use the
// stable, always-available value to avoid showing a falsely low percentage.

const defaultContextWindow = 200_000

var modelContextWindows = map[string]int{
	// Anthropic Claude — all 200K input tokens (stable).
	"anthropic.claude-opus-4-8":                 200_000,
	"anthropic.claude-opus-4-1-20250805-v1:0":   200_000,
	"anthropic.claude-opus-4-20250514-v1:0":     200_000,
	"anthropic.claude-sonnet-5":                 200_000,
	"anthropic.claude-sonnet-4-6":               200_000,
	"anthropic.claude-sonnet-4-5-20250929-v1:0": 200_000,
	"anthropic.claude-sonnet-4-20250514-v1:0":   200_000,
	"anthropic.claude-3-7-sonnet-20250219-v1:0": 200_000,
	"anthropic.claude-haiku-4-5-20251001-v1:0":  200_000,
	"anthropic.claude-3-5-haiku-20241022-v1:0":  200_000,

	// Google Gemini — ~1M tokens.
	"gemini-3.1-pro-preview": 1_048_576,
	"gemini-2.5-pro":         1_048_576,
}

// ContextWindow returns the input-token capacity for a model id, stripping the
// "aws:"/"gcp:" cloud-provider prefix used on the OpenAI route. Unknown models
// get a conservative 200K default rather than 0 so the percentage indicator
// always has something useful to show.
func ContextWindow(model string) int {
	if w, ok := modelContextWindows[stripPrefix(model)]; ok {
		return w
	}
	return defaultContextWindow
}

// ContextWindowFor returns the context window to display for a model: the user's
// explicit override (c.ContextWindow, when > 0) takes precedence over the
// per-model default, so e.g. `/context 1m` reflects a larger window.
func (c *Config) ContextWindowFor(model string) int {
	if c != nil && c.ContextWindow > 0 {
		return c.ContextWindow
	}
	return ContextWindow(model)
}

// ParseTokenCount parses a token count that may use a k/m suffix (case
// insensitive): "1m" or "1M" -> 1_000_000, "200k" -> 200_000, "131072" -> as-is.
// Underscores and commas are ignored. Returns an error on anything else.
func ParseTokenCount(s string) (int, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, fmt.Errorf("empty token count")
	}
	mult := 1
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult, s = 1_000, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1_000_000, s[:len(s)-1]
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("invalid token count %q (use e.g. 1m, 200k, or 1000000)", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("token count must not be negative")
	}
	result := n * mult
	if mult != 0 && result/mult != n {
		return 0, fmt.Errorf("token count %q is too large", s)
	}
	return result, nil
}
