#!/usr/bin/env bash
# Build the working tree as the dev version and install it like prod:
# binaries to ~/.local/bin plus a systemd user service (omni-dev-server).
# Safe to re-run: it rebuilds, overwrites the binaries and restarts the
# service, replacing any running dev server with the new build.
# Coexists with prod: omni-dev talks to omni-dev-server on :8788, with its
# own config (~/.config/omni-dev) and data (~/.local/share/omni-dev).
set -euo pipefail
cd "$(dirname "$0")/.."

APP=omni-dev ADDR=:8788 scripts/build.sh

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
install -m755 bin/omni-dev bin/omni-dev-server "$BIN/"

UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
mkdir -p "$UNIT_DIR"
cat > "$UNIT_DIR/omni-dev-server.service" <<'EOF'
[Unit]
Description=Omni dev server
After=network-online.target

[Service]
ExecStart=%h/.local/bin/omni-dev-server
Restart=on-failure

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable omni-dev-server.service
systemctl --user restart omni-dev-server.service

case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "warning: $BIN is not in your PATH" ;;
esac

echo "==> installed $BIN/omni-dev and $BIN/omni-dev-server"
echo "==> service omni-dev-server: $(systemctl --user is-active omni-dev-server.service)"
echo "    logs: journalctl --user -u omni-dev-server -f"
