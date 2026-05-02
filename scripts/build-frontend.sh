#!/usr/bin/env bash
# Builds the Next.js frontend and rsyncs it into server-go/internal/web/dist/
# so the Go binary can embed it. Used by CI, by the Dockerfile's web-build
# stage, and by hand for ad-hoc local builds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/web"

echo "==> installing web/ deps"
npm ci --no-audit --no-fund

echo "==> building web/ (next export)"
npm run build

echo "==> syncing web/out -> server-go/internal/web/dist"
DEST="$ROOT/server-go/internal/web/dist"
# Preserve the .gitkeep marker so the directory survives a clean checkout.
mkdir -p "$DEST"
find "$DEST" -mindepth 1 -not -name '.gitkeep' -delete
cp -R "$ROOT/web/out/." "$DEST/"

echo "==> done"
