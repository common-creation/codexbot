# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25
ARG NODE_VERSION=24.15.0

FROM node:${NODE_VERSION}-bookworm-slim AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY web/ ./
RUN npm run build

FROM golang:${GO_VERSION}-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w" -o /out/codexbot ./cmd/codexbot \
    && CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w" -o /out/runtime-manager ./cmd/runtime-manager

FROM docker:29.4.0-cli AS docker-cli

FROM debian:bookworm-slim AS base
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl jq tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 codexbot \
    && useradd --uid 10001 --gid 10001 --home-dir /var/lib/codexbot --no-create-home --shell /usr/sbin/nologin codexbot \
    && install -d -o codexbot -g codexbot -m 0700 /var/lib/codexbot \
    && install -d -o root -g root -m 0755 /etc/codexbot

FROM base AS control-plane
COPY --from=go-builder /out/codexbot /usr/local/bin/codexbot
COPY --from=web-builder /src/web/dist /opt/codexbot/web
COPY deploy/scripts/control-plane-entrypoint /usr/local/lib/codexbot/control-plane-entrypoint
RUN chmod 0755 /usr/local/bin/codexbot /usr/local/lib/codexbot/control-plane-entrypoint
# The entrypoint starts as root only to read Compose file secrets, then drops to
# UID/GID 10001 and clears all capabilities before executing codexbot.
USER root:root
ENV CODEXBOT_DATA_DIR=/var/lib/codexbot \
    CODEXBOT_STATIC_DIR=/opt/codexbot/web \
    CODEXBOT_LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD ["curl", "--fail", "--silent", "--show-error", "http://127.0.0.1:8080/healthz"]
ENTRYPOINT ["/usr/local/lib/codexbot/control-plane-entrypoint"]

FROM base AS runtime-manager
COPY --from=go-builder /out/runtime-manager /usr/local/bin/runtime-manager
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --chmod=0444 runtime/agent/seccomp_profile.json /etc/codexbot/chromium-seccomp.json
COPY deploy/scripts/runtime-manager-entrypoint /usr/local/lib/codexbot/runtime-manager-entrypoint
RUN chmod 0755 \
    /usr/local/bin/runtime-manager \
    /usr/local/bin/docker \
    /usr/local/lib/codexbot/runtime-manager-entrypoint
ENV CODEXBOT_RUNTIME_LISTEN_ADDR=:8081 \
    DOCKER_HOST=unix:///run/codexbot/docker.sock
EXPOSE 8081
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD ["curl", "--fail", "--silent", "--show-error", "http://127.0.0.1:8081/healthz"]
ENTRYPOINT ["/usr/local/lib/codexbot/runtime-manager-entrypoint"]
