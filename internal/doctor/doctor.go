// Package doctor runs environment and configuration checks for moony
// and renders a human-readable report. It's surfaced by the /doctor command and
// is meant to make environment quirks (GOROOT, module proxy) and the optional
// MCP/LSP wiring easy to diagnose.
package doctor

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/hooks"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/mcp"
	"github.com/pysty1995/moony/internal/netrc"
	"github.com/pysty1995/moony/internal/provider"
	"github.com/pysty1995/moony/internal/subagents"
)

// Status classifies a single check's outcome.
type Status int

const (
	// StatusOK means the check passed.
	StatusOK Status = iota
	// StatusWarn means the check found a non-fatal issue worth noting.
	StatusWarn
	// StatusFail means something the agent depends on is broken.
	StatusFail
)

func (s Status) icon() string {
	switch s {
	case StatusOK:
		return "✓"
	case StatusWarn:
		return "!"
	default:
		return "✗"
	}
}

// Check is the result of one diagnostic.
type Check struct {
	Name   string
	Status Status
	Detail string
}

// Report is the full set of checks plus a one-line summary.
type Report struct {
	Checks []Check
}

// add appends a check to the report.
func (r *Report) add(name string, status Status, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: detail})
}

// Counts returns how many checks passed, warned, and failed.
func (r *Report) Counts() (ok, warn, fail int) {
	for _, c := range r.Checks {
		switch c.Status {
		case StatusOK:
			ok++
		case StatusWarn:
			warn++
		default:
			fail++
		}
	}
	return
}

// String renders the report as aligned, human-readable lines suitable for the
// transcript.
func (r *Report) String() string {
	var b strings.Builder
	b.WriteString("Environment check (/doctor)\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  %s %-22s %s\n", c.Status.icon(), c.Name, c.Detail)
	}
	ok, warn, fail := r.Counts()
	parts := []string{fmt.Sprintf("%d ok", ok)}
	if warn > 0 {
		parts = append(parts, fmt.Sprintf("%d warning(s)", warn))
	}
	if fail > 0 {
		parts = append(parts, fmt.Sprintf("%d failure(s)", fail))
	}
	fmt.Fprintf(&b, "\nSummary: %s.", strings.Join(parts, ", "))
	if fail == 0 && warn == 0 {
		b.WriteString(" Everything looks good.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Run executes every diagnostic and returns the assembled report. Network-
// dependent checks honor ctx (use a timeout). The llm token check is provided
// as a closure so this package doesn't depend on the llm client construction;
// pass nil to skip the live auth check.
func Run(ctx context.Context, cfg *config.Config, tokenFn func(context.Context) error) *Report {
	r := &Report{}
	checkRuntime(r)
	checkGoEnv(r)
	checkOllama(ctx, r, cfg, tokenFn)
	checkConfigFiles(r, cfg)
	checkMCP(r, cfg)
	checkLSP(r, cfg)
	checkHooks(r, cfg)
	checkSubagentsAndCommands(r, cfg)
	checkSandbox(r, cfg)
	checkNetrc(r)
	return r
}

func checkRuntime(r *Report) {
	r.add("os", StatusOK, fmt.Sprintf("%s/%s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version()))

}

// checkGoEnv flags the two corporate-environment quirks documented in the
// README: a GOROOT pinned to a mismatched SDK and a blocked public module proxy.
func checkGoEnv(r *Report) {
	if root := os.Getenv("GOROOT"); root != "" {
		r.add("GOROOT", StatusWarn, root+" is set — build with `make` (it runs `env -u GOROOT`) to avoid stdlib mismatches")
	} else {
		r.add("GOROOT", StatusOK, "unset (uses the go tool's own stdlib)")
	}

	proxy := goEnv("GOPROXY")
	if proxy == "" {
		r.add("GOPROXY", StatusWarn, "unset — module downloads use the default (proxy.golang.org)")
	} else {
		r.add("GOPROXY", StatusOK, proxy)
	}
}

// goEnv reads a single `go env` value, returning "" on any error.
func goEnv(key string) string {
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkOllama tests TCP reachability of the configured Ollama server and, when
// tokenFn is supplied, runs a live auth/reachability probe against it. A
// successful dial confirms DNS + routing + firewall allow the host.
func checkOllama(ctx context.Context, r *Report, cfg *config.Config, tokenFn func(context.Context) error) {
	endpoint := cfg.OllamaBaseURL
	host := hostPort(endpoint)
	if host == "" {
		r.add("ollama", StatusWarn, "could not parse host from "+endpoint)
	} else {
		d := net.Dialer{}
		conn, err := d.DialContext(ctx, "tcp", host)
		if err != nil {
			r.add("ollama", StatusFail, "cannot reach "+host+": "+oneLine(err.Error())+" (set MN_OLLAMA_HOST)")
		} else {
			_ = conn.Close()
			r.add("ollama", StatusOK, "reachable ("+host+")")
		}
	}

	if tokenFn == nil {
		r.add("ollama probe", StatusWarn, "skipped (no live check requested)")
		return
	}
	start := time.Now()
	if err := tokenFn(ctx); err != nil {
		r.add("ollama probe", StatusFail, oneLine(err.Error()))
		return
	}
	r.add("ollama probe", StatusOK, fmt.Sprintf("responded in %s", time.Since(start).Round(time.Millisecond)))
}

// hostPort extracts host:port from a URL, defaulting to :443 for https / :80
// for http.
func hostPort(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Port() != "" {
		return u.Host
	}
	port := "443"
	if u.Scheme == "http" {
		port = "80"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// checkConfigFiles reports on the saved prefs file and the working directory.
func checkConfigFiles(r *Report, cfg *config.Config) {
	if base, err := config.GlobalConfigDir(); err == nil {
		dir := base
		prefs := filepath.Join(dir, "config.json")
		if _, err := os.Stat(prefs); err == nil {
			r.add("saved config", StatusOK, prefs)
		} else {
			r.add("saved config", StatusOK, "none yet (defaults in use) — would save to "+prefs)
		}
	} else {
		r.add("saved config", StatusWarn, "no user config dir: "+oneLine(err.Error()))
	}

	if cfg.WorkingDir == "" {
		r.add("working dir", StatusWarn, "unset")
	} else if info, err := os.Stat(cfg.WorkingDir); err != nil || !info.IsDir() {
		r.add("working dir", StatusFail, cfg.WorkingDir+" is not a directory")
	} else {
		r.add("working dir", StatusOK, cfg.WorkingDir)
	}

	r.add("model", StatusOK, fmt.Sprintf("%s via %s", cfg.Model, provider.Label(cfg.LLMProvider)))
}

// checkMCP validates the MCP config file (if any) without connecting.
func checkMCP(r *Report, cfg *config.Config) {
	cfgs, err := mcp.LoadConfigs(cfg.WorkingDir)
	if err != nil {
		r.add("mcp config", StatusFail, oneLine(err.Error()))
		return
	}
	if len(cfgs) == 0 {
		r.add("mcp config", StatusOK, "no servers configured")
		return
	}
	var names []string
	warn := false
	for _, c := range cfgs {
		names = append(names, c.Name)
		if c.Command != "" {
			if _, err := exec.LookPath(c.Command); err != nil {
				warn = true
				names[len(names)-1] = c.Name + " (missing: " + c.Command + ")"
			}
		}
	}
	sort.Strings(names)
	status := StatusOK
	if warn {
		status = StatusWarn
	}
	r.add("mcp config", status, fmt.Sprintf("%d server(s): %s", len(cfgs), strings.Join(names, ", ")))
}

// checkLSP validates the LSP config file (if any) without starting servers.
func checkLSP(r *Report, cfg *config.Config) {
	cfgs, err := lsp.LoadConfigs(cfg.WorkingDir)
	if err != nil {
		r.add("lsp config", StatusFail, oneLine(err.Error()))
		return
	}
	if len(cfgs) == 0 {
		r.add("lsp config", StatusOK, "no servers configured")
		return
	}
	var names []string
	warn := false
	for _, c := range cfgs {
		label := c.Name
		if c.Command != "" {
			if _, err := exec.LookPath(c.Command); err != nil {
				warn = true
				label += " (missing: " + c.Command + ")"
			}
		}
		names = append(names, label)
	}
	sort.Strings(names)
	status := StatusOK
	if warn {
		status = StatusWarn
	}
	r.add("lsp config", status, fmt.Sprintf("%d server(s): %s", len(cfgs), strings.Join(names, ", ")))
}

// checkHooks validates the hooks.json config files (if any) by loading them the
// same way the agent does. A malformed file is a failure; otherwise it reports
// how many hook commands are configured and for which lifecycle events.
func checkHooks(r *Report, cfg *config.Config) {
	set, err := hooks.New(cfg.WorkingDir, nil)
	if err != nil {
		r.add("hooks config", StatusFail, oneLine(err.Error()))
		return
	}
	if set.Empty() {
		r.add("hooks config", StatusOK, "no hooks configured")
		return
	}
	r.add("hooks config", StatusOK, fmt.Sprintf("%d hook(s): %s", set.Count(), strings.Join(set.Events(), ", ")))
}

// checkSubagentsAndCommands reports the sub-agent personas and custom slash
// commands discovered on disk, and flags personas whose tool allowlist names an
// unknown built-in tool.
func checkSubagentsAndCommands(r *Report, cfg *config.Config) {
	personas := subagents.Load(cfg.WorkingDir)
	if len(personas) == 0 {
		r.add("sub-agents", StatusOK, "no personas configured")
	} else {
		var names []string
		warn := false
		for _, p := range personas {
			label := p.Name
			if bad := unknownTools(p.Tools); len(bad) > 0 {
				warn = true
				label += " (unknown tools: " + strings.Join(bad, ", ") + ")"
			}
			names = append(names, label)
		}
		sort.Strings(names)
		status := StatusOK
		if warn {
			status = StatusWarn
		}
		r.add("sub-agents", status, fmt.Sprintf("%d persona(s): %s", len(personas), strings.Join(names, ", ")))
	}

	names := commandNames(cfg.WorkingDir)
	if len(names) == 0 {
		r.add("custom commands", StatusOK, "none configured")
		return
	}
	sort.Strings(names)
	r.add("custom commands", StatusOK, fmt.Sprintf("%d command(s): %s", len(names), strings.Join(names, ", ")))
}

// knownTools is the set of built-in tool names a persona allowlist may name.
// (MCP/task tools are dynamic and not validated here.)
var knownTools = map[string]bool{
	"read_file": true, "write_file": true, "edit_file": true, "multi_edit": true,
	"list_dir": true, "glob": true, "grep": true, "find_symbol": true,
	"bash": true, "web_fetch": true, "batch_web_fetch": true, "todo_write": true, "remember": true,
	"git_status": true, "git_diff": true, "git_log": true, "git_commit": true, "git_branch": true, "git_worktree": true,
}

// unknownTools returns the entries of an allowlist that aren't recognized
// built-in tools (ignoring dynamic mcp.*/task tools, which can't be checked
// statically).
func unknownTools(tools []string) []string {
	var bad []string
	for _, t := range tools {
		if strings.HasPrefix(t, "mcp.") || t == "task" {
			continue
		}
		if !knownTools[t] {
			bad = append(bad, t)
		}
	}
	return bad
}

// commandNames lists the custom slash commands discovered in the global and
// project command dirs (project overrides global on name collisions). It mirrors
// the loader in internal/tui, which is unexported.
func commandNames(workingDir string) []string {
	seen := map[string]bool{}
	add := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			seen["/"+strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}
	if base, err := config.GlobalConfigDir(); err == nil {
		add(filepath.Join(base, "commands"))
	}
	add(config.ProjectPath(workingDir, "commands"))

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// checkSandbox reports whether the OS-level bash sandbox is on and, when it is,
// whether the platform supports it (macOS sandbox-exec or Linux bwrap present).
// An enabled sandbox that can't run is a warning, because commands fall back to
// running unconfined.
func checkSandbox(r *Report, cfg *config.Config) {
	if config.NormalizeSandboxMode(cfg.SandboxMode) == config.SandboxOff {
		r.add("bash sandbox", StatusOK, "off (bash runs unconfined)")
		return
	}
	net := "network allowed"
	if !cfg.SandboxNetwork {
		net = "network denied"
	}
	var confineTool string
	switch runtime.GOOS {
	case "darwin":
		confineTool = "sandbox-exec"
	case "linux":
		confineTool = "bwrap"
	default:
		r.add("bash sandbox", StatusWarn, "enabled but unsupported on "+runtime.GOOS+" — bash runs unconfined")
		return
	}
	if _, err := exec.LookPath(confineTool); err != nil {
		r.add("bash sandbox", StatusWarn, "enabled but "+confineTool+" not found on PATH — bash runs unconfined")
		return
	}
	r.add("bash sandbox", StatusOK, "on, confined to working dir via "+confineTool+" ("+net+")")
}

// checkNetrc reports whether ~/.netrc Bearer auth for web_fetch/batch_web_fetch
// is active and how many hosts it covers.
func checkNetrc(r *Report) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MN_NETRC"))) {
	case "0", "off", "false", "no":
		r.add("netrc", StatusOK, "disabled via MN_NETRC — web_fetch sends no Bearer auth")
		return
	}
	path := netrc.Path()
	if path == "" {
		r.add("netrc", StatusOK, "no home dir / $NETRC — web_fetch sends no Bearer auth")
		return
	}
	if _, err := os.Stat(path); err != nil {
		r.add("netrc", StatusOK, fmt.Sprintf("no %s — web_fetch sends no Bearer auth (create it to authenticate internal hosts)", path))
		return
	}
	f, err := netrc.Load()
	if err != nil {
		r.add("netrc", StatusWarn, fmt.Sprintf("%s unreadable: %v", path, oneLine(err.Error())))
		return
	}
	r.add("netrc", StatusOK, fmt.Sprintf("%s — %d host(s); web_fetch sends Bearer auth to those hosts", path, len(f)))
}

// oneLine collapses whitespace/newlines so an error fits on a single report row.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Embedder is the minimal embedding capability CheckEmbeddings needs, so the
// doctor doesn't depend on the whole llm.Client.
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// CheckEmbeddings verifies MN_EMBEDDINGS_MODEL by embedding a tiny string end to
// end — the real network call, not just the presence of the setting, which is the
// class of "silently misconfigured" gap that makes semantic_search quietly return
// nothing. Returns "not configured" (OK, not a failure) when unset. Standalone
// and additive: not part of Run(), called separately by the Doctor panel because
// it costs a live request.
func CheckEmbeddings(ctx context.Context, cfg *config.Config, embedder Embedder) Check {
	model := strings.TrimSpace(cfg.EmbeddingsModel)
	if model == "" {
		return Check{Name: "embeddings", Status: StatusOK, Detail: "not configured (MN_EMBEDDINGS_MODEL unset)"}
	}
	if embedder == nil {
		return Check{Name: "embeddings", Status: StatusFail, Detail: "no embedding-capable client available to test with"}
	}
	start := time.Now()
	ectx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	vecs, err := embedder.Embed(ectx, model, []string{"doctor health check"})
	if err != nil {
		return Check{Name: "embeddings", Status: StatusFail, Detail: fmt.Sprintf("%s: %s", model, oneLine(err.Error()))}
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return Check{Name: "embeddings", Status: StatusFail, Detail: fmt.Sprintf("%s: returned an empty vector", model)}
	}
	return Check{Name: "embeddings", Status: StatusOK, Detail: fmt.Sprintf("%s: %d dims in %s", model, len(vecs[0]), time.Since(start).Round(time.Millisecond))}
}
