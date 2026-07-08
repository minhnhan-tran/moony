package config

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
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

// PersistentDotEnvPath is the file UpsertDotEnv writes to and LoadDotEnv prefers:
// $MOONY_ENV_FILE when set, else ~/.config/moony/.env. (LoadDotEnv checks the
// project-root ./.env only after the config-dir file, so persisting to the
// config dir always wins over a stray project .env on the next launch.)
func PersistentDotEnvPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("MOONY_ENV_FILE")); p != "" {
		return p, nil
	}
	return GlobalConfigPath(".env")
}

// UpsertDotEnv writes KEY=VALUE pairs into the persistent dotenv file so they
// survive a restart (LoadDotEnv reads them on the next launch, unless a real
// environment variable of the same name already wins). A key mapped to an empty
// value is REMOVED. Existing comments, blank lines, and unrelated keys are
// preserved verbatim; managed keys are rewritten in place and new keys appended
// (sorted, for a stable file). Writes atomically with 0600 perms. An empty map
// is a no-op and never touches disk.
func UpsertDotEnv(upserts map[string]string) error {
	if len(upserts) == 0 {
		return nil
	}
	path, err := PersistentDotEnvPath()
	if err != nil {
		return err
	}
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}

	handled := make(map[string]bool, len(upserts))
	out := make([]string, 0, len(lines)+len(upserts))
	for _, line := range lines {
		k, _, ok := parseEnvLine(line)
		if !ok {
			out = append(out, line) // keep blanks, comments, malformed lines
			continue
		}
		v, managed := upserts[k]
		if !managed {
			out = append(out, line)
			continue
		}
		handled[k] = true
		if strings.TrimSpace(v) == "" {
			continue // delete: drop the line
		}
		out = append(out, formatEnvLine(k, v))
	}

	newKeys := make([]string, 0, len(upserts))
	for k, v := range upserts {
		if !handled[k] && strings.TrimSpace(v) != "" {
			newKeys = append(newKeys, k)
		}
	}
	sort.Strings(newKeys)
	for _, k := range newKeys {
		out = append(out, formatEnvLine(k, upserts[k]))
	}

	content := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// formatEnvLine renders a KEY=VALUE line, wrapping the value in double quotes
// only when it contains whitespace or a '#' (which parseEnvLine would otherwise
// mishandle). API keys and URLs need no quoting, so they round-trip bare.
func formatEnvLine(k, v string) string {
	if strings.ContainsAny(v, " \t#") {
		return k + `="` + v + `"`
	}
	return k + "=" + v
}
