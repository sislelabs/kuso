#!/usr/bin/env bash
# release.sh — cut a kuso-server release.
#
# A release does ONE thing: produce a versioned bundle (image on ghcr +
# CLI binaries + crds.yaml + release.json) and publish a GitHub
# release. It does NOT roll any cluster. Live kuso instances poll the
# GH releases endpoint and pull themselves forward through their own
# in-cluster updater (`kuso upgrade`, the dashboard's Update button,
# or the auto-update setting). That keeps the release flow agnostic
# about who's running kuso and where.
#
# What it does (in order):
#   1. validates VERSION (vX.Y.Z) and git working tree (clean unless
#      KUSO_RELEASE_ALLOW_DIRTY=1).
#   2. rewrites server-go/internal/version/VERSION,
#      deploy/server-go.yaml, hack/install.sh, cli/.../CLI_VERSION
#      to the new tag.
#   2b. regenerates CHANGELOG.md via git-cliff (if installed).
#   3. builds the web app (pnpm/npm) so the embedded _next bundle
#      is up to date.
#   3b. syncs hack/install*.sh into the server-go embed dir.
#   4. cross-builds the server image for linux/amd64 via
#      `docker buildx --platform linux/amd64 --push`. The --platform
#      flag was the historical footgun: a default `docker build` on
#      a Mac produces arm64 and silently breaks every amd64 cluster.
#   4b. emits dist/release.json + dist/crds.yaml.
#   4c. cross-builds CLI binaries (darwin/linux × amd64/arm64).
#   4d. (KUSO_RELEASE_GH=1) cuts the GitHub release with all assets.
#   5. (DEV ONLY, KUSO_RELEASE_ROLL=1) ssh into the configured cluster
#      and `kubectl set image`. Used by `make local-roll` for the dev
#      test cluster; production clusters self-update via the updater
#      and should NEVER be in this path.
#   6. (KUSO_RELEASE_COMMIT=1) git commit + tag + push the version
#      bumps so install.sh-on-main and KUSO_REF=vX.Y.Z both resolve.
#
# Usage:
#   ./hack/release.sh v0.7.13
#   KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh v0.7.13
#   ./hack/release.sh --dry-run v0.7.13
#
# Tunables (env):
#   KUSO_RELEASE_GH=1         publish a GH release with all assets
#   KUSO_RELEASE_CLI=1        cross-build the CLI binaries
#   KUSO_RELEASE_COMMIT=1     git commit + tag + push the bumps
#   KUSO_RELEASE_PUSH=0       skip the git push (for local testing)
#   KUSO_RELEASE_OPERATOR=1   force operator image rebuild even if
#                             operator/ didn't change
#
#   Local dev escape hatch (almost never use these):
#   KUSO_RELEASE_ROLL=1       ssh + kubectl set image after publish.
#                             Bypasses the self-update path; use only
#                             when iterating on the updater itself.
#   KUSO_RELEASE_HOST         ssh target for ROLL (default: kuso.sislelabs.com)
#   KUSO_RELEASE_USER         ssh user (default: root)
#   KUSO_RELEASE_KEY          ssh key (default: ~/.ssh/keys/hetzner)
#   KUSO_RELEASE_NS           kube namespace (default: kuso)
#   KUSO_RELEASE_DEPLOY       deployment name (default: kuso-server)
#   KUSO_RELEASE_IMAGE        image repo (default: ghcr.io/sislelabs/kuso-server-go)
#   KUSO_RELEASE_SKIP_BUILD=1 skip docker push (paired with ROLL when
#                             you just want to flip a cluster to an
#                             already-released tag)

set -euo pipefail

VERSION=""
DRY_RUN=0
# Parse args. Flags can come before or after VERSION; flag-style is
# the modern convention but bare positional VERSION still works for
# muscle memory + the Makefile shim.
while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--dry-run) DRY_RUN=1; shift ;;
    -h|--help)
      cat <<EOF
usage: $0 [--dry-run] vX.Y.Z

Cuts a kuso release.

  --dry-run   don't push docker images, don't kubectl, don't gh release,
              don't git push. Logs every side-effecting step as
              [DRY-RUN]. Safe to run with no creds.

Env knobs (see release.sh header for the full list):
  KUSO_RELEASE_ROLL=1       roll the live cluster after build
  KUSO_RELEASE_COMMIT=1     git commit the version bumps
  KUSO_RELEASE_GH=1         gh release create with all artifacts
  KUSO_RELEASE_CLI=1        cross-build CLI binaries
  KUSO_RELEASE_PUSH=0       skip git push of commit + tag
  KUSO_RELEASE_OPERATOR=1   force-rebuild the operator image
EOF
      exit 0
      ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *)  if [[ -z "$VERSION" ]]; then VERSION="$1"; shift; else echo "extra arg: $1" >&2; exit 2; fi ;;
  esac
done
if [[ -z "$VERSION" ]]; then
  echo "usage: $0 [--dry-run] vX.Y.Z" >&2
  exit 2
fi
if ! [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]; then
  echo "error: VERSION must look like vX.Y.Z (got $VERSION)" >&2
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if [[ "$DRY_RUN" == "1" ]]; then
  printf '\033[1;35m=================================================\n'
  printf '          DRY RUN — no side effects will fire\n'
  printf '=================================================\033[0m\n'
fi

KUSO_RELEASE_HOST="${KUSO_RELEASE_HOST:-kuso.sislelabs.com}"
KUSO_RELEASE_USER="${KUSO_RELEASE_USER:-root}"
KUSO_RELEASE_KEY="${KUSO_RELEASE_KEY:-$HOME/.ssh/keys/hetzner}"
KUSO_RELEASE_NS="${KUSO_RELEASE_NS:-kuso}"
KUSO_RELEASE_DEPLOY="${KUSO_RELEASE_DEPLOY:-kuso-server}"
KUSO_RELEASE_IMAGE="${KUSO_RELEASE_IMAGE:-ghcr.io/sislelabs/kuso-server-go}"
# Operator deploy lives in its own namespace (operator-sdk default).
# Container in the deployment is named "manager" by convention.
KUSO_OPERATOR_NS="${KUSO_OPERATOR_NS:-kuso-operator-system}"
KUSO_OPERATOR_DEPLOY="${KUSO_OPERATOR_DEPLOY:-kuso-operator-controller-manager}"
KUSO_OPERATOR_CONTAINER="${KUSO_OPERATOR_CONTAINER:-manager}"
OPERATOR_IMAGE="${KUSO_RELEASE_OPERATOR_IMAGE:-ghcr.io/sislelabs/kuso-operator}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }
dry()  { printf '\033[1;35m[DRY-RUN]\033[0m %s\n' "$*"; }

# run executes its args unless DRY_RUN=1, in which case it just prints
# what would have happened. Cheap argv shell-escape (printf %q so the
# user can copy-paste the line and re-run by hand). Used to gate every
# real side effect (docker push, kubectl, gh release, git push) behind
# the --dry-run flag.
run() {
  if [[ "$DRY_RUN" == "1" ]]; then
    local q=""
    for a in "$@"; do q+=" $(printf '%q' "$a")"; done
    dry "$q"
    return 0
  fi
  "$@"
}

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
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "rewrite server-go/internal/version/VERSION + deploy/server-go.yaml + hack/install.sh + cli/{cmd/kusoCli/version/CLI_VERSION,pkg/kusoApi/VERSION}: ${CURRENT} → ${VERSION}"
  else
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

    # CLI VERSION embeds (kept in sync so dev builds without ldflags
    # report the right version).
    printf '%s\n' "$VERSION" > cli/cmd/kusoCli/version/CLI_VERSION
    printf '%s\n' "$VERSION" > cli/pkg/kusoApi/VERSION

    log "rewrote VERSION + deploy/server-go.yaml + hack/install.sh + CLI VERSIONs"
  fi
fi

# ---- 2b. CHANGELOG -------------------------------------------------
#
# git-cliff regenerates CHANGELOG.md from commit history. We tell it
# the in-flight version so the Unreleased section becomes [VERSION].
# Skipped silently if git-cliff isn't on PATH — the release still ships,
# just without an updated changelog. Install with `brew install git-cliff`
# / `cargo install git-cliff`.

if [[ -f cliff.toml ]] && command -v git-cliff >/dev/null 2>&1; then
  log "regenerating CHANGELOG.md (git-cliff)"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "git-cliff -c cliff.toml --tag $VERSION -o CHANGELOG.md"
  else
    git-cliff -c cliff.toml --tag "$VERSION" -o CHANGELOG.md >/dev/null
  fi
elif [[ -f cliff.toml ]]; then
  warn "cliff.toml present but git-cliff not on PATH — skipping CHANGELOG regen (brew install git-cliff)"
fi

# ---- 3. web build --------------------------------------------------

if [[ -d web ]]; then
  log "building web"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "(cd web && (pnpm build || npm run build)) → server-go/internal/web/dist/"
  elif command -v pnpm >/dev/null 2>&1; then
    (cd web && pnpm build >/dev/null) || fail "web build failed (pnpm)"
  elif command -v npm >/dev/null 2>&1; then
    # CI path: web/ is npm-managed (package-lock.json), so npm is what
    # GH Actions has wired up after `npm ci`. `npm run build` calls the
    # same `next build` script that `pnpm build` does.
    (cd web && npm run build --silent >/dev/null) || fail "web build failed (npm)"
  else
    warn "neither pnpm nor npm on PATH — assuming web/dist is already current"
  fi
fi

# ---- 3b. sync install scripts into the server-go embed -------------
#
# server-go embeds hack/install.sh and hack/install-cli.sh so a running
# instance can serve them at /install.sh and /install-cli.sh — bypassing
# the 5-minute raw.githubusercontent.com cache. Go's go:embed can't
# reach into ../../hack/, so we copy them into the embed dir first.
log "syncing install scripts into server-go embed"
if [[ "$DRY_RUN" == "1" ]]; then
  dry "cp hack/install{,-cli}.sh server-go/internal/installscripts/scripts/"
else
  cp hack/install.sh server-go/internal/installscripts/scripts/install.sh
  cp hack/install-cli.sh server-go/internal/installscripts/scripts/install-cli.sh
fi

# ---- 4. cross-build amd64 + push -----------------------------------
#
# Skipped when KUSO_RELEASE_SKIP_BUILD=1 — used by `make roll` when CI
# already built + pushed the image and we just need to flip the live
# cluster to it.

if [[ "${KUSO_RELEASE_SKIP_BUILD:-0}" != "1" ]]; then
  log "docker buildx --platform linux/amd64 → ${KUSO_RELEASE_IMAGE}:${VERSION}"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "docker buildx build --platform linux/amd64 --push -t ${KUSO_RELEASE_IMAGE}:${VERSION} -f server-go/Dockerfile ."
  else
    docker buildx build \
      --platform linux/amd64 \
      --push \
      -t "${KUSO_RELEASE_IMAGE}:${VERSION}" \
      -f server-go/Dockerfile \
      . >/dev/null
  fi
else
  log "skipping docker build (KUSO_RELEASE_SKIP_BUILD=1) — assuming ${KUSO_RELEASE_IMAGE}:${VERSION} already on registry"
fi

if [[ "$DRY_RUN" != "1" ]]; then log "image pushed: ${KUSO_RELEASE_IMAGE}:${VERSION}"; fi

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

# OPERATOR_IMAGE is defined at the top with the other defaults so both
# the GitHub-release writer below + the rollout step in §5b share the
# same value.

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

# ---- 4c. CLI binaries ----------------------------------------------
#
# install-cli.sh tries to download these from the GitHub release. Build
# them now (cross-compile, no docker) so they're ready for the
# `gh release create` upload below. Skipped silently when go isn't on
# PATH — the install-cli.sh fallback will go-install from source.

KUSO_RELEASE_CLI="${KUSO_RELEASE_CLI:-1}"
if [[ "${KUSO_RELEASE_CLI}" == "1" ]] && command -v go >/dev/null 2>&1; then
  log "cross-building CLI binaries (darwin/linux × amd64/arm64)"
  CLI_LDFLAGS="-X kuso/cmd/kusoCli/version.ldflagsVersion=${VERSION}"
  if [[ "$DRY_RUN" == "1" ]]; then
    for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
      dry "(cd cli && GOOS=${target%-*} GOARCH=${target#*-} go build -ldflags='$CLI_LDFLAGS' -o ${DIST_DIR}/kuso-${target} ./cmd)"
    done
  else
  for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
    GOOS="${target%-*}"
    GOARCH="${target#*-}"
    out="${DIST_DIR}/kuso-${target}"
    (cd cli && GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -ldflags="$CLI_LDFLAGS" -o "$out" ./cmd) \
      || fail "CLI build failed for ${target}"
  done
  ls -lh "$DIST_DIR"/kuso-* | awk '{print "    " $5 "  " $9}'
  fi  # close the dry-run branch
else
  warn "go not on PATH (or KUSO_RELEASE_CLI=0) — skipping CLI binaries; install-cli.sh will fall back to source"
fi

# Optionally cut the GH release. Off by default so iteration doesn't
# spam tags; turn on with KUSO_RELEASE_GH=1 once a tag is real.
if [[ "${KUSO_RELEASE_GH:-0}" == "1" ]]; then
  if ! command -v gh >/dev/null 2>&1; then
    warn "gh not installed — skipping GitHub release; upload dist/* manually"
  else
    log "creating GitHub release ${VERSION}"
    NOTES_FILE="$(mktemp)"
    # Prefer git-cliff's per-version slice if available (matches what
    # CHANGELOG.md will show); fall back to a flat `git log` between
    # tags. Either way the body is markdown gh accepts.
    if command -v git-cliff >/dev/null 2>&1 && [[ -f cliff.toml ]]; then
      # --unreleased prints commits since the most recent tag,
      # annotated as if they're $VERSION via --tag. Works regardless
      # of whether $VERSION's tag has been created yet (it usually
      # hasn't at this point in the script — gh release create makes
      # it).
      if ! git-cliff -c cliff.toml --unreleased --strip header --tag "$VERSION" > "$NOTES_FILE" 2>/dev/null; then
        git log --pretty=format:'- %s' "$(git describe --tags --abbrev=0 2>/dev/null || echo HEAD)..HEAD" > "$NOTES_FILE" || true
      fi
    else
      git log --pretty=format:'- %s' "$(git describe --tags --abbrev=0 2>/dev/null || echo HEAD)..HEAD" > "$NOTES_FILE" || true
    fi
    # Collect CLI assets if they exist; the * glob would fail-fast under
    # `set -e` if dist/kuso-* is empty, so check first.
    CLI_ASSETS=()
    for f in "$DIST_DIR"/kuso-darwin-arm64 "$DIST_DIR"/kuso-darwin-amd64 \
             "$DIST_DIR"/kuso-linux-amd64 "$DIST_DIR"/kuso-linux-arm64; do
      [[ -f "$f" ]] && CLI_ASSETS+=("$f")
    done
    if [[ "$DRY_RUN" == "1" ]]; then
      dry "gh release create $VERSION --title $VERSION --notes-file <…> $DIST_DIR/release.json $DIST_DIR/crds.yaml ${CLI_ASSETS[*]}"
    else
      gh release create "$VERSION" \
        --title "$VERSION" \
        --notes-file "$NOTES_FILE" \
        "$DIST_DIR/release.json" \
        "$DIST_DIR/crds.yaml" \
        "${CLI_ASSETS[@]}" >/dev/null
    fi
    rm -f "$NOTES_FILE"
    log "GitHub release ${VERSION} published (release.json + crds.yaml + ${#CLI_ASSETS[@]} CLI assets)"
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
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "ssh ${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST} 'kubectl set image -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} server=${KUSO_RELEASE_IMAGE}:${VERSION} && kubectl rollout status …'"
  else
    ssh -i "$KUSO_RELEASE_KEY" \
      -o StrictHostKeyChecking=accept-new \
      "${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}" \
      "kubectl set image -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} server=${KUSO_RELEASE_IMAGE}:${VERSION} && kubectl rollout status -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} --timeout=180s"
  fi

  # Verify /healthz reports the new version. Curl through the public
  # hostname so we exercise traefik + cert + the routed path. Skipped
  # in dry-run since no rollout actually fired.
  if [[ "$DRY_RUN" != "1" ]] && command -v curl >/dev/null 2>&1; then
    HEALTH="$(curl -s "https://${KUSO_RELEASE_HOST}/healthz" || true)"
    if [[ "$HEALTH" == *"\"version\":\"${VERSION}\""* ]]; then
      log "verified: /healthz reports ${VERSION}"
    else
      warn "/healthz returned: $HEALTH"
    fi
  fi
fi

# ---- 5b. operator image + CRDs (auto when operator/ changed) -----
#
# Detects whether operator/ changed since the last git tag. When it
# did, rebuilds the operator image, scps the CRDs, kubectl applies
# them, and rolls the operator deployment. Skipped when nothing
# under operator/ has changed.
#
# Override: KUSO_RELEASE_OPERATOR=1 forces the operator step even
# when git diff is empty (useful for explicit re-rolls, e.g. when
# you pulled the chart from another branch). KUSO_RELEASE_OPERATOR=0
# skips it explicitly.

operator_should_build() {
  if [[ "${KUSO_RELEASE_OPERATOR:-auto}" == "1" ]]; then
    return 0
  fi
  if [[ "${KUSO_RELEASE_OPERATOR:-auto}" == "0" ]]; then
    return 1
  fi
  # auto: did anything under operator/ change since the last tag?
  local last
  last="$(git describe --tags --abbrev=0 2>/dev/null || echo)"
  if [[ -z "$last" ]]; then
    # No previous tag — no baseline to diff against. Build to be
    # safe; the alternative is shipping a kuso-server that depends
    # on an operator chart the running operator doesn't have.
    return 0
  fi
  if ! git diff --quiet "$last"..HEAD -- operator/; then
    return 0
  fi
  return 1
}

# Bump the operator image tag using a parallel scheme to the kuso-
# server tag. Pattern: vMAJOR.MINOR.PATCH on kuso-server →
# vMAJOR.OPATCH on operator, where OPATCH starts at the kuso-server
# minor and increments per release that touches operator/. We don't
# enforce this — the operator just gets the same VERSION tag for
# simplicity. Sub-version mismatch only breaks if someone manually
# pins; we don't, the deploy yaml uses VERSION verbatim.
OPERATOR_VERSION="${KUSO_RELEASE_OPERATOR_VERSION:-${VERSION}}"

if [[ "${KUSO_RELEASE_ROLL:-0}" == "1" ]] && operator_should_build; then
  log "operator/ changed — building operator image ${OPERATOR_IMAGE}:${OPERATOR_VERSION}"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "docker buildx build --platform linux/amd64 --push -t ${OPERATOR_IMAGE}:${OPERATOR_VERSION} -f operator/Dockerfile operator"
  else
    docker buildx build \
      --platform linux/amd64 \
      --push \
      -t "${OPERATOR_IMAGE}:${OPERATOR_VERSION}" \
      -f operator/Dockerfile \
      operator >/dev/null
  fi
  log "operator image pushed: ${OPERATOR_IMAGE}:${OPERATOR_VERSION}"

  # Ship every CRD under operator/config/crd/bases/ to /tmp on the
  # cluster, kubectl apply them, then roll the operator deployment.
  # We don't filter to "only the changed CRD files" — applying an
  # unchanged CRD is a no-op (`unchanged` in kubectl output), so the
  # complexity isn't worth it.
  log "scp + apply CRDs"
  CRD_FILES=( operator/config/crd/bases/*.yaml )
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "scp ${CRD_FILES[*]} ${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}:/tmp/"
  else
    scp -i "$KUSO_RELEASE_KEY" \
      -o StrictHostKeyChecking=accept-new \
      "${CRD_FILES[@]}" \
      "${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}:/tmp/" >/dev/null
  fi

  # Build the kubectl apply args list dynamically — one -f per CRD.
  REMOTE_FLAGS=""
  for f in "${CRD_FILES[@]}"; do
    REMOTE_FLAGS="${REMOTE_FLAGS} -f /tmp/$(basename "$f")"
  done

  if [[ "$DRY_RUN" == "1" ]]; then
    dry "ssh ${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST} 'kubectl apply${REMOTE_FLAGS} && kubectl set image -n ${KUSO_OPERATOR_NS} deploy/${KUSO_OPERATOR_DEPLOY} ${KUSO_OPERATOR_CONTAINER}=${OPERATOR_IMAGE}:${OPERATOR_VERSION} && kubectl rollout status …'"
  else
    ssh -i "$KUSO_RELEASE_KEY" \
      -o StrictHostKeyChecking=accept-new \
      "${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}" \
      "kubectl apply ${REMOTE_FLAGS} && \
       kubectl set image -n ${KUSO_OPERATOR_NS} deploy/${KUSO_OPERATOR_DEPLOY} ${KUSO_OPERATOR_CONTAINER}=${OPERATOR_IMAGE}:${OPERATOR_VERSION} && \
       kubectl rollout status -n ${KUSO_OPERATOR_NS} deploy/${KUSO_OPERATOR_DEPLOY} --timeout=180s"
  fi
  log "operator rolled to ${OPERATOR_VERSION}"
fi

# ---- 6. commit + tag + push ----------------------------------------
#
# install.sh on `main` pulls CRDs from KUSO_REF (default "main"), so the
# manifests must actually exist on `main` after a release. We also push
# a git tag so anyone wanting to pin can `KUSO_REF=v0.7.10` and have it
# resolve. Skipping these is what bricked the v0.7.x installs (CRDs
# 404'd because tags were never pushed).

if [[ "${KUSO_RELEASE_COMMIT:-0}" == "1" ]]; then
  COMMIT_FILES=(
    server-go/internal/version/VERSION
    deploy/server-go.yaml
    hack/install.sh
    cli/cmd/kusoCli/version/CLI_VERSION
    cli/pkg/kusoApi/VERSION
  )
  # Include CHANGELOG.md if git-cliff regenerated it (see step 4d).
  [[ -f CHANGELOG.md ]] && COMMIT_FILES+=(CHANGELOG.md)

  if git diff --quiet -- "${COMMIT_FILES[@]}"; then
    warn "no version-file changes to commit"
  else
    if [[ "$DRY_RUN" == "1" ]]; then
      dry "git add ${COMMIT_FILES[*]} && git commit -m 'release: ${VERSION}'"
    else
      git add "${COMMIT_FILES[@]}"
      git commit -m "release: ${VERSION}" >/dev/null
    fi
    log "committed: release: ${VERSION}"
  fi

  if git rev-parse "${VERSION}" >/dev/null 2>&1; then
    warn "tag ${VERSION} already exists — skipping tag"
  else
    if [[ "$DRY_RUN" == "1" ]]; then
      dry "git tag -a ${VERSION} -m 'release ${VERSION}'"
    else
      git tag -a "${VERSION}" -m "release ${VERSION}"
    fi
    log "tagged: ${VERSION}"
  fi

  if [[ "${KUSO_RELEASE_PUSH:-1}" == "1" ]]; then
    if [[ "$DRY_RUN" == "1" ]]; then
      dry "git push origin HEAD && git push origin ${VERSION}"
    else
      git push origin HEAD
      git push origin "${VERSION}" || warn "tag push failed (already on remote?)"
    fi
    log "pushed commit + tag to origin"
  else
    warn "KUSO_RELEASE_PUSH=0 — commit + tag NOT pushed; install.sh on main will be stale"
  fi
fi

log "done — ${VERSION}"
