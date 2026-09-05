#!/usr/bin/env bash
# Install omni on a bare linux machine from a GitHub release — no git, go or
# gum needed, only bash, curl (or wget), sha256sum and systemd user services:
#
#   curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/install.sh | bash
#
# Downloads omni, omni-server and omni-guardian for this cpu into ~/.local/bin,
# verifies them against the release's checksums.txt, then runs the server as a
# systemd user service (omni-server.service) with the guardian watchdog timer.
# Safe to re-run: it overwrites the binaries and restarts the service, so a
# re-run is an upgrade. Knobs (env vars):
#   OMNI_VERSION    release tag to install (default: the latest release)
#   OMNI_SKIP_DEPS  set to 1 to skip the agent dependencies block at the end
set -euo pipefail

REPO=johnvilela/omni
BIN="$HOME/.local/bin"

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
printf '%s%sinstaller%s\n' "$C_OFF" "$C_DIM" "$C_OFF"

# --- preflight ----------------------------------------------------------------
[ "$(uname -s)" = Linux ] || die "omni runs on linux only (got $(uname -s))"
case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) die "no release binaries for $(uname -m) (only amd64 and arm64)" ;;
esac
command -v sha256sum >/dev/null || die "sha256sum not found (coreutils)"
command -v systemctl >/dev/null || die "systemctl not found — omni needs systemd user services"

# fetch URL [DEST]: curl or wget, whichever this machine has; DEST=- means stdout
if command -v curl >/dev/null; then
  fetch() { curl -fsSL --retry 3 -o "${2:--}" "$1"; }
elif command -v wget >/dev/null; then
  fetch() { wget -qO "${2:--}" "$1"; }
else
  die "need curl or wget to download the release"
fi

# --- resolve the release tag ---------------------------------------------------
step "resolving release"
TAG="${OMNI_VERSION:-}"
if [ -z "$TAG" ]; then
  if command -v curl >/dev/null; then
    # releases/latest redirects to releases/tag/<tag>: no api call, no rate limit
    TAG=$(curl -fsSL --retry 3 -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" \
      | sed -n 's|.*/releases/tag/||p')
  else
    TAG=$(fetch "https://api.github.com/repos/$REPO/releases/latest" \
      | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  fi
fi
[ -n "$TAG" ] || die "could not determine the latest release of $REPO — set OMNI_VERSION=vX.Y.Z"
info "omni $TAG (linux/$ARCH)"

# --- download + verify into a stage dir, then install atomically ---------------
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
BASE="https://github.com/$REPO/releases/download/$TAG"
ASSETS=("omni_linux_$ARCH" "omni-server_linux_$ARCH" "omni-guardian_linux_$ARCH")

step "downloading"
fetch "$BASE/checksums.txt" "$STAGE/checksums.txt" || die "no checksums.txt in release $TAG — is $TAG a real tag?"
for a in "${ASSETS[@]}"; do
  fetch "$BASE/$a" "$STAGE/$a" || die "download failed: $BASE/$a"
  info "$a"
done

step "verifying checksums"
# keep only the lines for the assets we fetched: sha256sum -c needs every
# listed file present, and the release lists both architectures
(cd "$STAGE" && grep -E " ($(IFS='|'; echo "${ASSETS[*]}"))\$" checksums.txt | sha256sum -c --quiet -) \
  || die "checksum mismatch — refusing to install"
info "ok"

step "installing to $BIN"
mkdir -p "$BIN"
# install(1) unlinks then writes, so a running omni-server never hits ETXTBSY
install -m755 "$STAGE/omni_linux_$ARCH" "$BIN/omni"
install -m755 "$STAGE/omni-server_linux_$ARCH" "$BIN/omni-server"
install -m755 "$STAGE/omni-guardian_linux_$ARCH" "$BIN/omni-guardian"
info "omni, omni-server, omni-guardian"

# --- systemd user units --------------------------------------------------------
step "systemd user services"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
mkdir -p "$UNIT_DIR"
cat > "$UNIT_DIR/omni-server.service" <<'EOF'
[Unit]
Description=Omni server
After=network-online.target

[Service]
ExecStart=%h/.local/bin/omni-server
Restart=on-failure

[Install]
WantedBy=default.target
EOF

# guardian: oneshot watchdog fired by a timer; interval is overridable via
# `omni guardian set-interval` (drop-in), so only the base units live here
cat > "$UNIT_DIR/omni-guardian.service" <<'EOF'
[Unit]
Description=Omni guardian

[Service]
Type=oneshot
ExecStart=%h/.local/bin/omni-guardian
EOF

cat > "$UNIT_DIR/omni-guardian.timer" <<'EOF'
[Unit]
Description=Omni guardian timer

[Timer]
# OnActiveSec, not OnBootSec: first run 2min after the timer is enabled, so
# an install never probes the server it just restarted mid-startup
OnActiveSec=2min
OnUnitActiveSec=2min

[Install]
WantedBy=timers.target
EOF

SERVICES_OK=1
if systemctl --user daemon-reload 2>/dev/null; then
  systemctl --user enable omni-server.service
  systemctl --user restart omni-server.service
  systemctl --user enable omni-guardian.timer
  systemctl --user restart omni-guardian.timer
  info "omni-server.service and omni-guardian.timer enabled"
else
  SERVICES_OK=0
  warn "systemctl --user is not reachable (no user session bus?) — units written but not started"
  warn "log in with a full session (or: export XDG_RUNTIME_DIR=/run/user/\$(id -u)) and run:"
  warn "  systemctl --user daemon-reload && systemctl --user enable --now omni-server.service omni-guardian.timer"
fi

# a headless box: user services die at logout unless the user lingers
if command -v loginctl >/dev/null; then
  ME=$(id -un)
  if [ "$(loginctl show-user "$ME" -p Linger --value 2>/dev/null)" != yes ]; then
    loginctl enable-linger "$ME" 2>/dev/null \
      || sudo -n loginctl enable-linger "$ME" 2>/dev/null \
      || warn "could not enable lingering — omni-server stops when you log out; fix: sudo loginctl enable-linger $ME"
  fi
fi

case ":$PATH:" in
  *":$BIN:"*) ;;
  *) warn "$BIN is not in your PATH — add it to your shell profile" ;;
esac

# --- agent dependencies --------------------------------------------------------
if [ "${OMNI_SKIP_DEPS:-}" = 1 ]; then
  step "skipping agent dependencies (OMNI_SKIP_DEPS=1)"
else
step "agent dependencies (idempotent; failures warn, never abort)"
# package manager: node + chromium for the /agent browser playbook
PKGS_MISSING=""
command -v node >/dev/null || PKGS_MISSING="nodejs npm"
command -v chromium >/dev/null || command -v chromium-browser >/dev/null || PKGS_MISSING="$PKGS_MISSING chromium"
if [ -n "$PKGS_MISSING" ]; then
  if command -v pacman >/dev/null; then
    sudo pacman -S --needed --noconfirm $PKGS_MISSING || warn "pacman install failed: $PKGS_MISSING"
  elif command -v apt-get >/dev/null; then
    { sudo apt-get update -qq && sudo apt-get install -y $PKGS_MISSING; } \
      || warn "apt install failed: $PKGS_MISSING (Ubuntu may need the chromium snap)"
  else
    warn "no pacman/apt-get — install manually: $PKGS_MISSING"
  fi
fi

# playwright-cli (ships in the playwright npm package) drives the browser
if ! command -v playwright-cli >/dev/null; then
  if command -v npm >/dev/null; then
    npm install -g playwright 2>/dev/null || sudo npm install -g playwright \
      || warn "could not npm install -g playwright"
  else
    warn "npm missing — skipped playwright install"
  fi
fi

# agent workspace + persistent chrome profile (logins survive restarts;
# log into LinkedIn etc. once via: chromium --user-data-dir=<profile>)
AGENT_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/omni/agent"
mkdir -p "$AGENT_DIR/chrome-profile"

# memoria: long-term memory hooks for the vendor CLIs in agent mode
if ! command -v memoria >/dev/null; then
  M_URL=$(fetch "https://api.github.com/repos/johnvilela/memoria/releases/latest" \
    | grep -o "https://[^\"]*memoria_linux_$ARCH" | head -n1) \
    && [ -n "$M_URL" ] && fetch "$M_URL" "$BIN/memoria" && chmod 755 "$BIN/memoria" \
    || warn "could not download memoria"
fi
if command -v memoria >/dev/null; then
  memoria setup --client claude-code,codex --global \
    || warn "memoria setup failed — run once by hand: memoria init --client claude-code,codex"
  if ! memoria list 2>/dev/null | grep -q "$AGENT_DIR"; then
    (cd "$AGENT_DIR" && memoria bootstrap --background) \
      || warn "memoria bootstrap of the agent workspace failed"
  fi
fi
fi

# --- summary --------------------------------------------------------------------
echo
step "installed omni $TAG"
info "$BIN/omni, $BIN/omni-server, $BIN/omni-guardian"
if [ "$SERVICES_OK" = 1 ]; then
  info "service omni-server: $(systemctl --user is-active omni-server.service || true)"
  info "timer omni-guardian: $(systemctl --user is-active omni-guardian.timer || true)"
fi
info "next: omni channels connect -c telegram   (then: omni llm connect -p claude, omni doctor)"
