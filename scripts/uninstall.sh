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

BIN="$HOME/.local/bin"
UNIT="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/omni-server.service"
apps=(omni omni-dev)
if [[ $DEV_ONLY == 1 ]]; then
  apps=(omni-dev)
fi

# prod server runs as a service; dev has none
if [[ $DEV_ONLY == 0 && -f "$UNIT" ]]; then
  systemctl --user disable --now omni-server.service 2>/dev/null || true
  rm -f "$UNIT"
  systemctl --user daemon-reload
  echo "removed service omni-server"
fi

for a in "${apps[@]}"; do
  for f in "$BIN/$a" "$BIN/$a-server"; do
    if [[ -f "$f" ]]; then
      rm -f "$f"
      echo "removed $f"
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
  echo "these hold your bot token and database:"
  printf '  %s\n' "${dirs[@]}"
  if [[ $ASSUME_YES == 1 ]]; then
    reply=y
  else
    read -rp "delete them? [y/N] " reply
  fi
  if [[ "$reply" =~ ^[Yy]$ ]]; then
    rm -rf "${dirs[@]}"
    echo "deleted"
  else
    echo "kept"
  fi
fi
echo "done"
