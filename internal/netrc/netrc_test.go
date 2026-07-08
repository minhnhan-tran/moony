package netrc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAndBearer(t *testing.T) {
	content := `
machine api.example.com login user1 password TOKEN1
machine git.example.com
  login user2
  password TOKEN2
`
	f := Parse(content)
	if got := len(f); got != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", got, f)
	}
	if b := f.Bearer("api.example.com"); b != "Bearer TOKEN1" {
		t.Fatalf("api bearer = %q", b)
	}
	if b := f.Bearer("git.example.com"); b != "Bearer TOKEN2" {
		t.Fatalf("git bearer = %q", b)
	}
	// Case-insensitive host match.
	if b := f.Bearer("API.Example.com"); b != "Bearer TOKEN1" {
		t.Fatalf("case-insensitive lookup failed: %q", b)
	}
	// Unknown host -> empty.
	if b := f.Bearer("other.com"); b != "" {
		t.Fatalf("expected empty bearer for unknown host, got %q", b)
	}
}

func TestParseIgnoresDefaultAndMacdef(t *testing.T) {
	content := `default login d password DEFTOK
machine real.com login u password REALTOK
macdef init
  cd /tmp
`
	f := Parse(content)
	// "default" must not produce a usable credential.
	if b := f.Bearer(""); b != "" {
		t.Fatalf("default stanza should be ignored, got %q", b)
	}
	if _, ok := f["d"]; ok {
		t.Fatal("default login should not be stored as a machine")
	}
	if b := f.Bearer("real.com"); b != "Bearer REALTOK" {
		t.Fatalf("real.com bearer = %q", b)
	}
}

func TestBearerNoPassword(t *testing.T) {
	f := Parse("machine nopass.com login only")
	if b := f.Bearer("nopass.com"); b != "" {
		t.Fatalf("expected empty bearer when password missing, got %q", b)
	}
}

func TestLoadFromNETRCEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom_netrc")
	if err := os.WriteFile(path, []byte("machine host.example.com login u password ENVTOK\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETRC", path)
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if b := f.Bearer("host.example.com"); b != "Bearer ENVTOK" {
		t.Fatalf("loaded bearer = %q", b)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Setenv("NETRC", filepath.Join(t.TempDir(), "does-not-exist"))
	f, err := Load()
	if err != nil {
		t.Fatalf("missing netrc should not error: %v", err)
	}
	if len(f) != 0 {
		t.Fatalf("expected empty File, got %v", f)
	}
}
