#!/usr/bin/env bash
# kuso-updater entrypoint. Runs inside the Job that the self-updater
# kicks off (server-go/internal/updater/applyJob). Reads env vars
# set by the server, walks the upgrade in clear phases, and writes
# JSON status to the ConfigMap so the UI can render progress without
# tailing pod logs.

set -euo pipefail

NS="${KUSO_NAMESPACE:-kuso}"
CM="${KUSO_STATUS_CONFIGMAP:-kuso-update-status}"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

write_status() {
  local phase="$1"
  local message="${2:-}"
  local started="${STARTED_AT:-$(now)}"
  local payload
  payload=$(jq -n \
    --arg phase   "$phase" \
    --arg message "$message" \
    --arg started "$started" \
    --arg updated "$(now)" \
    '{phase:$phase, message:$message, started:$started, updated:$updated}')

  kubectl create configmap "$CM" -n "$NS" \
    --from-literal=status="$payload" \
    --dry-run=client -o yaml \
  | kubectl apply -f - >/dev/null
}

STARTED_AT="$(now)"
write_status "applying-crds" "downloading ${KUSO_CRDS_URL}"

TMP_CRDS=$(mktemp)
curl -fsSL "$KUSO_CRDS_URL" -o "$TMP_CRDS"
kubectl apply -f "$TMP_CRDS" >/dev/null

write_status "rolling-server" "${KUSO_SERVER_IMAGE}"
kubectl set image -n "$NS" deploy/kuso-server "server=${KUSO_SERVER_IMAGE}" >/dev/null
kubectl rollout status -n "$NS" deploy/kuso-server --timeout=180s

write_status "rolling-operator" "${KUSO_OPERATOR_IMAGE}"
OP_NS="${KUSO_OPERATOR_NS:-kuso-operator-system}"
for d in kuso-operator-controller-manager kuso-operator; do
  if kubectl get -n "$OP_NS" "deploy/$d" >/dev/null 2>&1; then
    kubectl set image -n "$OP_NS" "deploy/$d" "manager=${KUSO_OPERATOR_IMAGE}" >/dev/null \
      || kubectl set image -n "$OP_NS" "deploy/$d" "*=${KUSO_OPERATOR_IMAGE}" >/dev/null
    kubectl rollout status -n "$OP_NS" "deploy/$d" --timeout=180s
    break
  fi
done

write_status "done" "upgraded to ${KUSO_TARGET_VERSION}"
echo "==> upgrade to ${KUSO_TARGET_VERSION} complete"
