package tools

import (
	"path/filepath"
	"testing"
)

// SetRoot re-roots the registry in place: the path sandbox + relative resolution
// follow the new root, registered tools survive, and a path under the OLD root is
// no longer accepted.
func TestSetRootReRootsInPlace(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()

	r := Default(oldRoot, 1<<20) // a full builtin toolset, rooted at oldRoot
	r.tempRoots = nil            // isolate the workspace boundary
	if !r.Has("read_file") {
		t.Fatal("Default registry should have read_file")
	}

	r.SetRoot(newRoot)

	if r.Root() != newRoot {
		t.Fatalf("Root() = %q, want %q", r.Root(), newRoot)
	}
	// Registered tools must survive a re-root (the whole point — MCP tools stay).
	if !r.Has("read_file") || !r.Has("bash") {
		t.Error("registered tools should survive SetRoot")
	}
	// A relative path now resolves under the NEW root.
	if p, err := r.resolve("a.txt"); err != nil || p != filepath.Join(newRoot, "a.txt") {
		t.Errorf("relative resolve = %q, %v; want under the new root", p, err)
	}
	// A path under the OLD root is now outside the sandbox.
	if _, err := r.resolve(filepath.Join(oldRoot, "b.txt")); err == nil {
		t.Error("a path under the old root must be rejected after SetRoot")
	}
}
