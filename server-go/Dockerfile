# Build kuso-server (Go) image with the embedded Next.js SPA bundle.
# Build context MUST be the kuso repo root, not server-go/, because the
# Next.js frontend lives at ../web.
#
#   docker build -f kuso/server-go/Dockerfile -t ghcr.io/sislelabs/kuso-server-go:v0.2.0-dev kuso
#
# Stages:
#   1. web-build   — compiles web/ into /tmp/dist via `next build`
#                    (output: "export"), copying web/out wholesale.
#   2. build       — copies the dist into server-go/internal/web/dist
#                    (which is //go:embed-ed by internal/web) and
#                    runs go build into /out/kuso-server.
#   3. final       — FROM scratch + the static binary + CA bundle.

# syntax=docker/dockerfile:1.7
# ↑ enables RUN --mount=type=cache for buildkit. Without it the cache
# mounts below are syntax errors on older docker frontends.

# ---- 1. web-build --------------------------------------------------------

FROM node:22-bookworm-slim AS web-build
WORKDIR /web

# Copy package metadata first so npm install caches across SPA edits
# that don't touch deps. The cache mount on /root/.npm survives across
# builds; --prefer-offline skips network for already-cached tarballs.
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund --prefer-offline

COPY web/ ./
# next.config.ts has output: "export" — `npm run build` emits the
# static site into web/out/. Stage the result at /tmp/dist so the
# next stage can grab it.
#
# .next/cache holds Next.js's incremental build artifacts. Persisting
# it across builds takes a 30s rebuild down to ~5s when no source
# changed — and even when source did change, Next reuses webpack
# module compilations.
RUN --mount=type=cache,target=/web/.next/cache \
    npm run build && cp -R out /tmp/dist

# ---- 2. go build ---------------------------------------------------------

FROM golang:1.26-bookworm AS build
ARG VERSION=v0.2.0-dev

# server-go/go.mod has `replace` directives pointing at the in-repo
# api/apiv1 and coolify modules. Those paths are relative to
# server-go/, so we lay the workspace out at /workspace/{server-go,
# api/apiv1,coolify} inside the container so the replaces resolve.
# WORKDIR drops into server-go/ so subsequent `go mod download` /
# `go build` invocations target the right module.
#
# Copy go.mod + go.sum files first so npm-style layer caching works:
# a server-go source edit doesn't bust the `go mod download` layer
# unless its dependencies changed.
WORKDIR /workspace
COPY api/apiv1/go.mod api/apiv1/go.sum* ./api/apiv1/
COPY coolify/go.mod coolify/go.sum* ./coolify/
COPY server-go/go.mod server-go/go.sum* ./server-go/

WORKDIR /workspace/server-go
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Now bring in the actual sources for each replaced module + server-go.
COPY api/apiv1/ /workspace/api/apiv1/
COPY coolify/ /workspace/coolify/
COPY server-go/ /workspace/server-go/

# Replace the placeholder web/dist with the real SPA bundle.
COPY --from=web-build /tmp/dist /workspace/server-go/internal/web/dist

# CGO_ENABLED=0 + -trimpath gives a fully static, reproducible binary
# that runs in a `scratch` rootfs.
#
# Cache /go/pkg/mod (module sources) + /root/.cache/go-build (compiled
# package artifacts). On a no-op rebuild this drops `go build` from
# ~50s to ~5s. The cache mounts are tagged so concurrent builds don't
# stomp each other.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/kuso-server \
      ./cmd/kuso-server

# Stage a writable, world-writable data dir to be COPYed into the
# scratch image — scratch has no shell to mkdir at runtime.
RUN mkdir -p /tmp/kuso-data && chmod 777 /tmp/kuso-data

# ---- 3. runtime image ----------------------------------------------------
#
# v0.9.38: switched from `scratch` to `alpine` so the runtime image
# carries postgresql-client. /api/admin/backup invokes pg_dump
# in-process to stream the metadata DB out to the CLI; without the
# binary on $PATH the endpoint 500s. Image grows by ~25 MB. Worth it
# for a working backup story; the alternatives (sidecar, separate
# Job) all add operational complexity.

FROM alpine:3.20
ARG VERSION=v0.2.0-dev

LABEL maintainer="https://kuso.sislelabs.com"
LABEL version=$VERSION
LABEL org.opencontainers.image.source="https://github.com/sislelabs/kuso"
LABEL org.opencontainers.image.description="kuso control-plane — Go HTTP API + embedded Next.js SPA"
LABEL org.opencontainers.image.licenses="AGPL-3.0"

# postgresql16-client carries pg_dump + psql at the version that
# matches the bundled Postgres pod. ca-certificates is needed for
# any outbound TLS (GitHub client + kube apiserver). tzdata is so
# log timestamps render with the operator's TZ if KUSO_TZ is set.
RUN apk add --no-cache postgresql16-client ca-certificates tzdata && \
    addgroup -S kuso && adduser -S -G kuso kuso

COPY --from=build /out/kuso-server /usr/local/bin/kuso-server

# Pre-create the data directory writable by the kuso user. In a
# deployment this is typically a PVC mount; the default lets the
# image run end-to-end without one.
RUN mkdir -p /var/lib/kuso && chown -R kuso:kuso /var/lib/kuso

USER kuso
ENV KUSO_DB_PATH=/var/lib/kuso/kuso.db
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/kuso-server"]
