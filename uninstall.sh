#!/usr/bin/env bash
#
# Uninstall Grove from GitHub Releases install.
#
#   curl -fsSL https://raw.githubusercontent.com/provasign/grove/main/uninstall.sh | bash
#
# Environment variables (all optional):
#   INSTALL_DIR    directory where grove was installed   (default: $HOME/bin)
#
set -euo pipefail

PRODUCT="grove"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m✅\033[0m %s\n' "$*"; }

if [ -f "${INSTALL_DIR}/${PRODUCT}" ]; then
  rm -f "${INSTALL_DIR}/${PRODUCT}"
  ok "removed ${INSTALL_DIR}/${PRODUCT}"
else
  info "${INSTALL_DIR}/${PRODUCT}: not found (already removed?)"
fi

printf '\n%s uninstalled from %s\n' "$PRODUCT" "$INSTALL_DIR"
printf 'PATH note: if this was your only Provasign tool, manually remove the PATH entry from ~/.zshrc or ~/.bashrc.\n'
