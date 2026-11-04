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
#      The binary is checked against the release's SHA256SUMS when
#      that asset exists (releases before it only get a warning).
#   2. Fall back to building the same tag from source if git + Go are
#      on PATH (the CLI module path is `kuso`, so `go install` from
#      GitHub can't resolve it).
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
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
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
EXT=""
[ "$OS" = "windows" ] && EXT=".exe"

if [ "$(id -u)" = "0" ]; then
  DEST="/usr/local/bin/kuso${EXT}"
else
  DEST="${HOME}/.local/bin/kuso${EXT}"
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

ASSET="kuso-${OS}-${ARCH}${EXT}"
URL=""
if [ -n "${VERSION:-}" ]; then
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
fi

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# verify_checksum <file>: dies on a mismatch; warns when the release has
# no SHA256SUMS (published before it existed) or no hashing tool exists.
verify_checksum() {
  SUMS="$(mktemp)"
  if ! curl -fsSL -o "$SUMS" "https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS" 2>/dev/null; then
    rm -f "$SUMS"
    warn "release ${VERSION} has no SHA256SUMS — installing without a checksum check"
    return 0
  fi
  want="$(awk -v a="$ASSET" '$2 == a || $2 == "*"a {print $1}' "$SUMS" | head -n1)"
  rm -f "$SUMS"
  [ -n "$want" ] || die "SHA256SUMS for ${VERSION} has no entry for ${ASSET} — refusing to install"
  got="$(sha256_file "$1")"
  if [ -z "$got" ]; then
    warn "no sha256sum/shasum on PATH — installing without a checksum check"
    return 0
  fi
  [ "$got" = "$want" ] || die "checksum mismatch for ${ASSET}: got ${got}, SHA256SUMS says ${want} — refusing to install"
  log "checksum verified (${got})"
}

if [ -n "$URL" ]; then
  log "trying ${URL}"
  TMP="$(mktemp)"
  if curl -fsSL -o "$TMP" "$URL" 2>/dev/null && [ -s "$TMP" ]; then
    verify_checksum "$TMP" || { rm -f "$TMP"; exit 1; }
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

# --- step 2: build-from-source fallback -----------------------------

if [ -z "$URL" ]; then
  if command -v go >/dev/null 2>&1 && command -v git >/dev/null 2>&1; then
    REF="${VERSION:-main}"
    log "no prebuilt binary — building ${REF} from source"
    SRC="$(mktemp -d)"
    if git clone --quiet --depth 1 --branch "$REF" "https://github.com/${REPO}.git" "$SRC" \
        && (cd "$SRC/cli" && go build -ldflags "-X kuso/cmd/kusoCli/version.ldflagsVersion=${REF}" -o "$DEST" ./cmd); then
      rm -rf "$SRC"
      log "installed kuso ${REF} (built from source) to ${DEST}"
    else
      rm -rf "$SRC"
      die "source build failed. Try: git clone https://github.com/${REPO} && cd kuso/cli && go build -o ${DEST} ./cmd"
    fi
  else
    die "no prebuilt binary, and no git + Go toolchain to build from source. Install Go (https://go.dev/dl/) or download a release manually."
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
