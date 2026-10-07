# Init-container image for strategy=nixpacks builds: bakes the nixpacks
# binary so each build doesn't fetch it from GitHub Releases. Tagged
# with the nixpacks version (see hack/release.sh).
FROM alpine:3.20

ARG NIXPACKS_VERSION=1.41.0
# Set by buildx; release.sh builds amd64, the sandbox builds host-native.
ARG TARGETARCH

RUN apk add --no-cache \
      bash \
      ca-certificates \
      curl \
      git \
      tar \
      xz

RUN case "${TARGETARCH}" in \
      amd64) triple=x86_64-unknown-linux-musl ;; \
      arm64) triple=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL "https://github.com/railwayapp/nixpacks/releases/download/v${NIXPACKS_VERSION}/nixpacks-v${NIXPACKS_VERSION}-${triple}.tar.gz" \
      | tar -xz -C /usr/local/bin nixpacks \
 && chmod +x /usr/local/bin/nixpacks \
 && /usr/local/bin/nixpacks --version

WORKDIR /workspace
