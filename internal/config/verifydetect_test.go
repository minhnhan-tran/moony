package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectVerifyCommand(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"empty dir", nil, ""},
		{"go module", map[string]string{"go.mod": "module x\n"}, "go build ./..."},
		{"makefile test target beats go", map[string]string{
			"go.mod":   "module x\n",
			"Makefile": "build:\n\techo hi\ntest:\n\tgo test ./...\n",
		}, "make test"},
		{"makefile check target", map[string]string{
			"Makefile": "check:\n\techo hi\n",
		}, "make check"},
		{"cargo", map[string]string{"Cargo.toml": "[package]\n"}, "cargo build"},
		{"npm with real test script", map[string]string{
			"package.json": `{"scripts":{"test":"jest"}}`,
		}, "npm test --silent"},
		{"npm placeholder test -> tsc if ts", map[string]string{
			"package.json":  `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`,
			"tsconfig.json": "{}",
		}, "npx --no-install tsc --noEmit"},
		{"pyproject pytest", map[string]string{
			"pyproject.toml": "[tool.pytest.ini_options]\n",
		}, "pytest -q"},
		{"pyproject no pytest", map[string]string{
			"pyproject.toml": "[project]\nname='x'\n",
		}, "python -m compileall -q ."},
		{"gradle", map[string]string{"build.gradle": "plugins {}\n"}, "./gradlew assemble -q"},
		{"maven", map[string]string{"pom.xml": "<project/>"}, "mvn -q -e test-compile"},
		{"pytest.ini only", map[string]string{"pytest.ini": "[pytest]\n"}, "pytest -q"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for n, c := range tc.files {
				writeFile(t, dir, n, c)
			}
			if got := detectVerifyCommand(dir); got != tc.want {
				t.Errorf("detectVerifyCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVerifyAutoOnlyFillsUnset: an explicit VerifyCommand is never overwritten by
// auto-detection, and detection only kicks in when opted in.
func TestVerifyAutoDoesNotOverrideExplicit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module x\n")

	// Simulate the tail of Load()'s auto-detect step.
	c := &Config{WorkingDir: dir, VerifyAuto: true, VerifyCommand: "make ci"}
	if c.VerifyAuto && c.VerifyCommand == "" {
		c.VerifyCommand = detectVerifyCommand(c.WorkingDir)
	}
	if c.VerifyCommand != "make ci" {
		t.Errorf("explicit command overwritten: got %q", c.VerifyCommand)
	}

	c2 := &Config{WorkingDir: dir, VerifyAuto: true}
	if c2.VerifyAuto && c2.VerifyCommand == "" {
		c2.VerifyCommand = detectVerifyCommand(c2.WorkingDir)
	}
	if c2.VerifyCommand != "go build ./..." {
		t.Errorf("auto-detect not applied: got %q", c2.VerifyCommand)
	}
}
