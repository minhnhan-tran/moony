#!/usr/bin/env bash
#
# ship.sh — one-command [rio: build] release.
#
# WHY: shipping via the agent is ~20 sequential model calls (stage → sign →
# commit → push → publish → verify), and each call re-sends the whole session
# context — which is how a single "ship it" can burn millions of input tokens.
# This script does the entire mechanical flow in ONE run, so the agent invokes it
# with a single tool call instead of orchestrating 20.
#
# It performs the exact repo ship flow (see CLAUDE.md → "Rio builds & releases"):
#   preflight (on main + version/CHANGELOG in sync)
#   → ac-sign ON
#   → SIGNED "<msg> [rio: build]" commit  (complete the AppleConnect biometry when prompted)
#   → verify the commit is actually signed
#   → push to main  (triggers the Rio pipeline)
#   → ac-sign OFF   (always restored, even on failure)
#   → make publish  (tarballs + GitHub release, notes spanning the last published tag)
#
# SAFETY: it commits ONLY what you have already staged, so unrelated dirty files
# are never swept in. Stage your files first, then run this. It aborts before
# touching anything if you're not on main or config.Version != the top CHANGELOG
# entry.
#
# Usage:
#   git add <your files>
#   scripts/ship.sh -m "summary of the change"        # commit the staged files as a [rio: build] release
#   scripts/ship.sh -m "trigger v4.2.12" --empty      # empty trigger commit (code already committed at this version)
#   scripts/ship.sh -m "..." --version 4.2.12         # pin the version passed to make publish
#
set -euo pipefail

MSG=""
EMPTY=0
VERSION=""
while [ $# -gt 0 ]; do
  case "$1" in
    -m|--message) MSG="${2:-}"; shift 2 ;;
    --empty)      EMPTY=1; shift ;;
    --version)    VERSION="${2:-}"; shift 2 ;;
    --version=*)  VERSION="${1#*=}"; shift ;;
    -h|--help)    sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "ship.sh: unknown option: $1 (try --help)" >&2; exit 2 ;;
  esac
done

die() { echo "ship.sh: $*" >&2; exit 1; }

[ -n "$MSG" ] || die "a commit message is required (-m \"...\"); [rio: build] is appended for you"

ROOT="$(git rev-parse --show-toplevel)" || die "not a git repo"
cd "$ROOT"

# ---- preflight (before touching anything) ----------------------------------
branch="$(git rev-parse --abbrev-ref HEAD)"
[ "$branch" = "main" ] || die "on branch '$branch', not main — ship from main"

ver="$(grep -Eo 'Version = "[0-9]+\.[0-9]+\.[0-9]+"' internal/config/config.go | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
clog="$(grep -Eo '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
[ -n "$ver" ] || die "could not read config.Version"
[ "$ver" = "$clog" ] || die "config.Version ($ver) != top CHANGELOG entry ($clog) — bump both first (make release), then ship"

if [ "$EMPTY" -eq 0 ] && git diff --cached --quiet; then
  die "nothing staged — 'git add' your files first, or pass --empty for a trigger-only commit"
fi

case "$MSG" in
  *"[rio: build]"*) FULL_MSG="$MSG" ;;   # already contains the marker
  *) FULL_MSG="$MSG [rio: build]" ;;
esac

echo "==> Shipping v$ver from main"
echo "    commit: $FULL_MSG"
[ "$EMPTY" -eq 1 ] && echo "    (empty trigger commit)" || echo "    files: $(git diff --cached --name-only | tr '\n' ' ')"

# ---- sign + commit + push --------------------------------------------------
# Always restore ac-sign to OFF on exit (success, failure, or Ctrl-C).
trap './scripts/toggle-ac-sign.sh off >/dev/null 2>&1 || true' EXIT

echo "==> Enabling ac-sign…"
./scripts/toggle-ac-sign.sh on

echo "==> Committing (complete the AppleConnect biometry prompt when it appears)…"
if [ "$EMPTY" -eq 1 ]; then
  git commit --allow-empty -m "$FULL_MSG"
else
  git commit -m "$FULL_MSG"
fi

# Verify the commit is actually signed before pushing (don't ship an unsigned
# [rio: build] commit if the biometry was cancelled).
sig="$(git log -1 --format='%G?')"
case "$sig" in
  G|U) echo "==> Commit is signed (%G? = $sig)." ;;
  *)   die "commit is NOT signed (%G? = '$sig') — biometry likely cancelled; nothing pushed. Undo with: git reset --soft HEAD~1" ;;
esac

echo "==> Pushing to main (triggers the Rio pipeline)…"
git push origin main

echo "==> Disabling ac-sign…"
./scripts/toggle-ac-sign.sh off
trap - EXIT

# ---- publish ---------------------------------------------------------------
echo "==> make publish (build tarballs + GitHub release, spanning notes)…"
if [ -n "$VERSION" ]; then
  make publish VERSION="$VERSION"
else
  make publish
fi

echo "✓ Shipped v$ver."
