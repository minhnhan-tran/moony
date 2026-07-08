package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv loads simple KEY=VALUE pairs from a local .env file into the process
// environment, but ONLY for variables that aren't already set — so real
// environment variables (and prod secret injectors like Whisper) always win.
//
// It's a LOCAL-DEV convenience for things like the auth secrets (MOONY_AUTH_SECRET,
// MOONY_OIDC_*). Keep .env out of git (it's .gitignored). Note: a .env in the
// project root is readable by the agent's own shell/file tools, so for real
// secrets prefer ~/.config/moony/.env (outside the project) or Whisper
// in prod. The bash/job tools also strip these secrets from agent subprocesses.
//
// The first existing file wins, in priority order:
//  1. $MOONY_ENV_FILE          (explicit path)
//  2. ~/.config/moony/.env
//  3. ./.env                 (project root)
//
// Lines may be blank, "# comments", or "[export ]KEY=VALUE" with optional
// surrounding quotes. Best-effort: a missing or malformed file is ignored.
func LoadDotEnv() {
	for _, path := range dotEnvCandidates() {
		if path != "" && loadEnvFile(path) {
			return // first existing file wins
		}
	}
}

func dotEnvCandidates() []string {
	paths := []string{strings.TrimSpace(os.Getenv("MOONY_ENV_FILE"))}
	if p, err := GlobalConfigPath(".env"); err == nil {
		paths = append(paths, p)
	}
	return append(paths, ".env")
}

// loadEnvFile reads path and sets any unset keys. Returns true if the file
// existed and was read (so the caller can stop at the first hit).
func loadEnvFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := parseEnvLine(sc.Text())
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(k); !set { // real env / Whisper wins
			_ = os.Setenv(k, v)
		}
	}
	return true
}

// parseEnvLine parses one .env line into (key, value, ok). ok is false for blank
// lines and comments.
func parseEnvLine(line string) (key, value string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	s = strings.TrimPrefix(s, "export ")
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:eq])
	value = strings.TrimSpace(s[eq+1:])
	// Strip one layer of matching surrounding quotes.
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	if key == "" {
		return "", "", false
	}
	return key, value, true
}
