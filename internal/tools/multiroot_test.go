package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMultiRootResolveAndRead covers scope-A multi-root: AddRoots dedupes and
// skips the primary, resolve accepts paths under any root and rejects paths
// outside all of them, and a file tool can actually read across roots.
func TestMultiRootResolveAndRead(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	outside := t.TempDir()

	r := NewRegistry(primary)
	r.AddRoots(extra, extra, primary) // duplicate + the primary itself are skipped
	r.tempRoots = nil                 // isolate workspace-root logic (temp scratch is covered separately)
	if got := r.Roots(); len(got) != 2 || got[0] != primary || got[1] != extra {
		t.Fatalf("Roots() = %v, want [primary, extra] (deduped, primary first)", got)
	}

	// A relative path resolves against the primary root.
	if p, err := r.resolve("a.txt"); err != nil || p != filepath.Join(primary, "a.txt") {
		t.Errorf("relative resolve = %q, %v; want under the primary root", p, err)
	}
	// An absolute path under an extra root is accepted.
	ep := filepath.Join(extra, "b.txt")
	if p, err := r.resolve(ep); err != nil || p != ep {
		t.Errorf("extra-root resolve = %q, %v; want accepted", p, err)
	}
	// An absolute path outside every root is rejected.
	if _, err := r.resolve(filepath.Join(outside, "c.txt")); err == nil {
		t.Error("a path outside all workspace roots must be rejected")
	}

	// End-to-end: the read tool reads a file in the extra root by absolute path.
	if err := os.WriteFile(ep, []byte("hello extra root"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"path": ep})
	out, err := (&readTool{r: r}).Execute(context.Background(), in)
	if err != nil || !strings.Contains(out, "hello extra root") {
		t.Errorf("read across roots = %q, %v; want the extra-root file's contents", out, err)
	}
}

// TestSingleRootStillConfines confirms that with no extra roots, traversal out
// of the working dir is still rejected (the default sandbox is unchanged). Temp
// scratch is cleared here so the assertion isolates traversal confinement — the
// temp escape hatch itself is covered by TestTempDirScratchAllowed.
func TestSingleRootStillConfines(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.tempRoots = nil
	if _, err := r.resolve("../escape.txt"); err == nil {
		t.Error("single-root resolve must still reject traversal outside the working dir")
	}
}

// TestTempDirScratchAllowed verifies the scratch escape hatch: absolute paths
// under the OS temp dir (or /tmp) are accepted even from a workspace root that
// is itself outside temp, while other absolute paths stay rejected.
func TestTempDirScratchAllowed(t *testing.T) {
	r := NewRegistry("/srv/app") // a non-temp root; resolve doesn't stat it
	for _, p := range []string{"/tmp/SECURITY_AUDIT_SUMMARY.md", filepath.Join(os.TempDir(), "scratch.md")} {
		if got, err := r.resolve(p); err != nil {
			t.Errorf("resolve(%q) = %v; want accepted as temp scratch", p, err)
		} else if got != filepath.Clean(p) {
			t.Errorf("resolve(%q) = %q; want the cleaned path", p, got)
		}
	}
	if _, err := r.resolve("/etc/passwd"); err == nil {
		t.Error("a non-temp absolute path outside the workspace must still be rejected")
	}
}
