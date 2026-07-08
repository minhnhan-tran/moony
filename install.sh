#!/usr/bin/env bash
#
# install.sh — download a prebuilt moony release and put it on your PATH.
#
# Fetches the binary for your OS/arch from the GitHub release (built and uploaded
# by scripts/publish-release.sh), verifies its checksum, and installs it. No
# authentication needed — releases are public.
#
# Usage:
#   ./install.sh [options]
#
# Options:
#   --version X.Y.Z   Install a specific release (default: the latest release).
#   --bindir DIR      Install the binary into DIR (default: /usr/local/bin if
#                     writable, else ~/.local/bin).
#   --prefix DIR      Install into DIR/bin (alternative to --bindir).
#   --repo HOST/OWNER/REPO   Override the source repo (default: from git remote).
#   --uninstall       Remove an installed moony and exit.
#   --no-modify-path  Don't add the install dir to your shell PATH automatically.
#   -h, --help        Show this help and exit.
#
# Environment overrides: VERSION, BINDIR, PREFIX, REPO, NO_MODIFY_PATH.

set -euo pipefail

BINARY="moony"
DEFAULT_REPO="github.com/pysty1995/moony"

VERSION="${VERSION:-}"
BINDIR="${BINDIR:-}"
PREFIX="${PREFIX:-}"
REPO="${REPO:-}"
DO_UNINSTALL=0
MODIFY_PATH=1; if [ -n "${NO_MODIFY_PATH:-}" ]; then MODIFY_PATH=0; fi

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE=$'\033[34m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'; C_DIM=$'\033[2m'; C_RST=$'\033[0m'
else
  C_BLUE=""; C_GREEN=""; C_YELLOW=""; C_RED=""; C_DIM=""; C_RST=""
fi
info() { printf '%s==>%s %s\n' "$C_BLUE" "$C_RST" "$*"; }
ok()   { printf '%s ✓ %s %s\n' "$C_GREEN" "$C_RST" "$*"; }
warn() { printf '%swarning:%s %s\n' "$C_YELLOW" "$C_RST" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$C_RED" "$C_RST" "$*" >&2; exit 1; }

usage() { sed -n '2,23p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version)   VERSION="${2:-}"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    --bindir)    BINDIR="${2:-}"; shift 2 ;;
    --bindir=*)  BINDIR="${1#*=}"; shift ;;
    --prefix)    PREFIX="${2:-}"; shift 2 ;;
    --prefix=*)  PREFIX="${1#*=}"; shift ;;
    --repo)      REPO="${2:-}"; shift 2 ;;
    --repo=*)    REPO="${1#*=}"; shift ;;
    --uninstall) DO_UNINSTALL=1; shift ;;
    --no-modify-path) MODIFY_PATH=0; shift ;;
    -h|--help)   usage; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

# resolve_bindir: explicit flag/env wins, then a writable /usr/local/bin, else ~/.local/bin.
resolve_bindir() {
  if [ -n "$BINDIR" ]; then printf '%s\n' "$BINDIR"; return; fi
  if [ -n "$PREFIX" ]; then printf '%s\n' "${PREFIX%/}/bin"; return; fi
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then printf '%s\n' /usr/local/bin; return; fi
  printf '%s\n' "$HOME/.local/bin"
}

install_file() {
  local src="$1" dir="$2" dest="$2/$BINARY"
  if mkdir -p "$dir" 2>/dev/null && [ -w "$dir" ]; then
    install -m 0755 "$src" "$dest"
  elif command -v sudo >/dev/null 2>&1; then
    warn "$dir needs elevated permissions; using sudo (you may be prompted)."
    sudo mkdir -p "$dir"; sudo install -m 0755 "$src" "$dest"
  else
    die "cannot write to $dir and sudo is unavailable — re-run with --bindir \"\$HOME/.local/bin\""
  fi
  printf '%s\n' "$dest"
}

sha256() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$@"; else sha256sum "$@"; fi; }

# shell_rc: the startup file to update for the user's login shell.
shell_rc() {
  case "$(basename "${SHELL:-/bin/sh}")" in
    zsh)  printf '%s\n' "${ZDOTDIR:-$HOME}/.zshrc" ;;
    bash) if [ "${os:-}" = "darwin" ]; then printf '%s\n' "$HOME/.bash_profile"; else printf '%s\n' "$HOME/.bashrc"; fi ;;
    fish) printf '%s\n' "$HOME/.config/fish/config.fish" ;;
    *)    printf '%s\n' "$HOME/.profile" ;;
  esac
}

# add_to_path: ensure $1 is on PATH via the user's shell rc (idempotent). Echoes
# the rc path on success; returns non-zero if it couldn't write.
add_to_path() {
  local dir="$1" rc line
  rc="$(shell_rc)"
  if [ "$(basename "${SHELL:-sh}")" = "fish" ]; then line="fish_add_path \"$dir\""; else line="export PATH=\"$dir:\$PATH\""; fi
  if [ -f "$rc" ] && grep -qF "$dir" "$rc" 2>/dev/null; then printf '%s\n' "$rc"; return 0; fi
  mkdir -p "$(dirname "$rc")" 2>/dev/null || true
  printf '\n# Added by moony install.sh\n%s\n' "$line" >> "$rc" 2>/dev/null || return 1
  printf '%s\n' "$rc"
}

# remove_old: this tool was renamed from sqe-code-agent to moony.
# Stop the old background daemon (its own 'daemon uninstall') and delete the stale
# old binary from common install dirs so it doesn't linger on your PATH. Config is
# auto-migrated by the new binary on first run, so it's left alone. Best-effort.
remove_old() {
  local d old
  for d in "${BIN_DEST_DIR:-}" "$HOME/.local/bin" /usr/local/bin "$HOME/bin"; do
    [ -n "$d" ] || continue
    old="$d/sqe-code-agent"
    [ -e "$old" ] || continue
    if [ -x "$old" ]; then "$old" daemon uninstall >/dev/null 2>&1 || true; fi
    if rm -f "$old" 2>/dev/null; then ok "Removed old sqe-code-agent from $d"
    elif command -v sudo >/dev/null 2>&1; then sudo rm -f "$old" 2>/dev/null && ok "Removed old sqe-code-agent from $d (sudo)" || true
    fi
  done
}

# ---- uninstall --------------------------------------------------------------
if [ "$DO_UNINSTALL" -eq 1 ]; then
  dir="$(resolve_bindir)"; dest="$dir/$BINARY"
  if [ ! -e "$dest" ]; then
    found="$(command -v "$BINARY" 2>/dev/null || true)"
    [ -n "$found" ] && dest="$found" || die "$BINARY not found in $dir or on PATH."
  fi
  if [ -w "$(dirname "$dest")" ]; then rm -f "$dest"; else sudo rm -f "$dest"; fi
  ok "Removed $dest"
  remove_old
  exit 0
fi

# ---- resolve repo (prefer git remote when run inside the checkout) ----------
if [ -z "$REPO" ]; then
  remote="$(git remote get-url origin 2>/dev/null || true)"
  case "$remote" in
    git@*)     slug="${remote#git@}"; REPO="${slug%%:*}/${slug#*:}" ;;
    https://*) REPO="${remote#https://}" ;;
    *)         REPO="$DEFAULT_REPO" ;;
  esac
  REPO="${REPO%.git}"
fi
HOST="${REPO%%/*}"

# ---- detect platform --------------------------------------------------------
case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64)  arch="amd64" ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
ASSET="${BINARY}_${os}_${arch}.tar.gz"

# ---- download (plain HTTPS — the repo is public, no auth needed) -----------
command -v curl >/dev/null 2>&1 || die "curl is required to download the release."

API_HOST="${HOST}"
[ "$API_HOST" = "github.com" ] && API_HOST="api.github.com/repos" || API_HOST="${API_HOST}/api/v3/repos"
API_BASE="https://${API_HOST}/${REPO#${HOST}/}"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/sqe-install.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

if [ -n "$VERSION" ]; then
  TAG="v${VERSION#v}"
else
  info "Resolving the latest release of $REPO..."
  TAG="$(curl -fsSL "$API_BASE/releases/latest" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  [ -n "$TAG" ] || die "could not resolve the latest release tag from $API_BASE/releases/latest"
fi

info "Downloading $BINARY $TAG ($os/$arch) from $REPO..."
DL_BASE="https://${HOST}/${REPO#${HOST}/}/releases/download/${TAG}"
curl -fsSL "$DL_BASE/$ASSET" -o "$TMP/$ASSET" \
  || die "could not download $ASSET for $TAG. Check the version, or run scripts/publish-release.sh."
curl -fsSL "$DL_BASE/SHA256SUMS" -o "$TMP/SHA256SUMS" 2>/dev/null || true
[ -f "$TMP/$ASSET" ] || die "downloaded archive $ASSET is missing — no build for $os/$arch in this release?"

# ---- verify checksum --------------------------------------------------------
if [ -f "$TMP/SHA256SUMS" ]; then
  line="$(grep -E " [*]?${ASSET}\$" "$TMP/SHA256SUMS" || true)"
  if [ -n "$line" ]; then
    ( cd "$TMP" && printf '%s\n' "$line" | sha256 -c - >/dev/null 2>&1 ) \
      && ok "Checksum verified" \
      || die "checksum mismatch for $ASSET — refusing to install."
  else
    warn "no SHA256SUMS entry for $ASSET; skipping checksum verification."
  fi
else
  warn "SHA256SUMS not found in the release; skipping checksum verification."
fi

# ---- extract + install ------------------------------------------------------
tar -C "$TMP" -xzf "$TMP/$ASSET" || die "failed to extract $ASSET."
[ -f "$TMP/$BINARY" ] || die "archive did not contain a '$BINARY' binary."
chmod +x "$TMP/$BINARY"

BIN_DEST_DIR="$(resolve_bindir)"
info "Installing to $BIN_DEST_DIR/$BINARY"
INSTALLED="$(install_file "$TMP/$BINARY" "$BIN_DEST_DIR")"
ok "Installed $INSTALLED"
remove_old

# ---- verify + put on PATH ---------------------------------------------------
if VER_OUT="$("$INSTALLED" --version 2>/dev/null)"; then
  ok "Verified: $VER_OUT"
else
  warn "Installed, but '$BINARY --version' didn't run cleanly. Try: $INSTALLED"
fi

case ":$PATH:" in
  *":$BIN_DEST_DIR:"*)
    printf '\n%sRun it with:%s %s\n' "$C_GREEN" "$C_RST" "$BINARY"
    ;;
  *)
    # $BIN_DEST_DIR isn't on PATH — add it to the user's shell startup file
    # automatically (idempotent), unless --no-modify-path / NO_MODIFY_PATH.
    if [ "$MODIFY_PATH" -eq 1 ] && RC_FILE="$(add_to_path "$BIN_DEST_DIR")"; then
      ok "Added $BIN_DEST_DIR to your PATH in $RC_FILE"
      printf 'Restart your terminal, or run %ssource %s%s — then start it with %s%s%s\n' "$C_DIM" "$RC_FILE" "$C_RST" "$C_DIM" "$BINARY" "$C_RST"
    else
      printf '\n%s%s is not on your PATH.%s Add it to your shell profile (e.g. ~/.zshrc):\n' "$C_YELLOW" "$BIN_DEST_DIR" "$C_RST"
      printf '  %sexport PATH="%s:$PATH"%s\n' "$C_DIM" "$BIN_DEST_DIR" "$C_RST"
      printf 'Until then, run it with its full path:\n  %s%s%s\n' "$C_DIM" "$INSTALLED" "$C_RST"
    fi
    ;;
esac
