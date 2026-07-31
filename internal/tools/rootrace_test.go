package tools

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// SetRoot is documented as safe once the session is idle, but the symbol-map and
// repo-map work runs in BACKGROUND goroutines that outlive a tool call — so "idle"
// does not mean "nothing is reading r.root / repoMapStr".
func TestSetRootRacesBackgroundMaps(t *testing.T) {
	dir := t.TempDir()
	for i := range 40 {
		sub := filepath.Join(dir, "pkg")
		_ = os.MkdirAll(sub, 0o755)
		_ = os.WriteFile(filepath.Join(sub, string(rune('a'+i%26))+"x.go"),
			[]byte("package pkg\n\nfunc Foo() {}\n"), 0o644)
	}
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(other, "m.go"), []byte("package m\n\nfunc Bar() {}\n"), 0o644)

	r := NewRegistry(dir)
	var wg sync.WaitGroup
	wg.Add(3)
	// Reader 1: the prompt-assembly path (kicks off background builds).
	go func() {
		defer wg.Done()
		for range 30 {
			_ = r.SymbolDigest()
		}
	}()
	// Reader 2: the repo map memo.
	go func() {
		defer wg.Done()
		for range 30 {
			_ = r.RepoMap()
		}
	}()
	// Writer: /cd re-roots in place.
	go func() {
		defer wg.Done()
		for i := range 30 {
			if i%2 == 0 {
				r.SetRoot(other)
			} else {
				r.SetRoot(dir)
			}
		}
	}()
	wg.Wait()
}
