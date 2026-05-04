#!/usr/bin/env sh
# install-cli.sh — one-liner installer for the kuso CLI.
#
#   curl -fsSL https://kuso.sislelabs.com/install-cli.sh | sh
#
# Strategy (in order):
#   1. Try to download a published release binary from GitHub
#      (sislelabs/kuso releases). Binaries are named
#      kuso-${OS}-${ARCH} — produced by hack/release.sh on every
#      tagged version.
#   2. Fall back to `go install kuso/cmd@latest` if Go is on PATH.
#      Reliable because it's just a Go module — same bits, slower.
#   3. Print a manual-build hint if neither works.
#
# Drops the binary into ~/.local/bin/kuso (or /usr/local/bin when run
# as root). Honors KUSO_CLI_VERSION=vX.Y.Z to pin a specific tag.
#
# Why no curl|sudo bash? sudo is only required if you want
# /usr/local/bin (i.e. you're already root). Otherwise ~/.local/bin
# is unprivileged and good enough — and avoids the all-too-common
# pattern of unauthenticated bash piped through sudo.

set -eu

REPO="${KUSO_CLI_REPO:-sislelabs/kuso}"
VERSION="${KUSO_CLI_VERSION:-latest}"

# --- helpers ---------------------------------------------------------

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

uname_os() {
  case "$(uname -s)" in
    Darwin*) echo "darwin" ;;
    Linux*)  echo "linux" ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
}

uname_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
}

OS="$(uname_os)"
ARCH="$(uname_arch)"

if [ "$(id -u)" = "0" ]; then
  DEST="/usr/local/bin/kuso"
else
  DEST="${HOME}/.local/bin/kuso"
  mkdir -p "$(dirname "$DEST")"
fi

resolve_latest() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"tag_name": *"[^"]*"' \
    | head -n1 \
    | sed 's/.*: *"\(.*\)"/\1/' \
    || true
}

# --- step 1: prebuilt binary ----------------------------------------

if [ "$VERSION" = "latest" ]; then
  VERSION="$(resolve_latest)"
fi

ASSET="kuso-${OS}-${ARCH}"
URL=""
if [ -n "${VERSION:-}" ]; then
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
fi

if [ -n "$URL" ]; then
  log "trying ${URL}"
  TMP="$(mktemp)"
  if curl -fsSL -o "$TMP" "$URL" 2>/dev/null && [ -s "$TMP" ]; then
    chmod +x "$TMP"
    mv "$TMP" "$DEST"
    log "installed kuso ${VERSION} to ${DEST}"
  else
    rm -f "$TMP"
    warn "no published binary at ${URL}"
    warn "(asset list: https://github.com/${REPO}/releases/${VERSION:-latest})"
    URL=""
  fi
fi

# --- step 2: go install fallback ------------------------------------

if [ -z "$URL" ]; then
  if command -v go >/dev/null 2>&1; then
    log "no prebuilt binary — falling back to 'go install kuso/cmd@latest'"
    GOBIN="$(dirname "$DEST")"
    export GOBIN
    if go install "github.com/${REPO}/cli/cmd@latest" 2>/dev/null; then
      # `go install` writes the binary as the package's *directory* name
      # (`cmd`) — rename to `kuso`. Quietly tolerate the "already there"
      # case (re-run install, target exists with the same name).
      if [ -f "${GOBIN}/cmd" ] && [ "${GOBIN}/cmd" != "${DEST}" ]; then
        mv -f "${GOBIN}/cmd" "${DEST}"
      fi
      log "installed kuso (go install) to ${DEST}"
    else
      die "go install failed. Try: git clone https://github.com/${REPO} && cd kuso/cli && go build -o ${DEST} ./cmd"
    fi
  else
    die "no Go toolchain and no prebuilt binary. Install Go (https://go.dev/dl/) or download a release manually."
  fi
fi

# --- step 3: PATH check ---------------------------------------------

case ":$PATH:" in
  *":$(dirname "$DEST"):"*) : ;;
  *) warn "$(dirname "$DEST") is not on PATH — add: export PATH=\"$(dirname "$DEST"):\$PATH\"" ;;
esac

echo
echo "Verify:    kuso version"
echo "Next:      kuso login --api https://your-kuso.example.com -u admin"
echo
echo "If your kuso instance still uses the install-default Let's Encrypt"
echo "*staging* cert, run with KUSO_INSECURE=1 until you flip to prod:"
echo "    KUSO_INSECURE=1 kuso login --api https://your-kuso.example.com -u admin"
