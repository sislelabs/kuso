# Build kuso-server (Go) image with the embedded Vue SPA bundle.
# Build context MUST be the kuso repo root, not server-go/, because the
# Vue client lives at ../client.
#
#   docker build -f kuso/server-go/Dockerfile -t ghcr.io/sislelabs/kuso-server-go:v0.2.0-dev kuso
#
# Stages:
#   1. web-build   — compiles client/ into /tmp/dist via vite
#   2. build       — copies the dist into server-go/internal/web/dist
#                    (which is //go:embed-ed by internal/web) and
#                    runs go build into /out/kuso-server
#   3. final       — FROM scratch + the static binary + CA bundle

# ---- 1. web-build --------------------------------------------------------

FROM node:22-bookworm-slim AS web-build
WORKDIR /web

# Copy package metadata first so yarn install caches across SPA edits
# that don't touch deps.
COPY client/package.json client/yarn.lock ./
RUN corepack enable && yarn install --immutable

COPY client/ ./
# vite.config.ts has outDir=../server/dist/public — override at build
# time to /tmp/dist so the next stage can grab it without touching the
# Vue project.
RUN yarn build --outDir /tmp/dist

# ---- 2. go build ---------------------------------------------------------

FROM golang:1.25-bookworm AS build
ARG VERSION=v0.2.0-dev

WORKDIR /src
COPY server-go/go.mod server-go/go.sum* ./
RUN go mod download
COPY server-go/ ./

# Replace the placeholder web/dist with the real SPA bundle.
COPY --from=web-build /tmp/dist /src/internal/web/dist

# CGO_ENABLED=0 + -trimpath gives a fully static, reproducible binary
# that runs in a `scratch` rootfs.
RUN CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/kuso-server \
      ./cmd/kuso-server

# Stage a writable, world-writable data dir to be COPYed into the
# scratch image — scratch has no shell to mkdir at runtime.
RUN mkdir -p /tmp/kuso-data && chmod 777 /tmp/kuso-data

# ---- 3. scratch image ----------------------------------------------------

FROM scratch
ARG VERSION=v0.2.0-dev

LABEL maintainer="https://kuso.sislelabs.com"
LABEL version=$VERSION
LABEL org.opencontainers.image.source="https://github.com/sislelabs/kuso"
LABEL org.opencontainers.image.description="kuso control-plane — Go HTTP API + embedded Vue SPA"
LABEL org.opencontainers.image.licenses="GPL-3.0"

# CA bundle is needed for the GitHub client + any outbound TLS we add
# later (kube API server hits go through client-go's transport, which
# uses this bundle by default).
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/kuso-server /kuso-server

# Pre-create the data directory so the SQLite file can land somewhere
# writable. In a deployment this is typically a PVC mount, but the
# default lets the image run end-to-end without one.
COPY --from=build /tmp/kuso-data /var/lib/kuso

ENV KUSO_DB_PATH=/var/lib/kuso/kuso.db
EXPOSE 3000
ENTRYPOINT ["/kuso-server"]
