package config

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

var (
	machineNameOnce sync.Once
	machineNameVal  string
)

// MachineName returns a friendly local name for this machine, suitable for
// telling a remote web viewer which computer a mirrored terminal session runs
// on. On macOS it prefers the System Settings "Computer Name" (scutil --get
// ComputerName, e.g. "John's MacBook Pro"); otherwise — and as a fallback — it
// uses the hostname with any trailing ".local" trimmed. The result is computed
// once and cached, since it can't change during a run.
func MachineName() string {
	machineNameOnce.Do(func() { machineNameVal = computeMachineName() })
	return machineNameVal
}

func computeMachineName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if name := strings.TrimSpace(string(out)); name != "" {
				return name
			}
		}
	}
	host, _ := os.Hostname()
	return normalizeHostname(host)
}

// normalizeHostname trims whitespace and the trailing ".local" mDNS suffix from
// a hostname for a cleaner display label.
func normalizeHostname(h string) string {
	return strings.TrimSuffix(strings.TrimSpace(h), ".local")
}
