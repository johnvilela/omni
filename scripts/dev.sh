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
install -m755 bin/omni-dev bin/omni-dev-server bin/omni-dev-guardian "$BIN/"

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

cat > "$UNIT_DIR/omni-dev-guardian.service" <<'EOF'
[Unit]
Description=Omni dev guardian

[Service]
Type=oneshot
ExecStart=%h/.local/bin/omni-dev-guardian
EOF

cat > "$UNIT_DIR/omni-dev-guardian.timer" <<'EOF'
[Unit]
Description=Omni dev guardian timer

[Timer]
# OnActiveSec, not OnBootSec: first run 2min after the timer is enabled, so
# an install never probes the server it just restarted mid-startup
OnActiveSec=2min
OnUnitActiveSec=2min

[Install]
WantedBy=timers.target
EOF

systemctl --user daemon-reload
systemctl --user enable omni-dev-server.service
systemctl --user restart omni-dev-server.service
systemctl --user enable omni-dev-guardian.timer
systemctl --user restart omni-dev-guardian.timer

case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "warning: $BIN is not in your PATH" ;;
esac

echo "==> installed $BIN/omni-dev, $BIN/omni-dev-server and $BIN/omni-dev-guardian"
echo "==> service omni-dev-server: $(systemctl --user is-active omni-dev-server.service)"
echo "==> timer omni-dev-guardian: $(systemctl --user is-active omni-dev-guardian.timer)"
echo "    logs: journalctl --user -u omni-dev-server -f · guardian: journalctl --user -u omni-dev-guardian"
