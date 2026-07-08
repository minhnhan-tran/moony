package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogHasPopularServers(t *testing.T) {
	cat := Catalog()
	if len(cat) < 5 {
		t.Fatalf("catalog should list several servers, got %d", len(cat))
	}
	e, ok := CatalogEntryByName("gopls")
	if !ok {
		t.Fatal("catalog should include gopls")
	}
	if e.Command != "gopls" || len(e.Extensions) == 0 || len(e.InstallArgv()) == 0 {
		t.Errorf("gopls entry incomplete: %+v", e)
	}
	if _, ok := CatalogEntryByName("nope"); ok {
		t.Error("CatalogEntryByName should miss unknown names")
	}
}

func TestAddRemoveServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	wd := t.TempDir()

	path, err := AddServer("gopls", "gopls", nil, []string{".go"}, false, wd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lsp.json not written: %v", err)
	}
	cfgs, err := LoadConfigs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Name != "gopls" || len(cfgs[0].Extensions) != 1 {
		t.Fatalf("AddServer didn't register the server: %+v", cfgs)
	}

	if _, err := RemoveServer("gopls", wd); err != nil {
		t.Fatal(err)
	}
	cfgs, _ = LoadConfigs(wd)
	if len(cfgs) != 0 {
		t.Fatalf("RemoveServer left the server behind: %+v", cfgs)
	}
	if _, err := RemoveServer("gopls", wd); err == nil {
		t.Error("removing a missing server should error")
	}
}
