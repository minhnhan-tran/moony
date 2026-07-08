package mcp

import "testing"

func TestMcpCatalog(t *testing.T) {
	cat := Catalog()
	if len(cat) < 4 {
		t.Fatalf("catalog should list several servers, got %d", len(cat))
	}
	e, ok := CatalogEntryByName("filesystem")
	if !ok {
		t.Fatal("catalog should include filesystem")
	}
	if e.Command == "" || len(e.Args) == 0 {
		t.Errorf("filesystem entry incomplete: %+v", e)
	}
	if _, ok := CatalogEntryByName("nope"); ok {
		t.Error("CatalogEntryByName should miss unknown names")
	}
}

func TestMcpCatalogCodebaseMemory(t *testing.T) {
	e, ok := CatalogEntryByName("codebase-memory")
	if !ok {
		t.Fatal("catalog should include codebase-memory")
	}
	if e.Command != "codebase-memory-mcp" {
		t.Errorf("codebase-memory command = %q, want codebase-memory-mcp", e.Command)
	}
	// It's a prebuilt binary resolved from PATH — no npx/uvx args, no remote URL.
	if len(e.Args) != 0 {
		t.Errorf("codebase-memory should take no args, got %v", e.Args)
	}
	if e.URL != "" {
		t.Errorf("codebase-memory is a stdio server, not a URL: %q", e.URL)
	}
}
