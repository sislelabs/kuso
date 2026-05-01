#!/usr/bin/env bash
#
# kuso single-command installer.
#
# Provisions k3s (if not already present), installs traefik + cert-manager
# + Let's Encrypt issuer + kuso CRDs + operator + server + in-cluster
# registry. Wires up the gotchas that bit us during the manual install:
#
#   - k3s containerd config to trust the registry on plain HTTP
#   - /etc/hosts entry on the host so kubelet can resolve the
#     in-cluster registry's DNS name when pulling images
#   - random KUSO_SESSION_KEY / JWT_SECRET / KUSO_ADMIN_PASSWORD
#     (printed once at the end so the user can record them)
#
# After this script finishes, https://<KUSO_DOMAIN>/ serves the kuso UI.
# `kuso login --api https://<KUSO_DOMAIN> -u admin -p <printed>` works
# from any workstation with reachable DNS.
#
# Usage:
#   curl -sfL https://raw.githubusercontent.com/sislelabs/kuso/main/hack/install.sh | sudo bash
#
#   curl -sfL .../install.sh | sudo \
#     KUSO_DOMAIN=kuso.example.com KUSO_EMAIL=you@example.com bash
#
# Tunable env (with defaults):
#   KUSO_DOMAIN          hostname for kuso UI (default: kuso.sislelabs.com)
#   KUSO_EMAIL           email for Let's Encrypt (default: ivilthe69@gmail.com)
#   KUSO_VERSION         server / operator image tag (default: v0.1.0-dev,
#                        always tracks the latest pushed dev image)
#   KUSO_REPO            GitHub source for raw manifest URLs
#                        (default: sislelabs/kuso)
#   KUSO_ADMIN_PASSWORD  override the auto-generated admin password
#   KUSO_SKIP_K3S=1      assume k3s + traefik already installed
#   KUSO_INSECURE_SECRETS=1  reuse the well-known dev secrets instead of
#                        generating random ones (kuso-admin / dev jwt /
#                        dev session). Only for local kind clusters.

set -euo pipefail

KUSO_DOMAIN="${KUSO_DOMAIN:-kuso.sislelabs.com}"
KUSO_EMAIL="${KUSO_EMAIL:-ivilthe69@gmail.com}"
KUSO_VERSION="${KUSO_VERSION:-v0.1.0-dev}"
KUSO_REPO="${KUSO_REPO:-sislelabs/kuso}"
KUSO_RAW="https://raw.githubusercontent.com/${KUSO_REPO}/main"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*"; exit 1; }

random_string() {
  # 32 url-safe chars from /dev/urandom. Fallback if openssl missing.
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32
  else
    head -c 64 /dev/urandom | tr -dc 'A-Za-z0-9' | head -c 32
  fi
}

# -------- 1. k3s --------
if [[ "${KUSO_SKIP_K3S:-0}" != "1" ]] && ! command -v k3s >/dev/null 2>&1; then
  log "installing k3s (single-node, traefik disabled — we install our own)"
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable=traefik --tls-san=${KUSO_DOMAIN} --write-kubeconfig-mode=644" sh -
else
  log "k3s already present; skipping install"
fi

# -------- 2. registry trust + /etc/hosts --------
# k3s containerd needs to know it can pull from the in-cluster registry
# over plain HTTP. Without this, kubelet rejects the Service's HTTPS-only
# resolve attempt with "TLS handshake failed".
log "writing /etc/rancher/k3s/registries.yaml (insecure HTTP for in-cluster registry)"
mkdir -p /etc/rancher/k3s
cat > /etc/rancher/k3s/registries.yaml <<EOF
mirrors:
  "kuso-registry.kuso.svc.cluster.local:5000":
    endpoint:
      - "http://kuso-registry.kuso.svc.cluster.local:5000"
configs:
  "kuso-registry.kuso.svc.cluster.local:5000":
    tls:
      insecure_skip_verify: true
EOF

# /etc/hosts mapping is the second half: kubelet itself runs on the host,
# not inside the cluster, so it can't resolve cluster-internal DNS.
# We point the registry's cluster DNS name at its Service ClusterIP. The
# IP is stable per-install (k8s allocates from a fixed range and we don't
# delete the Service across reinstalls). Updated by the post-Service-
# create step below.
ensure_registry_hosts_entry() {
  local ip="$1"
  if ! grep -q '\bkuso-registry\.kuso\.svc\.cluster\.local\b' /etc/hosts 2>/dev/null; then
    echo "$ip kuso-registry.kuso.svc.cluster.local" >> /etc/hosts
    log "added /etc/hosts: $ip -> kuso-registry.kuso.svc.cluster.local"
  else
    sed -i.bak -E "s|^.*kuso-registry\.kuso\.svc\.cluster\.local.*\$|$ip kuso-registry.kuso.svc.cluster.local|" /etc/hosts
    log "updated /etc/hosts: $ip -> kuso-registry.kuso.svc.cluster.local"
  fi
}

# Restart k3s if registries.yaml changed and k3s is already running, so
# containerd picks up the new mirror config.
if systemctl is-active --quiet k3s 2>/dev/null; then
  log "restarting k3s to pick up registries.yaml"
  systemctl restart k3s
fi

export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
export PATH="$PATH:/usr/local/bin"

until kubectl get nodes 2>/dev/null | grep -q ' Ready '; do
  log "waiting for k3s control-plane to be Ready..."
  sleep 3
done
log "k3s ready"

# -------- 3. helm (needed for traefik) --------
if ! command -v helm >/dev/null 2>&1; then
  log "installing helm"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

# -------- 4. traefik (ingress) --------
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

# -------- 5. cert-manager --------
if ! kubectl get ns cert-manager >/dev/null 2>&1; then
  log "installing cert-manager"
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.0/cert-manager.yaml >/dev/null
  log "waiting for cert-manager-webhook..."
  until kubectl wait --for=condition=Available --timeout=5s deployment/cert-manager-webhook -n cert-manager >/dev/null 2>&1; do
    sleep 3
  done
else
  log "cert-manager already installed; skipping"
fi

# -------- 6. ClusterIssuer --------
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

# -------- 7. CRDs --------
log "applying kuso CRDs"
# v0.2 ships 6 CRDs: KusoProject, KusoService, KusoEnvironment, KusoAddon,
# KusoBuild, plus the cluster-config Kuso. Names hardcoded so the install
# works without git clone.
for crd in kusoprojects kusoservices kusoenvironments kusoaddons kusobuilds; do
  url="${KUSO_RAW}/operator/config/crd/bases/application.kuso.sislelabs.com_${crd}.yaml"
  if ! curl -sfL "$url" | kubectl apply -f - >/dev/null; then
    warn "failed to apply CRD ${crd} from ${url}"
  fi
done
# kusoes (the cluster-config Kuso CRD) is still under the old filename
# prefix — keep until the operator's CRD generator gets re-run.
curl -sfL "${KUSO_RAW}/operator/config/crd/bases/application.kuso.dev_kusoes.yaml" \
  | kubectl apply -f - >/dev/null || true

# -------- 8. registry --------
log "deploying in-cluster registry"
curl -sfL "${KUSO_RAW}/deploy/registry.yaml" | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-registry -n kuso || warn "kuso-registry not yet ready"
REGISTRY_IP=$(kubectl get svc kuso-registry -n kuso -o jsonpath='{.spec.clusterIP}')
ensure_registry_hosts_entry "$REGISTRY_IP"

# -------- 9. server secrets --------
# Either generate random secrets or — for local kind dev — reuse the
# well-known dev defaults so the test scripts under hack/smoke keep
# working without surfacing a random password.
if [[ "${KUSO_INSECURE_SECRETS:-0}" == "1" ]]; then
  log "using INSECURE dev secrets (admin / kuso-admin)"
  ADMIN_PASSWORD="${KUSO_ADMIN_PASSWORD:-kuso-admin}"
  SESSION_KEY="dev-session-key-do-not-use-in-prod-3232"
  JWT_SECRET="dev-jwt-secret-do-not-use-in-prod-32-chars"
else
  ADMIN_PASSWORD="${KUSO_ADMIN_PASSWORD:-$(random_string)}"
  SESSION_KEY="$(random_string)"
  JWT_SECRET="$(random_string)"
fi

log "creating kuso-server-secrets"
kubectl create namespace kuso 2>/dev/null || true
kubectl create secret generic kuso-server-secrets -n kuso --dry-run=client -o yaml \
  --from-literal=KUSO_SESSION_KEY="$SESSION_KEY" \
  --from-literal=JWT_SECRET="$JWT_SECRET" \
  --from-literal=KUSO_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  | kubectl apply -f - >/dev/null

# -------- 10. operator --------
log "applying kuso operator (image tag ${KUSO_VERSION})"
curl -sfL "${KUSO_RAW}/deploy/operator.yaml" \
  | sed "s|kuso-operator:v0.1.0-dev|kuso-operator:${KUSO_VERSION}|g" \
  | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-operator-controller-manager -n kuso-operator-system

# -------- 11. server --------
log "applying kuso server (host ${KUSO_DOMAIN}, image tag ${KUSO_VERSION})"
curl -sfL "${KUSO_RAW}/deploy/server.yaml" \
  | sed "s|kuso.sislelabs.com|${KUSO_DOMAIN}|g; s|kuso-server:v0.1.0-dev|kuso-server:${KUSO_VERSION}|g" \
  | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-server -n kuso

# -------- 12. summary --------
echo
log "kuso is up"
echo
echo "  UI:        https://${KUSO_DOMAIN}/"
echo "  Admin:     admin"
echo "  Password:  ${ADMIN_PASSWORD}"
echo
echo "  CLI login from your workstation:"
echo "    kuso login --api https://${KUSO_DOMAIN} -u admin -p '${ADMIN_PASSWORD}'"
echo
if [[ "${KUSO_INSECURE_SECRETS:-0}" != "1" ]]; then
  cat <<EOF
  Save this password somewhere safe. To regenerate it later:

    kubectl -n kuso patch secret kuso-server-secrets \\
      --type=merge -p '{"stringData":{"KUSO_ADMIN_PASSWORD":"<new>"}}'
    kubectl -n kuso rollout restart deployment/kuso-server

  GitHub App: not yet configured. Follow docs/GITHUB_APP_SETUP.md to
  enable the repo picker and PR previews. The kuso UI will show a CTA
  until you create the kuso-github-app Secret.
EOF
fi
echo
