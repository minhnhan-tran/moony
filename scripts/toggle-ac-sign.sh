#!/usr/bin/env bash
#
# toggle-ac-sign.sh — turn AppleConnect (ac-sign) commit signing on or off.
#
# This repo signs commits with Apple's ac-sign S/MIME signer, wired through git's
# x509 signing backend:
#
#   commit.gpgsign    = true                     # sign every commit
#   gpg.format        = x509                      # use the S/MIME (x509) backend
#   gpg.x509.program  = /usr/local/bin/ac-sign    # the AppleConnect signer
#
# Signing is the `commit.gpgsign` flag; this script flips it (and reports state).
# By default it operates on THIS repository's local config; pass --global to flip
# your user-wide default instead.
#
# Usage:
#   ./scripts/toggle-ac-sign.sh [on|off|toggle|status] [--global]
#
# Commands:
#   toggle   Flip the current state (default when no command is given).
#   on       Enable ac-sign commit signing.
#   off      Disable commit signing.
#   status   Print the current state and exit.
#
# Options:
#   --global   Operate on global (~/.gitconfig) config instead of this repo.
#   -h|--help  Show this help and exit.

set -euo pipefail

CMD="toggle"
SCOPE="--local"
SCOPE_LABEL="repository"

usage() { sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//; /^set -euo/d'; }

for arg in "$@"; do
  case "$arg" in
    on|off|toggle|status) CMD="$arg" ;;
    --global)             SCOPE="--global"; SCOPE_LABEL="global (~/.gitconfig)" ;;
    --local)              SCOPE="--local";  SCOPE_LABEL="repository" ;;
    -h|--help)            usage; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; usage >&2; exit 2 ;;
  esac
done

# Local scope requires being inside a work tree.
if [[ "$SCOPE" == "--local" ]] && ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "error: not inside a git repository (use --global to set the user-wide default)" >&2
  exit 1
fi

# current state of commit.gpgsign in the chosen scope ("true"/"false"/"")
current() { git config "$SCOPE" --get commit.gpgsign 2>/dev/null || true; }

# is_on normalizes the value to a 0/1 exit status (git treats true/1/yes/on as set).
is_on() {
  case "$(echo "${1:-}" | tr '[:upper:]' '[:lower:]')" in
    true|1|yes|on) return 0 ;;
    *)             return 1 ;;
  esac
}

print_status() {
  local val program fmt
  val="$(current)"
  program="$(git config --get gpg.x509.program 2>/dev/null || echo '(unset)')"
  fmt="$(git config --get gpg.format 2>/dev/null || echo '(unset)')"
  if is_on "$val"; then
    echo "ac-sign commit signing: ON  [$SCOPE_LABEL]"
  else
    echo "ac-sign commit signing: OFF [$SCOPE_LABEL]  (commit.gpgsign=${val:-unset})"
  fi
  echo "  gpg.format       = $fmt"
  echo "  gpg.x509.program = $program"
}

# warn_if_misconfigured nudges the user when enabling signing but the x509/ac-sign
# backend isn't wired up — otherwise `git commit` would fail with a signing error.
warn_if_misconfigured() {
  local fmt program
  fmt="$(git config --get gpg.format 2>/dev/null || true)"
  program="$(git config --get gpg.x509.program 2>/dev/null || true)"
  if [[ "$fmt" != "x509" ]]; then
    echo "warning: gpg.format is '${fmt:-unset}', not 'x509' — ac-sign won't be used." >&2
    echo "         fix: git config $SCOPE gpg.format x509" >&2
  fi
  if [[ -z "$program" ]]; then
    echo "warning: gpg.x509.program is unset — set it to your ac-sign path, e.g.:" >&2
    echo "         git config $SCOPE gpg.x509.program /usr/local/bin/ac-sign" >&2
  elif [[ ! -x "$program" ]]; then
    echo "warning: signer '$program' not found or not executable — commits will fail to sign." >&2
  fi
}

enable()  { git config "$SCOPE" commit.gpgsign true;  warn_if_misconfigured; echo "Enabled ac-sign commit signing [$SCOPE_LABEL]."; }
disable() { git config "$SCOPE" commit.gpgsign false; echo "Disabled commit signing [$SCOPE_LABEL]."; }

case "$CMD" in
  status) print_status ;;
  on)     enable ;;
  off)    disable ;;
  toggle)
    if is_on "$(current)"; then disable; else enable; fi
    ;;
esac
