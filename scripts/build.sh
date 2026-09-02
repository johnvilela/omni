#!/usr/bin/env bash
# Build the omni CLI and server. Knobs (env vars):
#   APP   app/binary name          (default: omni; dev builds use omni-dev)
#   ADDR  default server address   (default: :8787)
#   OUT   output directory         (default: bin)
#   PROD  set to 1 for an optimized build (-trimpath, stripped)
# Outputs: $OUT/$APP (CLI) and $OUT/$APP-server (server).
# build.sh all -> dist/omni{,-server}_<os>_<arch> release matrix for CI.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ "${1:-}" == "all" ]]; then
  mkdir -p dist
  # ponytail: linux only — omni targets linux PCs (systemd install); add
  # darwin targets if a mac host ever appears
  for target in linux/amd64 linux/arm64; do
    os=${target%/*} arch=${target#*/}
    GOOS=$os GOARCH=$arch CGO_ENABLED=0 PROD=1 OUT=dist scripts/build.sh
    mv "dist/omni" "dist/omni_${os}_${arch}"
    mv "dist/omni-server" "dist/omni-server_${os}_${arch}"
    echo "built dist/omni_${os}_${arch} and dist/omni-server_${os}_${arch}"
  done
  exit 0
fi

APP="${APP:-omni}"
ADDR="${ADDR:-:8787}"
OUT="${OUT:-bin}"

# the version is NOT stamped here: both binaries share the single
# hand-bumped version in version/version.go
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
