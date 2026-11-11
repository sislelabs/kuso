# shellcheck shell=bash
# Shared by up.sh, reload.sh, down.sh and smoke.sh.

SANDBOX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SANDBOX_DIR}/../.." && pwd)"
STATE_ROOT="${SANDBOX_DIR}/.state"
DEFAULT_SANDBOX_NAME="kuso-sandbox"

# Instance selection: KUSO_SANDBOX_NAME, else the instance the last
# `up` in this checkout started (.state/current), else the default. Each
# instance keeps its own state dir, ports and compose project.
if [[ -z "${KUSO_SANDBOX_NAME:-}" && -f "${STATE_ROOT}/current" ]]; then
  KUSO_SANDBOX_NAME="$(cat "${STATE_ROOT}/current")"
fi
export KUSO_SANDBOX_NAME="${KUSO_SANDBOX_NAME:-$DEFAULT_SANDBOX_NAME}"
[[ "$KUSO_SANDBOX_NAME" =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "error: KUSO_SANDBOX_NAME must be lowercase letters, digits and dashes" >&2; exit 1; }
STATE_DIR="${STATE_ROOT}/${KUSO_SANDBOX_NAME}"
NODE="${KUSO_SANDBOX_NAME}-node"

# port_in_use <port>: something on the host listens there, or a sandbox
# container (running or stopped) has it published.
port_in_use() {
  local p="$1"
  if (exec 3<>"/dev/tcp/127.0.0.1/${p}") 2>/dev/null; then return 0; fi
  docker ps -a --filter "label=com.docker.compose.service=node" -q 2>/dev/null \
    | xargs -r docker inspect -f '{{range $k, $v := .HostConfig.PortBindings}}{{range $v}}{{.HostPort}} {{end}}{{end}}' 2>/dev/null \
    | tr ' ' '\n' | grep -qxF "$p" && return 0
  grep -qsE "=${p}\$" "${STATE_ROOT}"/*/ports.env 2>/dev/null
}

# free_port <start>: first port from <start> up that nothing uses.
free_port() {
  local p="$1"
  while port_in_use "$p"; do p=$((p + 1)); done
  echo "$p"
}

# Ports: explicit env wins, then what this instance was started with.
# A named instance started without ports gets free ones (only `up`
# allocates; SANDBOX_ALLOCATE_PORTS=1), so parallel sandboxes don't
# fight over 80/16443.
if [[ -f "${STATE_DIR}/ports.env" ]]; then
  while IFS='=' read -r k v; do
    [[ -n "$k" && -z "${!k:-}" ]] && printf -v "$k" '%s' "$v"
  done < "${STATE_DIR}/ports.env"
elif [[ "$KUSO_SANDBOX_NAME" != "$DEFAULT_SANDBOX_NAME" && "${SANDBOX_ALLOCATE_PORTS:-0}" == "1" ]]; then
  KUSO_SANDBOX_PORT="${KUSO_SANDBOX_PORT:-$(free_port 18080)}"
  KUSO_SANDBOX_API_PORT="${KUSO_SANDBOX_API_PORT:-$(free_port 16444)}"
fi
export KUSO_SANDBOX_PORT="${KUSO_SANDBOX_PORT:-80}"
export KUSO_SANDBOX_API_PORT="${KUSO_SANDBOX_API_PORT:-16443}"
SANDBOX_DOMAIN="kuso.localhost"
# ":80" is left off so the URLs match the port-less links kuso prints.
SANDBOX_PORT_SUFFIX=":${KUSO_SANDBOX_PORT}"
[[ "$KUSO_SANDBOX_PORT" == "80" ]] && SANDBOX_PORT_SUFFIX=""
SANDBOX_API_URL="http://${SANDBOX_DOMAIN}${SANDBOX_PORT_SUFFIX}"
SANDBOX_ADMIN_PASSWORD="kuso-admin"
SANDBOX_IMAGE_TAG="sandbox"
REGISTRY_HOST="kuso-registry.kuso.svc.cluster.local"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarn:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

compose()      { docker compose -f "${SANDBOX_DIR}/compose.yaml" "$@"; }
node_kubectl() { docker exec "$NODE" kubectl "$@"; }
node_running() { [[ "$(docker inspect -f '{{.State.Running}}' "$NODE" 2>/dev/null)" == "true" ]]; }

# The build controller hardcodes its builder image refs; read them from
# there so the sandbox imports exactly what build Jobs will ask for.
buildcontroller_const() {
  sed -nE "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"([^\"]+)\".*/\1/p" \
    "${REPO_ROOT}/server-go/internal/buildcontroller/buildcontroller.go"
}

image_ref() {
  case "$1" in
    server)     echo "ghcr.io/sislelabs/kuso-server-go:${SANDBOX_IMAGE_TAG}" ;;
    operator)   echo "ghcr.io/sislelabs/kuso-operator:${SANDBOX_IMAGE_TAG}" ;;
    nixpacks)   echo "$(buildcontroller_const defaultNixpacksImage):$(buildcontroller_const defaultNixpacksVersion)" ;;
    env-detect) echo "$(buildcontroller_const defaultEnvDetectImage):$(buildcontroller_const defaultEnvDetectTag)" ;;
    # Hardcoded in the addon chart, the backup/restore handlers, previewdb
    # and the snapshot adapter; most of them pull it with PullAlways.
    backup)     echo "ghcr.io/sislelabs/kuso-backup:latest" ;;
    *) die "unknown image: $1" ;;
  esac
}

# build_image <name>: host-native build from the working tree.
build_image() {
  local name="$1" ref
  ref="$(image_ref "$name")"
  [[ "$ref" == *:* && "$ref" != :* ]] || die "could not resolve the image ref for ${name}"
  log "building ${name} (${ref})"
  case "$name" in
    server)     docker build -q -t "$ref" -f "${REPO_ROOT}/server-go/Dockerfile" "${REPO_ROOT}" >/dev/null ;;
    operator)   docker build -q -t "$ref" "${REPO_ROOT}/operator" >/dev/null ;;
    nixpacks)   docker build -q -t "$ref" \
                  --build-arg "NIXPACKS_VERSION=$(buildcontroller_const defaultNixpacksVersion)" \
                  "${REPO_ROOT}/build/nixpacks" >/dev/null ;;
    env-detect) docker build -q -t "$ref" "${REPO_ROOT}/build/env-detect" >/dev/null ;;
    backup)     docker build -q -t "$ref" "${REPO_ROOT}/build/backup" >/dev/null ;;
  esac
}

# import_image <name>: load the image into the node's containerd. Skipped
# when the node already holds this exact image id.
import_image() {
  local name="$1" ref id marker
  ref="$(image_ref "$name")"
  id="$(docker image inspect -f '{{.Id}}' "$ref")" || die "image ${ref} is not built"
  marker="${STATE_DIR}/imported/${name}"
  if [[ -f "$marker" && "$(cat "$marker")" == "$id" ]] \
      && docker exec "$NODE" ctr -n k8s.io images ls -q | grep -qxF "$ref"; then
    return 0
  fi
  log "importing ${name} into the node"
  docker save "$ref" | docker exec -i "$NODE" ctr -n k8s.io images import - >/dev/null
  mkdir -p "$(dirname "$marker")"
  echo "$id" > "$marker"
}

# mirror_image <name>: push an imported ghcr.io image to the instance's
# mirror registry, which the node's containerd consults before ghcr.io.
# That is what serves PullAlways pulls of a host-arch build. Re-pushed
# only when the imported image changed.
mirror_image() {
  local name="$1" ref path marker
  ref="$(image_ref "$name")"
  [[ "$ref" == ghcr.io/* ]] || return 0
  path="mirror:5000/${ref#ghcr.io/}"
  marker="${STATE_DIR}/mirrored/${name}"
  if [[ -f "$marker" && "$(cat "$marker")" == "$(cat "${STATE_DIR}/imported/${name}")" ]]; then
    return 0
  fi
  log "pushing ${name} to the ghcr.io mirror"
  docker exec "$NODE" ctr -n k8s.io images tag --force "$ref" "$path" >/dev/null
  docker exec "$NODE" ctr -n k8s.io images push --plain-http "$path" "$path" >/dev/null \
    || die "pushing ${name} to the mirror failed"
  docker exec "$NODE" ctr -n k8s.io images rm "$path" >/dev/null
  mkdir -p "$(dirname "$marker")"
  cp "${STATE_DIR}/imported/${name}" "$marker"
}

# Kubelet pulls built images from the in-cluster registry by Service name,
# which only resolves inside pods. install.sh adds the same /etc/hosts line
# on a real host. Docker regenerates the file on container restart, so this
# runs on every `up`.
stamp_registry_host() {
  local ip
  ip="$(node_kubectl -n kuso get svc kuso-registry -o jsonpath='{.spec.clusterIP}')"
  [[ -n "$ip" ]] || die "kuso-registry Service has no ClusterIP"
  # /etc/hosts is a bind mount: rewrite in place, sed -i's rename would fail.
  docker exec "$NODE" sh -c "
    grep -v ' ${REGISTRY_HOST}\$' /etc/hosts > /tmp/hosts.new
    echo '${ip} ${REGISTRY_HOST}' >> /tmp/hosts.new
    cat /tmp/hosts.new > /etc/hosts"
}

# Docker VM disk: the node's images, PVCs and build cache all live there.
# k3s evicts pods when it fills, which looks like random breakage.
SANDBOX_MIN_FREE_GB="${KUSO_SANDBOX_MIN_FREE_GB:-8}"
SANDBOX_PRUNE_BELOW_GB="${KUSO_SANDBOX_PRUNE_BELOW_GB:-15}"
SANDBOX_MAX_USED_PCT="${KUSO_SANDBOX_MAX_USED_PCT:-80}"

# docker_disk: prints "<free GB> <used %>" for the filesystem Docker
# keeps images and volumes on (the VM disk on Docker Desktop).
docker_disk() {
  docker run --rm --entrypoint /bin/df "${KUSO_SANDBOX_K3S_IMAGE:-rancher/k3s:v1.36.6-k3s1}" -Pk / 2>/dev/null \
    | awk 'NR==2 { gsub("%","",$5); printf "%d %d\n", $4/1048576, $5 }'
}

# disk_guard [--check]: prune build cache (never images or volumes) when
# space is low, then refuse to go on below the floor. --check only reports.
disk_guard() {
  local free="" pct=""
  read -r free pct < <(docker_disk) || true
  [[ -n "$free" ]] || { warn "could not measure the Docker disk; skipping the disk guard"; return 0; }
  log "Docker disk: ${free} GB free, ${pct}% used"
  [[ "${1:-}" == "--check" ]] && return 0
  if (( free < SANDBOX_PRUNE_BELOW_GB || pct > SANDBOX_MAX_USED_PCT )); then
    log "low on disk: pruning Docker build cache (images and volumes are left alone)"
    docker builder prune -f >/dev/null 2>&1 || true
    read -r free pct < <(docker_disk) || true
    log "Docker disk after prune: ${free} GB free, ${pct}% used"
  fi
  if (( free < SANDBOX_MIN_FREE_GB )); then
    die "only ${free} GB free on the Docker disk; the sandbox needs ${SANDBOX_MIN_FREE_GB}. To free space:
  docker system df                 see what uses it
  docker builder prune -af         drop all build cache
  KUSO_SANDBOX_NAME=<n> make sandbox-down   remove sandboxes you no longer need
KUSO_SANDBOX_MIN_FREE_GB lowers the floor."
  fi
}

# Host-side curl through the instance's traefik: <host> may be a full
# http://<svc>.<project>.kuso.localhost/path URL or just the host name.
sandbox_curl() {
  local target="$1" host path
  shift
  target="${target#http://}"
  host="${target%%/*}"
  path="/${target#*/}"
  [[ "$target" == */* ]] || path="/"
  host="${host%%:*}"
  curl -sS --max-time "${CURL_TIMEOUT:-10}" -H "Host: ${host}" "http://127.0.0.1:${KUSO_SANDBOX_PORT}${path}" "$@"
}
