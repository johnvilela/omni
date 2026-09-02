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

echo "==> agent dependencies (idempotent; failures warn, never abort)"
# package manager: node + chromium for the /agent browser playbook
PKGS_MISSING=""
command -v node >/dev/null || PKGS_MISSING="nodejs npm"
command -v chromium >/dev/null || command -v chromium-browser >/dev/null || PKGS_MISSING="$PKGS_MISSING chromium"
if [ -n "$PKGS_MISSING" ]; then
  if command -v pacman >/dev/null; then
    sudo pacman -S --needed --noconfirm $PKGS_MISSING || echo "warning: pacman install failed: $PKGS_MISSING"
  elif command -v apt-get >/dev/null; then
    sudo apt-get install -y $PKGS_MISSING || echo "warning: apt install failed: $PKGS_MISSING (Ubuntu may need the chromium snap)"
  else
    echo "warning: no pacman/apt-get — install manually: $PKGS_MISSING"
  fi
fi

# playwright-cli (ships in the playwright npm package) drives the browser
if ! command -v playwright-cli >/dev/null; then
  npm install -g playwright 2>/dev/null || sudo npm install -g playwright \
    || echo "warning: could not npm install -g playwright"
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
      || echo "warning: could not download memoria"
  else
    echo "warning: no memoria release for $(uname -m)"
  fi
fi
if command -v memoria >/dev/null; then
  memoria setup --client claude-code,codex --global \
    || echo "warning: memoria setup failed — run once by hand: memoria init --client claude-code,codex"
  if ! memoria list 2>/dev/null | grep -q "$AGENT_DIR"; then
    (cd "$AGENT_DIR" && memoria bootstrap --background) \
      || echo "warning: memoria bootstrap of the agent workspace failed"
  fi
fi

echo "==> installed $BIN/omni and $BIN/omni-server"
echo "==> service omni-server: $(systemctl --user is-active omni-server.service)"
