package config

import (
	"os"
	"strings"
	"testing"
)

// TestMain makes the config test binary hermetic: (1) it points XDG_CONFIG_HOME
// (which GlobalConfigDir honors first) at a throwaway temp dir so no test reads
// the developer's real ~/.config/moony/config.json, and (2) it clears
// any ambient MOONY_* env vars inherited from the dev shell. Without this, saved
// prefs (fast_model / cheap_reads) or an exported MOONY_FAST_MODEL would leak into
// Load()/FastModel() and break model-resolution assertions. Tests that need a
// specific env set it themselves via t.Setenv.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && strings.HasPrefix(kv[:i], "MOONY_") {
			os.Unsetenv(kv[:i])
		}
	}
	dir, err := os.MkdirTemp("", "sqe-config-test")
	if err == nil {
		os.Setenv("XDG_CONFIG_HOME", dir)
	}
	code := m.Run()
	if dir != "" {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
