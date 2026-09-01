#!/usr/bin/env bash
# Build the omni CLI and server. Knobs (env vars):
#   APP   app/binary name          (default: omni; dev builds use omni-dev)
#   ADDR  default server address   (default: :8787)
#   OUT   output directory         (default: bin)
#   PROD  set to 1 for an optimized build (-trimpath, stripped)
# Outputs: $OUT/$APP (CLI) and $OUT/$APP-server (server).
set -euo pipefail
cd "$(dirname "$0")/.."

APP="${APP:-omni}"
ADDR="${ADDR:-:8787}"
OUT="${OUT:-bin}"

# versions are NOT stamped here: each binary carries its own hand-bumped
# version var (cli/main.go and server/main.go)
X="-X main.app=$APP -X main.defaultAddr=$ADDR"
FLAGS=()
LDFLAGS="$X"
if [[ "${PROD:-}" == "1" ]]; then
  FLAGS+=(-trimpath)
  LDFLAGS="-s -w $X"
fi

mkdir -p "$OUT"
go build "${FLAGS[@]}" -ldflags "$LDFLAGS" -o "$OUT/$APP" ./cli
go build "${FLAGS[@]}" -ldflags "$LDFLAGS" -o "$OUT/$APP-server" ./server
echo "built $OUT/$APP and $OUT/$APP-server"
