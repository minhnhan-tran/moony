package plugins

// Git-based plugin marketplaces, Claude-Code style. A marketplace is a cloned
// git repo with a `.claude-plugin/marketplace.json` listing plugins; each plugin
// is a directory with a `.claude-plugin/plugin.json` plus convention dirs
// (commands/, agents/, skills/, and a .mcp.json). Installing a marketplace
// plugin copies its components into the app's existing skills/agents/commands
// dirs and registers its MCP servers, so the normal loaders pick them up; an
// install manifest records exactly what was written so removal is precise.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/mcp"
)

// Marketplace is an added marketplace: a name, its git URL, and the local clone.
type Marketplace struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Path string `json:"path"`
}

type registryFile struct {
	Marketplaces []Marketplace `json:"marketplaces"`
}

// marketplaceManifest is the .claude-plugin/marketplace.json schema (the subset
// we use): a name and a list of plugins.
type marketplaceManifest struct {
	Name    string              `json:"name"`
	Plugins []marketplacePlugin `json:"plugins"`
}

// marketplacePlugin is one entry in marketplace.json. Source may be a string
// (a path relative to the repo root) or omitted (defaults to a subdir named
// after the plugin); object sources (external git) are not supported.
type marketplacePlugin struct {
	Name        string          `json:"name"`
	Source      json.RawMessage `json:"source,omitempty"`
	Description string          `json:"description,omitempty"`
}

func (p marketplacePlugin) relPath() string {
	var s string
	if len(p.Source) > 0 && json.Unmarshal(p.Source, &s) == nil && strings.TrimSpace(s) != "" {
		return s
	}
	return p.Name
}

// installedPlugin records what a marketplace-plugin install wrote, so Remove can
// reverse exactly those changes (and not touch files it didn't create).
type installedPlugin struct {
	Plugin      string   `json:"plugin"`
	Marketplace string   `json:"marketplace"`
	Files       []string `json:"files,omitempty"`     // copied command/agent files
	SkillDirs   []string `json:"skillDirs,omitempty"` // copied skill directories
	MCP         []string `json:"mcp,omitempty"`       // mcp.json server names added
}

type manifestFile struct {
	Installed []installedPlugin `json:"installed"`
}

// MarketPluginStatus describes a marketplace plugin for listing in the UI.
type MarketPluginStatus struct {
	Marketplace string   `json:"marketplace"`
	Name        string   `json:"name"`
	Description string   `json:"desc,omitempty"`
	Installed   bool     `json:"installed"`
	Skills      []string `json:"skills,omitempty"`
	Agents      []string `json:"agents,omitempty"`
	Commands    []string `json:"commands,omitempty"`
	MCP         []string `json:"mcp,omitempty"`
}

// cloneRepo / pullRepo are indirected so tests can stub git out.
var cloneRepo = func(ctx context.Context, url, dest string) error {
	out, err := runGit(ctx, "clone", "--depth", "1", url, dest)
	if err != nil {
		return fmt.Errorf("git clone failed: %v: %s", err, lastLine(out))
	}
	return nil
}

var pullRepo = func(ctx context.Context, dir string) error {
	out, err := runGit(ctx, "-C", dir, "pull", "--ff-only")
	if err != nil {
		return fmt.Errorf("git pull failed: %v: %s", err, lastLine(out))
	}
	return nil
}

// runGit runs the git CLI on this machine and returns its combined output.
func runGit(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "git", args...).CombinedOutput()
}

// --- registry ----------------------------------------------------------------

func sqeConfigBase() (string, error) {
	return config.GlobalConfigDir()
}

func registryPath() (string, error) {
	base, err := sqeConfigBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "marketplaces.json"), nil
}

func marketplacesDir() (string, error) {
	base, err := sqeConfigBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "marketplaces"), nil
}

func loadRegistry() registryFile {
	var r registryFile
	path, err := registryPath()
	if err != nil {
		return r
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

func saveRegistry(r registryFile) error {
	path, err := registryPath()
	if err != nil {
		return err
	}
	return writeJSONFile(path, r)
}

// Marketplaces returns the added marketplaces.
func Marketplaces() []Marketplace { return loadRegistry().Marketplaces }

// defaultMarketplaces are pre-registered for every user so curated
// skill/MCP/plugin marketplaces are available out of the box. It is empty by
// default — add marketplaces with `/plugins marketplace add <url>`. Entries here
// are cloned lazily by EnsureDefaultMarketplaces; one that can't be reached
// (offline, no access, private repo) is simply skipped so nothing blocks on it.
var defaultMarketplaces = []Marketplace{}

// DefaultMarketplaces returns the built-in default marketplaces (name + URL).
func DefaultMarketplaces() []Marketplace {
	out := make([]Marketplace, len(defaultMarketplaces))
	copy(out, defaultMarketplaces)
	return out
}

// EnsureDefaultMarketplaces clones and registers any built-in default
// marketplace not already added. Best-effort and idempotent: an already-added
// default is left untouched, and a default that can't be cloned (offline/auth)
// is skipped rather than erroring. Safe to run in the background at startup.
func EnsureDefaultMarketplaces(ctx context.Context) {
	for _, d := range defaultMarketplaces {
		if _, ok := marketplaceByName(d.Name); ok {
			continue // already registered (clone exists)
		}
		_, _ = AddMarketplace(ctx, d.Name, d.URL) // best effort
	}
}

func marketplaceByName(name string) (Marketplace, bool) {
	for _, m := range loadRegistry().Marketplaces {
		if m.Name == name {
			return m, true
		}
	}
	return Marketplace{}, false
}

// AddMarketplace clones a marketplace git repo and registers it. name is
// optional (derived from the URL when empty). It errors if the name is already
// added or the repo lacks a .claude-plugin/marketplace.json.
func AddMarketplace(ctx context.Context, name, url string) (Marketplace, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return Marketplace{}, fmt.Errorf("a git URL is required")
	}
	if name == "" {
		name = deriveMarketplaceName(url)
	}
	if !validName(name) {
		return Marketplace{}, fmt.Errorf("invalid marketplace name %q", name)
	}
	if _, exists := marketplaceByName(name); exists {
		return Marketplace{}, fmt.Errorf("marketplace %q is already added (remove it first, or use a different name)", name)
	}
	dir, err := marketplacesDir()
	if err != nil {
		return Marketplace{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Marketplace{}, err
	}
	dest := filepath.Join(dir, name)
	_ = os.RemoveAll(dest) // clear any stale clone
	if err := cloneRepo(ctx, url, dest); err != nil {
		return Marketplace{}, err
	}
	if _, err := readMarketplaceManifest(dest); err != nil {
		_ = os.RemoveAll(dest)
		return Marketplace{}, fmt.Errorf("%s is not a plugin marketplace: %w", url, err)
	}
	m := Marketplace{Name: name, URL: url, Path: dest}
	reg := loadRegistry()
	reg.Marketplaces = append(reg.Marketplaces, m)
	if err := saveRegistry(reg); err != nil {
		return Marketplace{}, err
	}
	return m, nil
}

// UpdateMarketplace git-pulls a marketplace clone.
func UpdateMarketplace(ctx context.Context, name string) error {
	m, ok := marketplaceByName(name)
	if !ok {
		return fmt.Errorf("unknown marketplace %q", name)
	}
	return pullRepo(ctx, m.Path)
}

// RemoveMarketplace deletes a marketplace's clone and registry entry. Plugins
// already installed from it stay installed (remove them with /plugins remove).
func RemoveMarketplace(name string) error {
	reg := loadRegistry()
	out := reg.Marketplaces[:0]
	found := false
	for _, m := range reg.Marketplaces {
		if m.Name == name {
			found = true
			_ = os.RemoveAll(m.Path)
			continue
		}
		out = append(out, m)
	}
	if !found {
		return fmt.Errorf("unknown marketplace %q", name)
	}
	reg.Marketplaces = out
	return saveRegistry(reg)
}

func readMarketplaceManifest(clonePath string) (marketplaceManifest, error) {
	var man marketplaceManifest
	path := filepath.Join(clonePath, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return man, fmt.Errorf("missing .claude-plugin/marketplace.json")
	}
	if err := json.Unmarshal(data, &man); err != nil {
		return man, fmt.Errorf("invalid marketplace.json: %w", err)
	}
	return man, nil
}

// pluginDir resolves a plugin's directory inside its marketplace clone, guarding
// against path traversal outside the clone.
func pluginDir(m Marketplace, p marketplacePlugin) (string, bool) {
	dir := filepath.Clean(filepath.Join(m.Path, p.relPath()))
	rel, err := filepath.Rel(m.Path, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false
	}
	return dir, true
}

// --- listing ------------------------------------------------------------------

// MarketplacePlugins lists every plugin across all added marketplaces with its
// install state and a summary of its components.
func MarketplacePlugins(workingDir string) []MarketPluginStatus {
	manifest := loadManifest()
	out := []MarketPluginStatus{}
	for _, m := range Marketplaces() {
		man, err := readMarketplaceManifest(m.Path)
		if err != nil {
			continue
		}
		for _, p := range man.Plugins {
			st := MarketPluginStatus{
				Marketplace: m.Name, Name: p.Name, Description: p.Description,
				Installed: manifestHas(manifest, p.Name, m.Name),
			}
			if dir, ok := pluginDir(m, p); ok {
				st.Skills, st.Agents, st.Commands, st.MCP = scanPluginComponents(dir)
			}
			out = append(out, st)
		}
	}
	return out
}

// scanPluginComponents reports the component names a plugin dir contributes.
func scanPluginComponents(dir string) (skills, agents, commands, mcpServers []string) {
	for _, e := range readDirSafe(filepath.Join(dir, "skills")) {
		if e.IsDir() && fileExists(filepath.Join(dir, "skills", e.Name(), "SKILL.md")) {
			skills = append(skills, e.Name())
		}
	}
	for _, e := range readDirSafe(filepath.Join(dir, "agents")) {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			agents = append(agents, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	for _, e := range readDirSafe(filepath.Join(dir, "commands")) {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			commands = append(commands, "/"+strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	for name := range readPluginMCP(dir) {
		mcpServers = append(mcpServers, name)
	}
	return
}

// --- install / remove ---------------------------------------------------------

type mcpServerSpec struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// readPluginMCP reads a plugin's .mcp.json (or mcp.json), returning its servers.
func readPluginMCP(dir string) map[string]mcpServerSpec {
	for _, name := range []string{".mcp.json", "mcp.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var f struct {
			MCPServers map[string]mcpServerSpec `json:"mcpServers"`
		}
		if json.Unmarshal(data, &f) == nil && len(f.MCPServers) > 0 {
			return f.MCPServers
		}
	}
	return nil
}

// InstallFromMarketplace copies a marketplace plugin's components into the app's
// global skills/agents/commands dirs and registers its MCP servers, recording
// what it wrote. Components that already exist are skipped (and not recorded, so
// removal won't delete a pre-existing file). Returns a summary of what it added.
func InstallFromMarketplace(plugin, marketName, workingDir string) (string, error) {
	m, ok := marketplaceByName(marketName)
	if !ok {
		return "", fmt.Errorf("unknown marketplace %q — add it with /plugins marketplace add <git-url>", marketName)
	}
	man, err := readMarketplaceManifest(m.Path)
	if err != nil {
		return "", err
	}
	var mp marketplacePlugin
	found := false
	for _, p := range man.Plugins {
		if p.Name == plugin {
			mp, found = p, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("marketplace %q has no plugin %q", marketName, plugin)
	}
	dir, ok := pluginDir(m, mp)
	if !ok {
		return "", fmt.Errorf("plugin %q is missing from the %q clone (try /plugins marketplace update %s)", plugin, marketName, marketName)
	}

	cfgBase, err := sqeConfigBase()
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	rec := installedPlugin{Plugin: plugin, Marketplace: marketName}
	var added []string

	// commands/*.md -> ~/.config/moony/commands/
	if dst, n, err := copyMDFiles(filepath.Join(dir, "commands"), filepath.Join(cfgBase, "commands")); err != nil {
		return "", err
	} else {
		rec.Files = append(rec.Files, dst...)
		for _, f := range n {
			added = append(added, "command /"+f)
		}
	}
	// agents/*.md -> ~/.config/moony/agents/
	if dst, n, err := copyMDFiles(filepath.Join(dir, "agents"), filepath.Join(cfgBase, "agents")); err != nil {
		return "", err
	} else {
		rec.Files = append(rec.Files, dst...)
		for _, f := range n {
			added = append(added, "agent "+f)
		}
	}
	// skills/<name>/ -> ~/.claude/skills/<name>/
	skillsDst := filepath.Join(home, ".claude", "skills")
	for _, e := range readDirSafe(filepath.Join(dir, "skills")) {
		if !e.IsDir() || !fileExists(filepath.Join(dir, "skills", e.Name(), "SKILL.md")) {
			continue
		}
		target := filepath.Join(skillsDst, e.Name())
		if fileExists(target) {
			continue // don't clobber an existing skill
		}
		if err := copyDir(filepath.Join(dir, "skills", e.Name()), target); err != nil {
			return "", fmt.Errorf("copy skill %q: %w", e.Name(), err)
		}
		rec.SkillDirs = append(rec.SkillDirs, target)
		added = append(added, "skill "+e.Name())
	}
	// .mcp.json servers -> global mcp.json (via the existing AddServer)
	for name, s := range readPluginMCP(dir) {
		args := make([]string, len(s.Args))
		for i, a := range s.Args {
			args[i] = strings.ReplaceAll(a, "${root}", workingDir)
		}
		if _, err := mcp.AddServer(name, s.Command, args, s.URL, s.Headers, true, workingDir); err != nil {
			return "", fmt.Errorf("register mcp %q: %w", name, err)
		}
		rec.MCP = append(rec.MCP, name)
		added = append(added, "mcp "+name)
	}

	manifest := loadManifest()
	manifest = manifestUpsert(manifest, rec)
	if err := saveManifest(manifest); err != nil {
		return "", err
	}
	if len(added) == 0 {
		return "already installed (no new components)", nil
	}
	return strings.Join(added, ", "), nil
}

// RemoveFromMarketplace deletes exactly the components a previous install wrote
// (per the manifest) and unregisters its MCP servers.
func RemoveFromMarketplace(plugin, marketName, workingDir string) (string, error) {
	manifest := loadManifest()
	rec, ok := manifestGet(manifest, plugin, marketName)
	if !ok {
		return "", fmt.Errorf("%s@%s is not installed", plugin, marketName)
	}
	var removed []string
	for _, f := range rec.Files {
		if os.Remove(f) == nil {
			removed = append(removed, filepath.Base(f))
		}
	}
	for _, d := range rec.SkillDirs {
		if os.RemoveAll(d) == nil {
			removed = append(removed, "skill "+filepath.Base(d))
		}
	}
	for _, name := range rec.MCP {
		if _, err := mcp.RemoveServer(name, workingDir); err == nil {
			removed = append(removed, "mcp "+name)
		}
	}
	manifest = manifestDelete(manifest, plugin, marketName)
	if err := saveManifest(manifest); err != nil {
		return "", err
	}
	if len(removed) == 0 {
		return "nothing to remove", nil
	}
	return strings.Join(removed, ", "), nil
}

// --- manifest -----------------------------------------------------------------

func manifestPath() (string, error) {
	base, err := sqeConfigBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "installed-plugins.json"), nil
}

func loadManifest() manifestFile {
	var mf manifestFile
	path, err := manifestPath()
	if err != nil {
		return mf
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &mf)
	}
	return mf
}

func saveManifest(mf manifestFile) error {
	path, err := manifestPath()
	if err != nil {
		return err
	}
	return writeJSONFile(path, mf)
}

func manifestHas(mf manifestFile, plugin, market string) bool {
	_, ok := manifestGet(mf, plugin, market)
	return ok
}

func manifestGet(mf manifestFile, plugin, market string) (installedPlugin, bool) {
	for _, r := range mf.Installed {
		if r.Plugin == plugin && r.Marketplace == market {
			return r, true
		}
	}
	return installedPlugin{}, false
}

func manifestUpsert(mf manifestFile, rec installedPlugin) manifestFile {
	for i, r := range mf.Installed {
		if r.Plugin == rec.Plugin && r.Marketplace == rec.Marketplace {
			// merge recorded paths so a re-install that adds the rest is tracked
			rec.Files = append(r.Files, rec.Files...)
			rec.SkillDirs = append(r.SkillDirs, rec.SkillDirs...)
			rec.MCP = append(r.MCP, rec.MCP...)
			mf.Installed[i] = rec
			return mf
		}
	}
	mf.Installed = append(mf.Installed, rec)
	return mf
}

func manifestDelete(mf manifestFile, plugin, market string) manifestFile {
	out := mf.Installed[:0]
	for _, r := range mf.Installed {
		if r.Plugin == plugin && r.Marketplace == market {
			continue
		}
		out = append(out, r)
	}
	mf.Installed = out
	return mf
}

// --- helpers ------------------------------------------------------------------

// SplitRef splits a "plugin@marketplace" reference into its parts; market is ""
// for a built-in catalog plugin (no "@").
func SplitRef(s string) (name, market string) {
	if i := strings.IndexByte(s, '@'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// deriveMarketplaceName turns a git URL into a short name (last path segment,
// minus a .git suffix).
func deriveMarketplaceName(url string) string {
	s := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func validName(name string) bool {
	return name != "" && name == filepath.Base(name) && name != "." && name != ".." && !strings.ContainsAny(name, "/\\ ")
}

func readDirSafe(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return entries
}

// copyMDFiles copies every *.md from srcDir into dstDir (skipping ones that
// already exist), returning the destination paths written and their base names
// (without .md). A missing srcDir is fine (returns nothing).
func copyMDFiles(srcDir, dstDir string) (written, names []string, err error) {
	entries := readDirSafe(srcDir)
	if len(entries) == 0 {
		return nil, nil, nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		dst := filepath.Join(dstDir, filepath.Base(e.Name()))
		if fileExists(dst) {
			continue
		}
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return nil, nil, err
		}
		if err := copyFile(filepath.Join(srcDir, e.Name()), dst); err != nil {
			return nil, nil, err
		}
		written = append(written, dst)
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	return written, names, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
