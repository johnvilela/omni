#!/usr/bin/env bash
# omni container entrypoint — the systemd of the image. `serve` (the default)
# supervises the three processes scripts/install.sh runs as user units:
#   ai-memory serve   Restart=on-failure; ~/.config/omni/ai-memory.env re-read per spawn
#   omni-server       Restart=on-failure (the guardian heals a hung server with pkill,
#                     /ops restart simply exits)
#   omni-guardian     loop mode (OMNI_GUARDIAN_INTERVAL) instead of a timer
# Any other argv runs a command instead: `docker compose run --rm omni doctor`.
# Seeding is idempotent and runs on every start, so an image upgrade needs no
# migration. Everything under $HOME is the volume; the binaries are the image.
# Vendor clis: a host binary bind-mounted at /opt/host/<name>/<name> wins; a
# name in OMNI_VENDOR_INSTALL without one is npm-installed into ~/.local.
set -euo pipefail

step() { printf '==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf 'WARN %s\n' "$*" >&2; }
die()  { printf 'ERROR %s\n' "$*" >&2; exit 1; }

# --- not serving: run the cli, or any binary on PATH (claude login, bash) ------
if [ $# -gt 0 ] && [ "$1" != serve ]; then
  if type -P "$1" >/dev/null; then exec "$@"; fi
  exec omni "$@"
fi

HOME=${HOME:-/home/omni}
CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}"
DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
OMNI_CONFIG="$CONFIG/omni"
AGENT_DIR="$DATA/omni/agent"
AI_DATA="$DATA/ai-memory"
AI_CONFIG="$CONFIG/ai-memory/config.toml"
AI_ENV="$OMNI_CONFIG/ai-memory.env"
PORT=${OMNI_ADDR:-:8787}; PORT=${PORT##*:}
API="http://127.0.0.1:$PORT"
# unset would mean "oneshot" to the guardian, which the respawn loop would then
# restart every 2s — default the loop cadence instead
: "${OMNI_GUARDIAN_INTERVAL:=2m}"; export OMNI_GUARDIAN_INTERVAL

# --- seed (idempotent, mirrors scripts/install.sh) -----------------------------
step "omni container: seeding $HOME"
[ -w "$HOME" ] || die "$HOME is not writable — a bind-mounted home must be owned by uid $(id -u) (chown -R $(id -u):$(id -g) <dir>)"
mkdir -p "$HOME/.local/bin" "$HOME/.local/lib" "$OMNI_CONFIG" "$AGENT_DIR/chrome-profile" "$AI_DATA" "${AI_CONFIG%/*}"
# chromium cannot be running at container start: drop a stale profile lock
rm -f "$AGENT_DIR"/chrome-profile/Singleton{Lock,Socket,Cookie}

if [ ! -f "$AI_ENV" ]; then
  printf '%s\n' 'AI_MEMORY_DECAY__OBSERVATION_RETENTION_DAYS=30' > "$AI_ENV"
  chmod 600 "$AI_ENV"
fi
if [ ! -f "$AGENT_DIR/.ai-memory.toml" ]; then
  cat > "$AGENT_DIR/.ai-memory.toml" <<'TOML'
workspace = "omni"
project = "assistant"
project_strategy = "repo-root"
TOML
fi
if [ ! -f "$AI_CONFIG" ]; then
  ai-memory --data-dir "$AI_DATA" --config "$AI_CONFIG" init || warn "ai-memory init failed"
fi

# --- vendor clis: a host-mounted binary beats anything in the volume -----------
step "vendor clis"
for n in claude codex gemini; do
  if [ -x "/opt/host/$n/$n" ]; then
    ln -sfn "/opt/host/$n/$n" "$HOME/.local/bin/$n"
    info "$n: host binary (read-only mount)"
  elif [ -L "$HOME/.local/bin/$n" ] && [ ! -e "$HOME/.local/bin/$n" ]; then
    rm -f "$HOME/.local/bin/$n" # the host mount is gone: drop the dangling link
  fi
done
# the host's ~/.claude.json carries its mcp servers (ai-memory mcp-bridge) and
# onboarding state; copied once because claude rewrites it in place, which a
# read-write single-file bind mount cannot survive
if [ -f /opt/host/claude/claude.json ] && [ ! -f "$HOME/.claude.json" ]; then
  cp /opt/host/claude/claude.json "$HOME/.claude.json" && chmod 600 "$HOME/.claude.json"
fi

# ai-memory's client wiring writes into ~/.claude and ~/.codex — the volume, or
# the host's own dirs when they are bind-mounted. Those already carry the
# host's entries for the same 127.0.0.1:49374 (the container's ai-memory here)
# and must not be rewritten with container paths.
if mountpoint -q "$HOME/.claude"; then
  info "~/.claude is shared with the host — keeping its ai-memory hooks and mcp"
else
  ai-memory install-mcp --client claude-code --apply || warn "ai-memory MCP setup failed for claude-code"
  ai-memory install-hooks --agent claude-code --capture-assistant --apply || warn "ai-memory hook setup failed for claude-code"
fi
if mountpoint -q "$HOME/.codex"; then
  info "~/.codex is shared with the host — keeping its ai-memory mcp and hooks"
else
  ai-memory install-mcp --client codex --apply || warn "ai-memory MCP setup failed for codex"
  ai-memory install-hooks --agent codex --capture-assistant --apply || warn "ai-memory hook setup failed for codex"
fi

# --- supervise -----------------------------------------------------------------
pids=()
# run NAME CMD...: Restart=on-failure with a 2s RestartSec, on ANY exit code —
# /ops restart exits 0 on purpose. One subshell per process so each has its
# own trap: TERM/INT kills the child and ends the loop. Never $(run ...): the
# loop must stay a child of this shell for wait/kill.
run() {
  local name=$1; shift
  (
    child=
    trap '[ -n "$child" ] && kill -TERM "$child" 2>/dev/null; wait "$child" 2>/dev/null; exit 0' TERM INT
    while :; do
      "$@" & child=$!
      wait "$child" && rc=0 || rc=$?
      info "$name exited ($rc) — restarting in 2s"
      sleep 2 & wait $! # backgrounded so the trap can interrupt it
    done
  ) &
  pids+=("$!")
}

# wait_status SECONDS: poll /status until the server answers or the budget ends
wait_status() {
  local i
  for ((i = 0; i < $1; i++)); do
    curl -fsS "$API/status" >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

# fresh env per spawn: /memory_retention rewrites ai-memory.env, then pkills
# ai-memory so this loop restarts it with the new value
ai_memory() {
  set -a
  AI_MEMORY_CAPTURE_ASSISTANT=true
  AI_MEMORY_CONSOLIDATE_ON_SESSION_END=false
  AI_MEMORY_BACKFILL_ON_START=false
  AI_MEMORY_EMBEDDING_PROVIDER=none
  AI_MEMORY_AUTO_IMPROVE__SCHEDULER__ENABLED=false
  AI_MEMORY_MAINTENANCE__LINT_INTERVAL_SECS=0
  AI_MEMORY_MAINTENANCE__EMBEDDING_BACKFILL_INTERVAL_SECS=0
  if [ -f "$AI_ENV" ]; then . "$AI_ENV"; fi
  set +a
  exec nice -n 10 ai-memory --data-dir "$AI_DATA" --config "$AI_CONFIG" serve \
    --transport http --bind 127.0.0.1:49374 --workspace omni --project assistant
}

# the timer's OnActiveSec=2min, without the timer: never probe a server that is
# still booting (each respawn re-waits; the loop's own first-pass delay follows)
guardian() {
  wait_status 60 || true
  exec omni-guardian
}

# best-effort, after the server answers: a GitHub, npm or Telegram hiccup never
# blocks startup, and a failure is a WARN with the command to retry by hand
post_start() {
  wait_status 60 || warn "omni-server did not answer /status within 60s — see docker logs"
  info "omni-server $(curl -fsS "$API/status" 2>/dev/null || echo 'not responding')"
  if [ -n "${TELEGRAM_BOT_TOKEN:-}" ] \
     && ! curl -fsS "$API/channels/telegram" 2>/dev/null | grep -q '"connected":true'; then
    curl -fsS -X POST "$API/channels/telegram/connect" -H 'Content-Type: application/json' -d '{}' >/dev/null \
      && info "telegram connected from TELEGRAM_BOT_TOKEN" \
      || warn "telegram connect failed — check TELEGRAM_BOT_TOKEN, then: omni channels connect -c telegram"
  fi
  # vendor clis the host did not provide: npm into the volume (re-run = upgrade)
  local n pkg log
  IFS=, read -ra wanted <<<"${OMNI_VENDOR_INSTALL:-}"
  for n in "${wanted[@]}"; do
    n=${n// /}; [ -n "$n" ] || continue
    [ -x "/opt/host/$n/$n" ] && continue
    case "$n" in
      claude) pkg=@anthropic-ai/claude-code ;;
      codex) pkg=@openai/codex ;;
      gemini) pkg=@google/gemini-cli ;;
      *) warn "OMNI_VENDOR_INSTALL: unknown cli '$n' (claude, codex, gemini)"; continue ;;
    esac
    log="$DATA/omni/npm-$n.log"
    if npm install -g --prefix "$HOME/.local" "$pkg" >"$log" 2>&1; then
      info "$n: installed/upgraded in the volume ($pkg)"
    else
      warn "$n: npm install failed (see $log) — retry: docker exec omni npm install -g --prefix ~/.local $pkg"
    fi
  done
  local p
  IFS=, read -ra plugins <<<"${OMNI_PLUGINS:-}"
  for p in "${plugins[@]}"; do
    p=${p// /}; [ -n "$p" ] || continue
    if omni plugins install "$p" </dev/null; then
      info "plugin $p installed"
    else
      warn "plugin $p failed — retry: docker exec -it omni omni plugins install $p"
    fi
  done
}

step "starting ai-memory, omni-server, omni-guardian"
run ai-memory ai_memory
run omni-server omni-server
run omni-guardian guardian
post_start &
post_pid=$!

trap 'step "stopping"; kill -TERM "${pids[@]}" "$post_pid" 2>/dev/null || true' TERM INT
# the first wait returns 143 when TERM interrupts it (the trap ran) — not an
# error under set -e; the second collects the loops so the exit code is 0
wait || true
wait || true
step "stopped"
