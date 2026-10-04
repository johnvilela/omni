# syntax=docker/dockerfile:1
# omni in one container: omni-server, omni-guardian and ai-memory supervised by
# scripts/docker-entrypoint.sh, plus the agent browser stack (node, chromium,
# playwright-cli). The vendor clis (claude, codex, gemini) are NOT baked in:
# scripts/install-docker.sh bind-mounts the ones already on the PC read-only at
# /opt/host/<name>/<name>, and the entrypoint npm-installs the rest into the
# home volume (OMNI_VENDOR_INSTALL). Binaries live in the image; everything the
# owner owns lives in /home/omni (one volume). See docker-compose.yml.

# --- build: cross-compile on the build host, never under qemu -----------------
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 PROD=1 OUT=/out scripts/build.sh

# --- runtime -------------------------------------------------------------------
# node:*-bookworm-slim: debian (apt chromium exists for arm64; ubuntu only has
# the snap) with node preinstalled for playwright-cli and the volume npm installs
FROM node:22-bookworm-slim
ARG TARGETARCH
ARG AI_MEMORY_VERSION=latest

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl chromium fonts-liberation git procps tzdata \
    && rm -rf /var/lib/apt/lists/*

# debian's /usr/bin/chromium wrapper sources /etc/chromium.d/*: no usable
# sandbox in a container, no display, and a 64M /dev/shm by default
RUN printf '%s\n' 'export CHROMIUM_FLAGS="$CHROMIUM_FLAGS --no-sandbox --headless=new --disable-dev-shm-usage --disable-gpu"' \
      > /etc/chromium.d/omni

# playwright-cli (npm @playwright/cli, not the playwright package) drives the
# browser for /agent over cdp; root-owned, omni only runs it
RUN npm install -g @playwright/cli && npm cache clean --force

# ai-memory (akitaonrails/ai-memory), sha256-verified, outside the volume.
# /usr/bin/ai-memory too: a host ~/.claude/settings.json shared into the
# container may reference that path in its hooks (pacman installs it there).
RUN set -eu; \
    case "$TARGETARCH" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; *) echo "unsupported arch: $TARGETARCH" >&2; exit 1 ;; esac; \
    asset="ai-memory-linux-$a.tar.gz"; \
    if [ "$AI_MEMORY_VERSION" = latest ]; \
      then base="https://github.com/akitaonrails/ai-memory/releases/latest/download"; \
      else base="https://github.com/akitaonrails/ai-memory/releases/download/$AI_MEMORY_VERSION"; fi; \
    cd /tmp \
    && curl -fsSL --retry 3 -o "$asset" "$base/$asset" \
    && curl -fsSL --retry 3 -o "$asset.sha256" "$base/$asset.sha256" \
    && sha256sum -c "$asset.sha256" \
    && mkdir -p /opt/ai-memory && tar -xzf "$asset" -C /opt/ai-memory \
    && ln -s /opt/ai-memory/ai-memory /usr/local/bin/ai-memory \
    && ln -s /opt/ai-memory/ai-memory /usr/bin/ai-memory \
    && rm -f "$asset" "$asset.sha256"

# the node image ships user `node` as uid 1000; omni takes that uid so a bind
# mount chown'd 1000:1000 just works. bash: /terminal uses the login shell.
RUN userdel -r node && (groupdel node 2>/dev/null || true) \
    && groupadd -g 1000 omni && useradd -u 1000 -g 1000 -m -s /bin/bash omni \
    && mkdir -p /opt/host /home/omni/.local/bin /home/omni/.local/lib /home/omni/.config/omni \
                /home/omni/.local/share/omni/agent/chrome-profile \
    && chown -R omni:omni /home/omni

COPY --from=build /out/omni /out/omni-server /out/omni-guardian /usr/local/bin/
COPY --chmod=755 scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# OMNI_CONTAINER=1 flips the Go binaries into container mode (no systemd).
# DISABLE_AUTOUPDATER: a volume-installed claude must not swap itself for a
# native binary behind npm's back — the entrypoint upgrades it.
ENV HOME=/home/omni \
    PATH=/home/omni/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    OMNI_CONTAINER=1 \
    OMNI_AI_MEMORY_URL=http://127.0.0.1:49374 \
    OMNI_GUARDIAN_INTERVAL=2m \
    OMNI_VENDOR_INSTALL=claude,codex,gemini \
    DISABLE_AUTOUPDATER=1 \
    NPM_CONFIG_UPDATE_NOTIFIER=false

USER omni
WORKDIR /home/omni
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8787/status || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]
