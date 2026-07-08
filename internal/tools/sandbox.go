package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// sandboxSetting is the resolved sandbox configuration for a command.
type sandboxSetting struct {
	enabled bool
	network bool
}

// sandboxEnabledEnv reads SQE_SANDBOX. It's the fallback used when the registry
// isn't explicitly configured (e.g. the standalone web server or tests).
// Values on/1/true/yes/workdir enable it; anything else disables it.
func sandboxEnabledEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_SANDBOX"))) {
	case "1", "on", "true", "yes", "workdir":
		return true
	}
	return false
}

// sandboxNetworkEnv reports whether sandboxed commands may use the network.
// Network is allowed by default; set SQE_SANDBOX_NET=0 to deny it.
func sandboxNetworkEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SQE_SANDBOX_NET"))) {
	case "0", "off", "false", "no":
		return false
	}
	return true
}

// sandboxCommand wraps (name, args) with an OS-level sandbox — sandbox-exec on
// macOS, Bubblewrap (bwrap) on Linux — so the command can only write under the
// workspace roots plus temp and common build caches, when the sandbox is
// enabled and the platform's confinement tool is available. Otherwise it
// returns the command unchanged so behavior is identical to running it
// directly. roots is the set of writable workspace roots (primary first).
func sandboxCommand(set sandboxSetting, roots []string, name string, args ...string) (string, []string) {
	if !set.enabled {
		return name, args
	}
	switch runtime.GOOS {
	case "darwin":
		return sandboxCommandDarwin(set, roots, name, args...)
	case "linux":
		return sandboxCommandLinux(set, roots, name, args...)
	default:
		return name, args
	}
}

func sandboxCommandDarwin(set sandboxSetting, roots []string, name string, args ...string) (string, []string) {
	sbexec, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return name, args // unavailable: run unconfined rather than fail
	}
	profile := sandboxProfile(roots, set.network)
	wrapped := append([]string{"-p", profile, name}, args...)
	return sbexec, wrapped
}

// sandboxCommandLinux wraps the command with Bubblewrap (bwrap), re-creating
// the same "read anything, write only under an allowlist" confinement as the
// macOS sandbox-exec profile. Falls back to running unconfined when bwrap
// isn't installed.
func sandboxCommandLinux(set sandboxSetting, roots []string, name string, args ...string) (string, []string) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return name, args // unavailable: run unconfined rather than fail
	}
	// "--" ends bwrap's own option parsing so the wrapped command (and any of its
	// args that start with "-") can never be misread as a bwrap flag.
	wrapped := append(bwrapArgs(roots, set.network), "--", name)
	wrapped = append(wrapped, args...)
	return bwrap, wrapped
}

// writableRoots returns the canonicalized set of paths that should remain
// writable inside the sandbox: the workspace roots, temp directories, and
// common dependency/build caches that legitimate tooling writes to outside the
// project (go build cache, module cache, npm, cargo, pip, gradle, …). Shared by
// both the macOS SBPL profile and the Linux bwrap arg list.
func writableRoots(roots []string) []string {
	var writable []string
	for _, root := range roots {
		writable = append(writable, canonical(root))
	}
	writable = append(writable, canonical(os.TempDir()), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, rel := range []string{
			"Library/Caches", ".cache", ".npm", ".cargo", filepath.Join("go", "pkg", "mod"),
			".gradle", ".m2", ".pub-cache", ".rustup", ".bun", ".deno", ".gem",
		} {
			writable = append(writable, filepath.Join(home, rel))
		}
	}
	return writable
}

// sandboxProfile builds an SBPL profile that allows everything by default,
// denies all file writes, then re-allows writes under each workspace root, temp
// directories, and common dependency/build caches. SBPL is last-match-wins, so
// the order matters.
func sandboxProfile(roots []string, allowNetwork bool) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	if !allowNetwork {
		b.WriteString("(deny network*)\n")
	}
	b.WriteString("(deny file-write*)\n(allow file-write*\n")
	seen := map[string]bool{}
	for _, p := range writableRoots(roots) {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		fmt.Fprintf(&b, "  (subpath %q)\n", p)
	}
	// Standard write sinks and the controlling terminal.
	b.WriteString("  (literal \"/dev/null\")\n  (literal \"/dev/zero\")\n  (regex #\"^/dev/\")\n)\n")
	return b.String()
}

// bwrapArgs builds the Bubblewrap argument list that bind-mounts the whole
// root filesystem read-only, then re-binds each writable root (and temp/build
// caches) read-write over the top — the same "deny by default, allow-list
// writes" shape as the macOS profile. Network is unshared unless allowNetwork.
// The command to run is appended by the caller.
func bwrapArgs(roots []string, allowNetwork bool) []string {
	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/dev/shm", // POSIX shared memory (Chromium, some Python/torch) — the minimal --dev has none
		"--die-with-parent",
	}
	if !allowNetwork {
		args = append(args, "--unshare-net")
	}
	seen := map[string]bool{}
	for _, p := range writableRoots(roots) {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(p); err != nil {
			continue // bwrap --bind requires the source to exist
		}
		args = append(args, "--bind", p, p)
	}
	return args
}

// canonical resolves symlinks so the profile matches the paths the kernel
// actually checks (e.g. /var → /private/var on macOS). Falls back to a cleaned
// path when resolution fails.
func canonical(p string) string {
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}
