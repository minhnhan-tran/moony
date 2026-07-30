package web

// Tests for the read-only file viewer. The security cases are the point of this
// file: the viewer is the only endpoint that serves file *contents*, and the
// browser may be reaching a laptop across the relay, so any path escaping the
// session's workspace would expose ~/.ssh/id_rsa, ~/.aws/credentials, .env and
// the rest of the machine to anyone who can open a session.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/llm"
)

// viewerServer builds a server whose session workspace is a temp repo, plus a
// secret file OUTSIDE that workspace that no request may ever reach.
func viewerServer(t *testing.T) (h http.Handler, sessionID, wd, secret string) {
	t.Helper()
	fake := &fakeStreamer{turns: []llm.Response{{StopReason: "end_turn", Blocks: []llm.ContentBlock{llm.TextBlock("hi")}}}}
	srv, key := newTestServer(t, fake, config.ApprovalAuto, false)
	h = srv.Handler()

	rec := postJSON(t, h, "POST", "/api/projects/"+key+"/sessions", "")
	var created sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create session: %v (%s)", err, rec.Body)
	}
	sess := srv.openSession(created.ID)
	if sess == nil || sess.agent == nil {
		t.Fatal("session did not open")
	}
	wd = sess.agent.Config().WorkingDir

	// A realistic repo layout.
	mustWrite(t, filepath.Join(wd, "main.go"), "package main\n\nfunc main() {}\n")
	if err := os.MkdirAll(filepath.Join(wd, "internal", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(wd, "internal", "web", "server.go"), "package web\n")
	if err := os.MkdirAll(filepath.Join(wd, "node_modules", "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(wd, "node_modules", "junk", "x.js"), "// noise\n")

	// The thing an attacker wants, one level above the workspace.
	secret = filepath.Join(filepath.Dir(wd), "id_rsa_secret")
	mustWrite(t, secret, "PRIVATE KEY DO NOT LEAK\n")
	return h, created.ID, wd, secret
}

func getFile(t *testing.T, h http.Handler, id, path string) (int, fileView) {
	t.Helper()
	rec := postJSON(t, h, "GET", "/api/sessions/"+id+"/file?path="+path, "")
	var out fileView
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body)
		}
	}
	return rec.Code, out
}

func TestViewFileServesWorkspaceFile(t *testing.T) {
	h, id, _, _ := viewerServer(t)

	code, got := getFile(t, h, id, "main.go")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if !strings.Contains(got.Content, "func main()") {
		t.Errorf("content not served: %q", got.Content)
	}
	if got.Path != "main.go" {
		t.Errorf("path = %q, want repo-relative main.go", got.Path)
	}
	if got.Lines != 3 {
		t.Errorf("lines = %d, want 3", got.Lines)
	}

	// Nested path.
	if code, got := getFile(t, h, id, "internal/web/server.go"); code != http.StatusOK || !strings.Contains(got.Content, "package web") {
		t.Errorf("nested file: code=%d content=%q", code, got.Content)
	}
}

// The core security test: every shape of "escape the workspace" must 404, and
// must never return the secret's contents.
func TestViewFileRefusesEscapingTheWorkspace(t *testing.T) {
	h, id, _, secret := viewerServer(t)
	secretName := filepath.Base(secret)

	for _, probe := range []string{
		"../" + secretName,
		"../../" + secretName,
		"internal/../../" + secretName,
		"internal/web/../../../" + secretName,
		"..%2f" + secretName,
		"./../" + secretName,
		secret,          // absolute
		"/etc/passwd",   // absolute, outside
		"~/.ssh/id_rsa", // home-relative
		"....//" + secretName,
	} {
		code, got := getFile(t, h, id, probe)
		if code == http.StatusOK {
			t.Errorf("probe %q was SERVED (code 200) content=%q — workspace escape", probe, got.Content)
			continue
		}
		if strings.Contains(got.Content, "PRIVATE KEY") {
			t.Errorf("probe %q leaked the secret", probe)
		}
	}
}

// A symlink INSIDE the repo pointing outside it is the case a lexical prefix
// check would miss, so containment is enforced after symlink resolution.
func TestViewFileRefusesSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	h, id, wd, secret := viewerServer(t)

	link := filepath.Join(wd, "escape.pem")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	code, got := getFile(t, h, id, "escape.pem")
	if code == http.StatusOK {
		t.Errorf("a symlink out of the workspace was served: %q", got.Content)
	}
	if strings.Contains(got.Content, "PRIVATE KEY") {
		t.Error("symlink escape leaked the secret")
	}

	// A symlink to a directory outside the repo must not be browsable either.
	dirLink := filepath.Join(wd, "outside")
	if err := os.Symlink(filepath.Dir(secret), dirLink); err == nil {
		rec := postJSON(t, h, "GET", "/api/sessions/"+id+"/files?path=outside", "")
		if rec.Code == http.StatusOK {
			t.Errorf("a directory symlink out of the workspace was listed: %s", rec.Body)
		}
	}
}

// A file that legitimately lives inside the workspace via a symlink still works,
// so the confinement doesn't over-block (macOS /var -> /private/var made this a
// real risk for temp-dir workspaces).
func TestViewFileAllowsInWorkspaceSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	h, id, wd, _ := viewerServer(t)
	if err := os.Symlink(filepath.Join(wd, "main.go"), filepath.Join(wd, "alias.go")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if code, got := getFile(t, h, id, "alias.go"); code != http.StatusOK || !strings.Contains(got.Content, "func main()") {
		t.Errorf("in-workspace symlink should be served: code=%d content=%q", code, got.Content)
	}
}

func TestViewFileRejectsDirsAndMissing(t *testing.T) {
	h, id, _, _ := viewerServer(t)
	for _, probe := range []string{"internal", "", "does/not/exist.go"} {
		if code, _ := getFile(t, h, id, probe); code == http.StatusOK {
			t.Errorf("probe %q should not be served as a file", probe)
		}
	}
}

// Binary content must be flagged rather than shoved into JSON, and a huge file
// must be truncated rather than read into memory whole.
func TestViewFileBinaryAndTruncation(t *testing.T) {
	h, id, wd, _ := viewerServer(t)

	mustWrite(t, filepath.Join(wd, "blob.bin"), "MZ\x00\x01\x02binary")
	code, got := getFile(t, h, id, "blob.bin")
	if code != http.StatusOK {
		t.Fatalf("binary file code = %d", code)
	}
	if !got.Binary || got.Content != "" {
		t.Errorf("binary should be flagged with no content: binary=%v content=%q", got.Binary, got.Content)
	}

	big := strings.Repeat("x", maxViewBytes+4096)
	mustWrite(t, filepath.Join(wd, "big.txt"), big)
	code, got = getFile(t, h, id, "big.txt")
	if code != http.StatusOK {
		t.Fatalf("big file code = %d", code)
	}
	if !got.Truncated {
		t.Error("an oversized file should be marked truncated")
	}
	if len(got.Content) > maxViewBytes {
		t.Errorf("served %d bytes, cap is %d", len(got.Content), maxViewBytes)
	}
	if got.Size != int64(len(big)) {
		t.Errorf("size should report the true on-disk size, got %d want %d", got.Size, len(big))
	}
}

// A UTF-8 rune sliced by the size cap must not make a text file look binary.
func TestBytesLookBinary(t *testing.T) {
	if bytesLookBinary([]byte("hello"), false) {
		t.Error("plain ASCII is not binary")
	}
	if !bytesLookBinary([]byte("a\x00b"), false) {
		t.Error("a NUL byte means binary")
	}
	if bytesLookBinary([]byte("héllo"), false) {
		t.Error("valid UTF-8 is not binary")
	}
	// Clip the file mid-rune, as the size cap can: "olé" ends in a 2-byte rune,
	// so dropping its last byte leaves a dangling continuation byte.
	clipped := []byte("olé")
	clipped = clipped[:len(clipped)-1]
	if utf8.Valid(clipped) {
		t.Fatalf("test setup: %q should be invalid UTF-8", clipped)
	}
	if bytesLookBinary(clipped, true) {
		t.Error("a rune clipped by truncation must not be reported as binary")
	}
	if !bytesLookBinary(clipped, false) {
		t.Error("invalid UTF-8 that isn't truncated is binary")
	}
	if bytesLookBinary(nil, false) {
		t.Error("empty is not binary")
	}
}

func TestFileTreeListsWorkspace(t *testing.T) {
	h, id, _, _ := viewerServer(t)

	list := func(path string) (int, fileTree) {
		t.Helper()
		rec := postJSON(t, h, "GET", "/api/sessions/"+id+"/files?path="+path, "")
		var out fileTree
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v (%s)", err, rec.Body)
			}
		}
		return rec.Code, out
	}

	code, root := list("")
	if code != http.StatusOK {
		t.Fatalf("root listing code = %d", code)
	}
	var names []string
	for _, e := range root.Entries {
		names = append(names, e.Name)
		if e.Name == "node_modules" {
			t.Error("node_modules should be skipped")
		}
	}
	if !contains(names, "main.go") || !contains(names, "internal") {
		t.Errorf("root listing missing expected entries: %v", names)
	}
	// Directories sort before files.
	if len(root.Entries) > 1 && !root.Entries[0].Dir {
		t.Errorf("directories should sort first, got %+v", root.Entries)
	}

	// Descending works and paths stay repo-relative.
	code, sub := list("internal/web")
	if code != http.StatusOK {
		t.Fatalf("subdir code = %d", code)
	}
	if len(sub.Entries) != 1 || sub.Entries[0].Path != "internal/web/server.go" {
		t.Errorf("subdir entries wrong: %+v", sub.Entries)
	}

	// Escapes must fail here too.
	for _, probe := range []string{"..", "../..", "/etc"} {
		if code, out := list(probe); code == http.StatusOK {
			t.Errorf("listing %q should be refused, got %+v", probe, out)
		}
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// resolveInWorkspace is the single chokepoint for confinement, so test it
// directly as well as through HTTP.
func TestResolveInWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "a", "b", "c.txt"), "ok")

	if _, ok := resolveInWorkspace(root, "a/b/c.txt"); !ok {
		t.Error("an in-root path should resolve")
	}
	if _, ok := resolveInWorkspace(root, "a/../a/b/c.txt"); !ok {
		t.Error("a path that stays inside after cleaning should resolve")
	}
	for _, bad := range []string{"..", "../x", "a/../../x", "/etc/passwd", ""} {
		if _, ok := resolveInWorkspace(root, bad); ok && bad != "" {
			t.Errorf("%q should be refused", bad)
		}
	}
	if _, ok := resolveInWorkspace("", "a.txt"); ok {
		t.Error("an empty root must never resolve")
	}
	// A sibling directory sharing the root's name prefix must not count as inside.
	sibling := root + "-secrets"
	if err := os.MkdirAll(sibling, 0o755); err == nil {
		mustWrite(t, filepath.Join(sibling, "s.txt"), "secret")
		if _, ok := resolveInWorkspace(root, "../"+filepath.Base(sibling)+"/s.txt"); ok {
			t.Error("a prefix-sharing sibling directory must not be treated as inside the root")
		}
	}
}
