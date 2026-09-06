#!/usr/bin/env bash
# Uninstalls lazymesh: removes the binary install.sh placed. Leaves
# ~/.config/lazymesh alone by default -- it holds the persisted mesh
# identity (real puzzle-grinding work to generate), the isolated
# contact_policy.json (past "Answer + Trust" ring decisions), config.yaml,
# and the local-tools workspace, if enabled. Pass --purge to remove all of
# that too.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/macula-io/macula-lazymesh/main/uninstall.sh | bash
#   curl -fsSL .../uninstall.sh | bash -s -- --purge
#
# Env overrides:
#   LAZYMESH_INSTALL_DIR  where to look for the binary (default: $HOME/.local/bin)
set -euo pipefail

INSTALL_DIR="${LAZYMESH_INSTALL_DIR:-$HOME/.local/bin}"
BIN_PATH="${INSTALL_DIR}/lazymesh"

# lazymesh always resolves its config/identity directory as
# $HOME/.config/lazymesh via os.UserHomeDir() + a literal ".config/lazymesh"
# join -- unlike macula-cli it does NOT branch on os.UserConfigDir(), so
# this is the same path on Linux and macOS alike (see internal/config/
# config.go's Default()/DefaultPath()). Windows equivalent is handled by
# uninstall.ps1.
CONFIG_DIR="${HOME}/.config/lazymesh"

log() { printf '%s\n' "$*" >&2; }

purge=0
for arg in "$@"; do
  case "$arg" in
    --purge) purge=1 ;;
    *) log "uninstall.sh: unknown argument: $arg"; exit 2 ;;
  esac
done

if [ -e "$BIN_PATH" ]; then
  rm -f "$BIN_PATH"
  log "removed ${BIN_PATH}"
else
  log "no binary found at ${BIN_PATH} (already removed, or installed elsewhere -- set LAZYMESH_INSTALL_DIR)"
fi

if [ "$purge" -eq 1 ]; then
  if [ -e "$CONFIG_DIR" ]; then
    rm -rf "$CONFIG_DIR"
    log "removed ${CONFIG_DIR} (--purge: identity, contact policy, config, and workspace all deleted too)"
  else
    log "no config directory found at ${CONFIG_DIR}"
  fi
else
  if [ -e "$CONFIG_DIR" ]; then
    log "left ${CONFIG_DIR} in place (identity, contact policy, config, workspace) — pass --purge to remove it too"
  fi
fi
