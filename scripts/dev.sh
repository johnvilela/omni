#!/usr/bin/env bash
# Build the working tree into the omni:dev image and run it as the omni-dev
# container (docker-compose.dev.yml): own name, port 127.0.0.1:8788, own volume
# omni-dev-home, settings in .env.dev — coexists with a prod container.
# Safe to re-run: it rebuilds the image and recreates the container with it,
# replacing any running dev server with the new build.
# Like scripts/install-docker.sh, the claude/codex/gemini binaries on this PC
# are bind-mounted read-only (generated docker-compose.dev.override.yml); a cli
# this PC lacks, or has as a node script, is npm-installed inside the volume.
# Logins stay in the volume (docker exec -it omni-dev claude login, once);
# OMNI_SHARE_HOST_CLIS=1 bind-mounts ~/.claude ~/.codex ~/.gemini instead and
# hands any agent in the container this PC's credentials. OMNI_NO_HOST_CLIS=1
# installs every cli inside. ~/.local/bin/omni-dev wraps `docker exec omni-dev omni`.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v gum >/dev/null || { echo "this script needs gum (https://github.com/charmbracelet/gum)"; exit 1; }
command -v docker >/dev/null || { echo "docker not found — install Docker Engine: https://docs.docker.com/engine/install/"; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "the docker compose plugin is missing: https://docs.docker.com/compose/install/linux/"; exit 1; }
docker info >/dev/null 2>&1 || { echo "cannot talk to the docker daemon — is it running, and are you in the docker group?"; exit 1; }

gum style --foreground 6 \
  '  ___  __  __ _   _ ___ ' \
  ' / _ \|  \/  | \ | |_ _|' \
  '| | | | |\/| |  \| || | ' \
  '| |_| | |  | | |\  || | ' \
  ' \___/|_|  |_|_| \_|___|'
gum style --faint "dev container"

# the image's /home/omni and the omni-dev-home volume belong to uid 1000; the
# prod installer handles other uids with a bind home, dev does not bother
if [[ "$(id -u)" != 1000 ]]; then
  gum log --level error "uid $(id -u): the omni-dev-home volume belongs to uid 1000 — use scripts/install-docker.sh (it handles other uids) or chown a bind home"
  exit 1
fi

# --- .env.dev: the dev bot token and settings, never committed ------------------
ENV=.env.dev
# setenv KEY VALUE: set a live or commented key, or append it
setenv() {
  if grep -qE "^#? *$1=" "$ENV"; then
    sed -i -E "s|^#? *$1=.*|$1=$2|" "$ENV"
  else
    printf '%s=%s\n' "$1" "$2" >> "$ENV"
  fi
}
# getenv KEY: the live value, '' when unset or commented out
getenv() { sed -n -E "s|^$1=(.*)$|\1|p" "$ENV" | tail -n1; }

if [[ ! -f $ENV ]]; then
  cp .env.example "$ENV"
  chmod 600 "$ENV"
  tz=$(timedatectl show -p Timezone --value 2>/dev/null || cat /etc/timezone 2>/dev/null || true)
  [[ -n $tz ]] && setenv TZ "$tz"
  gum log --level info "$ENV created — put a dev bot token in TELEGRAM_BOT_TOKEN there (not the prod bot)"
fi

# --- api token: one value for the container and a host-built omni-dev cli ------------
# The server takes OMNI_API_TOKEN from the env instead of generating one in the
# volume; the same value lands in ~/.local/share/omni-dev/api-token (0600), where
# a host `scripts/build.sh` cli (APP=omni-dev ADDR=:8788) reads it. So the host
# binary talks to the container's api straight over 127.0.0.1:8788, no docker exec.
if [[ -z "$(getenv OMNI_API_TOKEN)" ]]; then
  setenv OMNI_API_TOKEN "$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
fi
TOKEN_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/omni-dev"
mkdir -p "$TOKEN_DIR" && chmod 700 "$TOKEN_DIR"
umask 077; printf '%s\n' "$(getenv OMNI_API_TOKEN)" > "$TOKEN_DIR/api-token"; umask 022

# --- host clis: share what this PC has, install the rest inside -----------------
MOUNTS=()
INSTALL=()
SHARED=()
# detect_cli NAME: a standalone ELF binary is mounted read-only into the
# container; a script (npm/node shim) cannot travel alone, so the container
# installs its own copy into the volume instead
detect_cli() {
  local name=$1 p r
  if [[ "${OMNI_NO_HOST_CLIS:-}" == 1 ]]; then
    INSTALL+=("$name"); return
  fi
  if ! p=$(command -v "$name" 2>/dev/null); then
    INSTALL+=("$name"); return
  fi
  r=$(readlink -f "$p")
  if [[ "$(head -c4 "$r" | LC_ALL=C tr -d '\177')" == ELF ]]; then
    MOUNTS+=("$r:/opt/host/$name/$name:ro"); SHARED+=("$name")
  else
    INSTALL+=("$name")
  fi
}
for c in claude codex gemini; do detect_cli "$c"; done
if [[ "${OMNI_SHARE_HOST_CLIS:-}" == 1 ]]; then
  gum log --level warn "OMNI_SHARE_HOST_CLIS=1: agents in the container can read and rewrite this PC's cli logins"
  for d in .claude .codex .gemini; do
    [[ -d "$HOME/$d" ]] && MOUNTS+=("$HOME/$d:/home/omni/$d")
  done
  [[ -f "$HOME/.claude.json" ]] && MOUNTS+=("$HOME/.claude.json:/opt/host/claude/claude.json:ro")
else
  gum log --level info "cli logins stay in the volume — once: docker exec -it omni-dev claude login"
fi
setenv OMNI_VENDOR_INSTALL "$(IFS=,; echo "${INSTALL[*]-}")"

# the override carries everything specific to this PC; docker-compose.dev.yml
# stays the committed generic file
OVERRIDE=docker-compose.dev.override.yml
{
  echo "# generated by scripts/dev.sh — re-run it after upgrading a cli on this PC"
  if [[ ${#MOUNTS[@]} -eq 0 ]]; then
    printf 'services:\n  omni: {}\n'
  else
    printf 'services:\n  omni:\n    volumes:\n'
    for m in "${MOUNTS[@]}"; do printf '      - %s\n' "$m"; done
  fi
} > "$OVERRIDE"
[[ ${#SHARED[@]} -gt 0 ]] && gum log --level info "sharing from this PC: ${SHARED[*]}"
[[ ${#INSTALL[@]} -gt 0 ]] && gum log --level info "installed inside the container: ${INSTALL[*]}"

# --- build + up -----------------------------------------------------------------
COMPOSE=(docker compose -f docker-compose.dev.yml -f "$OVERRIDE")
gum spin --show-error --title "building omni:dev" -- "${COMPOSE[@]}" build
gum log --level info "built omni:dev"
"${COMPOSE[@]}" up -d
st=starting
for _ in $(seq 1 60); do
  st=$(docker inspect -f '{{.State.Health.Status}}' omni-dev 2>/dev/null || echo starting)
  [[ $st == healthy ]] && break
  sleep 2
done
[[ $st == healthy ]] || gum log --level warn "omni-dev is '$st' after two minutes — check: docker logs omni-dev"

# --- host wrapper: omni-dev <cmd> runs the cli inside the container -----------------
BIN="$HOME/.local/bin"
mkdir -p "$BIN"
MARK='# omni-dev (docker wrapper)'
cat > "$BIN/omni-dev" <<WRAP
#!/usr/bin/env bash
$MARK: runs the cli inside the omni-dev container — scripts/dev.sh
t=; [ -t 0 ] && [ -t 1 ] && t=-t
exec docker exec -i \$t omni-dev omni "\$@"
WRAP
chmod 755 "$BIN/omni-dev"
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) gum log --level warn "$BIN is not in your PATH" ;;
esac

lines=(
  "container omni-dev: $st · api 127.0.0.1:8788 · volume omni-dev-home · settings $ENV"
  "cli: omni-dev <cmd>  (docker exec -it omni-dev omni <cmd>)"
  "host cli: APP=omni-dev ADDR=:8788 scripts/build.sh → bin/omni-dev status  (token in $TOKEN_DIR/api-token)"
)
if [[ -z "$(getenv TELEGRAM_BOT_TOKEN)" ]]; then
  lines+=("telegram: set TELEGRAM_BOT_TOKEN in $ENV and re-run, or: omni-dev channels connect -c telegram")
fi
lines+=("logs: docker logs -f omni-dev · stop: docker stop omni-dev · wipe: scripts/uninstall.sh --dev")
gum style --border rounded --border-foreground 6 --padding "0 1" "${lines[@]}"
