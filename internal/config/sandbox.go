package config

import "strings"

// Sandbox modes for the OS-level bash sandbox (macOS).
const (
	// SandboxOff disables the sandbox; bash runs unconfined.
	SandboxOff = "off"
	// SandboxWorkdir confines bash file writes to the working directory plus
	// temp and common build/dependency caches.
	SandboxWorkdir = "workdir"
)

// NormalizeSandboxMode maps friendly aliases to a valid sandbox mode, defaulting
// to off.
func NormalizeSandboxMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "1", "true", "yes", "workdir", "work", "confined":
		return SandboxWorkdir
	default:
		return SandboxOff
	}
}

// SandboxLabel returns a short label for the header/status, or "" when off.
func SandboxLabel(mode string) string {
	if NormalizeSandboxMode(mode) == SandboxWorkdir {
		return "sandbox"
	}
	return ""
}
