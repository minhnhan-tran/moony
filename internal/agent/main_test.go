package agent

import (
	"os"
	"strings"
	"testing"
)

// TestMain makes the agent test binary hermetic: it points XDG_CONFIG_HOME at a
// throwaway temp dir and clears any ambient MN_* env vars inherited from the
// dev shell. Without this, an exported MN_FAST_MODEL (or saved prefs read via
// config.Load) leaks into config.FastModel and breaks model-routing assertions
// (e.g. the sub-agent/adaptive tests). Tests that need a specific env set it
// themselves via t.Setenv.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && strings.HasPrefix(kv[:i], "MN_") {
			os.Unsetenv(kv[:i])
		}
	}
	dir, err := os.MkdirTemp("", "sqe-agent-test")
	if err == nil {
		os.Setenv("XDG_CONFIG_HOME", dir)
	}
	code := m.Run()
	if dir != "" {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
