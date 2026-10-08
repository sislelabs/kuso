#!/usr/bin/env bash
#
# Bring up (or refresh) the kuso sandbox: build images from the working
# tree, start a single-node k3s in Docker, install kuso into it.
# Safe to re-run. See README.md.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v docker >/dev/null || die "docker is required"
docker info >/dev/null 2>&1 || die "the docker daemon is not reachable"
mkdir -p "$STATE_DIR"

IMAGES=(server operator nixpacks env-detect)

if [[ "${KUSO_SANDBOX_SKIP_BUILD:-0}" != "1" ]]; then
  for img in "${IMAGES[@]}"; do build_image "$img"; done
fi

log "starting the k3s node"
compose up -d node >/dev/null

log "waiting for the node to be Ready"
for _ in $(seq 1 90); do
  if node_kubectl get nodes 2>/dev/null | grep -q ' Ready '; then ready=1; break; fi
  sleep 2
done
[[ "${ready:-0}" == "1" ]] || { docker logs --tail 40 "$NODE" >&2; die "the k3s node did not become Ready"; }

# A re-run after a code change loads a new image under the same tag;
# pods only pick it up when restarted. Remember which ones were replaced.
replaced=()
for img in "${IMAGES[@]}"; do
  marker="${STATE_DIR}/imported/${img}"
  before="$(cat "$marker" 2>/dev/null || true)"
  import_image "$img"
  [[ -n "$before" && "$before" != "$(cat "$marker")" ]] && replaced+=("$img")
done

compose --profile tools run --rm -T tools /repo/hack/sandbox/bootstrap.sh

for img in ${replaced[@]+"${replaced[@]}"}; do
  case "$img" in
    server)
      log "restarting the server on its new image"
      node_kubectl -n kuso rollout restart deployment/kuso-server deployment/kuso-activator >/dev/null
      node_kubectl -n kuso rollout status deployment/kuso-server --timeout=300s >/dev/null
      ;;
    operator)
      log "restarting the operator on its new image"
      node_kubectl -n kuso-operator-system rollout restart deployment/kuso-operator-controller-manager >/dev/null
      node_kubectl -n kuso-operator-system rollout status deployment/kuso-operator-controller-manager --timeout=300s >/dev/null
      ;;
  esac
done

stamp_registry_host

docker exec "$NODE" cat /output/kubeconfig.yaml \
  | sed "s#https://127.0.0.1:6443#https://127.0.0.1:${KUSO_SANDBOX_API_PORT}#" > "${STATE_DIR}/kubeconfig"
chmod 600 "${STATE_DIR}/kubeconfig"

log "building the kuso CLI"
mkdir -p "${STATE_DIR}/bin"
if command -v go >/dev/null; then
  (cd "${REPO_ROOT}/cli" && go build -o "${STATE_DIR}/bin/kuso" ./cmd)
else
  # No Go on the host: cross-compile for it in a container.
  goos="$(uname -s | tr '[:upper:]' '[:lower:]')"
  goarch="$(uname -m)"; [[ "$goarch" == "x86_64" ]] && goarch=amd64; [[ "$goarch" == "aarch64" ]] && goarch=arm64
  docker run --rm -v "${REPO_ROOT}:/src:ro" -v "${STATE_DIR}/bin:/out" \
    -v kuso-sandbox-gocache:/go -w /src/cli \
    -e GOOS="$goos" -e GOARCH="$goarch" -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
    golang:1.26-bookworm go build -o /out/kuso ./cmd
fi

log "waiting for the API"
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null -H "Host: ${SANDBOX_DOMAIN}" "http://127.0.0.1:${KUSO_SANDBOX_PORT}/healthz" 2>/dev/null; then up=1; break; fi
  sleep 2
done
[[ "${up:-0}" == "1" ]] || die "the API did not answer on http://127.0.0.1:${KUSO_SANDBOX_PORT} (Host: ${SANDBOX_DOMAIN})"

"${SANDBOX_DIR}/kuso" login --api "$SANDBOX_API_URL" -u admin -p "$SANDBOX_ADMIN_PASSWORD" >/dev/null \
  || die "kuso login against ${SANDBOX_API_URL} failed"

cat <<MSG

kuso sandbox is up.

  UI / API   ${SANDBOX_API_URL}
  Login      admin / ${SANDBOX_ADMIN_PASSWORD}
  CLI        hack/sandbox/kuso <command>      (already logged in; never touches ~/.kuso)
  kubectl    KUBECONFIG=hack/sandbox/.state/kubeconfig kubectl ...
  Services   http://<service>.<project>.${SANDBOX_DOMAIN}${SANDBOX_PORT_SUFFIX}

  make sandbox-reload   rebuild + restart server and operator
  make sandbox-smoke    scripted end-to-end check
  make sandbox-down     delete everything
MSG
