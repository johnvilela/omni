#!/usr/bin/env bash
# Remove omni from this machine: binaries, systemd units, and (after
# confirmation) config (bot token) and data (database). Needs only bash, so
# it also works on a machine installed by the one-line installer:
#   curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/uninstall.sh | bash
#   --dev      remove only the dev container (omni-dev, scripts/dev.sh)
#   --yes, -y  don't ask before deleting config and data
# The dev stack is docker: the omni-dev container and omni:dev image go, the
# omni-dev-home volume (token, database) only after confirmation, plus the
# ~/.local/bin/omni-dev wrapper and, when run from the repo, .env.dev and the
# generated override. The prod docker install is not touched: see
# scripts/install-docker.sh (docker compose down -v in its directory).
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
HAVE_DOCKER=0
if command -v docker >/dev/null && docker info >/dev/null 2>&1; then HAVE_DOCKER=1; fi

# --- native prod install: systemd user units + binaries ---------------------------
if [[ $DEV_ONLY == 0 ]]; then
  UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  if [[ -f "$UNIT_DIR/omni-server.service" ]]; then
    systemctl --user disable --now omni-server.service 2>/dev/null || true
    rm -f "$UNIT_DIR/omni-server.service"
    systemctl --user daemon-reload 2>/dev/null || true
    info "removed service omni-server"
  fi
  if [[ -f "$UNIT_DIR/omni-guardian.timer" ]]; then
    systemctl --user disable --now omni-guardian.timer 2>/dev/null || true
    rm -f "$UNIT_DIR/omni-guardian.timer" "$UNIT_DIR/omni-guardian.service"
    rm -rf "$UNIT_DIR/omni-guardian.timer.d"
    systemctl --user daemon-reload 2>/dev/null || true
    info "removed timer omni-guardian"
  fi
  for f in "$BIN/omni" "$BIN/omni-server" "$BIN/omni-guardian"; do
    if [[ -f "$f" ]]; then
      rm -f "$f"
      info "removed $f"
    fi
  done
fi

# --- dev container (scripts/dev.sh) ------------------------------------------------
DEV_VOLUME=
if [[ $HAVE_DOCKER == 1 ]]; then
  if [[ -n "$(docker ps -aq -f 'name=^omni-dev$')" ]]; then
    docker rm -f omni-dev >/dev/null
    info "removed container omni-dev"
  fi
  if docker image inspect omni:dev >/dev/null 2>&1; then
    docker image rm omni:dev >/dev/null
    info "removed image omni:dev"
  fi
  if docker volume inspect omni-dev_omni-dev-home >/dev/null 2>&1; then
    DEV_VOLUME=omni-dev_omni-dev-home
  fi
fi
if [[ -f "$BIN/omni-dev" ]] && grep -qF '# omni-dev (docker wrapper)' "$BIN/omni-dev"; then
  rm -f "$BIN/omni-dev"
  info "removed $BIN/omni-dev"
fi
REPO="$(cd "$(dirname "$0")/.." 2>/dev/null && pwd || true)"
if [[ -n $REPO && -f "$REPO/docker-compose.dev.yml" ]]; then
  rm -f "$REPO/docker-compose.dev.override.yml"
fi

# --- data: prod config/data dirs and the dev volume, after confirmation ----------------
dirs=()
if [[ $DEV_ONLY == 0 ]]; then
  for d in "${XDG_CONFIG_HOME:-$HOME/.config}/omni" "${XDG_DATA_HOME:-$HOME/.local/share}/omni"; do
    [[ -d "$d" ]] && dirs+=("$d")
  done
fi
targets=("${dirs[@]}")
[[ -n $DEV_VOLUME ]] && targets+=("docker volume $DEV_VOLUME")
[[ -n $REPO && -f "$REPO/.env.dev" ]] && targets+=("$REPO/.env.dev")

if [[ ${#targets[@]} -gt 0 ]]; then
  echo
  printf '%s%sthese hold your bot token and database:%s\n' "$C_BOLD" "$C_YELLOW" "$C_OFF"
  printf '  %s\n' "${targets[@]}"
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
    [[ ${#dirs[@]} -gt 0 ]] && rm -rf "${dirs[@]}"
    [[ -n $DEV_VOLUME ]] && docker volume rm "$DEV_VOLUME" >/dev/null
    [[ -n $REPO && -f "$REPO/.env.dev" ]] && rm -f "$REPO/.env.dev"
    info "deleted"
  else
    echo "kept"
  fi
fi
echo "done"
