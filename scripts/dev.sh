#!/usr/bin/env bash
# Build the working tree as the dev version and install it to ~/.local/bin.
# Coexists with the prod install: omni-dev talks to omni-dev-server on :8788,
# with its own config (~/.config/omni-dev) and data (~/.local/share/omni-dev).
set -euo pipefail
cd "$(dirname "$0")/.."

APP=omni-dev ADDR=:8788 scripts/build.sh

BIN="$HOME/.local/bin"
mkdir -p "$BIN"
install -m755 bin/omni-dev bin/omni-dev-server "$BIN/"

echo "==> installed $BIN/omni-dev and $BIN/omni-dev-server"
echo "    no service for dev: run 'omni-dev-server' in a terminal, then 'omni-dev channels'"
