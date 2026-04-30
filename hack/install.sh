#!/usr/bin/env bash
#
# kuso single-command installer.
#
# Provisions k3s (if not already present) on the local machine, installs
# traefik (LoadBalancer) and cert-manager, applies kuso CRDs and the
# operator + server deployments, and creates a Let's Encrypt ClusterIssuer.
#
# After this script finishes, https://<KUSO_DOMAIN>/ should serve the kuso
# UI. Apps deployed as KusoApp CRs will get reconciled by the operator.
#
# Usage:
#   curl -sfL https://raw.githubusercontent.com/sislelabs/kuso/main/hack/install.sh | sudo bash
#   # or with overrides:
#   curl -sfL .../install.sh | KUSO_DOMAIN=kuso.example.com KUSO_EMAIL=you@example.com sudo bash
#
# Required env (with defaults):
#   KUSO_DOMAIN     hostname for the kuso UI (default: kuso.sislelabs.com)
#   KUSO_EMAIL      email for Let's Encrypt registration (default: ivilthe69@gmail.com)
#   KUSO_VERSION    image tag for kuso-server / kuso-operator (default: v0.1.0-dev)
#   KUSO_REPO       GitHub source for raw manifest URLs (default: sislelabs/kuso)

set -euo pipefail

KUSO_DOMAIN="${KUSO_DOMAIN:-kuso.sislelabs.com}"
KUSO_EMAIL="${KUSO_EMAIL:-ivilthe69@gmail.com}"
KUSO_VERSION="${KUSO_VERSION:-v0.1.0-dev}"
KUSO_REPO="${KUSO_REPO:-sislelabs/kuso}"
KUSO_RAW="https://raw.githubusercontent.com/${KUSO_REPO}/main"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*"; exit 1; }

# -------- 1. k3s --------
if ! command -v k3s >/dev/null 2>&1; then
  log "installing k3s (single-node, no traefik — we install our own)"
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable=traefik --tls-san=${KUSO_DOMAIN} --write-kubeconfig-mode=644" sh -
else
  log "k3s already installed; skipping"
fi

export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
PATH="$PATH:/usr/local/bin"

until kubectl get nodes 2>/dev/null | grep -q ' Ready '; do
  log "waiting for k3s control-plane to be Ready..."
  sleep 3
done
log "k3s ready"

# -------- 2. helm (needed for traefik) --------
if ! command -v helm >/dev/null 2>&1; then
  log "installing helm"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

# -------- 3. traefik (ingress) --------
if ! kubectl get svc -n traefik traefik >/dev/null 2>&1; then
  log "installing traefik"
  helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1 || true
  helm repo update >/dev/null
  helm upgrade --install traefik traefik/traefik \
    -n traefik --create-namespace \
    --set ports.web.expose.default=true \
    --set ports.websecure.expose.default=true \
    --set service.type=LoadBalancer \
    --wait --timeout=180s >/dev/null
else
  log "traefik already installed; skipping"
fi

# -------- 4. cert-manager --------
if ! kubectl get ns cert-manager >/dev/null 2>&1; then
  log "installing cert-manager"
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.0/cert-manager.yaml >/dev/null
  log "waiting for cert-manager-webhook to be ready..."
  until kubectl wait --for=condition=Available --timeout=5s deployment/cert-manager-webhook -n cert-manager >/dev/null 2>&1; do
    sleep 3
  done
else
  log "cert-manager already installed; skipping"
fi

# -------- 5. ClusterIssuer --------
log "creating Let's Encrypt ClusterIssuer (email=${KUSO_EMAIL})"
until kubectl apply -f - >/dev/null 2>&1 <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ${KUSO_EMAIL}
    privateKeySecretRef:
      name: letsencrypt-prod-key
    solvers:
      - http01:
          ingress:
            class: traefik
EOF
do
  sleep 3
done

# -------- 6. kuso CRDs --------
log "applying kuso CRDs"
kubectl apply -k "${KUSO_RAW}/operator/config/crd/bases/" 2>/dev/null \
  || kubectl apply -f "${KUSO_RAW}/operator/config/crd/bases/" 2>/dev/null \
  || {
    warn "kustomize/glob over raw URL not supported; falling back to single-file fetch"
    # CRD names hardcoded — keep in sync with operator/config/crd/bases/
    for crd in kusoapps kusobuilds kusoes kusomails kusopipelines kusoprometheuses \
               kusoaddonmemcacheds kusoaddonmongodbs kusoaddonmysqls kusoaddonpostgres \
               kusoaddonrabbitmqs kusoaddonredis \
               kusocouchdbs kusoelasticsearches kusokafkas kusomemcacheds kusomongodbs \
               kusomysqls kusopostgresqls kusorabbitmqs kusoredis; do
      kubectl apply -f "${KUSO_RAW}/operator/config/crd/bases/application.kuso.dev_${crd}.yaml"
    done
  }

# -------- 7. kuso operator --------
log "applying kuso operator (image tag ${KUSO_VERSION})"
curl -sfL "${KUSO_RAW}/deploy/operator.yaml" | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-operator-controller-manager -n kuso-operator-system

# -------- 8. kuso server --------
log "applying kuso server (host ${KUSO_DOMAIN}, image tag ${KUSO_VERSION})"
curl -sfL "${KUSO_RAW}/deploy/server.yaml" \
  | sed "s|kuso.sislelabs.com|${KUSO_DOMAIN}|g; s|kuso-server:v0.1.0-dev|kuso-server:${KUSO_VERSION}|g" \
  | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-server -n kuso

# -------- 9. summary --------
log "kuso is up"
echo
echo "  UI:       https://${KUSO_DOMAIN}/"
echo "  Default admin: admin / kuso-admin"
echo
echo "  IMPORTANT: change the admin password and rotate the JWT_SECRET /"
echo "  KUSO_SESSION_KEY in the kuso-server-secrets Secret before exposing"
echo "  this instance to anyone besides yourself."
echo
echo "  Deploy your first app:"
echo "    kubectl apply -f ${KUSO_RAW}/deploy/hello-world.yaml"
echo
