package lsp

// The LSP "marketplace": a curated catalog of popular language servers the web
// UI can install. Installing registers the server in lsp.json (see edit.go) and,
// when the binary isn't already on PATH, optionally runs the (vetted) install
// command. Only these catalog argvs are ever run server-side — never arbitrary
// user input — which keeps the install action's blast radius small.

import "os/exec"

// CatalogEntry is one curated language server.
type CatalogEntry struct {
	Name        string   // unique id and lsp.json key, e.g. "gopls"
	Language    string   // display label, e.g. "Go"
	Desc        string   // one-line description
	Command     string   // the binary to run
	Args        []string // server args (stdio mode, etc.)
	Extensions  []string // file extensions it handles
	Install     string   // human-readable install command (shown in the UI)
	installArgv []string // argv run server-side to install the binary (catalog-only)
}

// InstallArgv is the argv used to install this entry's binary, or nil if none.
func (e CatalogEntry) InstallArgv() []string { return e.installArgv }

// catalog is the built-in marketplace. Install commands favor the most common
// per-language tool (npm for the JS-based servers, go/rustup/gem/brew otherwise).
var catalog = []CatalogEntry{
	{Name: "gopls", Language: "Go", Desc: "Official Go language server.",
		Command: "gopls", Extensions: []string{".go"},
		Install: "go install golang.org/x/tools/gopls@latest", installArgv: []string{"go", "install", "golang.org/x/tools/gopls@latest"}},
	{Name: "pyright", Language: "Python", Desc: "Microsoft's Python type checker and language server.",
		Command: "pyright-langserver", Args: []string{"--stdio"}, Extensions: []string{".py", ".pyi"},
		Install: "npm install -g pyright", installArgv: []string{"npm", "install", "-g", "pyright"}},
	{Name: "typescript", Language: "TypeScript / JavaScript", Desc: "typescript-language-server (TS + JS).",
		Command: "typescript-language-server", Args: []string{"--stdio"}, Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
		Install: "npm install -g typescript-language-server typescript", installArgv: []string{"npm", "install", "-g", "typescript-language-server", "typescript"}},
	{Name: "rust-analyzer", Language: "Rust", Desc: "Official Rust language server.",
		Command: "rust-analyzer", Extensions: []string{".rs"},
		Install: "rustup component add rust-analyzer", installArgv: []string{"rustup", "component", "add", "rust-analyzer"}},
	{Name: "bash", Language: "Bash", Desc: "bash-language-server.",
		Command: "bash-language-server", Args: []string{"start"}, Extensions: []string{".sh", ".bash"},
		Install: "npm install -g bash-language-server", installArgv: []string{"npm", "install", "-g", "bash-language-server"}},
	{Name: "yaml", Language: "YAML", Desc: "Red Hat YAML language server.",
		Command: "yaml-language-server", Args: []string{"--stdio"}, Extensions: []string{".yaml", ".yml"},
		Install: "npm install -g yaml-language-server", installArgv: []string{"npm", "install", "-g", "yaml-language-server"}},
	{Name: "json", Language: "JSON / HTML / CSS", Desc: "VS Code's extracted servers (vscode-langservers-extracted).",
		Command: "vscode-json-language-server", Args: []string{"--stdio"}, Extensions: []string{".json", ".jsonc"},
		Install: "npm install -g vscode-langservers-extracted", installArgv: []string{"npm", "install", "-g", "vscode-langservers-extracted"}},
	{Name: "clangd", Language: "C / C++", Desc: "LLVM clangd.",
		Command: "clangd", Extensions: []string{".c", ".h", ".cc", ".cpp", ".hpp", ".cxx"},
		Install: "brew install llvm", installArgv: []string{"brew", "install", "llvm"}},
	{Name: "lua", Language: "Lua", Desc: "lua-language-server.",
		Command: "lua-language-server", Extensions: []string{".lua"},
		Install: "brew install lua-language-server", installArgv: []string{"brew", "install", "lua-language-server"}},
	{Name: "ruby", Language: "Ruby", Desc: "Solargraph language server.",
		Command: "solargraph", Args: []string{"stdio"}, Extensions: []string{".rb"},
		Install: "gem install solargraph", installArgv: []string{"gem", "install", "solargraph"}},
}

// Catalog returns the built-in marketplace of language servers.
func Catalog() []CatalogEntry { return catalog }

// CatalogEntryByName returns the catalog entry with the given name.
func CatalogEntryByName(name string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.Name == name {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// BinaryInstalled reports whether command is resolvable on PATH. It's a var so
// tests can stub binary detection.
var BinaryInstalled = func(command string) bool {
	if command == "" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil
}
