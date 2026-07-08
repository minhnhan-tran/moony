package relay

import (
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the public binary-distribution endpoints, enabled by
// SetDownloads(dir):
//
//   - GET /download         a small HTML landing page (curl one-liner + links)
//   - GET /download/{file}  a prebuilt binary (or SHA256SUMS) from the dir
//   - GET /install.sh       a curl|sh installer that picks the right macOS arch
//
// They're meant to be public (see internal/auth: /download, /download/, and
// /install.sh bypass AppleConnect) so `curl -fsSL https://relay/install.sh | sh`
// works without an interactive login. The files are baked into the relay image
// (see the Dockerfile), so the served version always matches this relay.

// baseURL reconstructs the public origin (scheme://host) of req, trusting the
// L7 load balancer's X-Forwarded-Proto (TLS is terminated at the PLB, so the
// relay itself sees plain HTTP behind an https VIP).
func baseURL(req *http.Request) string {
	scheme := "https"
	if xf := req.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	} else if req.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + req.Host
}

// serveDownloadFile streams one file from the downloads dir. The name must be a
// plain filename (no path separators / traversal) that exists in the dir.
func (r *Relay) serveDownloadFile(w http.ResponseWriter, req *http.Request) {
	if r.downloads == "" {
		http.NotFound(w, req)
		return
	}
	name := strings.TrimPrefix(req.URL.Path, "/download/")
	if name == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
		http.NotFound(w, req)
		return
	}
	path := filepath.Join(r.downloads, name)
	// Transition alias for the sqe-code-agent -> moony rename: an OLD
	// (pre-rename) client self-updates by requesting sqe-code-agent_<os>_<arch>,
	// which this relay no longer carries. Serve it the equivalently-named new
	// binary so it can cross the rename in place — it tolerates the missing
	// SHA256SUMS entry (the download is over HTTPS), then runs as the new binary
	// and migrates config + daemon on next start.
	if _, statErr := os.Stat(path); statErr != nil && strings.HasPrefix(name, "sqe-code-agent_") {
		alt := filepath.Join(r.downloads, "moony_"+strings.TrimPrefix(name, "sqe-code-agent_"))
		if _, e := os.Stat(alt); e == nil {
			path = alt
		}
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, req, name, info.ModTime(), f)
}

// serveInstallScript returns a POSIX-sh installer that downloads the right macOS
// binary (by uname arch) from this same relay and drops it on the user's PATH.
func (r *Relay) serveInstallScript(w http.ResponseWriter, req *http.Request) {
	if r.downloads == "" {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	script := strings.ReplaceAll(installScript, "__BASE__", baseURL(req))
	script = strings.ReplaceAll(script, "__TUNNEL__", r.tunnelAddr)
	_, _ = io.WriteString(w, script)
}

// serveDownloadPage renders a minimal landing page: the curl one-liner plus a
// direct link per file in the downloads dir.
func (r *Relay) serveDownloadPage(w http.ResponseWriter, req *http.Request) {
	if r.downloads == "" {
		http.NotFound(w, req)
		return
	}
	var links strings.Builder
	if entries, err := os.ReadDir(r.downloads); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			links.WriteString(`      <li><a href="/download/` + html.EscapeString(n) + `">` + html.EscapeString(prettyAsset(n)) + "</a></li>\n")
		}
	}
	page := downloadPageHTML
	page = strings.ReplaceAll(page, "__VERSION__", html.EscapeString(r.version))
	page = strings.ReplaceAll(page, "__BASE__", html.EscapeString(baseURL(req)))
	page = strings.ReplaceAll(page, "__LINKS__", links.String())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, page)
}

// prettyAsset gives a human label for a known download filename.
func prettyAsset(name string) string {
	switch name {
	case "moony_darwin_arm64":
		return "macOS — Apple Silicon (arm64)"
	case "moony_darwin_amd64":
		return "macOS — Intel (amd64)"
	case "moony_linux_amd64":
		return "Linux — x86_64 (amd64)"
	case "moony_linux_arm64":
		return "Linux — arm64"
	case "SHA256SUMS":
		return "SHA256SUMS (checksums)"
	}
	return name
}

// installScript is the /install.sh body; __BASE__ is replaced with the relay's
// public origin at serve time so the script pulls binaries from the same host.
// It supports macOS and Linux (amd64/arm64), so `curl … | sh` works on a laptop
// or inside a Linux container.
const installScript = `#!/bin/sh
# moony installer — fetches the CLI (TUI + connector) from the relay
# that served this script.  Usage:  curl -fsSL __BASE__/install.sh | sh
set -eu

base="__BASE__"
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Darwin) goos="darwin" ;;
  Linux)  goos="linux" ;;
  *) echo "Unsupported OS: $os (need Darwin or Linux)." >&2; exit 1 ;;
esac
case "$arch" in
  arm64|aarch64) goarch="arm64" ;;
  x86_64|amd64)  goarch="amd64" ;;
  *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
esac
file="moony_${goos}_${goarch}"

dest="${SQE_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dest"
tmp="$(mktemp)"
echo "Downloading moony ($goos/$goarch) from $base ..."
# --progress-bar (not -s) so a piped 'curl … | sh' still shows download progress
# for the ~30 MB binary; -f fails on HTTP errors, -L follows redirects.
curl -fL --progress-bar "$base/download/$file" -o "$tmp"
chmod +x "$tmp"
mv "$tmp" "$dest/moony"
echo "Installed moony -> $dest/moony"

# Clean up a previous install under the OLD name — this tool was renamed from
# sqe-code-agent to moony. Stop the old background daemon (via its own
# 'daemon uninstall') and delete the stale old binary so it doesn't linger on your
# PATH. Config is auto-migrated on first run, so it's left in place. Best-effort:
# never fails the install.
for d in "$dest" "$HOME/.local/bin" /usr/local/bin "$HOME/bin"; do
  old="$d/sqe-code-agent"
  [ -e "$old" ] || continue
  if [ -x "$old" ]; then "$old" daemon uninstall >/dev/null 2>&1 || true; fi
  rm -f "$old" 2>/dev/null && echo "Removed old sqe-code-agent -> $old" || true
done

# Register this machine with the relay as an always-on background connector
# (launchd on macOS, systemd --user on Linux), so it stays available in the web
# UI without anyone launching the TUI, AND save the relay as the interactive
# default so a hand-run 'moony' uses it too (it takes over the connector
# while open; the daemon yields and resumes when you quit). Best-effort — never
# fail the install over it. __TUNNEL__ is the relay's advertised public tunnel;
# when empty the daemon falls back to its built-in default relay.
relay_tunnel="__TUNNEL__"
echo "Registering this machine with the relay (background daemon) ..."
if [ -n "$relay_tunnel" ]; then
  if "$dest/moony" daemon install --relay "$relay_tunnel"; then ok=1; else ok=0; fi
else
  if "$dest/moony" daemon install; then ok=1; else ok=0; fi
fi
if [ "$ok" = 1 ]; then
  echo "Done. This machine will stay available in the web UI; manage it with 'moony daemon status|stop|uninstall'."
else
  echo "Note: couldn't install the background daemon automatically — run 'moony daemon install' later." >&2
fi

case ":$PATH:" in
  *":$dest:"*)
    echo "Done. Run 'moony' to start."
    ;;
  *)
    # $dest isn't on PATH yet — add it to the user's shell startup file
    # automatically (idempotent). Set SQE_NO_MODIFY_PATH=1 to skip and just print
    # the hint instead.
    if [ -n "${SQE_NO_MODIFY_PATH:-}" ]; then
      echo "Note: $dest is not on your PATH; add it, e.g.  export PATH=\"$dest:\$PATH\""
      echo "Done. Run it with: $dest/moony"
    else
      case "$(basename "${SHELL:-/bin/sh}")" in
        zsh)  rc="${ZDOTDIR:-$HOME}/.zshrc";              addline="export PATH=\"$dest:\$PATH\"" ;;
        bash) if [ "$goos" = "darwin" ]; then rc="$HOME/.bash_profile"; else rc="$HOME/.bashrc"; fi; addline="export PATH=\"$dest:\$PATH\"" ;;
        fish) rc="$HOME/.config/fish/config.fish";        addline="fish_add_path \"$dest\"" ;;
        *)    rc="$HOME/.profile";                        addline="export PATH=\"$dest:\$PATH\"" ;;
      esac
      added=0
      if [ -f "$rc" ] && grep -qF "$dest" "$rc" 2>/dev/null; then
        added=1  # already referenced (e.g. an earlier run) — just needs a reload
      elif mkdir -p "$(dirname "$rc")" 2>/dev/null && printf '\n# Added by moony installer\n%s\n' "$addline" >> "$rc" 2>/dev/null; then
        added=1
        echo "Added $dest to your PATH in $rc"
      fi
      if [ "$added" -eq 1 ]; then
        echo "Done. Restart your terminal (or run: . \"$rc\"), then run 'moony'."
      else
        echo "Note: couldn't update a shell profile automatically; add it manually:  export PATH=\"$dest:\$PATH\""
        echo "Done. Run it with: $dest/moony"
      fi
    fi
    ;;
esac
`

// downloadPageHTML is a standalone (non-SPA) page; the __TOKENS__ are filled in
// at serve time. Self-contained styling (system fonts only — no web-font CDN,
// which the Apple network blocks); color-scheme makes it adapt to light/dark.
const downloadPageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Download moony</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; max-width: 42rem; margin: 3rem auto; padding: 0 1.25rem; line-height: 1.5; }
  h1 { font-size: 1.4rem; margin-bottom: .25rem; }
  .ver { color: #8a8a8a; font-weight: 400; font-size: 1rem; }
  pre { background: rgba(127,127,127,.16); padding: .8rem 1rem; border-radius: .5rem; overflow-x: auto; }
  code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .cmd { position: relative; }
  .cmd pre { margin: 0; padding-right: 5rem; }
  .copy { position: absolute; top: .5rem; right: .5rem; font: inherit; font-size: .8rem; line-height: 1; padding: .35rem .6rem; border-radius: .4rem; border: 1px solid rgba(127,127,127,.45); background: rgba(127,127,127,.16); color: inherit; cursor: pointer; }
  .copy:hover { background: rgba(127,127,127,.3); }
  .copy.copied { color: #1a7f37; border-color: #1a7f37; }
  ul { padding-left: 1.1rem; }
  li { margin: .35rem 0; }
  a { color: #2f6fdd; }
  .muted { color: #8a8a8a; font-size: .9rem; }
</style>
</head>
<body>
  <h1>moony <span class="ver">v__VERSION__</span></h1>
  <p>Install the macOS terminal app (TUI + connector) with one command:</p>
  <div class="cmd">
    <pre><code id="install-cmd">curl -fsSL __BASE__/install.sh | sh</code></pre>
    <button type="button" id="copy-btn" class="copy" aria-label="Copy command to clipboard">Copy</button>
  </div>

  <p>Or download a binary directly:</p>
  <ul>
__LINKS__  </ul>
  <p class="muted">After installing, run <code>moony</code>. Verify downloads against <code>SHA256SUMS</code>.</p>
<script>
  (function () {
    var btn = document.getElementById('copy-btn'), code = document.getElementById('install-cmd');
    if (!btn || !code) return;
    function flash() { btn.textContent = 'Copied!'; btn.classList.add('copied'); setTimeout(function () { btn.textContent = 'Copy'; btn.classList.remove('copied'); }, 1600); }
    function fallback() {
      try {
        var rng = document.createRange(); rng.selectNodeContents(code);
        var sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(rng);
        document.execCommand('copy'); sel.removeAllRanges(); flash();
      } catch (e) {}
    }
    btn.addEventListener('click', function () {
      var text = code.textContent;
      if (navigator.clipboard && navigator.clipboard.writeText) { navigator.clipboard.writeText(text).then(flash, fallback); }
      else { fallback(); }
    });
  })();
</script>
</body>
</html>
`
