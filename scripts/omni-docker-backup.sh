#!/usr/bin/env bash
# Back up or restore the omni docker install: the whole /home/omni volume
# (config, sqlite db, ai-memory, plugins, chrome profile, in-volume clis) plus
# the .env settings, as one tar.gz. The container is stopped around the copy
# so the sqlite files are consistent, and started again afterwards.
#
#   omni-docker-backup.sh backup [FILE]    default: ./omni-backup-<date>.tgz
#   omni-docker-backup.sh restore FILE     replaces the home of the install in OMNI_DIR
#
# Moving to another PC: back up here, run install-docker.sh there (it mounts
# that PC's own clis and logins), then restore. Portable .env keys travel
# (TELEGRAM_BOT_TOKEN, api keys, OMNI_PLUGINS, TZ, OMNI_GUARDIAN_INTERVAL);
# PC-specific ones stay as the new install set them (OMNI_UID, OMNI_GID,
# OMNI_VENDOR_INSTALL, OMNI_PORT, OMNI_VERSION). Knob: OMNI_DIR (~/omni-docker).
set -euo pipefail

DIR="${OMNI_DIR:-$HOME/omni-docker}"

if [ -t 1 ]; then
  C_CYAN=$'\033[36m' C_BOLD=$'\033[1m' C_YELLOW=$'\033[33m' C_RED=$'\033[31m' C_OFF=$'\033[0m'
else
  C_CYAN='' C_BOLD='' C_YELLOW='' C_RED='' C_OFF=''
fi
step() { printf '%s%s==> %s%s\n' "$C_BOLD" "$C_CYAN" "$*" "$C_OFF"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '%sWARN%s %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
die() { printf '%sERROR%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; exit 1; }
usage() { sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

cmd=${1:-}
[ $# -gt 0 ] && shift
case "$cmd" in backup | restore) ;; *) usage ;; esac

[ -f "$DIR/docker-compose.yml" ] || die "no omni docker install in $DIR — run scripts/install-docker.sh first, or set OMNI_DIR"
docker inspect omni >/dev/null 2>&1 || die "no omni container — start it once: (cd $DIR && docker compose up -d)"
# the home mount as compose set it up: a named volume, or a bind dir for a non-1000 uid
SRC=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/home/omni"}}{{if eq .Type "volume"}}{{.Name}}{{else}}{{.Source}}{{end}}{{end}}{{end}}' omni)
[ -n "$SRC" ] || die "the omni container has no /home/omni mount"
UID_=$(sed -n 's/^OMNI_UID=//p' "$DIR/.env" | tail -n1); UID_=${UID_:-1000}
GID_=$(sed -n 's/^OMNI_GID=//p' "$DIR/.env" | tail -n1); GID_=${GID_:-1000}

compose() { (cd "$DIR" && docker compose "$@"); }
# tarbox DIR CMD: alpine with the home at /stage/home and DIR at /backup, run
# as root so the uids stored in the volume survive both ways
tarbox() {
  docker run --rm -v "$SRC":/stage/home -v "$DIR/.env":/stage/env:ro -v "$1":/backup alpine sh -c "$2"
}
# setenv KEY VALUE: set a live or commented key in .env, or append it (twin of install-docker.sh)
setenv() {
  if grep -qE "^#? *$1=" "$DIR/.env"; then
    sed -i -E "s|^#? *$1=.*|$1=$2|" "$DIR/.env"
  else
    printf '%s=%s\n' "$1" "$2" >> "$DIR/.env"
  fi
}

case "$cmd" in
backup)
  OUT=$(realpath -m "${1:-omni-backup-$(date +%Y%m%d-%H%M).tgz}")
  mkdir -p "$(dirname "$OUT")"
  step "stopping omni for a consistent copy"
  compose stop
  trap 'compose start >/dev/null' EXIT
  step "archiving $SRC and .env"
  tarbox "$(dirname "$OUT")" "tar czf /backup/$(basename "$OUT") -C /stage home env && chown $(id -u):$(id -g) /backup/$(basename "$OUT")"
  info "$OUT ($(du -h "$OUT" | cut -f1))"
  info "restore anywhere with: $(basename "$0") restore $(basename "$OUT")"
  ;;
restore)
  [ $# -eq 1 ] || usage
  IN=$(realpath "$1") || die "no such file: $1"
  # grep reads to EOF on purpose: grep -q would SIGPIPE tar and trip pipefail
  tar tzf "$IN" 2>/dev/null | grep '^home/' >/dev/null || die "$IN is not an omni backup (no home/ inside)"
  step "stopping omni"
  compose stop
  trap 'compose start >/dev/null' EXIT
  step "replacing $SRC with the backup"
  # clean replace: a stale fresh-install file must not shadow the restored one
  tarbox "$(dirname "$IN")" "cd /stage/home && find . -mindepth 1 -delete && cd /stage && tar xzf /backup/$(basename "$IN") home && chown -R $UID_:$GID_ home"
  step "carrying the portable .env keys"
  # token, keys, plugins, tz: the backup's; uid/port/version/vendor list: this PC's
  (tar xzf "$IN" -O env 2>/dev/null || true) | while IFS='=' read -r k v; do
    case "$k" in
      TELEGRAM_BOT_TOKEN | ANTHROPIC_API_KEY | OPENAI_API_KEY | GEMINI_API_KEY | OMNI_PLUGINS | TZ | OMNI_GUARDIAN_INTERVAL)
        if [ -n "$v" ]; then setenv "$k" "$v"; info "$k restored"; fi ;;
    esac
  done
  info "restored — omni starts again now; check with: docker exec -it omni omni doctor"
  ;;
esac
