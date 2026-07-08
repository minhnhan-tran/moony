#!/usr/bin/env bash
#
# publish-release.sh — cross-compile moony and publish a GitHub release.
#
# Builds static (CGO-free) binaries for the supported platforms, packages each as
# a .tar.gz, writes SHA256SUMS, and creates/updates the GitHub release for the
# version's tag, uploading the assets. install.sh then downloads from that release.
#
# Typical flow:
#   make release VERSION=0.16.0        # bump config.Version + CHANGELOG, commit, tag
#   git push origin main --tags        # publish the commit + tag
#   ./scripts/publish-release.sh       # build + upload binaries for v0.16.0
#
# Usage:
#   ./scripts/publish-release.sh [options]
#
# Options:
#   --version X.Y.Z   Version to publish (default: config.Version). Tag = vX.Y.Z.
#   --platforms LIST  Space-separated os/arch list (default: "darwin/arm64 darwin/amd64 linux/amd64 linux/arm64").
#   --draft           Create the release as a draft.
#   --dry-run         Build the dist/ artifacts only; do not touch GitHub.
#   -h, --help        Show this help and exit.

set -euo pipefail

BINARY="moony"
PLATFORMS="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"
DRAFT=0
DRY_RUN=0
VERSION=""

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"
ROOT="$(cd -- "$SCRIPT_DIR/.." >/dev/null 2>&1 && pwd -P)"
DIST="$ROOT/dist"

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BLUE=$'\033[34m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'; C_RST=$'\033[0m'
else
  C_BLUE=""; C_GREEN=""; C_YELLOW=""; C_RED=""; C_RST=""
fi
info() { printf '%s==>%s %s\n' "$C_BLUE" "$C_RST" "$*"; }
ok()   { printf '%s ✓ %s %s\n' "$C_GREEN" "$C_RST" "$*"; }
warn() { printf '%swarning:%s %s\n' "$C_YELLOW" "$C_RST" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$C_RED" "$C_RST" "$*" >&2; exit 1; }

usage() { sed -n '2,28p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version)    VERSION="${2:-}"; shift 2 ;;
    --version=*)  VERSION="${1#*=}"; shift ;;
    --platforms)  PLATFORMS="${2:-}"; shift 2 ;;
    --platforms=*) PLATFORMS="${1#*=}"; shift ;;
    --draft)      DRAFT=1; shift ;;
    --dry-run)    DRY_RUN=1; shift ;;
    -h|--help)    usage; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

# sha256 helper that works on macOS (shasum) and Linux (sha256sum).
sha256() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$@"; else sha256sum "$@"; fi; }

# ---- resolve version + repo -------------------------------------------------
command -v go >/dev/null 2>&1 || die "Go is not installed or not on PATH."
[ -f "$ROOT/go.mod" ] || die "go.mod not found in $ROOT."

if [ -z "$VERSION" ]; then
  VERSION="$(grep -Eo 'Version[[:space:]]*=[[:space:]]*"[0-9]+\.[0-9]+\.[0-9]+"' "$ROOT/internal/config/config.go" | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
  [ -n "$VERSION" ] || die "could not read config.Version; pass --version X.Y.Z."
fi
TAG="v$VERSION"

# Repo + host from the git remote (falls back to the known coordinates).
REMOTE="$(git -C "$ROOT" remote get-url origin 2>/dev/null || true)"
case "$REMOTE" in
  git@*) slug="${REMOTE#git@}"; HOST="${slug%%:*}"; path="${slug#*:}" ;;
  https://*) slug="${REMOTE#https://}"; HOST="${slug%%/*}"; path="${slug#*/}" ;;
  *) HOST="github.com"; path="pysty1995/moony" ;;
esac
path="${path%.git}"
OWNER="${path%%/*}"; REPO="${path##*/}"
REPO_ARG="$HOST/$OWNER/$REPO"

info "Publishing $BINARY $TAG to $REPO_ARG"
info "Platforms: $PLATFORMS"

# ---- build ------------------------------------------------------------------
rm -rf "$DIST"
mkdir -p "$DIST/stage"

for plat in $PLATFORMS; do
  os="${plat%%/*}"; arch="${plat##*/}"
  [ "$os" != "$plat" ] && [ -n "$arch" ] || die "bad platform '$plat' (want os/arch)."
  info "Building $os/$arch..."
  # GIT_TERMINAL_PROMPT=0: the ,direct GOPROXY fallback can send `go` to
  # `git clone https://github.com/...` for a dep missing from the module proxy;
  # without this it would block forever on an interactive
  # "Username for 'https://github.com':" prompt in non-interactive/CI contexts.
  # Disabling the prompt turns that into a fast, visible error instead of a hang.
  env -u GOROOT \
    GIT_TERMINAL_PROMPT=0 \
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w" -o "$DIST/stage/$BINARY" "$ROOT" \
    || die "build failed for $os/$arch."
  members=( "$BINARY" )
  tar -C "$DIST/stage" -czf "$DIST/${BINARY}_${os}_${arch}.tar.gz" "${members[@]}"
  rm -rf "$DIST/stage/$BINARY"
  ok "Packaged ${BINARY}_${os}_${arch}.tar.gz"
done
rmdir "$DIST/stage" 2>/dev/null || true

( cd "$DIST" && sha256 ./*.tar.gz | sed 's| \./| |' > SHA256SUMS )
ok "Wrote SHA256SUMS"
info "Artifacts in $DIST:"; ( cd "$DIST" && ls -1sh ./*.tar.gz SHA256SUMS )

if [ "$DRY_RUN" -eq 1 ]; then
  ok "Dry run — built $DIST only; not touching GitHub."
  exit 0
fi

# ---- publish ----------------------------------------------------------------
command -v gh >/dev/null 2>&1 || die "gh (GitHub CLI) is required to publish. Install it and 'gh auth login --hostname $HOST'."
gh auth status --hostname "$HOST" >/dev/null 2>&1 || die "Not authenticated to $HOST. Run: gh auth login --hostname $HOST"

# Previous released version: the highest existing release tag below this one.
# Release tags are created on the remote by `gh` (via --target), so local git
# tags can be stale — prefer the GitHub release list, and fall back to local
# tags only if gh can't answer.
PREV_TAG="$(gh release list -R "$REPO_ARG" --json tagName --jq '.[].tagName' 2>/dev/null \
  | grep -E '^v[0-9]' | grep -vx "$TAG" | sort -V | tail -1 || true)"
[ -n "$PREV_TAG" ] || PREV_TAG="$(git -C "$ROOT" tag --list 'v*' --sort=-version:refname 2>/dev/null | grep -vx "$TAG" | head -1 || true)"
PREV_VERSION="${PREV_TAG#v}"
[ -n "$PREV_VERSION" ] && info "Spanning release notes since ${PREV_TAG}" || warn "No previous release found; notes will cover only $TAG."

# Release notes: every CHANGELOG.md section newer than the previous release —
# from the version being published down to (but excluding) PREV_VERSION — so the
# body summarizes everything shipped since the last release, not just one
# version. Falls back to gh's generated notes if nothing matches.
NOTES_FILE="$DIST/.notes.md"
awk -v ver="$VERSION" -v prev="$PREV_VERSION" '
  $0 ~ ("^## \\[" ver "\\]") {flag=1}
  flag && prev != "" && $0 ~ ("^## \\[" prev "\\]") {exit}
  flag {print}
' "$ROOT/CHANGELOG.md" > "$NOTES_FILE" 2>/dev/null || true

ASSETS=( "$DIST"/*.tar.gz "$DIST/SHA256SUMS" )

if gh release view "$TAG" -R "$REPO_ARG" >/dev/null 2>&1; then
  info "Release $TAG exists — refreshing notes + uploading assets (clobbering)..."
  if [ -s "$NOTES_FILE" ]; then
    gh release edit "$TAG" -R "$REPO_ARG" --notes-file "$NOTES_FILE"
  fi
  gh release upload "$TAG" -R "$REPO_ARG" --clobber "${ASSETS[@]}"
else
  info "Creating release $TAG..."
  create_args=( "$TAG" -R "$REPO_ARG" --title "$BINARY $TAG" )
  [ "$DRAFT" -eq 1 ] && create_args+=( --draft )
  if [ -s "$NOTES_FILE" ]; then
    create_args+=( --notes-file "$NOTES_FILE" )
  else
    warn "No CHANGELOG entry for $VERSION; using generated notes."
    create_args+=( --generate-notes )
  fi
  # Tag the current commit if the tag doesn't exist on the remote yet.
  create_args+=( --target "$(git -C "$ROOT" rev-parse --abbrev-ref HEAD)" )
  gh release create "${create_args[@]}" "${ASSETS[@]}"
fi
rm -f "$NOTES_FILE"

ok "Published $TAG"
printf '\n%sUsers can now install with:%s ./install.sh\n' "$C_GREEN" "$C_RST"
