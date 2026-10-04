#!/usr/bin/env bash
# Install omni as a docker container — one container holds the server, the
# guardian, ai-memory and the agent browser stack; everything you own lives in
# one volume. Needs docker with the compose plugin, nothing else:
#
#   curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/install-docker.sh | bash
#
# The claude/codex/gemini clis already on this PC are reused: a standalone
# binary is bind-mounted read-only into the container, your ~/.claude, ~/.codex
# and ~/.gemini logins are shared, and only a cli this PC lacks (or has as a
# node script) is installed inside the container's volume.
# Safe to re-run: it refreshes docker-compose.yml and the override, pulls the
# newest image and restarts — a re-run is an upgrade (also after upgrading a
# cli on this PC, so the mount follows the new version). .env is never
# overwritten; only missing keys are added. Knobs (env vars):
#   OMNI_DIR            where the compose files live (default: ~/omni-docker)
#   OMNI_VERSION        image tag to run (default: latest)
#   TELEGRAM_BOT_TOKEN  bot token (default: prompt, or leave it for later)
#   OMNI_NO_HOST_CLIS   set to 1 to install every cli inside the container
#   OMNI_RAW, OMNI_BIN  dev: where to fetch the compose files from / put the wrapper
set -euo pipefail

REPO=johnvilela/omni
RAW="${OMNI_RAW:-https://raw.githubusercontent.com/$REPO/master}"
DIR="${OMNI_DIR:-$HOME/omni-docker}"
BIN="${OMNI_BIN:-$HOME/.local/bin}"

# --- output helpers (colors only on a terminal) --------------------------------
if [ -t 1 ]; then
  C_CYAN=$'\033[36m' C_BOLD=$'\033[1m' C_DIM=$'\033[2m' C_YELLOW=$'\033[33m' C_RED=$'\033[31m' C_OFF=$'\033[0m'
else
  C_CYAN='' C_BOLD='' C_DIM='' C_YELLOW='' C_RED='' C_OFF=''
fi
step() { printf '%s%s==> %s%s\n' "$C_BOLD" "$C_CYAN" "$*" "$C_OFF"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '%sWARN%s %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
die() { printf '%sERROR%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; exit 1; }

printf '%s' "$C_CYAN"
printf '%s\n' \
  '  ___  __  __ _   _ ___ ' \
  ' / _ \|  \/  | \ | |_ _|' \
  '| | | | |\/| |  \| || | ' \
  '| |_| | |  | | |\  || | ' \
  ' \___/|_|  |_|_| \_|___|'
printf '%s%sdocker installer%s\n' "$C_OFF" "$C_DIM" "$C_OFF"

# --- preflight ----------------------------------------------------------------
[ "$(uname -s)" = Linux ] || die "omni runs on linux only (got $(uname -s))"
command -v docker >/dev/null || die "docker not found — install Docker Engine: https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is missing: https://docs.docker.com/compose/install/linux/"
docker info >/dev/null 2>&1 || die "cannot talk to the docker daemon — is it running, and are you in the docker group? (sudo usermod -aG docker $USER, then log out and back in)"

# fetch URL DEST: curl or wget, whichever this machine has
if command -v curl >/dev/null; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "need curl or wget to download the compose files"
fi

# --- compose files --------------------------------------------------------------
step "compose files in $DIR"
mkdir -p "$DIR"
fetch "$RAW/docker-compose.yml" "$DIR/docker-compose.yml" || die "download failed: $RAW/docker-compose.yml"
fetch "$RAW/scripts/omni-docker-backup.sh" "$DIR/omni-docker-backup.sh" && chmod 755 "$DIR/omni-docker-backup.sh" \
  || warn "could not download omni-docker-backup.sh (backup/restore helper)"
if [ ! -f "$DIR/.env" ]; then
  fetch "$RAW/.env.example" "$DIR/.env" || die "download failed: $RAW/.env.example"
  chmod 600 "$DIR/.env"
  info ".env created"
else
  info ".env kept (only missing keys are added)"
fi

# setenv KEY VALUE: set a live or commented key in .env, or append it
setenv() {
  if grep -qE "^#? *$1=" "$DIR/.env"; then
    sed -i -E "s|^#? *$1=.*|$1=$2|" "$DIR/.env"
  else
    printf '%s=%s\n' "$1" "$2" >> "$DIR/.env"
  fi
}
# getenv KEY: the live value, '' when unset or commented out
getenv() { sed -n -E "s|^$1=(.*)$|\1|p" "$DIR/.env" | tail -n1; }

# --- host clis: share what this PC has, install the rest inside -----------------
step "vendor clis"
MOUNTS=()
INSTALL=()
SHARED=()
# detect_cli NAME: a standalone ELF binary is mounted read-only into the
# container; a script (npm/node shim) cannot travel alone, so the container
# installs its own copy into the volume instead
detect_cli() {
  local name=$1 p r
  if [ "${OMNI_NO_HOST_CLIS:-}" = 1 ]; then
    INSTALL+=("$name"); return
  fi
  if ! p=$(command -v "$name" 2>/dev/null); then
    INSTALL+=("$name"); info "$name: not on this PC — installed inside the container"; return
  fi
  r=$(readlink -f "$p")
  if [ "$(head -c4 "$r" | LC_ALL=C tr -d '\177')" = ELF ]; then
    MOUNTS+=("$r:/opt/host/$name/$name:ro"); SHARED+=("$name")
    info "$name: sharing the host binary ($r)"
  else
    INSTALL+=("$name"); info "$name: a node script on this PC — installed inside the container instead"
  fi
}
for c in claude codex gemini; do detect_cli "$c"; done
if [ "${OMNI_NO_HOST_CLIS:-}" = 1 ]; then
  info "OMNI_NO_HOST_CLIS=1: every cli is installed inside the container"
else
  for d in .claude .codex .gemini; do
    if [ -d "$HOME/$d" ]; then
      MOUNTS+=("$HOME/$d:/home/omni/$d"); info "sharing ~/$d (login, settings)"
    fi
  done
  if [ -f "$HOME/.claude.json" ]; then
    MOUNTS+=("$HOME/.claude.json:/opt/host/claude/claude.json:ro")
  fi
fi
setenv OMNI_VENDOR_INSTALL "$(IFS=,; echo "${INSTALL[*]-}")"

# --- container user: the volume is owned by uid 1000, other uids get a bind home -
UID_=$(id -u) GID_=$(id -g)
setenv OMNI_UID "$UID_"
setenv OMNI_GID "$GID_"
if [ "$UID_" != 1000 ]; then
  mkdir -p "$DIR/home"
  MOUNTS+=("$DIR/home:/home/omni")
  info "uid $UID_: the container home is $DIR/home (the named volume belongs to uid 1000)"
fi

# the override carries everything specific to this PC; docker-compose.yml stays
# the generic file refreshed from the repo
OVERRIDE="$DIR/docker-compose.override.yml"
{
  echo "# generated by scripts/install-docker.sh — re-run it after upgrading a cli on this PC"
  if [ ${#MOUNTS[@]} -eq 0 ]; then
    printf 'services:\n  omni: {}\n'
  else
    printf 'services:\n  omni:\n    volumes:\n'
    for m in "${MOUNTS[@]}"; do printf '      - %s\n' "$m"; done
  fi
} > "$OVERRIDE"

# --- settings -------------------------------------------------------------------
step "settings"
if [ -n "${TELEGRAM_BOT_TOKEN:-}" ]; then
  setenv TELEGRAM_BOT_TOKEN "$TELEGRAM_BOT_TOKEN"
  info "telegram token from TELEGRAM_BOT_TOKEN"
elif [ -n "$(getenv TELEGRAM_BOT_TOKEN)" ]; then
  info "telegram token already in .env"
elif { : </dev/tty; } 2>/dev/null; then
  printf '    Telegram bot token from @BotFather (enter to skip): '
  IFS= read -r token </dev/tty || token=
  if [ -n "$token" ]; then
    setenv TELEGRAM_BOT_TOKEN "$token"
  else
    info "skipped — later: omni channels connect -c telegram"
  fi
else
  info "no terminal for the token prompt — later: omni channels connect -c telegram"
fi
if [ -z "$(getenv TZ)" ] || [ "$(getenv TZ)" = UTC ]; then
  tz=$(timedatectl show -p Timezone --value 2>/dev/null || cat /etc/timezone 2>/dev/null || true)
  if [ -n "$tz" ]; then setenv TZ "$tz"; info "timezone $tz (crons run in it)"; fi
fi
if [ -n "${OMNI_VERSION:-}" ]; then setenv OMNI_VERSION "$OMNI_VERSION"; fi
PORT=$(getenv OMNI_PORT); PORT=${PORT:-8787}
# a native omni-server on 8787 would make compose fail; our own container is fine
if [ -z "$(getenv OMNI_PORT)" ] && [ -z "$(docker ps -q -f name='^omni$')" ] \
   && command -v ss >/dev/null && ss -ltn 2>/dev/null | grep -q ":8787 "; then
  die "port 8787 is already in use on this PC (a native omni-server?) — set OMNI_PORT=8788 in $DIR/.env and re-run"
fi

# --- up -----------------------------------------------------------------------
step "starting the container"
# pull first so a re-run upgrades; offline (or a local image tag) still starts
(cd "$DIR" && docker compose pull) || warn "image pull failed — starting the image already on this machine, if any"
(cd "$DIR" && docker compose up -d)
st=starting
for _ in $(seq 1 60); do
  st=$(docker inspect -f '{{.State.Health.Status}}' omni 2>/dev/null || echo starting)
  [ "$st" = healthy ] && break
  sleep 2
done
if [ "$st" = healthy ]; then
  info "omni is healthy"
else
  warn "omni is '$st' after two minutes — check: docker logs omni"
fi

# --- host cli -------------------------------------------------------------------
step "host cli"
MARK='# omni (docker wrapper)'
existing=$(command -v omni || true)
if [ -z "$existing" ] || grep -qF "$MARK" "$existing" 2>/dev/null; then
  mkdir -p "$BIN"
  cat > "$BIN/omni" <<WRAP
#!/usr/bin/env bash
$MARK: runs the cli inside the omni container — scripts/install-docker.sh
t=; [ -t 0 ] && [ -t 1 ] && t=-t
exec docker exec -i \$t omni omni "\$@"
WRAP
  chmod 755 "$BIN/omni"
  info "$BIN/omni wraps: docker exec -it omni omni"
  case ":$PATH:" in
    *":$BIN:"*) ;;
    *) warn "$BIN is not in your PATH — add it to your shell profile" ;;
  esac
else
  info "native omni at $existing reaches the container at 127.0.0.1:$PORT for api commands"
  info "plugins, doctor and guardian act on this PC — run those as: docker exec -it omni omni <cmd>"
fi

# --- summary --------------------------------------------------------------------
echo
step "omni is running in docker ($DIR)"
[ ${#SHARED[@]} -gt 0 ] && info "shared from this PC: ${SHARED[*]}"
[ ${#INSTALL[@]} -gt 0 ] && info "installed inside (login once with: docker exec -it omni <cli> login): ${INSTALL[*]}"
[ -z "$(getenv TELEGRAM_BOT_TOKEN)" ] && info "next: omni channels connect -c telegram"
info "then: omni llm connect -p claude   omni pairing approve telegram <CODE>   omni doctor"
info "logs: docker logs -f omni · upgrade (also after upgrading a cli on this PC): re-run this installer"
info "backup / move to another PC: $DIR/omni-docker-backup.sh backup   (restore FILE on the other side)"
