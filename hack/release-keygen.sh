#!/usr/bin/env bash
# Generate an Ed25519 keypair for signing kuso release manifests.
#
# Workflow:
#   1. Run this script once per project install. It writes:
#        server-go/internal/updater/releasekey.pub  (committed to git)
#        ~/.kuso/release-keys/release.pem           (NEVER committed)
#   2. Commit the updated releasekey.pub. Future builds will refuse
#      unsigned releases (or releases signed by a different key).
#   3. release.sh reads ~/.kuso/release-keys/release.pem to sign
#      manifests. A built binary verifies against the embedded pub.
#
# Key rotation: re-run this script, commit the new pub, ship a release
# signed with the old key one last time so existing instances can
# update to a build that contains the new pub. Then sign all future
# releases with the new private key.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PUB_DST="$REPO_ROOT/server-go/internal/updater/releasekey.pub"
# Must match where release.sh looks for the signing key
# (KUSO_KEYS_DIR/release.pem) — a key generated anywhere else is
# silently never used for signing.
KEY_DIR="${KUSO_KEYS_DIR:-$HOME/.kuso/release-keys}"
PRIV_DST="$KEY_DIR/release.pem"

mkdir -p "$KEY_DIR"
chmod 700 "$KEY_DIR"

if [[ -s "$PRIV_DST" ]]; then
  # Never overwrite an existing private key, but DO re-derive and
  # embed its public half — recovers the "key exists but
  # releasekey.pub was never committed" state idempotently.
  echo "private key already exists at $PRIV_DST — re-deriving public key only"
  openssl pkey -in "$PRIV_DST" -pubout -outform DER \
    | tail -c 32 \
    | base64 \
    | tr -d '\n' > "$PUB_DST"
  echo "wrote public key: $PUB_DST  (commit this)"
  echo "to rotate the private key, delete it manually first."
  exit 0
fi

# OpenSSL 3.0+ ships Ed25519 support out of the box. The raw private key
# is 32 bytes; we encode it as PEM so the same format works for both
# openssl signing and the future Go signing helper.
openssl genpkey -algorithm ed25519 -out "$PRIV_DST"
chmod 600 "$PRIV_DST"

# Extract the public key as raw 32 bytes, base64-encode for the embed.
openssl pkey -in "$PRIV_DST" -pubout -outform DER \
  | tail -c 32 \
  | base64 \
  | tr -d '\n' > "$PUB_DST"

echo "wrote private key: $PRIV_DST  (chmod 600, never commit)"
echo "wrote public key:  $PUB_DST  (commit this)"
echo
echo "Next:"
echo "  1. git add $PUB_DST && git commit -m 'release: rotate signing key'"
echo "  2. Update hack/release.sh to sign release.json with $PRIV_DST"
echo "  3. Ship the next release; future installs verify against the embedded pub."
