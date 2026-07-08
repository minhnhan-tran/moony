package tools

import (
	"os"
	"strings"
	"testing"
)

func TestScrubbedEnvRemovesSecrets(t *testing.T) {
	t.Setenv("MOONY_OLLAMA_TOKEN", "topsecret")
	t.Setenv("MOONY_ENV_FILE", "/tmp/secrets.env")
	t.Setenv("PATH_LIKE_KEEPER", "keepme") // a non-secret var must survive

	env := scrubbedEnv()
	joined := strings.Join(env, "\n")

	for _, leaked := range []string{"topsecret"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("scrubbedEnv leaked a secret value %q", leaked)
		}
	}
	for key := range sensitiveEnvKeys {
		for _, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				t.Errorf("scrubbedEnv kept sensitive key %q", key)
			}
		}
	}
	if !strings.Contains(joined, "PATH_LIKE_KEEPER=keepme") {
		t.Error("scrubbedEnv dropped a non-secret var")
	}
	// PATH is needed for commands to run — make sure ordinary env survives.
	if os.Getenv("PATH") != "" && !strings.Contains(joined, "PATH=") {
		t.Error("scrubbedEnv dropped PATH")
	}
}

// scrubbedEnvFor pins $PWD to the given dir (so a subprocess's $PWD matches its
// cmd.Dir), replacing any inherited PWD rather than duplicating it, and still
// strips secrets.
func TestScrubbedEnvForPinsPWD(t *testing.T) {
	t.Setenv("PWD", "/some/stale/launch/dir")
	t.Setenv("MOONY_OLLAMA_TOKEN", "topsecret")

	env := scrubbedEnvFor("/work/IOSApp")

	var pwds []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PWD=") {
			pwds = append(pwds, kv)
		}
		if strings.Contains(kv, "topsecret") {
			t.Error("scrubbedEnvFor leaked a secret")
		}
	}
	if len(pwds) != 1 {
		t.Fatalf("want exactly one PWD entry, got %v", pwds)
	}
	if pwds[0] != "PWD=/work/IOSApp" {
		t.Errorf("PWD = %q, want PWD=/work/IOSApp (pinned to the working dir)", pwds[0])
	}
}
