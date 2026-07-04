#!/usr/bin/env bash
# CRD dry-run smoke test — applies the CRD schemas + server-side dry-runs
# every sample CR against a kind cluster. Extracted verbatim from the old
# .github/workflows/ci.yml `crd-smoke` job so the pre-push FULL-mode
# operator check matches what CI used to enforce.
#
# Requires kind + kubectl. Spins up (and tears down) a throwaway cluster.
set -euo pipefail

cluster="kuso-hook-smoke"
cleanup() { kind delete cluster --name "$cluster" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "  creating kind cluster $cluster"
kind create cluster --name "$cluster" --wait 90s >/dev/null

# Sample CRs declare `namespace: kuso`; create it so the server-side
# dry-run tests CR schema validity, not a missing-namespace error.
kubectl create namespace kuso --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f operator/config/crd/bases/

fail=0
for f in operator/config/samples/application_v1alpha1_*.yaml; do
  if kubectl apply --dry-run=server -f "$f" >/dev/null 2>&1; then
    echo "  OK    $f"
  else
    echo "  FAIL  $f"
    kubectl apply --dry-run=server -f "$f" 2>&1 | sed 's/^/        /'
    fail=$((fail + 1))
  fi
done

if [[ "$fail" -gt 0 ]]; then
  echo "  ==> $fail sample(s) failed"
  exit 1
fi
echo "  all CR samples validated"
