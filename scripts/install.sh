#!/usr/bin/env bash
# Install the prod omni CLI + server to ~/.local/bin and run the server
# as a systemd user service (omni-server.service). Safe to re-run: it
# rebuilds, overwrites the binaries and restarts the service.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v gum >/dev/null || { echo "this script needs gum (https://github.com/charmbracelet/gum)"; exit 1; }
step() { gum style --bold --foreground 6 "==> $*"; }
warn() { gum log --level warn "$*"; }

gum style --foreground 6 \
  '  ___  __  __ _   _ ___ ' \
  ' / _ \|  \/  | \ | |_ _|' \
  '| | | | |\/| |  \| || | ' \
  '| |_| | |  | | |\  || | ' \
  ' \___/|_|  |_|_| \_|___|'
gum style --faint "installer"

gum spin --show-error --title "running tests" -- go test ./...
gum log --level info "tests passed"

gum spin --show-error --title "building (prod)" -- env PROD=1 scripts/build.sh
gum log --level info "built bin/omni{,-server,-guardian}"

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
install -m755 bin/omni bin/omni-server bin/omni-guardian "$BIN/"

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

systemctl --user daemon-reload
systemctl --user enable omni-server.service
systemctl --user restart omni-server.service
systemctl --user enable omni-guardian.timer
systemctl --user restart omni-guardian.timer

case ":$PATH:" in
  *":$BIN:"*) ;;
  *) warn "$BIN is not in your PATH" ;;
esac

step "agent dependencies (idempotent; failures warn, never abort)"
# package manager: node + chromium for the /agent browser playbook
PKGS_MISSING=""
command -v node >/dev/null || PKGS_MISSING="nodejs npm"
command -v chromium >/dev/null || command -v chromium-browser >/dev/null || PKGS_MISSING="$PKGS_MISSING chromium"
if [ -n "$PKGS_MISSING" ]; then
  if command -v pacman >/dev/null; then
    sudo pacman -S --needed --noconfirm $PKGS_MISSING || warn "pacman install failed: $PKGS_MISSING"
  elif command -v apt-get >/dev/null; then
    sudo apt-get install -y $PKGS_MISSING || warn "apt install failed: $PKGS_MISSING (Ubuntu may need the chromium snap)"
  else
    warn "no pacman/apt-get — install manually: $PKGS_MISSING"
  fi
fi

# playwright-cli (ships in the playwright npm package) drives the browser
if ! command -v playwright-cli >/dev/null; then
  npm install -g playwright 2>/dev/null || sudo npm install -g playwright \
    || warn "could not npm install -g playwright"
fi

# agent workspace + persistent chrome profile (logins survive restarts;
# log into LinkedIn etc. once via: chromium --user-data-dir=<profile>)
AGENT_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/omni/agent"
mkdir -p "$AGENT_DIR/chrome-profile"

# memoria: long-term memory hooks for the vendor CLIs in agent mode
if ! command -v memoria >/dev/null; then
  case "$(uname -m)" in x86_64) M_ARCH=amd64 ;; aarch64) M_ARCH=arm64 ;; *) M_ARCH="" ;; esac
  if [ -n "$M_ARCH" ]; then
    M_URL=$(curl -fsSL https://api.github.com/repos/johnvilela/memoria/releases/latest \
      | grep -o "https://[^\"]*memoria_linux_$M_ARCH") \
      && curl -fsSL -o "$BIN/memoria" "$M_URL" && chmod 755 "$BIN/memoria" \
      || warn "could not download memoria"
  else
    warn "no memoria release for $(uname -m)"
  fi
fi
if command -v memoria >/dev/null; then
  memoria setup --client claude-code,codex --global \
    || warn "memoria setup failed — run once by hand: memoria init --client claude-code,codex"
  if ! memoria list 2>/dev/null | grep -q "$AGENT_DIR"; then
    (cd "$AGENT_DIR" && memoria bootstrap --background) \
      || warn "memoria bootstrap of the agent workspace failed"
  fi
fi

gum style --border rounded --border-foreground 6 --padding "0 1" \
  "installed $BIN/omni, $BIN/omni-server and $BIN/omni-guardian" \
  "service omni-server: $(systemctl --user is-active omni-server.service)" \
  "timer omni-guardian: $(systemctl --user is-active omni-guardian.timer)"
