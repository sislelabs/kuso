#!/usr/bin/env bash
#
# CRD dry-run smoke test (smoke level "3b" from docs/PRD.md phasing).
#
# Spins up a kind cluster, applies every kuso CRD, and validates that
# every sample CR in operator/config/samples/ is accepted by the API
# server. Confirms the rebrand didn't break operator schemas.
#
# Does NOT install the operator binary or the kuso server. For a full
# end-to-end install, see docs/INSTALL.md (TODO: not written yet).
#
# Requires: kind, kubectl, docker (for kind nodes).
#
# Usage:
#   hack/smoke/crd-dryrun.sh           # bring up cluster, validate, tear down
#   KEEP_CLUSTER=1 hack/smoke/crd-dryrun.sh   # leave cluster running for inspection

set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-kuso-smoke}"
CONTEXT="kind-${CLUSTER_NAME}"

cd "$(dirname "$0")/../.."

cleanup() {
  if [[ "${KEEP_CLUSTER:-0}" != "1" ]]; then
    echo "==> tearing down cluster ${CLUSTER_NAME}"
    kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  else
    echo "==> KEEP_CLUSTER=1 set; leaving ${CLUSTER_NAME} running"
    echo "    delete with: kind delete cluster --name ${CLUSTER_NAME}"
  fi
}
trap cleanup EXIT

echo "==> creating kind cluster ${CLUSTER_NAME}"
kind create cluster --name "${CLUSTER_NAME}" --wait 90s >/dev/null

echo "==> applying CRDs"
kubectl --context "${CONTEXT}" apply -f operator/config/crd/bases/ >/dev/null

echo "==> server-side dry-run on every sample CR"
fail=0
for f in operator/config/samples/application_v1alpha1_*.yaml; do
  out=$(kubectl --context "${CONTEXT}" apply --dry-run=server -f "$f" 2>&1) || {
    echo "FAIL  $f"
    echo "$out" | sed 's/^/      /'
    fail=$((fail + 1))
    continue
  }
  echo "OK    $f"
done

if [[ "$fail" -gt 0 ]]; then
  echo "==> ${fail} sample(s) failed validation"
  exit 1
fi

echo "==> all samples validated"
