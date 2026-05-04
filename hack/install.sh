#!/usr/bin/env bash
#
# kuso single-command installer.
#
# Provisions k3s (if not already present), installs traefik + cert-manager
# + Let's Encrypt issuer + kuso CRDs + operator + server + in-cluster
# registry on a single Linux host.
#
# Architecture: every kuso install is its own k8s cluster. The script
# handles the gotchas:
#
#   - k3s containerd config to trust the registry on plain HTTP
#   - /etc/hosts entry on the host so kubelet can resolve the
#     in-cluster registry's DNS name when pulling images
#   - random KUSO_SESSION_KEY / JWT_SECRET / KUSO_ADMIN_PASSWORD
#     (printed once at the end so the user can record them)
#   - DNS pre-flight: refuses to provision Let's Encrypt unless
#     <domain> already resolves to this host's public IP, since
#     ACME failures eat into the rate-limit budget
#   - Let's Encrypt staging first, then prod after the staging cert
#     validates. Avoids burning the prod quota on misconfigs.
#   - Optional interactive GitHub App wizard for repo deploys + OAuth
#
# After this script finishes, https://<KUSO_DOMAIN>/ serves the kuso UI.
# `kuso login --api https://<KUSO_DOMAIN> -u admin -p <printed>` works
# from any workstation with reachable DNS.
#
# Usage:
#   curl -sfL https://raw.githubusercontent.com/sislelabs/kuso/main/hack/install.sh \
#     | sudo bash -s -- --domain kuso.example.com --email you@example.com
#
#   # Or via env (older shells, sudo -E):
#   curl -sfL .../install.sh | sudo \
#     KUSO_DOMAIN=kuso.example.com KUSO_EMAIL=you@example.com bash
#
# Tunable env / flags:
#   KUSO_DOMAIN / --domain      hostname for kuso UI (REQUIRED)
#   KUSO_EMAIL  / --email       email for Let's Encrypt (REQUIRED)
#   KUSO_VERSION / --operator-version    operator image tag
#   KUSO_SERVER_VERSION / --server-version  server image tag
#   KUSO_REPO   / --repo        github source for raw manifests
#   KUSO_ADMIN_PASSWORD         override the auto-generated admin password
#   KUSO_SKIP_K3S=1             assume k3s + traefik already installed
#   KUSO_INSECURE_SECRETS=1     reuse the well-known dev secrets (kind/dev only)
#   KUSO_SKIP_DNS_CHECK=1       skip the pre-flight DNS resolve
#   KUSO_LE_ENV=staging|prod    which Let's Encrypt environment to use
#                               (default: staging — flip to prod via UI later)
#   KUSO_GITHUB_APP_ENV         path to GitHub App credentials file
#                               (one KEY=VALUE per line — APP_ID, APP_SLUG,
#                               CLIENT_ID, CLIENT_SECRET, WEBHOOK_SECRET, ORG)
#   KUSO_GITHUB_APP_PEM         path to GitHub App private key .pem
#   KUSO_GITHUB_WIZARD=1        interactive prompts for the above on stdin

set -euo pipefail

# --- defaults ---
KUSO_DOMAIN="${KUSO_DOMAIN:-}"
KUSO_EMAIL="${KUSO_EMAIL:-}"
KUSO_VERSION="${KUSO_VERSION:-v0.2.6}"
KUSO_SERVER_VERSION="${KUSO_SERVER_VERSION:-v0.7.8}"
KUSO_REPO="${KUSO_REPO:-sislelabs/kuso}"
KUSO_LE_ENV="${KUSO_LE_ENV:-staging}"

# --- arg parsing ---
# Both flags and env vars are accepted. Flags win when both are
# set — call site clarity > env state.
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain)            KUSO_DOMAIN="$2"; shift 2 ;;
    --email)             KUSO_EMAIL="$2"; shift 2 ;;
    --operator-version)  KUSO_VERSION="$2"; shift 2 ;;
    --server-version)    KUSO_SERVER_VERSION="$2"; shift 2 ;;
    --repo)              KUSO_REPO="$2"; shift 2 ;;
    --le-prod)           KUSO_LE_ENV="prod"; shift ;;
    --le-staging)        KUSO_LE_ENV="staging"; shift ;;
    --no-dns-check)      KUSO_SKIP_DNS_CHECK=1; shift ;;
    --github-wizard)     KUSO_GITHUB_WIZARD=1; shift ;;
    -h|--help)
      # Print the doc header (everything between line 2 and the
      # `set -euo` guard, minus the guard line itself).
      sed -n '2,/^set -euo/p' "$0" | sed -e 's/^# \?//' -e '$d'
      exit 0
      ;;
    *) printf 'unknown flag: %s\n' "$1" >&2; exit 2 ;;
  esac
done

# Pin manifests to the same ref as the server image so install is
# reproducible. Default uses the server version tag (which has a
# release.json + crds.yaml committed alongside). For HEAD installs
# pass --repo and KUSO_REF=main explicitly.
KUSO_REF="${KUSO_REF:-${KUSO_SERVER_VERSION}}"
KUSO_RAW="https://raw.githubusercontent.com/${KUSO_REPO}/${KUSO_REF}"

# --- styling ---
log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

random_string() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 32 | tr -dc 'A-Za-z0-9' | head -c 32
  else
    head -c 64 /dev/urandom | tr -dc 'A-Za-z0-9' | head -c 32
  fi
}

# -------- 0. pre-flight --------

# Required inputs. Hard fail with the install URL example so the user
# can re-invoke correctly from the error.
if [[ -z "$KUSO_DOMAIN" ]]; then
  die "missing --domain.  Example: curl ... | sudo bash -s -- --domain kuso.example.com --email you@example.com"
fi
if [[ -z "$KUSO_EMAIL" ]]; then
  die "missing --email (Let's Encrypt registration email)"
fi

# Must run as root because we touch /etc/rancher/k3s/, /etc/hosts,
# and run systemctl. Refusing here saves a confusing partial install.
if [[ $EUID -ne 0 ]]; then
  die "install.sh must run as root.  Re-invoke with sudo."
fi

# OS check. k3s officially supports Ubuntu 22/24 + Debian 12/13.
# Other distros may work but we don't test them — refuse loudly so
# someone running CentOS doesn't end up with a partial install they
# have to debug at midnight.
if [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-}" in
    ubuntu)
      case "${VERSION_ID:-}" in
        22.04|24.04) ;;
        *) warn "untested Ubuntu version ${VERSION_ID}; install may fail. Continuing." ;;
      esac
      ;;
    debian)
      case "${VERSION_ID:-}" in
        12|13) ;;
        *) warn "untested Debian version ${VERSION_ID}; install may fail. Continuing." ;;
      esac
      ;;
    *)
      warn "untested OS ${ID:-unknown}; install may fail. Continuing." ;;
  esac
else
  warn "/etc/os-release not found; can't verify OS. Continuing."
fi

# DNS pre-flight. Without this, Let's Encrypt http-01 challenges fail
# silently and the user thinks the install hung. The fix is always
# "you need to point a DNS A record at this box first" — so we check
# up front and tell them exactly that.
if [[ "${KUSO_SKIP_DNS_CHECK:-0}" != "1" ]]; then
  log "checking DNS for ${KUSO_DOMAIN}"
  # Public IPv4 of this host. Multiple fallbacks in case one
  # service is down or rate-limits us.
  PUBLIC_IP=""
  for url in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
    PUBLIC_IP=$(curl -fsSL --max-time 5 "$url" 2>/dev/null | tr -d '\n')
    [[ -n "$PUBLIC_IP" ]] && break
  done
  if [[ -z "$PUBLIC_IP" ]]; then
    warn "couldn't determine this host's public IP — skipping DNS check"
  else
    RESOLVED=$(getent hosts "$KUSO_DOMAIN" | awk '{print $1}' | head -1)
    if [[ -z "$RESOLVED" ]]; then
      die "DNS lookup for ${KUSO_DOMAIN} returned no answer.

Add an A record pointing ${KUSO_DOMAIN} at ${PUBLIC_IP} (and a wildcard
*.${KUSO_DOMAIN} for deployed apps). Wait for propagation, then re-run.

Or pass --no-dns-check to skip this guard (Let's Encrypt will still
fail, but you'll be on your own for diagnosis)."
    fi
    if [[ "$RESOLVED" != "$PUBLIC_IP" ]]; then
      warn "DNS for ${KUSO_DOMAIN} resolves to ${RESOLVED} but this host is ${PUBLIC_IP}."
      warn "Let's Encrypt will fail until DNS converges. Continuing anyway — staging certs will work either way."
    else
      log "DNS check OK (${KUSO_DOMAIN} -> ${PUBLIC_IP})"
    fi
  fi
fi

# -------- 1. k3s --------
if [[ "${KUSO_SKIP_K3S:-0}" != "1" ]] && ! command -v k3s >/dev/null 2>&1; then
  log "installing k3s (single-node, traefik disabled — we install our own)"
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable=traefik --tls-san=${KUSO_DOMAIN} --write-kubeconfig-mode=644" sh -
else
  log "k3s already present; skipping install"
fi

# -------- 2. registry trust + /etc/hosts --------
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

# -------- 3. helm --------
if ! command -v helm >/dev/null 2>&1; then
  log "installing helm"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi

# -------- 4. traefik --------
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
# Two issuers always, so the user (or the kuso UI) can flip between
# staging and prod without re-running install. Default cert points
# at the one named in KUSO_LE_ENV. Staging certs are not browser-
# trusted but use a much higher rate limit (50k/week vs 50/week
# for prod), which makes "DNS still propagating" not catastrophic.
log "creating Let's Encrypt ClusterIssuers (staging + prod, default=${KUSO_LE_ENV})"
for variant in staging prod; do
  if [[ "$variant" == "staging" ]]; then
    le_url="https://acme-staging-v02.api.letsencrypt.org/directory"
  else
    le_url="https://acme-v02.api.letsencrypt.org/directory"
  fi
  until kubectl apply -f - >/dev/null 2>&1 <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-${variant}
spec:
  acme:
    server: ${le_url}
    email: ${KUSO_EMAIL}
    privateKeySecretRef:
      name: letsencrypt-${variant}-key
    solvers:
      - http01:
          ingress:
            class: traefik
EOF
  do
    sleep 3
  done
done
DEFAULT_ISSUER="letsencrypt-${KUSO_LE_ENV}"

# -------- 7. CRDs --------
log "applying kuso CRDs"
for crd in kusoprojects kusoservices kusoenvironments kusoaddons kusobuilds; do
  url="${KUSO_RAW}/operator/config/crd/bases/application.kuso.sislelabs.com_${crd}.yaml"
  if ! curl -sfL "$url" | kubectl apply -f - >/dev/null; then
    warn "failed to apply CRD ${crd} from ${url}"
  fi
done
curl -sfL "${KUSO_RAW}/operator/config/crd/bases/application.kuso.dev_kusoes.yaml" \
  | kubectl apply -f - >/dev/null || true

# -------- 8. registry --------
log "deploying in-cluster registry"
kubectl create namespace kuso 2>/dev/null || true
curl -sfL "${KUSO_RAW}/deploy/registry.yaml" | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-registry -n kuso || warn "kuso-registry not yet ready"
REGISTRY_IP=$(kubectl get svc kuso-registry -n kuso -o jsonpath='{.spec.clusterIP}')
ensure_registry_hosts_entry "$REGISTRY_IP"

# -------- 9. server secrets --------
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

# -------- 9b. GitHub App --------
# Three paths in priority order:
#   1. KUSO_GITHUB_WIZARD=1: interactive prompts. The user runs
#      install on their box, the script walks them through GH App
#      creation step-by-step, then asks them to paste the values.
#   2. KUSO_GITHUB_APP_ENV pre-set: non-interactive — read from a
#      file. Good for CI / unattended re-installs.
#   3. neither: skip entirely. kuso UI shows a CTA to wire it later.
gh_seed_from_env_file() {
  local env_file="$1"
  local pem_file="$2"
  log "seeding kuso-github-app from $env_file"
  # shellcheck disable=SC1090
  set -a; source "$env_file"; set +a
  for k in APP_ID APP_SLUG CLIENT_ID CLIENT_SECRET WEBHOOK_SECRET ORG; do
    if [[ -z "${!k:-}" ]]; then
      die "$env_file is missing $k"
    fi
  done
  kubectl create secret generic kuso-github-app -n kuso --dry-run=client -o yaml \
    --from-literal=GITHUB_APP_ID="$APP_ID" \
    --from-literal=GITHUB_APP_SLUG="$APP_SLUG" \
    --from-literal=GITHUB_APP_CLIENT_ID="$CLIENT_ID" \
    --from-literal=GITHUB_APP_CLIENT_SECRET="$CLIENT_SECRET" \
    --from-literal=GITHUB_APP_WEBHOOK_SECRET="$WEBHOOK_SECRET" \
    --from-file=GITHUB_APP_PRIVATE_KEY="$pem_file" \
    | kubectl apply -f - >/dev/null
  kubectl patch secret -n kuso kuso-server-secrets --type=merge -p "$(cat <<JSON
{"stringData":{
  "GITHUB_CLIENT_ID":"$CLIENT_ID",
  "GITHUB_CLIENT_SECRET":"$CLIENT_SECRET",
  "GITHUB_CLIENT_CALLBACKURL":"https://${KUSO_DOMAIN}/api/auth/github/callback",
  "GITHUB_CLIENT_ORG":"$ORG"
}}
JSON
)" >/dev/null
}

prompt() {
  local prompt_text="$1"
  local var_name="$2"
  local default="${3:-}"
  local val
  if [[ -n "$default" ]]; then
    read -r -p "$prompt_text [$default]: " val </dev/tty
  else
    read -r -p "$prompt_text: " val </dev/tty
  fi
  printf -v "$var_name" '%s' "${val:-$default}"
}

run_github_wizard() {
  cat <<WIZ

────────────────────────────────────────────────────────────────────
  GitHub App setup
────────────────────────────────────────────────────────────────────

  kuso uses a GitHub App for two things:
    1. listing/picking repos when you create a service
    2. receiving push + PR webhooks for automatic deploys + previews

  Without it you can still create services, but you'll have to set the
  repo URL manually and trigger builds with 'kuso build trigger'.

  Setup steps (do this in another tab):

    1. Go to:  https://github.com/settings/apps/new
       (or for an org: https://github.com/organizations/<org>/settings/apps/new)

    2. Fill in:
         App name:           kuso-${KUSO_DOMAIN%%.*}
         Homepage URL:       https://${KUSO_DOMAIN}/
         Callback URL:       https://${KUSO_DOMAIN}/api/auth/github/callback
         Webhook URL:        https://${KUSO_DOMAIN}/api/github/webhook
         Webhook secret:     <generate one — paste it back here below>
         Permissions:        Contents: Read & write
                             Metadata: Read-only
                             Pull requests: Read & write
                             Webhooks: Read & write
         Subscribe to:       push, pull_request, installation

    3. After creating, click 'Generate a private key' and download the
       .pem file. Save it on THIS box at /etc/kuso/github-app.pem
       (chmod 600 it).

    4. Note the App ID, Slug, Client ID, Client Secret from the App
       page. You'll paste them in next.

WIZ
  read -r -p "Press Enter when the GitHub App is created and the .pem is on this box..." </dev/tty

  prompt "GitHub App ID"           GH_APP_ID
  prompt "GitHub App slug"         GH_APP_SLUG  "kuso-${KUSO_DOMAIN%%.*}"
  prompt "Client ID"               GH_CLIENT_ID
  prompt "Client Secret"           GH_CLIENT_SECRET
  prompt "Webhook secret"          GH_WEBHOOK_SECRET
  prompt "Org / user the App is on" GH_ORG
  prompt "Path to .pem private key" GH_PEM_PATH    "/etc/kuso/github-app.pem"

  if [[ ! -r "$GH_PEM_PATH" ]]; then
    warn "private key not readable at $GH_PEM_PATH; skipping GitHub App seed"
    return
  fi

  local tmpenv
  tmpenv=$(mktemp)
  cat > "$tmpenv" <<EOF
APP_ID=$GH_APP_ID
APP_SLUG=$GH_APP_SLUG
CLIENT_ID=$GH_CLIENT_ID
CLIENT_SECRET=$GH_CLIENT_SECRET
WEBHOOK_SECRET=$GH_WEBHOOK_SECRET
ORG=$GH_ORG
EOF
  gh_seed_from_env_file "$tmpenv" "$GH_PEM_PATH"
  rm -f "$tmpenv"

  # Persist for re-runs so the next install doesn't re-prompt.
  mkdir -p /etc/kuso
  install -m 0600 "$GH_PEM_PATH" /etc/kuso/github-app.pem
  cat > /etc/kuso/github-app.env <<EOF
APP_ID=$GH_APP_ID
APP_SLUG=$GH_APP_SLUG
CLIENT_ID=$GH_CLIENT_ID
CLIENT_SECRET=$GH_CLIENT_SECRET
WEBHOOK_SECRET=$GH_WEBHOOK_SECRET
ORG=$GH_ORG
EOF
  chmod 600 /etc/kuso/github-app.env
  log "GitHub App credentials saved to /etc/kuso/github-app.{env,pem}"
}

if [[ "${KUSO_GITHUB_WIZARD:-0}" == "1" ]]; then
  run_github_wizard
elif [[ -n "${KUSO_GITHUB_APP_ENV:-}" ]]; then
  if [[ ! -r "$KUSO_GITHUB_APP_ENV" ]]; then
    die "KUSO_GITHUB_APP_ENV=$KUSO_GITHUB_APP_ENV not readable"
  fi
  if [[ -z "${KUSO_GITHUB_APP_PEM:-}" || ! -r "$KUSO_GITHUB_APP_PEM" ]]; then
    die "KUSO_GITHUB_APP_PEM must point at a readable .pem file"
  fi
  gh_seed_from_env_file "$KUSO_GITHUB_APP_ENV" "$KUSO_GITHUB_APP_PEM"
elif [[ -r /etc/kuso/github-app.env && -r /etc/kuso/github-app.pem ]]; then
  # Re-install: pick up existing credentials saved by an earlier wizard run.
  log "found existing /etc/kuso/github-app.{env,pem} — reseeding"
  gh_seed_from_env_file /etc/kuso/github-app.env /etc/kuso/github-app.pem
fi

# -------- 10. operator --------
log "applying kuso operator (image tag ${KUSO_VERSION})"
# Same image-rewrite pattern as the server: tolerant regex so the
# committed manifest's tag (vX.Y.Z OR v0.1.0-dev legacy) gets
# replaced with whatever the user asked for.
curl -sfL "${KUSO_RAW}/deploy/operator.yaml" \
  | sed -E "s|kuso-operator:[A-Za-z0-9._-]+|kuso-operator:${KUSO_VERSION}|g" \
  | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-operator-controller-manager -n kuso-operator-system

# -------- 11. server --------
log "applying kuso server (host ${KUSO_DOMAIN}, image tag ${KUSO_SERVER_VERSION})"
# The fetched manifest pins a specific tag at the install ref; we
# rewrite to whatever the user requested via --server-version. The
# regex matches `kuso-server-go:vX.Y.Z` regardless of the embedded
# tag so an upgrade-via-install works without surgery on the YAML.
curl -sfL "${KUSO_RAW}/deploy/server-go.yaml" \
  | sed -E "s|kuso-server-go:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-server-go:${KUSO_SERVER_VERSION}|g" \
  | kubectl apply -f - >/dev/null

kubectl apply -f - <<EOF >/dev/null
apiVersion: v1
kind: ServiceAccount
metadata: { name: kuso-server, namespace: kuso }
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: kuso-server-cluster-admin }
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: kuso-server
    namespace: kuso
---
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
  annotations:
    cert-manager.io/cluster-issuer: ${DEFAULT_ISSUER}
spec:
  ingressClassName: traefik
  tls:
    - { hosts: [${KUSO_DOMAIN}], secretName: kuso-server-tls }
  rules:
    - host: ${KUSO_DOMAIN}
      http:
        paths:
          - { path: /, pathType: Prefix, backend: { service: { name: kuso-server, port: { number: 80 } } } }
EOF

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
if [[ "$KUSO_LE_ENV" == "staging" ]]; then
  cat <<EOF
  ⚠  Currently using Let's Encrypt STAGING certs (browser will warn
     about untrusted cert). Once you've confirmed DNS is correct and
     the dashboard loads, flip to prod:

       kubectl -n kuso annotate ingress kuso-server \\
         cert-manager.io/cluster-issuer=letsencrypt-prod --overwrite
       kubectl -n kuso delete secret kuso-server-tls

     A fresh prod cert will be requested automatically.

EOF
fi

if [[ "${KUSO_INSECURE_SECRETS:-0}" != "1" ]]; then
  cat <<EOF
  Save the password somewhere safe. To regenerate it later:

    kubectl -n kuso patch secret kuso-server-secrets \\
      --type=merge -p '{"stringData":{"KUSO_ADMIN_PASSWORD":"<new>"}}'
    kubectl -n kuso rollout restart deployment/kuso-server

EOF
  if ! kubectl get secret -n kuso kuso-github-app >/dev/null 2>&1; then
    cat <<EOF
  GitHub App: not yet configured. Re-run with --github-wizard to set
  it up interactively, or follow docs/GITHUB_APP_SETUP.md for the
  full reference. Without it, services can still build via
  'kuso build trigger' but the repo picker is empty.

EOF
  fi
fi
echo
