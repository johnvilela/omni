#!/usr/bin/env bash
# Remove omni from this machine: binaries, systemd service, and (after
# confirmation) config (bot token) and data (database).
#   --dev      remove only the dev install (omni-dev)
#   --yes, -y  don't ask before deleting config and data
set -euo pipefail

DEV_ONLY=0
ASSUME_YES=0
for arg in "$@"; do
  case "$arg" in
    --dev) DEV_ONLY=1 ;;
    --yes | -y) ASSUME_YES=1 ;;
    *)
      echo "usage: uninstall.sh [--dev] [--yes]"
      exit 2
      ;;
  esac
done

command -v gum >/dev/null || { echo "this script needs gum (https://github.com/charmbracelet/gum)"; exit 1; }

gum style --foreground 6 \
  '  ___  __  __ _   _ ___ ' \
  ' / _ \|  \/  | \ | |_ _|' \
  '| | | | |\/| |  \| || | ' \
  '| |_| | |  | | |\  || | ' \
  ' \___/|_|  |_|_| \_|___|'
gum style --faint "uninstaller"

BIN="$HOME/.local/bin"
apps=(omni omni-dev)
if [[ $DEV_ONLY == 1 ]]; then
  apps=(omni-dev)
fi

# both prod and dev run as systemd user services ($app-server.service)
# plus the $app-guardian.timer watchdog
for a in "${apps[@]}"; do
  UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  unit="$UNIT_DIR/$a-server.service"
  if [[ -f "$unit" ]]; then
    systemctl --user disable --now "$a-server.service" 2>/dev/null || true
    rm -f "$unit"
    systemctl --user daemon-reload
    gum log --level info "removed service $a-server"
  fi
  if [[ -f "$UNIT_DIR/$a-guardian.timer" ]]; then
    systemctl --user disable --now "$a-guardian.timer" 2>/dev/null || true
    rm -f "$UNIT_DIR/$a-guardian.timer" "$UNIT_DIR/$a-guardian.service"
    rm -rf "$UNIT_DIR/$a-guardian.timer.d"
    systemctl --user daemon-reload
    gum log --level info "removed timer $a-guardian"
  fi
done

for a in "${apps[@]}"; do
  for f in "$BIN/$a" "$BIN/$a-server" "$BIN/$a-guardian"; do
    if [[ -f "$f" ]]; then
      rm -f "$f"
      gum log --level info "removed $f"
    fi
  done
done

dirs=()
for a in "${apps[@]}"; do
  for d in "${XDG_CONFIG_HOME:-$HOME/.config}/$a" "${XDG_DATA_HOME:-$HOME/.local/share}/$a"; do
    if [[ -d "$d" ]]; then
      dirs+=("$d")
    fi
  done
done

if [[ ${#dirs[@]} -gt 0 ]]; then
  echo
  gum style --bold --foreground 3 "these hold your bot token and database:"
  printf '  %s\n' "${dirs[@]}"
  if [[ $ASSUME_YES == 1 ]] || gum confirm --default=false "delete them?"; then
    rm -rf "${dirs[@]}"
    gum log --level info "deleted"
  else
    echo "kept"
  fi
fi
echo "done"
