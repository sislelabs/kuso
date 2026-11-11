#!/usr/bin/env bash
#
# Installs kuso into the sandbox cluster from the working tree mounted at
# /repo. Runs inside the `tools` container (see compose.yaml); up.sh is the
# entry point.
#
# Follows hack/install.sh's order. What it leaves out is listed in
# manifests.sh. Keep the two in step when install.sh changes.

set -euo pipefail

REPO="${REPO:-/repo}"
DOMAIN="${KUSO_SANDBOX_DOMAIN:-kuso.localhost}"
IMAGE_TAG="${KUSO_SANDBOX_IMAGE_TAG:-sandbox}"
PORT_SUFFIX=":${KUSO_SANDBOX_PORT:-80}"
[[ "$PORT_SUFFIX" == ":80" ]] && PORT_SUFFIX=""

# shellcheck source=hack/sandbox/manifests.sh
source "${REPO}/hack/sandbox/manifests.sh"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# wait_rollout <namespace> <deployment> <timeout>: on failure, print what
# an operator would look at next instead of a bare timeout.
wait_rollout() {
  local ns="$1" name="$2" timeout="$3"
  if kubectl -n "$ns" rollout status "deployment/${name}" --timeout="$timeout" >/dev/null 2>&1; then
    return 0
  fi
  echo "---- ${ns}/${name} did not become ready within ${timeout} ----" >&2
  kubectl -n "$ns" get pods -o wide >&2 || true
  kubectl -n "$ns" get events --sort-by=.lastTimestamp 2>/dev/null | tail -20 >&2 || true
  kubectl -n "$ns" logs "deployment/${name}" --all-containers --tail=40 >&2 || true
  die "${ns}/${name} is not ready"
}

applied() {
  local want="$1" m
  for m in "${SANDBOX_APPLIED[@]}"; do [[ "$m" == "$want" ]] && return 0; done
  return 1
}

# manifest <name>: cat deploy/<name>.yaml, refusing names manifests.sh
# doesn't list as applied so the two can't disagree.
manifest() {
  applied "$1" || die "deploy/$1.yaml is not in SANDBOX_APPLIED (hack/sandbox/manifests.sh)"
  cat "${REPO}/deploy/$1.yaml"
}

kubectl create namespace kuso --dry-run=client -o yaml | kubectl apply -f - >/dev/null

log "PriorityClass kuso-platform"
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: kuso-platform
value: 100000
globalDefault: false
description: "kuso control-plane pods. Survives workload eviction."
EOF

if ! kubectl get svc -n traefik traefik >/dev/null 2>&1; then
  log "traefik"
  helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1 || true
  helm repo update >/dev/null
  helm upgrade --install traefik traefik/traefik \
    --version "${KUSO_TRAEFIK_CHART_VERSION:-42.3.1}" \
    -n traefik --create-namespace \
    --set ports.web.expose.default=true \
    --set ports.websecure.expose.default=true \
    --set service.spec.type=LoadBalancer \
    --set providers.kubernetesIngress.allowExternalNameServices=true \
    --wait --timeout=300s >/dev/null
fi

log "CRDs"
kubectl apply -f "${REPO}/operator/config/crd/bases/" >/dev/null

log "registry"
# Same defaults as a fresh install: registry auth and buildkitd mTLS on.
# kuso-server fills in the htpasswd hash and the certificates; the
# registry and buildkitd wait for them. Only on a sandbox that has no
# registry yet: an older one started its node without the credential in
# registries.yaml (run `make sandbox-down` first to switch it over).
if ! kubectl -n kuso get deployment kuso-registry >/dev/null 2>&1; then
  kubectl -n kuso get secret kuso-registry-auth >/dev/null 2>&1 \
    || kubectl -n kuso create secret generic kuso-registry-auth \
      --from-literal=username=kuso \
      --from-literal=password=6b75736f2d73616e64626f782d6465762d6f6e6c79 \
      --from-literal=enabled=true >/dev/null
  kubectl -n kuso get secret kuso-buildkitd-tls >/dev/null 2>&1 \
    || kubectl -n kuso create secret generic kuso-buildkitd-tls --from-literal=enabled=true >/dev/null
fi
manifest registry | kubectl apply -f - >/dev/null
manifest vulnscan-egress | kubectl apply -f - >/dev/null

log "buildkitd"
manifest buildkitd | kubectl apply -f - >/dev/null
# The manifest keeps buildkitd off control-plane nodes; the sandbox has
# only the one node.
if [[ -n "$(kubectl -n kuso get deployment kuso-buildkitd -o jsonpath='{.spec.template.spec.affinity}')" ]]; then
  kubectl -n kuso patch deployment kuso-buildkitd --type=json \
    -p '[{"op":"remove","path":"/spec/template/spec/affinity"}]' >/dev/null
fi

log "postgres"
kubectl apply -f "${REPO}/hack/sandbox/postgres.yaml" >/dev/null
wait_rollout kuso kuso-postgres 180s

log "secrets (well-known dev values, same as KUSO_INSECURE_SECRETS=1)"
# KUSO_UPDATER_DISABLED and KUSO_PUBLIC_URL ride along here because the
# Deployment loads this Secret with envFrom; that avoids a second rollout
# from `kubectl set env`. KUSO_PUBLIC_URL carries the host port: without
# it the URLs kuso prints (deploy hooks, GitHub App callbacks) drop it.
kubectl create secret generic kuso-server-secrets -n kuso --dry-run=client -o yaml \
  --from-literal=KUSO_SESSION_KEY="dev-session-key-do-not-use-in-prod-3232" \
  --from-literal=JWT_SECRET="dev-jwt-secret-do-not-use-in-prod-32-chars" \
  --from-literal=KUSO_ALLOW_INSECURE_JWT="1" \
  --from-literal=KUSO_DOMAIN="$DOMAIN" \
  --from-literal=KUSO_REQUIRE_SIGNATURES="true" \
  --from-literal=KUSO_METRICS_SCRAPE_TOKEN="dev-metrics-scrape-token-do-not-use" \
  --from-literal=KUSO_UPDATER_DISABLED="true" \
  --from-literal=KUSO_PUBLIC_URL="http://${DOMAIN}${PORT_SUFFIX}" \
  --from-literal=KUSO_ALLOW_PRIVATE_OUTBOUND="true" \
  | kubectl apply -f - >/dev/null
# KUSO_ALLOW_PRIVATE_OUTBOUND (above) is the existing knob for internal-only
# installs: the server reaches the sandbox S3 through traefik's ClusterIP.
# It also lets notification webhooks and uptime probes reach private
# addresses, so SSRF checks can't be exercised against this sandbox.

# A GitHub App that only exists here: the id and key are fake (the key is
# a throwaway generated by up.sh), which is enough to register the webhook
# route. Projects keep installation id 0, so the push/preview path makes
# no GitHub API calls and builds clone the public repo anonymously.
# hack/sandbox/gh-event signs deliveries with the same secret.
if [[ -n "${KUSO_SANDBOX_STATE:-}" && -f "${KUSO_SANDBOX_STATE}/github-app.pem" ]]; then
  log "GitHub App (sandbox-only, fake credentials)"
  kubectl create secret generic kuso-github-app -n kuso --dry-run=client -o yaml \
    --from-literal=GITHUB_APP_ID="1" \
    --from-literal=GITHUB_APP_SLUG="kuso-sandbox" \
    --from-literal=GITHUB_APP_WEBHOOK_SECRET="kuso-sandbox-webhook-secret" \
    --from-file=GITHUB_APP_PRIVATE_KEY="${KUSO_SANDBOX_STATE}/github-app.pem" \
    | kubectl apply -f - >/dev/null
fi
kubectl create secret generic kuso-admin-credentials -n kuso --dry-run=client -o yaml \
  --from-literal=password="${KUSO_SANDBOX_ADMIN_PASSWORD:-kuso-admin}" \
  | kubectl apply -f - >/dev/null

log "operator"
manifest operator \
  | sed -E "s|kuso-operator:[A-Za-z0-9._-]+|kuso-operator:${IMAGE_TAG}|g" \
  | sed -E "s|imagePullPolicy: Always|imagePullPolicy: IfNotPresent|g" \
  | kubectl apply -f - >/dev/null
wait_rollout kuso-operator-system kuso-operator-controller-manager 300s

log "server"
manifest server-go \
  | sed -E "s|kuso-server-go:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-server-go:${IMAGE_TAG}|g" \
  | kubectl apply -f - >/dev/null
manifest kuso-admission-policy | kubectl apply -f - >/dev/null

log "activator"
manifest kuso-activator \
  | sed -E "s|kuso-server-go:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-server-go:${IMAGE_TAG}|g" \
  | kubectl apply -f - >/dev/null

# install.sh also adds an https-redirect Middleware and a TLS block here;
# the sandbox serves plain HTTP.
kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata:
  name: kuso-server
  namespace: kuso
  labels: { app.kubernetes.io/name: kuso-server }
spec:
  selector: { app.kubernetes.io/name: kuso-server }
  ports:
    - { name: http, port: 80, targetPort: 3000, protocol: TCP }
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kuso-server
  namespace: kuso
spec:
  ingressClassName: traefik
  rules:
    - host: ${DOMAIN}
      http:
        paths:
          - { path: /, pathType: Prefix, backend: { service: { name: kuso-server, port: { number: 80 } } } }
EOF

log "prometheus"
kubectl create secret generic kuso-prometheus-scrape -n kuso --dry-run=client -o yaml \
  --from-literal=token="dev-metrics-scrape-token-do-not-use" \
  | kubectl apply -f - >/dev/null
# The production sizing (256Mi request) is more than the sandbox needs.
manifest prometheus \
  | sed -E 's|requests: \{ cpu: 50m, memory: 256Mi \}|requests: { cpu: 20m, memory: 96Mi }|; s|retention.size=2GB|retention.size=512MB|' \
  | kubectl apply -f - >/dev/null

if [[ "${KUSO_SANDBOX_S3:-1}" == "1" ]]; then
  log "backup S3 (MinIO at s3.sandbox.test)"
  kubectl apply -f "${REPO}/hack/sandbox/s3.yaml" >/dev/null
  traefik_ip="$(kubectl -n traefik get svc traefik -o jsonpath='{.spec.clusterIP}')"
  [[ -n "$traefik_ip" ]] || die "traefik Service has no ClusterIP"
  # k3s's Corefile imports /etc/coredns/custom/*.server and reloads on change.
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  sandbox.server: |
    sandbox.test:53 {
      hosts {
        ${traefik_ip} s3.sandbox.test
      }
    }
EOF
  wait_rollout kuso-sandbox sandbox-s3 180s
  kubectl -n kuso-sandbox delete pod sandbox-s3-mkbucket --ignore-not-found >/dev/null
  kubectl -n kuso-sandbox run sandbox-s3-mkbucket --restart=Never --quiet \
    --image=pgsty/mc:RELEASE.2026-10-26T00-00-00Z \
    --env="MC_HOST_s=http://kuso-sandbox:kuso-sandbox-s3-secret@sandbox-s3:9000" \
    -- mb --ignore-existing s/kuso-backups >/dev/null
  kubectl -n kuso-sandbox wait --for=jsonpath='{.status.phase}'=Succeeded pod/sandbox-s3-mkbucket --timeout=120s >/dev/null \
    || { kubectl -n kuso-sandbox logs sandbox-s3-mkbucket >&2 || true; die "creating the kuso-backups bucket failed"; }
  kubectl -n kuso-sandbox delete pod sandbox-s3-mkbucket >/dev/null
fi

wait_rollout kuso kuso-registry 180s
wait_rollout kuso kuso-buildkitd 300s
wait_rollout kuso kuso-server 300s
wait_rollout kuso kuso-activator 180s

log "kuso is installed"
