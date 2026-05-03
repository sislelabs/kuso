#!/usr/bin/env bash
# install-cli.sh — one-liner installer for the kuso CLI.
#
#   curl -fsSL https://kuso.sislelabs.com/install-cli.sh | sh
#
# Detects OS + arch, downloads the matching binary from the latest
# GitHub release, and drops it into ~/.local/bin/kuso (or /usr/local/bin
# when run as root).

set -eu

REPO="${KUSO_CLI_REPO:-sislelabs/kuso}"
VERSION="${KUSO_CLI_VERSION:-latest}"

uname_os() {
  case "$(uname -s)" in
    Darwin*) echo "darwin" ;;
    Linux*)  echo "linux" ;;
    *) echo "unsupported"; exit 1 ;;
  esac
}

uname_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) echo "unsupported"; exit 1 ;;
  esac
}

OS="$(uname_os)"
ARCH="$(uname_arch)"

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"tag_name": *"[^"]*"' \
    | head -n1 \
    | sed 's/.*: *"\(.*\)"/\1/')"
fi
if [ -z "$VERSION" ]; then
  echo "error: could not resolve latest version" >&2
  exit 1
fi

URL="https://github.com/${REPO}/releases/download/${VERSION}/kuso-${OS}-${ARCH}"
TMP="$(mktemp)"
echo "==> downloading ${URL}"
curl -fsSL -o "$TMP" "$URL"
chmod +x "$TMP"

if [ "$(id -u)" = "0" ]; then
  DEST="/usr/local/bin/kuso"
else
  DEST="${HOME}/.local/bin/kuso"
  mkdir -p "$(dirname "$DEST")"
fi
mv "$TMP" "$DEST"
echo "==> installed kuso ${VERSION} to ${DEST}"

case ":$PATH:" in
  *":$(dirname "$DEST"):"*) : ;;
  *) echo "==> add this to your shell init: export PATH=\"$(dirname "$DEST"):\$PATH\"" ;;
esac

echo
echo "next: kuso login --api https://your-kuso.example.com -u admin"
