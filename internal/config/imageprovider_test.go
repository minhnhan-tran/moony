package config

import (
	"testing"

	"github.com/pysty1995/moony/internal/provider"
)

// generate_image needs an OpenAI-compatible chat route that returns image parts, and
// few backends have one. Without an override, a session on a local or text-only model
// simply cannot generate images.
func TestImageProviderFor(t *testing.T) {
	for _, tc := range []struct {
		name           string
		override, sess string
		want           string
	}{
		{name: "no override inherits the session provider", override: "", sess: provider.Ollama, want: ""},
		{name: "override routes elsewhere", override: provider.OpenAI, sess: provider.Ollama, want: provider.OpenAI},
		// Same provider is not an override; returning it would send callers down a
		// redundant path and make the config look active when it isn't.
		{name: "override equal to the session is a no-op", override: provider.OpenAI, sess: provider.OpenAI, want: ""},
		{name: "case-insensitive equality still a no-op", override: "OpenAI", sess: provider.OpenAI, want: ""},
		{name: "whitespace-only is not an override", override: "   ", sess: provider.Ollama, want: ""},
		// A typo must not silently become the default provider and route images to a
		// backend the user never named.
		{name: "unknown provider id is ignored", override: "opneai", sess: provider.Ollama, want: ""},
	} {
		c := &Config{ImageProvider: tc.override, LLMProvider: tc.sess}
		if got := c.ImageProviderFor(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	var nilCfg *Config
	if got := nilCfg.ImageProviderFor(); got != "" {
		t.Errorf("nil config must yield no override, got %q", got)
	}
}

func TestImageProviderEnv(t *testing.T) {
	t.Setenv("MN_IMAGE_PROVIDER", "openai")
	if got := Load().ImageProvider; got != provider.OpenAI {
		t.Errorf("ImageProvider = %q, want %q", got, provider.OpenAI)
	}
}

// An unset env var must leave the field empty (inherit), not guess a provider.
func TestImageProviderEnvUnsetInherits(t *testing.T) {
	t.Setenv("MN_IMAGE_PROVIDER", "")
	if got := Load().ImageProvider; got != "" {
		t.Errorf("ImageProvider = %q, want empty when unset", got)
	}
}

// The choice must survive a restart, and an explicit "inherit" must be remembered as a
// real decision rather than reverting to whatever the env says.
func TestImageProviderPrefsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("MN_IMAGE_PROVIDER", "") // prove prefs win over the env

	t.Run("an explicit provider persists", func(t *testing.T) {
		pick := provider.OpenAI
		save := &Config{ImageProvider: pick, ImageProviderPersona: &pick}
		if err := save.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got := Load().ImageProvider; got != provider.OpenAI {
			t.Errorf("ImageProvider = %q, want %q after reload", got, provider.OpenAI)
		}
	})

	t.Run("an explicit clear persists as inherit", func(t *testing.T) {
		t.Setenv("MN_IMAGE_PROVIDER", "groq") // the env would otherwise win
		empty := ""
		save := &Config{ImageProviderPersona: &empty}
		if err := save.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got := Load().ImageProvider; got != "" {
			t.Errorf("ImageProvider = %q, want empty (the user chose inherit)", got)
		}
	})
}
