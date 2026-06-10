# syntax=docker/dockerfile:1.7
#
# Build the standalone Discord incident-bridge bot. This module is NOT in the
# root go.work, so the build context is just this directory and we build with
# the workspace disabled (GOWORK=off) against the module's own go.mod/go.sum.
#
#   docker build -t ghcr.io/sislelabs/kuso-incident-bot:vX.Y.Z build/incident-bot
FROM golang:1.26-alpine AS build
ENV CGO_ENABLED=0 GOWORK=off
WORKDIR /src

# Module graph first so dependency downloads cache across source-only changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/incident-bot .

# Minimal runtime: static binary + CA roots for the HTTPS Discord gateway and
# the (possibly TLS) kuso API.
FROM alpine:3.20
RUN --mount=type=cache,target=/var/cache/apk apk add ca-certificates
COPY --from=build /out/incident-bot /usr/local/bin/incident-bot
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/incident-bot"]
