#!/usr/bin/env bash
# Validate CRDs, CR samples and a render matrix of every watched chart
# against a real apiserver with `kubectl apply --dry-run=server`.
#
# `helm template` and the Go chart tests only see text. Whole classes of
# bug only show up at the apiserver: a document with no apiVersion (broke
# every project's reconcile for 55 days), a label value that decodes as an
# int/bool, a field an older apiserver doesn't serve. This catches them
# before a release does.
#
# Needs: a kube context pointing at a throwaway cluster (CI uses kind),
# kubectl, helm, python3 + PyYAML. Run from anywhere:
#   kind create cluster --name kuso-ci && hack/ci/apiserver-validate.sh
set -euo pipefail

cd "$(dirname "$0")/../.."

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "==> namespaces"
kubectl create namespace kuso --dry-run=client -o yaml | kubectl apply -f - >/dev/null

echo "==> kuso CRDs"
kubectl apply -f operator/config/crd/bases/ >/dev/null

# Third-party kinds the charts render. A stub CRD that accepts any spec is
# enough for what this checks (object metadata, apiVersion/kind wiring);
# the real schemas belong to Traefik and CloudNativePG.
echo "==> stub CRDs for traefik.io + postgresql.cnpg.io"
stub_crd() { # stub_crd <group> <version> <kind> <plural>
  cat <<EOF
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: $4.$1
spec:
  group: $1
  scope: Namespaced
  names: {kind: $3, plural: $4, singular: $(tr '[:upper:]' '[:lower:]' <<<"$3"), listKind: $3List}
  versions:
    - name: $2
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
EOF
}
{
  stub_crd traefik.io v1alpha1 IngressRouteTCP ingressroutetcps
  stub_crd postgresql.cnpg.io v1 Cluster clusters
  stub_crd postgresql.cnpg.io v1 Pooler poolers
} > "$work/stub-crds.yaml"
kubectl apply -f "$work/stub-crds.yaml" >/dev/null
kubectl wait --for=condition=Established --timeout=60s crd --all >/dev/null

fail=0
dry_run() { # dry_run <file> <label>
  local out
  if out="$(kubectl apply --dry-run=server -f "$1" 2>&1)"; then
    echo "  OK    $2"
  else
    echo "  FAIL  $2"
    sed 's/^/        /' <<<"$out"
    fail=$((fail + 1))
  fi
}

echo "==> CR samples"
for f in operator/config/samples/application_v1alpha1_kuso{project,service,environment,addon,build}.yaml; do
  [[ -f "$f" ]] && dry_run "$f" "$f"
done

echo "==> rendered charts"
python3 hack/chart-check.py render "$work/rendered" >/dev/null
for f in "$work"/rendered/*.yaml; do
  dry_run "$f" "$(basename "$f" .yaml)"
done

if (( fail > 0 )); then
  echo "==> ${fail} manifest(s) rejected by the apiserver"
  exit 1
fi
echo "==> apiserver accepted every CRD, sample and rendered chart"
