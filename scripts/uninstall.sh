#!/usr/bin/env bash
# Remove omni from this machine: binaries, systemd units, and (after
# confirmation) config (bot token) and data (database). Needs only bash, so
# it also works on a machine installed by the one-line installer:
#   curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/uninstall.sh | bash
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

if [ -t 1 ]; then
  C_CYAN=$'\033[36m' C_BOLD=$'\033[1m' C_DIM=$'\033[2m' C_YELLOW=$'\033[33m' C_OFF=$'\033[0m'
else
  C_CYAN='' C_BOLD='' C_DIM='' C_YELLOW='' C_OFF=''
fi
info() { printf '    %s\n' "$*"; }

printf '%s' "$C_CYAN"
printf '%s\n' \
  '  ___  __  __ _   _ ___ ' \
  ' / _ \|  \/  | \ | |_ _|' \
  '| | | | |\/| |  \| || | ' \
  '| |_| | |  | | |\  || | ' \
  ' \___/|_|  |_|_| \_|___|'
printf '%s%suninstaller%s\n' "$C_OFF" "$C_DIM" "$C_OFF"

BIN="$HOME/.local/bin"
apps=(omni omni-dev)
if [[ $DEV_ONLY == 1 ]]; then
  apps=(omni-dev)
fi

# both prod and dev run as systemd user services ($app-server.service)
# plus the $app-guardian.timer watchdog
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
for a in "${apps[@]}"; do
  unit="$UNIT_DIR/$a-server.service"
  if [[ -f "$unit" ]]; then
    systemctl --user disable --now "$a-server.service" 2>/dev/null || true
    rm -f "$unit"
    systemctl --user daemon-reload 2>/dev/null || true
    info "removed service $a-server"
  fi
  if [[ -f "$UNIT_DIR/$a-guardian.timer" ]]; then
    systemctl --user disable --now "$a-guardian.timer" 2>/dev/null || true
    rm -f "$UNIT_DIR/$a-guardian.timer" "$UNIT_DIR/$a-guardian.service"
    rm -rf "$UNIT_DIR/$a-guardian.timer.d"
    systemctl --user daemon-reload 2>/dev/null || true
    info "removed timer $a-guardian"
  fi
done

for a in "${apps[@]}"; do
  for f in "$BIN/$a" "$BIN/$a-server" "$BIN/$a-guardian"; do
    if [[ -f "$f" ]]; then
      rm -f "$f"
      info "removed $f"
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
  printf '%s%sthese hold your bot token and database:%s\n' "$C_BOLD" "$C_YELLOW" "$C_OFF"
  printf '  %s\n' "${dirs[@]}"
  delete=0
  if [[ $ASSUME_YES == 1 ]]; then
    delete=1
  elif ( : </dev/tty ) 2>/dev/null; then
    # read from the terminal, not stdin: stdin is the script when piped to bash
    read -r -p "delete them? [y/N] " answer </dev/tty || answer=""
    [[ $answer == [yY]* ]] && delete=1
  else
    echo "no terminal to confirm on — kept (pass --yes to delete)"
  fi
  if [[ $delete == 1 ]]; then
    rm -rf "${dirs[@]}"
    info "deleted"
  else
    echo "kept"
  fi
fi
echo "done"
