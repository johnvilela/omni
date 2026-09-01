#!/usr/bin/env bash
# Install the prod omni CLI + server to ~/.local/bin and run the server
# as a systemd user service (omni-server.service). Safe to re-run: it
# rebuilds, overwrites the binaries and restarts the service.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> running tests"
go test ./...

echo "==> building (prod)"
PROD=1 scripts/build.sh

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
install -m755 bin/omni bin/omni-server "$BIN/"

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

systemctl --user daemon-reload
systemctl --user enable omni-server.service
systemctl --user restart omni-server.service

case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "warning: $BIN is not in your PATH" ;;
esac

echo "==> installed $BIN/omni and $BIN/omni-server"
echo "==> service omni-server: $(systemctl --user is-active omni-server.service)"
