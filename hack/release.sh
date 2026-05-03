#!/usr/bin/env bash
# release.sh — cut a kuso-server release.
#
# What it does (in order):
#   1. validates VERSION (vX.Y.Z) and git working tree (clean unless
#      KUSO_RELEASE_ALLOW_DIRTY=1).
#   2. rewrites server-go/internal/version/VERSION,
#      deploy/server-go.yaml, and hack/install.sh to the new tag.
#   3. builds the web app (pnpm --dir web build) so the embedded
#      _next bundle is up to date.
#   4. cross-builds the server image for linux/amd64 via
#      `docker buildx --platform linux/amd64 --push`. The --platform
#      flag was the historical footgun: a default `docker build` on
#      a Mac produces arm64 and silently breaks every amd64 cluster.
#   5. (optional, KUSO_RELEASE_ROLL=1) ssh into the configured
#      cluster and `kubectl set image` to roll the deployment.
#   6. (optional, KUSO_RELEASE_COMMIT=1) git commit the bumped
#      version files.
#
# Usage:
#   ./hack/release.sh v0.3.5
#   KUSO_RELEASE_ROLL=1 ./hack/release.sh v0.3.5
#   KUSO_RELEASE_ROLL=1 KUSO_RELEASE_COMMIT=1 ./hack/release.sh v0.3.5
#
# Tunables (env):
#   KUSO_RELEASE_HOST    ssh target for rollout (default: kuso.sislelabs.com)
#   KUSO_RELEASE_USER    ssh user (default: root)
#   KUSO_RELEASE_KEY     ssh key path (default: ~/.ssh/keys/hetzner)
#   KUSO_RELEASE_NS      kube namespace (default: kuso)
#   KUSO_RELEASE_DEPLOY  deployment name (default: kuso-server)
#   KUSO_RELEASE_IMAGE   image repo (default: ghcr.io/sislelabs/kuso-server-go)

set -euo pipefail

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi
if ! [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]; then
  echo "error: VERSION must look like vX.Y.Z (got $VERSION)" >&2
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

KUSO_RELEASE_HOST="${KUSO_RELEASE_HOST:-kuso.sislelabs.com}"
KUSO_RELEASE_USER="${KUSO_RELEASE_USER:-root}"
KUSO_RELEASE_KEY="${KUSO_RELEASE_KEY:-$HOME/.ssh/keys/hetzner}"
KUSO_RELEASE_NS="${KUSO_RELEASE_NS:-kuso}"
KUSO_RELEASE_DEPLOY="${KUSO_RELEASE_DEPLOY:-kuso-server}"
KUSO_RELEASE_IMAGE="${KUSO_RELEASE_IMAGE:-ghcr.io/sislelabs/kuso-server-go}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

# ---- 1. preflight --------------------------------------------------

if [[ "${KUSO_RELEASE_ALLOW_DIRTY:-0}" != "1" ]]; then
  if ! git diff --quiet || ! git diff --cached --quiet; then
    fail "working tree is dirty. commit/stash or set KUSO_RELEASE_ALLOW_DIRTY=1"
  fi
fi

CURRENT="$(cat server-go/internal/version/VERSION | tr -d '[:space:]')"
if [[ "$CURRENT" == "$VERSION" ]]; then
  warn "VERSION already at $VERSION — skipping rewrite step"
else
  log "bumping $CURRENT → $VERSION"
fi

# ---- 2. rewrite version files --------------------------------------

if [[ "$CURRENT" != "$VERSION" ]]; then
  printf '%s\n' "$VERSION" > server-go/internal/version/VERSION

  # deploy/server-go.yaml: the image: line carries an explicit tag.
  sed -i.bak \
    "s|kuso-server-go:${CURRENT}|kuso-server-go:${VERSION}|g" \
    deploy/server-go.yaml
  rm deploy/server-go.yaml.bak

  # hack/install.sh: KUSO_SERVER_VERSION default + the sed line that
  # rewrites the deploy yaml during fresh installs.
  sed -i.bak \
    -e "s|KUSO_SERVER_VERSION:-${CURRENT}|KUSO_SERVER_VERSION:-${VERSION}|g" \
    -e "s|kuso-server-go:${CURRENT}|kuso-server-go:${VERSION}|g" \
    -e "s|server image tag (default: ${CURRENT};|server image tag (default: ${VERSION};|g" \
    hack/install.sh
  rm hack/install.sh.bak

  log "rewrote VERSION + deploy/server-go.yaml + hack/install.sh"
fi

# ---- 3. web build --------------------------------------------------

if [[ -d web ]]; then
  log "building web (pnpm --dir web build)"
  if command -v pnpm >/dev/null 2>&1; then
    (cd web && pnpm build >/dev/null) || fail "web build failed"
  else
    warn "pnpm not on PATH — assuming web/dist is already current"
  fi
fi

# ---- 4. cross-build amd64 + push -----------------------------------

log "docker buildx --platform linux/amd64 → ${KUSO_RELEASE_IMAGE}:${VERSION}"
docker buildx build \
  --platform linux/amd64 \
  --push \
  -t "${KUSO_RELEASE_IMAGE}:${VERSION}" \
  -f server-go/Dockerfile \
  . >/dev/null

log "image pushed: ${KUSO_RELEASE_IMAGE}:${VERSION}"

# ---- 4b. release.json + crds.yaml + GH release ---------------------
#
# The kuso self-updater (server-go/internal/updater) reads release.json
# from the latest GitHub release to figure out what to upgrade to and
# whether the CRD changes are auto-applyable. We emit the manifest +
# bundle the CRD YAMLs here so a `gh release create` later (manual or
# CI) attaches both files alongside the docker images.
#
# Migration classification is intentionally trivial today: all changes
# in v0.x are "additive" (preserve-unknown-fields on every CRD).
# When we tighten schemas the manifest can declare specific
# migrations and pre-rewrite scripts.

DIST_DIR="${REPO_ROOT}/dist"
mkdir -p "$DIST_DIR"

OPERATOR_IMAGE="${KUSO_RELEASE_OPERATOR_IMAGE:-ghcr.io/sislelabs/kuso-operator}"

log "writing dist/release.json + dist/crds.yaml for GitHub release"

# Bundle every CRD into a single applyable file. The updater Job's
# entrypoint runs `kubectl apply -f /tmp/crds.yaml` and trusts that
# all of these are safe to re-apply (additive only, today).
{
  for f in operator/config/crd/bases/*.yaml; do
    if [[ -f "$f" ]]; then
      printf -- '---\n'
      cat "$f"
    fi
  done
} > "$DIST_DIR/crds.yaml"

# release.json: stable wire shape consumed by internal/updater.Manifest.
# Keep "additive" as the default migration kind; the moment we ship
# something destructive, this script grows a CRD-diff step.
PUBLISHED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cat > "$DIST_DIR/release.json" <<EOF
{
  "version": "${VERSION}",
  "publishedAt": "${PUBLISHED_AT}",
  "components": {
    "server":   { "image": "${KUSO_RELEASE_IMAGE}:${VERSION}" },
    "operator": { "image": "${OPERATOR_IMAGE}:${VERSION}" }
  },
  "crds": {
    "url": "https://github.com/${KUSO_RELEASE_REPO:-sislelabs/kuso}/releases/download/${VERSION}/crds.yaml",
    "minServer": "v0.4.0",
    "migrations": []
  },
  "breaking": false
}
EOF
log "wrote ${DIST_DIR}/release.json"

# Optionally cut the GH release. Off by default so iteration doesn't
# spam tags; turn on with KUSO_RELEASE_GH=1 once a tag is real.
if [[ "${KUSO_RELEASE_GH:-0}" == "1" ]]; then
  if ! command -v gh >/dev/null 2>&1; then
    warn "gh not installed — skipping GitHub release; upload dist/* manually"
  else
    log "creating GitHub release ${VERSION}"
    NOTES_FILE="$(mktemp)"
    git log --pretty=format:'- %s' "$(git describe --tags --abbrev=0 2>/dev/null || echo HEAD)..HEAD" > "$NOTES_FILE" || true
    gh release create "$VERSION" \
      --title "$VERSION" \
      --notes-file "$NOTES_FILE" \
      "$DIST_DIR/release.json" \
      "$DIST_DIR/crds.yaml" >/dev/null
    rm -f "$NOTES_FILE"
    log "GitHub release ${VERSION} published with release.json + crds.yaml"
  fi
fi

# ---- 5. optional rollout -------------------------------------------

if [[ "${KUSO_RELEASE_ROLL:-0}" == "1" ]]; then
  log "rolling deploy/${KUSO_RELEASE_DEPLOY} on ${KUSO_RELEASE_HOST}"
  # accept-new auto-trusts a previously-unknown host on first contact
  # so the script doesn't wedge waiting for an interactive yes/no.
  # The known_hosts file still gets the entry — second run is fully
  # verified. Don't disable host key checking entirely; that opens us
  # up to MITM on every subsequent run.
  ssh -i "$KUSO_RELEASE_KEY" \
    -o StrictHostKeyChecking=accept-new \
    "${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}" \
    "kubectl set image -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} server=${KUSO_RELEASE_IMAGE}:${VERSION} && kubectl rollout status -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} --timeout=180s"

  # Verify /healthz reports the new version. Curl through the public
  # hostname so we exercise traefik + cert + the routed path.
  if command -v curl >/dev/null 2>&1; then
    HEALTH="$(curl -s "https://${KUSO_RELEASE_HOST}/healthz" || true)"
    if [[ "$HEALTH" == *"\"version\":\"${VERSION}\""* ]]; then
      log "verified: /healthz reports ${VERSION}"
    else
      warn "/healthz returned: $HEALTH"
    fi
  fi
fi

# ---- 6. optional commit --------------------------------------------

if [[ "${KUSO_RELEASE_COMMIT:-0}" == "1" ]]; then
  if git diff --quiet -- server-go/internal/version/VERSION deploy/server-go.yaml hack/install.sh; then
    warn "no version-file changes to commit"
  else
    git add server-go/internal/version/VERSION deploy/server-go.yaml hack/install.sh
    git commit -m "release: ${VERSION}" >/dev/null
    log "committed: release: ${VERSION}"
  fi
fi

log "done — ${VERSION}"
