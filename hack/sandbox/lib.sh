# shellcheck shell=bash
# Shared by up.sh, reload.sh, down.sh and smoke.sh.

SANDBOX_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SANDBOX_DIR}/../.." && pwd)"
STATE_DIR="${SANDBOX_DIR}/.state"

NODE="kuso-sandbox-node"
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
