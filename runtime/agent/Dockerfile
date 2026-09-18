# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25
ARG NODE_VERSION=24.15.0

FROM golang:${GO_VERSION}-bookworm AS worker-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w" -o /out/agent-worker ./cmd/agent-worker

FROM node:${NODE_VERSION}-bookworm-slim AS node-runtime

FROM debian:bookworm-slim

ARG TARGETARCH
ARG KASMVNC_VERSION=1.5.0
ARG KASMVNC_SHA256_AMD64=770fd3df51510beecc89666879d82faf411276e68c6e11df612f736b891b5f71
ARG KASMVNC_SHA256_ARM64=aa83a1a6c9069d1a02239988b07a3a2a082a433042b4d4ee2b9e9f6b2df9643c
ARG CODEX_VERSION=0.153.4

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=en_US.UTF-8 \
    LC_ALL=en_US.UTF-8 \
    DISPLAY=:1 \
    HOME=/home/agent \
    XDG_CONFIG_HOME=/var/lib/codexbot/profile/xdg/config \
    XDG_DATA_HOME=/var/lib/codexbot/profile/xdg/data \
    XDG_CACHE_HOME=/var/lib/codexbot/profile/xdg/cache \
    XDG_STATE_HOME=/var/lib/codexbot/profile/xdg/state \
    CHROMIUM_USER_DATA_DIR=/var/lib/codexbot/profile/chromium \
    CHROMIUM_PATH=/usr/bin/chromium \
    PLAYWRIGHT_BROWSERS_PATH=0 \
    KASMVNC_HOME=/var/lib/codexbot/profile/kasmvnc \
    KASMVNC_PORT=6901 \
    KASMVNC_DISPLAY=:1 \
    KASMVNC_GEOMETRY=1440x900 \
    AGENT_WORKER_LISTEN_ADDR=0.0.0.0:8082 \
    CODEXBOT_DESKTOP_LEASE_FILE=/var/lib/codexbot/profile/desktop-lease.json

COPY --from=node-runtime /usr/local/ /usr/local/

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        chromium \
        chromium-sandbox \
        bubblewrap \
        curl \
        dbus-x11 \
        fonts-dejavu-core \
        fonts-noto-cjk \
        fonts-noto-color-emoji \
        imagemagick \
        jq \
        locales \
        procps \
        python3 \
        tini \
        util-linux \
        x11-xserver-utils \
        xdotool \
        xfce4 \
        xfce4-terminal \
    && sed -i 's/^# *en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen \
    && locale-gen \
    && case "${TARGETARCH}" in \
         amd64) kasm_arch=amd64; kasm_sha="${KASMVNC_SHA256_AMD64}" ;; \
         arm64) kasm_arch=arm64; kasm_sha="${KASMVNC_SHA256_ARM64}" ;; \
         *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
       esac \
    && curl --fail --location --retry 3 \
        --output /tmp/kasmvnc.deb \
        "https://github.com/kasmtech/KasmVNC/releases/download/v${KASMVNC_VERSION}/kasmvncserver_bookworm_${KASMVNC_VERSION}_${kasm_arch}.deb" \
    && echo "${kasm_sha}  /tmp/kasmvnc.deb" | sha256sum --check --strict \
    && apt-get install -y --no-install-recommends /tmp/kasmvnc.deb \
    && rm -f /tmp/kasmvnc.deb \
    && rm -rf /var/lib/apt/lists/*

RUN npm install --global "@openai/codex@${CODEX_VERSION}" \
    && npm cache clean --force

RUN groupadd --gid 1000 agent \
    && useradd --uid 1000 --gid 1000 --create-home --home-dir /home/agent --shell /bin/bash agent \
    && usermod --append --groups ssl-cert agent \
    && install -d -o agent -g agent -m 0750 \
        /run/codexbot \
        /var/lib/codexbot/profile \
        /var/lib/codexbot/profile/chromium \
        /var/lib/codexbot/profile/kasmvnc \
        /var/lib/codexbot/profile/kasmvnc/.vnc \
        /var/lib/codexbot/profile/xdg \
        /var/lib/codexbot/profile/xdg/config \
        /var/lib/codexbot/profile/xdg/data \
        /var/lib/codexbot/profile/xdg/cache \
        /var/lib/codexbot/profile/xdg/state \
        /var/lib/codexbot/profile/outbox \
    && install -d -o agent -g agent -m 0755 /opt/codexbot/browser-mcp

COPY runtime/agent/browser-mcp/package.json /opt/codexbot/browser-mcp/package.json
RUN cd /opt/codexbot/browser-mcp \
    && npm install --omit=dev --ignore-scripts \
    && npm cache clean --force \
    && chown -R agent:agent /opt/codexbot/browser-mcp

COPY --from=worker-builder --chown=agent:agent /out/agent-worker /usr/local/bin/agent-worker
COPY --chown=agent:agent runtime/agent/bin/ /usr/local/lib/codexbot/
COPY --chown=agent:agent runtime/agent/config/ /usr/local/share/codexbot/
COPY runtime/agent/config/chromium-defaults /etc/chromium.d/codexbot

RUN node /usr/local/lib/codexbot/disable-vnc-idle-timeout.mjs

RUN chmod 0755 \
        /usr/local/bin/agent-worker \
        /usr/local/lib/codexbot/agent-entrypoint \
        /usr/local/lib/codexbot/browser-mcp.mjs \
        /usr/local/lib/codexbot/chromium-visible \
        /usr/local/lib/codexbot/desktop-mcp.py \
        /usr/local/lib/codexbot/collaboration-mcp.py \
        /usr/local/lib/codexbot/healthcheck \
        /usr/local/share/codexbot/xstartup

USER agent:agent
WORKDIR /home/agent

EXPOSE 6901 8082

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=6 \
    CMD ["/usr/local/lib/codexbot/healthcheck"]

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/lib/codexbot/agent-entrypoint"]
