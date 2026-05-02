# Build kuso-server (Go) image. Phase 0 of the NestJS → Go rewrite.
# See kuso/docs/REWRITE.md.
#
#   docker build -t ghcr.io/sislelabs/kuso-server-go:v0.2.0-dev kuso/server-go
#
# Build context is `kuso/server-go/`; the binary is fully self-contained
# (single-package main, no cgo, embedded VERSION).

FROM golang:1.25-bookworm AS build
ARG VERSION=v0.2.0-dev

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .

# CGO_ENABLED=0 + -trimpath gives a fully static, reproducible binary that
# runs in a `scratch` rootfs.
RUN CGO_ENABLED=0 GOOS=linux \
    go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/kuso-server \
      ./cmd/kuso-server

# Stage a writable, world-writable data dir to be COPYed into the
# scratch image — scratch has no shell to mkdir at runtime.
RUN mkdir -p /tmp/kuso-data && chmod 777 /tmp/kuso-data

FROM scratch
ARG VERSION=v0.2.0-dev

LABEL maintainer="https://kuso.sislelabs.com"
LABEL version=$VERSION
LABEL org.opencontainers.image.source="https://github.com/sislelabs/kuso"
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
