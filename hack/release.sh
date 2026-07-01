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
#   KUSO_RELEASE_BREAKING=1   force release.json breaking=true (override
#                             the conventional-commits scan; rare but
#                             useful when prior commits lacked markers)
#   KUSO_RELEASE_BREAKING=0   force release.json breaking=false
#   KUSO_RELEASE_SKIP_VISIBILITY_CHECK=1
#                             skip the post-push ghcr public-pull check.
#                             Use when the target cluster has a private-
#                             registry pull-secret wired up (rare).
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

# latest_ghcr_tag <repo> — print the most recent vX.Y.Z tag for an
# org/repo container package on ghcr (anonymous read). Empty string
# on any failure (no curl, network, malformed JSON, no tags).
# Uses an external python script (hack/ghcr-latest-tag.py) so we
# don't have to escape multi-line python through bash heredocs —
# that's the path that broke when this was inlined.
latest_ghcr_tag() {
  local repo="$1"
  local script="${REPO_ROOT}/hack/ghcr-latest-tag.py"
  if ! command -v curl >/dev/null || ! command -v python3 >/dev/null; then
    return 0
  fi
  if [[ ! -x "$script" ]]; then
    return 0
  fi
  local tok
  tok="$(curl -sf "https://ghcr.io/token?scope=repository:${repo}:pull&service=ghcr.io" \
    | python3 -c 'import sys,json; print(json.load(sys.stdin).get("token",""))' 2>/dev/null || true)"
  [[ -z "$tok" ]] && return 0
  # GHCR's /tags/list returns tags OLDEST-first and, without ?n=, only a
  # bounded first page. A naive single fetch on a repo with many tags
  # returns only the oldest page, so the "latest" picker lands on a stale
  # tag (this is how release.json once pinned the operator to an OLD
  # version → a downgrade). Walk every page via the RFC 5988
  # `Link: …; rel="next"` header, merge all tags into one JSON blob, then
  # let the python sorter pick the highest semver. `?n=1000` also keeps
  # the page count tiny for the foreseeable tag volume.
  python3 - "$repo" "$tok" "$script" <<'PYGHCR' 2>/dev/null || true
import json, re, subprocess, sys, urllib.request

repo, tok, script = sys.argv[1], sys.argv[2], sys.argv[3]
tags, url = [], f"https://ghcr.io/v2/{repo}/tags/list?n=1000"
seen_pages = 0
while url and seen_pages < 50:  # 50-page hard stop guards a pathological loop
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {tok}"})
    try:
        resp = urllib.request.urlopen(req, timeout=15)
    except Exception:
        break
    body = resp.read()
    try:
        tags.extend(json.loads(body).get("tags", []) or [])
    except json.JSONDecodeError:
        break
    seen_pages += 1
    # Follow Link: <…>; rel="next" if present.
    link = resp.headers.get("Link", "")
    m = re.search(r'<([^>]+)>;\s*rel="next"', link)
    if not m:
        break
    nxt = m.group(1)
    url = nxt if nxt.startswith("http") else f"https://ghcr.io{nxt}"

# Hand the merged tag list to the existing semver sorter so the picking
# logic stays in one place.
proc = subprocess.run(["python3", script], input=json.dumps({"tags": tags}).encode(),
                      capture_output=True)
sys.stdout.write(proc.stdout.decode())
PYGHCR
}
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

    # deploy/kuso-activator.yaml: runs the same kuso-server-go image in
    # --activator mode, so its tag must move in lockstep. Tolerant regex
    # (any prior version) so it can't go stale the way KUSO_SERVER_VERSION
    # once did.
    sed -i.bak \
      -E "s|kuso-server-go:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-server-go:${VERSION}|g" \
      deploy/kuso-activator.yaml
    rm deploy/kuso-activator.yaml.bak

    # deploy/operator.yaml: same shape — the image: line is the
    # source of truth for `kubectl apply -f deploy/` direct users.
    # Pre-v0.9.4 this was frozen at v0.1.0-dev for releases on end.
    sed -i.bak \
      -E "s|kuso-operator:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-operator:${VERSION}|g" \
      deploy/operator.yaml
    rm deploy/operator.yaml.bak

    # hack/install.sh: KUSO_SERVER_VERSION default + KUSO_VERSION
    # (operator pin) default + the sed line that rewrites the deploy
    # yaml during fresh installs.
    #
    # CRITICAL: match whatever the file CURRENTLY holds, not ${CURRENT}.
    # The version-default lines drift out of lockstep with
    # server-go/internal/version/VERSION (the source of ${CURRENT}) —
    # e.g. KUSO_SERVER_VERSION sat at v0.17.0 for many releases while
    # CURRENT marched on, so an `…:-${CURRENT}` anchored sed never
    # matched and the default went stale → fresh installs pulled an old
    # server image and the install's `kubectl wait` timed out. The
    # tolerant `v[0-9]…` regex (same as the KUSO_VERSION line) catches
    # any prior value, so the default is always rewritten to ${VERSION}.
    sed -i.bak \
      -e "s|KUSO_SERVER_VERSION=\"\${KUSO_SERVER_VERSION:-v[0-9][0-9.]*[a-zA-Z0-9.-]*}\"|KUSO_SERVER_VERSION=\"\${KUSO_SERVER_VERSION:-${VERSION}}\"|g" \
      -e "s|KUSO_VERSION=\"\${KUSO_VERSION:-v[0-9][0-9.]*[a-zA-Z0-9.-]*}\"|KUSO_VERSION=\"\${KUSO_VERSION:-${VERSION}}\"|g" \
      -e "s|kuso-server-go:v[0-9][0-9.]*[a-zA-Z0-9.-]*|kuso-server-go:${VERSION}|g" \
      -e "s|server image tag (default: v[0-9][0-9.]*[a-zA-Z0-9.-]*;|server image tag (default: ${VERSION};|g" \
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
# git-cliff regenerates the full release history from commit messages.
# We then split it at the 50-release line: the most recent 50 stay in
# CHANGELOG.md (the file shipped + rendered on GitHub); older entries
# get pushed to CHANGELOG.archive.md. Without the cap CHANGELOG.md
# grows unboundedly — every release adds another section, and after
# 150+ releases the file is 80+ KB of mostly-irrelevant history that
# slows PR diffs + SPA loads.
#
# Skipped silently if git-cliff isn't on PATH — the release still
# ships, just without an updated changelog.

CHANGELOG_KEEP_RELEASES=50

if [[ -f cliff.toml ]] && command -v git-cliff >/dev/null 2>&1; then
  log "regenerating CHANGELOG.md (git-cliff, keep last $CHANGELOG_KEEP_RELEASES releases)"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "git-cliff -c cliff.toml --tag $VERSION -o /tmp/changelog-full.md && split at release #$CHANGELOG_KEEP_RELEASES"
  else
    git-cliff -c cliff.toml --tag "$VERSION" -o /tmp/kuso-changelog-full.md >/dev/null
    # Find the line number where release #(KEEP+1) begins. awk counts
    # `## [vX.Y.Z]` headers; when the count crosses the threshold we
    # capture the line number and stop.
    cutoff_line=$(awk -v k="$CHANGELOG_KEEP_RELEASES" '
      /^## \[/ { count++; if (count == k+1) { print NR; exit } }
    ' /tmp/kuso-changelog-full.md)
    if [[ -z "$cutoff_line" ]]; then
      # Fewer than KEEP+1 releases — nothing to archive yet, just
      # publish the full history.
      cp /tmp/kuso-changelog-full.md CHANGELOG.md
    else
      head_lines=$((cutoff_line - 1))
      head -n "$head_lines" /tmp/kuso-changelog-full.md > /tmp/kuso-changelog-recent.md
      tail -n +"$cutoff_line" /tmp/kuso-changelog-full.md > /tmp/kuso-changelog-older.md
      # Recent file + archive-link footer.
      {
        cat /tmp/kuso-changelog-recent.md
        printf '\n---\n\nOlder releases archived in [CHANGELOG.archive.md](./CHANGELOG.archive.md).\n\n<!-- generated by git-cliff -->\n'
      } > CHANGELOG.md
      # Archive: stable header + the older slice. Append-only is wrong
      # here (one cliff regeneration emits the whole history); we
      # rebuild the archive from the older slice every release so the
      # exact same content always lands in the same file.
      {
        printf '# Changelog archive\n\nOlder release entries split out of the main CHANGELOG.md once it grew past %d releases. Promoted out of the main file release-by-release.\n\n' "$CHANGELOG_KEEP_RELEASES"
        cat /tmp/kuso-changelog-older.md
      } > CHANGELOG.archive.md
    fi
    rm -f /tmp/kuso-changelog-full.md /tmp/kuso-changelog-recent.md /tmp/kuso-changelog-older.md
  fi
elif [[ -f cliff.toml ]]; then
  warn "cliff.toml present but git-cliff not on PATH — skipping CHANGELOG regen (brew install git-cliff)"
fi

# ---- 3. web build (only when local-rolling) ------------------------
#
# The Dockerfile's first stage runs `npm ci && next build` itself, so
# the local web build was duplicated work for every `make ship` —
# 30-45s extra wall time, identical bytes. We only need a local build
# when KUSO_RELEASE_ROLL=1 wants to set the deploy/*.yaml's image tag
# AND something embeds web from local disk (it doesn't, but keeping
# the door open). For the standard ship path, skip entirely.

if [[ "${KUSO_RELEASE_ROLL:-0}" == "1" ]] && [[ -d web ]]; then
  log "building web (local-roll path)"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "(cd web && (pnpm build || npm run build)) → server-go/internal/web/dist/"
  elif command -v pnpm >/dev/null 2>&1; then
    (cd web && pnpm build >/dev/null) || fail "web build failed (pnpm)"
  elif command -v npm >/dev/null 2>&1; then
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

# ---- 4a2. updater image --------------------------------------------
#
# The in-cluster updater Job pulls
# `ghcr.io/sislelabs/kuso-updater:${VERSION}`. Without a versioned tag
# matching this release, every `kuso upgrade` from this version
# forward fails with ImagePullBackOff. We rebuild + retag both
# :${VERSION} and :latest on every release. The image rarely changes
# (alpine + kubectl + a small entrypoint script), but tagging cheaply
# keeps the path predictable.

UPDATER_IMAGE="${KUSO_RELEASE_UPDATER_IMAGE:-ghcr.io/sislelabs/kuso-updater}"
if [[ "${KUSO_RELEASE_SKIP_BUILD:-0}" != "1" ]]; then
  log "docker buildx → ${UPDATER_IMAGE}:${VERSION} (+ :latest)"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "docker buildx build --platform linux/amd64 --push -t ${UPDATER_IMAGE}:${VERSION} -t ${UPDATER_IMAGE}:latest -f build/updater/Dockerfile build/updater"
  else
    docker buildx build \
      --platform linux/amd64 \
      --push \
      -t "${UPDATER_IMAGE}:${VERSION}" \
      -t "${UPDATER_IMAGE}:latest" \
      -f build/updater/Dockerfile \
      build/updater >/dev/null
    log "updater image pushed: ${UPDATER_IMAGE}:${VERSION}"
  fi
fi

# ---- 4a2b. nixpacks builder image ----------------------------------
#
# Init-container image used by kusobuild Jobs when strategy=nixpacks.
# Bakes the nixpacks binary so each build doesn't curl|tar 30 MB from
# GitHub Releases. Tagged with the *nixpacks version* (not the kuso
# version) so the chart can pin to a specific nixpacks release; the
# :latest tag is pushed in parallel so a fresh install picks the
# current one.

NIXPACKS_VERSION="${KUSO_RELEASE_NIXPACKS_VERSION:-1.41.0}"
NIXPACKS_IMAGE="${KUSO_RELEASE_NIXPACKS_IMAGE:-ghcr.io/sislelabs/kuso-nixpacks}"
if [[ "${KUSO_RELEASE_SKIP_BUILD:-0}" != "1" ]]; then
  # The nixpacks image is tagged by the *nixpacks* version, not the
  # kuso release version — so it's stable across kuso ships unless
  # NIXPACKS_VERSION bumps. Cheap probe via docker buildx imagetools
  # against the registry tag; on hit we skip the whole 30 MB push.
  # That makes flaky-ghcr re-shipping idempotent (the previous push
  # of v0.9.5 timed out at this step after 10000s on layer push).
  # Override with KUSO_RELEASE_FORCE_NIXPACKS=1 when changing the
  # nixpacks Dockerfile without bumping NIXPACKS_VERSION.
  if [[ "${KUSO_RELEASE_FORCE_NIXPACKS:-0}" != "1" ]] \
      && docker buildx imagetools inspect "${NIXPACKS_IMAGE}:${NIXPACKS_VERSION}" >/dev/null 2>&1; then
    log "nixpacks image ${NIXPACKS_IMAGE}:${NIXPACKS_VERSION} already on ghcr — skipping rebuild"
  else
    log "docker buildx → ${NIXPACKS_IMAGE}:${NIXPACKS_VERSION} (+ :latest)"
    if [[ "$DRY_RUN" == "1" ]]; then
      dry "docker buildx build --platform linux/amd64 --push --build-arg NIXPACKS_VERSION=${NIXPACKS_VERSION} -t ${NIXPACKS_IMAGE}:${NIXPACKS_VERSION} -t ${NIXPACKS_IMAGE}:latest -f build/nixpacks/Dockerfile build/nixpacks"
    else
      docker buildx build \
        --platform linux/amd64 \
        --push \
        --build-arg "NIXPACKS_VERSION=${NIXPACKS_VERSION}" \
        -t "${NIXPACKS_IMAGE}:${NIXPACKS_VERSION}" \
        -t "${NIXPACKS_IMAGE}:latest" \
        -f build/nixpacks/Dockerfile \
        build/nixpacks >/dev/null
      log "nixpacks image pushed: ${NIXPACKS_IMAGE}:${NIXPACKS_VERSION}"
    fi
  fi
fi

# ---- 4a2b. env-detect image ----------------------------------------
#
# Bakes ripgrep + jq into a small alpine image so the env-detect
# init container runs as runAsUser:1000 instead of `apk add`-as-root
# at runtime. Tagged with KUSO_RELEASE_ENV_DETECT_TAG (default "v1"
# — bump on Dockerfile changes) so the chart can pin a known good
# image even when its own version stays put. Same idempotent
# inspect-before-build dance as the nixpacks image.

ENV_DETECT_TAG="${KUSO_RELEASE_ENV_DETECT_TAG:-v1}"
ENV_DETECT_IMAGE="${KUSO_RELEASE_ENV_DETECT_IMAGE:-ghcr.io/sislelabs/kuso-env-detect}"
if [[ "${KUSO_RELEASE_SKIP_BUILD:-0}" != "1" ]]; then
  if [[ "${KUSO_RELEASE_FORCE_ENV_DETECT:-0}" != "1" ]] \
      && docker buildx imagetools inspect "${ENV_DETECT_IMAGE}:${ENV_DETECT_TAG}" >/dev/null 2>&1; then
    log "env-detect image ${ENV_DETECT_IMAGE}:${ENV_DETECT_TAG} already on ghcr — skipping rebuild"
  else
    log "docker buildx → ${ENV_DETECT_IMAGE}:${ENV_DETECT_TAG} (+ :latest)"
    if [[ "$DRY_RUN" == "1" ]]; then
      dry "docker buildx build --platform linux/amd64 --push -t ${ENV_DETECT_IMAGE}:${ENV_DETECT_TAG} -t ${ENV_DETECT_IMAGE}:latest -f build/env-detect/Dockerfile build/env-detect"
    else
      docker buildx build \
        --platform linux/amd64 \
        --push \
        -t "${ENV_DETECT_IMAGE}:${ENV_DETECT_TAG}" \
        -t "${ENV_DETECT_IMAGE}:latest" \
        -f build/env-detect/Dockerfile \
        build/env-detect >/dev/null
      log "env-detect image pushed: ${ENV_DETECT_IMAGE}:${ENV_DETECT_TAG}"
    fi
  fi
fi

# ---- 4a3. operator image -------------------------------------------
#
# Decide what operator image to bake into release.json. Two paths:
#
#   - operator/ changed since last tag → build + push at $VERSION
#     (so the new server has a matching operator).
#   - operator/ unchanged → reuse the last operator tag actually
#     present on ghcr (queried at release time so we don't chase
#     phantom tags). KUSO_RELEASE_OPERATOR_VERSION overrides both.

operator_should_build() {
  if [[ "${KUSO_RELEASE_OPERATOR:-auto}" == "1" ]]; then
    return 0
  fi
  if [[ "${KUSO_RELEASE_OPERATOR:-auto}" == "0" ]]; then
    return 1
  fi
  local last
  last="$(git describe --tags --abbrev=0 2>/dev/null || echo)"
  if [[ -z "$last" ]]; then
    # No previous tag → can't diff. Build to be safe.
    return 0
  fi
  if ! git diff --quiet "$last"..HEAD -- operator/; then
    return 0
  fi
  return 1
}

if [[ -n "${KUSO_RELEASE_OPERATOR_VERSION:-}" ]]; then
  OPERATOR_VERSION="$KUSO_RELEASE_OPERATOR_VERSION"
elif operator_should_build; then
  OPERATOR_VERSION="$VERSION"
else
  OPERATOR_VERSION="$(latest_ghcr_tag sislelabs/kuso-operator)"
  if [[ -z "$OPERATOR_VERSION" ]]; then
    warn "couldn't query ghcr for operator tags; falling back to ${VERSION}"
    OPERATOR_VERSION="$VERSION"
  else
    log "operator/ unchanged — release.json will pin operator to last built tag ${OPERATOR_VERSION}"
  fi
fi

# Correct the eager operator pin from step 2. The version-bump block
# rewrote deploy/operator.yaml + install.sh's KUSO_VERSION default to
# ${VERSION} unconditionally — BUT when operator/ is unchanged we do NOT
# build+push an operator image at ${VERSION}, so that pin dangles. Fresh
# installs (`kubectl apply -f deploy/operator.yaml`, or install.sh which
# fetches the same file) then pull a 404 tag → ImagePullBackOff → the
# install's `kubectl wait` times out. This bit v0.18.69: server shipped
# at v0.18.69 but the operator image only existed up to v0.18.68, so the
# deploy yaml pointed at a phantom kuso-operator:v0.18.69.
#
# release.json was always correct (it pins OPERATOR_VERSION via the
# query above), so auto-updating clusters were fine — only the
# kubectl-apply / fresh-install path broke. Re-stamp the two install
# sources to the tag we actually pinned so all three paths agree.
if [[ "$OPERATOR_VERSION" != "$VERSION" && "$CURRENT" != "$VERSION" ]]; then
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "re-stamp deploy/operator.yaml + install.sh KUSO_VERSION default: ${VERSION} → ${OPERATOR_VERSION} (operator not rebuilt this release)"
  else
    log "operator not rebuilt — re-stamping install sources to real operator tag ${OPERATOR_VERSION} (was ${VERSION})"
    sed -i.bak \
      -E "s|kuso-operator:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-operator:${OPERATOR_VERSION}|g" \
      deploy/operator.yaml
    rm deploy/operator.yaml.bak
    sed -i.bak \
      -e "s|KUSO_VERSION=\"\${KUSO_VERSION:-v[0-9][0-9.]*[a-zA-Z0-9.-]*}\"|KUSO_VERSION=\"\${KUSO_VERSION:-${OPERATOR_VERSION}}\"|g" \
      hack/install.sh
    rm hack/install.sh.bak
  fi
fi

if operator_should_build && [[ "${KUSO_RELEASE_SKIP_BUILD:-0}" != "1" ]]; then
  log "building operator image ${OPERATOR_IMAGE}:${OPERATOR_VERSION}"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "docker buildx build --platform linux/amd64 --push -t ${OPERATOR_IMAGE}:${OPERATOR_VERSION} -f operator/Dockerfile operator"
  else
    docker buildx build \
      --platform linux/amd64 \
      --push \
      -t "${OPERATOR_IMAGE}:${OPERATOR_VERSION}" \
      -f operator/Dockerfile \
      operator >/dev/null
    log "operator image pushed: ${OPERATOR_IMAGE}:${OPERATOR_VERSION}"
  fi
fi

# ---- 4a4. ghcr visibility precheck ---------------------------------
#
# Brand-new container packages on ghcr default to private even when
# the org's other packages are public. Symptom: the GitHub release
# publishes successfully, the kube updater Job pulls the new image,
# gets a 401 from the ghcr token endpoint, and crashloops in
# ImagePullBackOff. Discovered the hard way on v0.11.1 when the
# kuso-updater image (added in a recent release) had never been
# flipped to public — the cluster stayed on the prior version while
# the updater Job re-tried forever and the notifications channel
# flooded with "Pod crashed" alerts.
#
# GitHub's REST API does NOT expose a container-package-visibility-
# change endpoint (only the web UI does), so we can't auto-fix.
# What we CAN do is detect the condition and fail loudly before we
# publish the GH release — if a release.json points at unpullable
# images, every cluster on auto-update would try, fail, and alert.
#
# Runs AFTER the operator block so OPERATOR_VERSION is bound — when
# operator/ is unchanged since the last tag, that variable is set
# by the latest_ghcr_tag query above, not by the build path.
#
# Skip via KUSO_RELEASE_SKIP_VISIBILITY_CHECK=1 (e.g. for a release
# of a not-yet-public image when the dev cluster has a pull-secret
# wired up — rare).
if [[ "${KUSO_RELEASE_SKIP_VISIBILITY_CHECK:-0}" != "1" && "$DRY_RUN" != "1" ]]; then
  log "checking ghcr image visibility (anonymous pull)"
  visibility_failures=()
  # Belt-and-suspenders: also check the operator tag actually baked into
  # the INSTALL sources, not just OPERATOR_VERSION (release.json's pin).
  # release.json was always correct in the v0.18.69 incident — what
  # broke was deploy/operator.yaml pointing at a phantom tag the
  # fresh-install path pulls directly. Reading the tag back out of the
  # file (rather than trusting a variable) catches the whole class of
  # "install source points at an unpushed image" bugs, however it got
  # there. The set collapses to one entry when everything agrees.
  declare -a OPERATOR_PIN_CHECKS=("${OPERATOR_IMAGE}:${OPERATOR_VERSION}")
  deploy_op_tag="$(sed -nE 's|.*image:[[:space:]]*ghcr.io/sislelabs/kuso-operator:([A-Za-z0-9._-]+).*|\1|p' deploy/operator.yaml | head -1)"
  if [[ -n "$deploy_op_tag" ]]; then
    OPERATOR_PIN_CHECKS+=("${OPERATOR_IMAGE}:${deploy_op_tag}")
  fi
  install_op_tag="$(sed -nE 's|^KUSO_VERSION="\$\{KUSO_VERSION:-([^}]+)\}".*|\1|p' hack/install.sh | head -1)"
  if [[ -n "$install_op_tag" ]]; then
    OPERATOR_PIN_CHECKS+=("${OPERATOR_IMAGE}:${install_op_tag}")
  fi
  # Dedupe so we don't HEAD the same manifest three times.
  mapfile -t OPERATOR_PIN_CHECKS < <(printf '%s\n' "${OPERATOR_PIN_CHECKS[@]}" | sort -u)
  for img in "${KUSO_RELEASE_IMAGE}:${VERSION}" "${OPERATOR_PIN_CHECKS[@]}" "${UPDATER_IMAGE}:${VERSION}"; do
    repo_part="${img%:*}"     # strip :tag
    ref_part="${img##*:}"     # tag only
    path="${repo_part#ghcr.io/}"
    # ghcr's anonymous-pull flow: GET /token first to mint an anon
    # bearer, then HEAD /manifests with that bearer. A public package
    # returns 200; a private one returns 401 on the manifest step
    # (the token endpoint itself returns 200 for both).
    token=$(curl -sSL "https://ghcr.io/token?scope=repository:${path}:pull&service=ghcr.io" 2>/dev/null \
      | sed -nE 's/.*"token"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p')
    if [[ -z "$token" ]]; then
      visibility_failures+=("${img} (token mint failed)")
      continue
    fi
    code=$(curl -sSL -o /dev/null -w "%{http_code}" \
      -H "Authorization: Bearer ${token}" \
      -H "Accept: application/vnd.oci.image.index.v1+json" \
      "https://ghcr.io/v2/${path}/manifests/${ref_part}" 2>/dev/null || echo "000")
    if [[ "$code" == "200" ]]; then
      log "  ${img} ✓ public"
    else
      visibility_failures+=("${img} (HTTP $code)")
    fi
  done
  if [[ ${#visibility_failures[@]} -gt 0 ]]; then
    msg="the following images are not publicly pullable from ghcr (kube nodes won't be able to fetch them without imagePullSecrets):"
    for f in "${visibility_failures[@]}"; do
      msg="${msg}
       - ${f}"
    done
    msg="${msg}
       Fix: open each package's settings page on GitHub and flip
       visibility to Public. For sislelabs packages:
         https://github.com/orgs/sislelabs/packages/container/<name>/settings
       Then re-run \`make ship VERSION=${VERSION}\` (or skip the
       check with KUSO_RELEASE_SKIP_VISIBILITY_CHECK=1 if you know
       the cluster has a pull-secret for them)."
    fail "${msg}"
  fi
fi

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
# all of these are safe to re-apply (additive only, today). If you
# ship a destructive schema change (rename, removal, type narrow),
# this is the wrong tool — apply the manual migration over ssh
# before cutting the release.
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

# breaking is derived from conventional-commits markers between the last
# released tag and HEAD: either a "BREAKING CHANGE" footer in the body
# or a "!:" suffix after the type (e.g. "refactor!:", "feat(api)!:").
# Previously hardcoded false, which lied to the updater on every release
# that dropped back-compat paths. The updater uses this flag to gate
# auto-upgrade on clusters with conservative update policies.
#
# Override: set KUSO_RELEASE_BREAKING=1 to force-flag a release that
# contains breaking work the commits didn't mark with the convention.
# Set =0 to force-clear (rare, but useful when a "!:" was added in
# error). Empty/unset = use the auto-detection below.
if [[ -n "${KUSO_RELEASE_BREAKING:-}" ]]; then
  case "${KUSO_RELEASE_BREAKING}" in
    1|true|yes)  BREAKING=true  ;;
    0|false|no)  BREAKING=false ;;
    *) fail "KUSO_RELEASE_BREAKING must be 0/1 (got: ${KUSO_RELEASE_BREAKING})" ;;
  esac
  log "breaking flag forced by KUSO_RELEASE_BREAKING → breaking=${BREAKING}"
else
  PREV_TAG="$(git tag --list 'v*' --sort=-v:refname | grep -v "^${VERSION}\$" | head -1)"
  if [[ -z "$PREV_TAG" ]]; then
    BREAKING=false
  else
    # %B is the full message (subject + body). grep -E with multiline -z
    # is tricky in portable bash; instead, scan subject for "!:" and
    # body for the literal footer string.
    if git log --format='%s' "${PREV_TAG}..HEAD" | grep -qE '^[a-z]+(\([^)]+\))?!:'; then
      BREAKING=true
    elif git log --format='%B' "${PREV_TAG}..HEAD" | grep -q '^BREAKING CHANGE'; then
      BREAKING=true
    else
      BREAKING=false
    fi
    log "breaking-change scan: ${PREV_TAG}..HEAD → breaking=${BREAKING}"
  fi
fi

cat > "$DIST_DIR/release.json" <<EOF
{
  "version": "${VERSION}",
  "publishedAt": "${PUBLISHED_AT}",
  "components": {
    "server":   { "image": "${KUSO_RELEASE_IMAGE}:${VERSION}" },
    "operator": { "image": "${OPERATOR_IMAGE}:${OPERATOR_VERSION}" },
    "updater":  { "image": "${UPDATER_IMAGE:-ghcr.io/sislelabs/kuso-updater}:${VERSION}" }
  },
  "crds": {
    "url": "https://github.com/${KUSO_RELEASE_REPO:-sislelabs/kuso}/releases/download/${VERSION}/crds.yaml",
    "minServer": "v0.4.0",
    "migrations": []
  },
  "breaking": ${BREAKING}
}
EOF
log "wrote ${DIST_DIR}/release.json (breaking=${BREAKING})"

# ---- 4b2. release.json signature -----------------------------------
#
# Ed25519 signature over the raw release.json bytes. The kuso updater
# REQUIRES this by default (server-go/internal/updater/updater.go::
# requireSignatures defaults to true since v0.9.77). Without a valid
# signature, installs refuse to auto-update — which is the whole point
# of the supply-chain defence.
#
# First-run UX: if KUSO_RELEASE_PRIVATE_KEY isn't set AND a keypair
# doesn't yet exist at the conventional path, we generate one,
# print the public key, and prompt the user to bake it into their
# install (env var KUSO_RELEASE_PUBLIC_KEY on kuso-server).
#
# To explicitly ship unsigned (almost never the right answer, but
# useful for first install before a keypair is wired): pass
# KUSO_RELEASE_UNSIGNED=1.

KUSO_KEYS_DIR="${KUSO_KEYS_DIR:-${HOME}/.kuso/release-keys}"
KUSO_RELEASE_PRIVATE_KEY="${KUSO_RELEASE_PRIVATE_KEY:-${KUSO_KEYS_DIR}/release.pem}"

if [[ "${KUSO_RELEASE_UNSIGNED:-0}" == "1" ]]; then
  warn "KUSO_RELEASE_UNSIGNED=1 set — shipping release.json without a signature"
  warn "Installs with KUSO_REQUIRE_SIGNATURES=true (the default) will REFUSE to auto-update from this release"
else
  if [[ ! -f "$KUSO_RELEASE_PRIVATE_KEY" ]]; then
    log "no release keypair at ${KUSO_RELEASE_PRIVATE_KEY} — generating one"
    if [[ "$DRY_RUN" == "1" ]]; then
      dry "openssl genpkey -algorithm ed25519 -out ${KUSO_RELEASE_PRIVATE_KEY}"
    else
      mkdir -p "$KUSO_KEYS_DIR"
      chmod 0700 "$KUSO_KEYS_DIR"
      openssl genpkey -algorithm ed25519 -out "$KUSO_RELEASE_PRIVATE_KEY"
      chmod 0600 "$KUSO_RELEASE_PRIVATE_KEY"
    fi
    PUB_B64=""
    if [[ "$DRY_RUN" != "1" ]]; then
      PUB_B64=$(openssl pkey -in "$KUSO_RELEASE_PRIVATE_KEY" -pubout -outform DER 2>/dev/null \
        | tail -c 32 | openssl base64 -A)
    fi
    cat <<NEXT

==> generated a fresh Ed25519 release keypair.

Private key: ${KUSO_RELEASE_PRIVATE_KEY}   (keep this secret + backed up)
Public key (base64): ${PUB_B64}

To enable signature verification on your kuso install, set this on
the kuso-server Deployment:

    kubectl -n kuso set env deployment/kuso-server \\
      KUSO_RELEASE_PUBLIC_KEY=${PUB_B64}
    kubectl -n kuso rollout restart deployment/kuso-server

Or, to opt out of verification (NOT RECOMMENDED), set
KUSO_REQUIRE_SIGNATURES=false on the install. Without either,
the install will fail to auto-update until you wire the key.

NEXT
  fi

  log "signing release.json with ${KUSO_RELEASE_PRIVATE_KEY}"
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "openssl pkeyutl -sign -inkey \"$KUSO_RELEASE_PRIVATE_KEY\" -rawin -in \"$DIST_DIR/release.json\" | base64 > \"$DIST_DIR/release.json.sig\""
  else
    openssl pkeyutl -sign -inkey "$KUSO_RELEASE_PRIVATE_KEY" -rawin \
      -in "$DIST_DIR/release.json" \
      | openssl base64 -A \
      > "$DIST_DIR/release.json.sig"
    if [[ "$(uname)" == "Darwin" ]]; then
      printf '\n' >> "$DIST_DIR/release.json.sig"
    fi
    log "wrote ${DIST_DIR}/release.json.sig ($(wc -c < "$DIST_DIR/release.json.sig") bytes)"
  fi
fi

# ---- 4c. CLI binaries ----------------------------------------------
#
# install-cli.sh tries to download these from the GitHub release. Build
# them now (cross-compile, no docker) so they're ready for the
# `gh release create` upload below. Skipped silently when go isn't on
# PATH — the install-cli.sh fallback will go-install from source.

KUSO_RELEASE_CLI="${KUSO_RELEASE_CLI:-1}"
if [[ "${KUSO_RELEASE_CLI}" == "1" ]] && command -v go >/dev/null 2>&1; then
  log "cross-building CLI binaries (darwin/linux × amd64/arm64) in parallel"
  CLI_LDFLAGS="-X kuso/cmd/kusoCli/version.ldflagsVersion=${VERSION}"
  if [[ "$DRY_RUN" == "1" ]]; then
    for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
      dry "(cd cli && GOOS=${target%-*} GOARCH=${target#*-} go build -ldflags='$CLI_LDFLAGS' -o ${DIST_DIR}/kuso-${target} ./cmd)"
    done
  else
    # Fan out the four targets concurrently. They have no shared
    # state and Go's build cache is per-target (different GOOS+
    # GOARCH key into different cache subtrees), so on a multi-core
    # box wall-clock drops from ~4× to ~1.1×. PIDs collected so we
    # can fail loudly if any single target errored.
    declare -a build_pids=()
    for target in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
      GOOS="${target%-*}"
      GOARCH="${target#*-}"
      out="${DIST_DIR}/kuso-${target}"
      (
        cd cli && GOOS="$GOOS" GOARCH="$GOARCH" \
          go build -ldflags="$CLI_LDFLAGS" -o "$out" ./cmd
      ) &
      build_pids+=("$!:$target")
    done
    fail_targets=()
    for entry in "${build_pids[@]}"; do
      pid="${entry%%:*}"
      tgt="${entry##*:}"
      if ! wait "$pid"; then
        fail_targets+=("$tgt")
      fi
    done
    if [[ ${#fail_targets[@]} -gt 0 ]]; then
      fail "CLI build failed for: ${fail_targets[*]}"
    fi
    ls -lh "$DIST_DIR"/kuso-* | awk '{print "    " $5 "  " $9}'
  fi
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
      # Two-phase: create the release with no assets first, then
      # upload each asset with retries. The bundled-create form
      # (everything in one call) was rolling back the entire release
      # on a single asset's transient 404/422 from the GH upload API
      # — and we hit those, repeatedly, on the cross-built CLI
      # binaries. Doing it incrementally lets each upload retry
      # independently and keeps a partial release around to recover.
      if ! gh release view "$VERSION" >/dev/null 2>&1; then
        # GitHub's release-create endpoint occasionally 502s; retry
        # a few times with backoff before giving up.
        ok=0
        for try in 1 2 3; do
          if gh release create "$VERSION" \
              --title "$VERSION" \
              --notes-file "$NOTES_FILE" >/dev/null 2>&1; then
            ok=1; break
          fi
          warn "gh release create attempt ${try}/3 failed; retrying"
          sleep $((try * 3))
        done
        if [[ "$ok" != "1" ]]; then
          fail "couldn't create GH release after 3 tries"
        fi
      fi
      ALL_ASSETS=( "$DIST_DIR/release.json" "$DIST_DIR/crds.yaml" "${CLI_ASSETS[@]}" )
      # Upload the signature too when present.
      if [[ -f "$DIST_DIR/release.json.sig" ]]; then
        ALL_ASSETS+=( "$DIST_DIR/release.json.sig" )
      fi
      for asset in "${ALL_ASSETS[@]}"; do
        ok=0
        for try in 1 2 3; do
          if gh release upload "$VERSION" "$asset" --clobber >/dev/null 2>&1; then
            ok=1; break
          fi
          warn "upload of $(basename "$asset") attempt ${try}/3 failed; retrying"
          sleep $((try * 2))
        done
        if [[ "$ok" != "1" ]]; then
          fail "couldn't upload $(basename "$asset") after 3 tries"
        fi
      done
    fi
    rm -f "$NOTES_FILE"
    log "GitHub release ${VERSION} published (release.json + crds.yaml + ${#CLI_ASSETS[@]} CLI assets)"
  fi
fi

# ---- 5. optional rollout -------------------------------------------

# ---- 5-pre. CRDs FIRST (when operator/ changed) --------------------
#
# The new kuso-server refuses readiness until the CRD schemas it
# expects are applied (the stale-CRD gate). If we rolled the server
# first and applied CRDs afterwards, the server rollout would time out
# and abort the script BEFORE the CRD-apply step ever ran — a
# chicken-and-egg deadlock we actually hit in production. So scp +
# apply the CRDs BEFORE the server roll below. CRD apply is idempotent
# (`kubectl apply` of an unchanged CRD is a no-op), and only the CRD
# schemas — not the operator image — gate server readiness, so the
# operator IMAGE roll stays in section 5b where it belongs.
#
# Gated on operator_should_build: when nothing under operator/ changed,
# the CRDs didn't change either, so the server's schema expectation is
# already satisfied and there's nothing to apply.
if [[ "${KUSO_RELEASE_ROLL:-0}" == "1" ]] && operator_should_build; then
  log "operator/ changed — applying CRDs BEFORE server roll on ${KUSO_RELEASE_HOST}"
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
    dry "ssh ${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST} 'kubectl apply${REMOTE_FLAGS}'"
  else
    ssh -i "$KUSO_RELEASE_KEY" \
      -o StrictHostKeyChecking=accept-new \
      "${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}" \
      "kubectl apply ${REMOTE_FLAGS}"
  fi
  log "CRDs applied"
fi

if [[ "${KUSO_RELEASE_ROLL:-0}" == "1" ]]; then
  log "rolling deploy/${KUSO_RELEASE_DEPLOY} on ${KUSO_RELEASE_HOST}"
  # accept-new auto-trusts a previously-unknown host on first contact
  # so the script doesn't wedge waiting for an interactive yes/no.
  # The known_hosts file still gets the entry — second run is fully
  # verified. Don't disable host key checking entirely; that opens us
  # up to MITM on every subsequent run.
  # kuso-activator runs the SAME kuso-server-go image in --activator mode
  # (see deploy/kuso-activator.yaml), so it must roll in lockstep. It's a
  # separate Deployment the server-roll above doesn't touch — without this
  # an activator-code change (e.g. the scale-to-zero / stopped-page path)
  # silently never ships and the cluster keeps serving old behaviour.
  # Tolerate its absence (older installs may predate the split) with
  # `|| true` on the activator lines only.
  if [[ "$DRY_RUN" == "1" ]]; then
    dry "ssh ${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST} 'kubectl set image -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} server=${KUSO_RELEASE_IMAGE}:${VERSION} && kubectl set image -n ${KUSO_RELEASE_NS} deploy/kuso-activator activator=${KUSO_RELEASE_IMAGE}:${VERSION} && kubectl rollout status …'"
  else
    ssh -i "$KUSO_RELEASE_KEY" \
      -o StrictHostKeyChecking=accept-new \
      "${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}" \
      "kubectl set image -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} server=${KUSO_RELEASE_IMAGE}:${VERSION} && \
       kubectl rollout status -n ${KUSO_RELEASE_NS} deploy/${KUSO_RELEASE_DEPLOY} --timeout=180s && \
       (kubectl set image -n ${KUSO_RELEASE_NS} deploy/kuso-activator activator=${KUSO_RELEASE_IMAGE}:${VERSION} && \
        kubectl rollout status -n ${KUSO_RELEASE_NS} deploy/kuso-activator --timeout=180s || echo 'kuso-activator not present — skipping (older install)')"
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

# ---- 5b. operator image roll (auto when operator/ changed) -------
#
# Detects whether operator/ changed since the last git tag. When it
# did, rolls the operator deployment to the freshly-built image.
# Skipped when nothing under operator/ has changed.
#
# NOTE: the CRDs are scp'd + applied in section 5-pre ABOVE (before the
# server roll), NOT here — the new server's stale-CRD readiness gate
# needs the schemas in place before its rollout, so applying them after
# the server roll would deadlock. The operator IMAGE (which does NOT
# gate server readiness) rolls here.
#
# Override: KUSO_RELEASE_OPERATOR=1 forces the operator step even
# when git diff is empty (useful for explicit re-rolls, e.g. when
# you pulled the chart from another branch). KUSO_RELEASE_OPERATOR=0
# skips it explicitly.

if [[ "${KUSO_RELEASE_ROLL:-0}" == "1" ]] && operator_should_build; then
  log "operator/ changed — rolling operator on ${KUSO_RELEASE_HOST} (image already built above; CRDs already applied)"

  if [[ "$DRY_RUN" == "1" ]]; then
    dry "ssh ${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST} 'kubectl set image -n ${KUSO_OPERATOR_NS} deploy/${KUSO_OPERATOR_DEPLOY} ${KUSO_OPERATOR_CONTAINER}=${OPERATOR_IMAGE}:${OPERATOR_VERSION} && kubectl rollout status …'"
  else
    ssh -i "$KUSO_RELEASE_KEY" \
      -o StrictHostKeyChecking=accept-new \
      "${KUSO_RELEASE_USER}@${KUSO_RELEASE_HOST}" \
      "kubectl set image -n ${KUSO_OPERATOR_NS} deploy/${KUSO_OPERATOR_DEPLOY} ${KUSO_OPERATOR_CONTAINER}=${OPERATOR_IMAGE}:${OPERATOR_VERSION} && \
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
    deploy/operator.yaml
    # kuso-activator.yaml's image tag is rewritten in the version-bump
    # step (it runs the kuso-server-go image); it MUST be committed too,
    # else the released tag ships an activator pinned to the PREVIOUS
    # version. v0.18.74 shipped that bug — the sed bumped it but it was
    # never staged, so the tag's activator pointed at v0.18.73.
    deploy/kuso-activator.yaml
    hack/install.sh
    server-go/internal/installscripts/scripts/install.sh
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

      # Tag-push needs an explicit collision check. Plain `git push
      # origin vX.Y.Z` rejects-and-warns when the remote tag exists
      # pointing at a different SHA — we used to swallow that with
      # `|| warn`, which left the remote tag pointing at whatever
      # came before (typically an aborted earlier run) while the
      # GitHub release + ghcr image were built from the new commit.
      # The artifacts and the tag would disagree, which is exactly
      # the trap that caught us shipping v0.11.0.
      local_sha="$(git rev-list -n1 "${VERSION}")"
      remote_sha="$(git ls-remote origin "refs/tags/${VERSION}" | awk '{print $1}')"
      # ls-remote returns the tag object's own SHA for annotated
      # tags; dereference both sides to the commit they point at.
      if [[ -n "$remote_sha" ]]; then
        remote_commit="$(git rev-list -n1 "$remote_sha" 2>/dev/null || echo "$remote_sha")"
      else
        remote_commit=""
      fi

      if [[ -z "$remote_commit" ]]; then
        # No remote tag yet — normal first-push path.
        git push origin "${VERSION}"
      elif [[ "$remote_commit" == "$local_sha" ]]; then
        # Identical pointer — re-running ship is idempotent. Don't
        # log this as failure.
        log "tag ${VERSION} already on origin at the right commit — skipping push"
      else
        # Divergent: remote tag points at a different commit than the
        # one we just built artifacts from. This happens when an
        # earlier ship attempt for the same VERSION partially failed
        # and left the tag stranded on the pre-release commit. Auto-
        # recover by force-replacing the remote tag — the GH release
        # + ghcr image are already at $local_sha, so making the tag
        # agree is the only consistent move. Force-pushing a tag is
        # safe in this workflow: live kuso instances follow
        # release.json (NOT git tags), and `KUSO_REF=vX.Y.Z` pinning
        # resolves to whatever the tag currently points at anyway.
        warn "tag ${VERSION} on origin points at ${remote_commit:0:12}, but we just built ${local_sha:0:12} — force-replacing the remote tag"
        git push --delete origin "${VERSION}" >/dev/null 2>&1 || true
        git push origin "${VERSION}"
        log "tag ${VERSION} force-repointed to ${local_sha:0:12}"
      fi

      # The GH release was created in step 4, BEFORE the tag landed
      # on origin. GH defaults to draft when its target tag-name
      # doesn't yet exist remotely — so the release is sitting as a
      # draft right now. Flip it to published + mark as latest now
      # that the tag is on origin. Best-effort: gh CLI not installed
      # or GH API hiccups just leaves the release as draft, which is
      # recoverable manually.
      if [[ "${KUSO_RELEASE_GH:-0}" == "1" ]] && command -v gh >/dev/null 2>&1; then
        if gh release edit "${VERSION}" --draft=false --latest >/dev/null 2>&1; then
          log "GH release ${VERSION} published + marked latest"
        else
          warn "gh release edit ${VERSION} --draft=false --latest failed — release may still be draft"
        fi
      fi
    fi
    log "pushed commit + tag to origin"
  else
    warn "KUSO_RELEASE_PUSH=0 — commit + tag NOT pushed; install.sh on main will be stale"
  fi
fi

log "done — ${VERSION}"
