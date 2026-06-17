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
#   - Let's Encrypt PROD by default — DNS pre-flight catches most
#     misconfigs. Pass --le-staging if you're iterating on DNS and
#     want to avoid the 50 certs/week prod rate limit; staging
#     certs are not browser-trusted but cost nothing.
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
#                               (default: prod). LE prod is rate-limited
#                               to 50 certs/week per registered domain
#                               (the eTLD+1, e.g. example.com), and 5
#                               failed validations per hour. Use staging
#                               while iterating on DNS — it has 30000
#                               certs/week limits but is not browser-
#                               trusted.
#   KUSO_GITHUB_APP_ENV         path to GitHub App credentials file
#                               (one KEY=VALUE per line — APP_ID, APP_SLUG,
#                               CLIENT_ID, CLIENT_SECRET, WEBHOOK_SECRET, ORG)
#   KUSO_GITHUB_APP_PEM         path to GitHub App private key .pem
#   KUSO_GITHUB_WIZARD=1        interactive prompts for the above on stdin

set -euo pipefail

# --- defaults ---
KUSO_DOMAIN="${KUSO_DOMAIN:-}"
KUSO_EMAIL="${KUSO_EMAIL:-}"
KUSO_VERSION="${KUSO_VERSION:-v0.18.74}"
KUSO_SERVER_VERSION="${KUSO_SERVER_VERSION:-v0.18.74}"
KUSO_REPO="${KUSO_REPO:-sislelabs/kuso}"
KUSO_LE_ENV="${KUSO_LE_ENV:-prod}"

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

# Manifests (CRDs, deploy/*.yaml) are pulled from KUSO_REF on GitHub.
# Defaults to "main" because that's the only ref guaranteed to exist:
# `make release-roll` doesn't push git tags, only docker images. To pin
# manifests to a specific commit, pass KUSO_REF=<sha>.
KUSO_REF="${KUSO_REF:-main}"
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
      warn "Let's Encrypt will fail until DNS converges."
      if [[ "$KUSO_LE_ENV" == "prod" ]]; then
        warn "You're on LE prod (default). Each failed validation eats your 5/hour budget."
        warn "If DNS is still propagating, re-run with --le-staging to avoid burning prod quota."
      fi
    else
      log "DNS check OK (${KUSO_DOMAIN} -> ${PUBLIC_IP})"
    fi
  fi
fi

# -------- 1. k3s --------
if [[ "${KUSO_SKIP_K3S:-0}" != "1" ]] && ! command -v k3s >/dev/null 2>&1; then
  log "installing k3s (single-node, traefik disabled — we install our own)"
  # --secrets-encryption enables AES-CBC encryption-at-rest for kube
  # Secrets in k3s's kine datastore. Without this, every Secret
  # (kuso-server-secrets, kuso-postgres-conn, kuso-github-app, every
  # addon's connection-string secret, every clone-token) sits in
  # plaintext on disk. Disk theft → full credential compromise.
  # The flag must be passed at install — adding it later requires
  # an Encryption-Provider-Config rotation procedure. Disable via
  # KUSO_SKIP_SECRETS_ENCRYPTION=1 only when integrating with an
  # external KMS that handles encryption at the storage layer.
  K3S_EXTRA="--disable=traefik --tls-san=${KUSO_DOMAIN} --write-kubeconfig-mode=644"
  if [[ "${KUSO_SKIP_SECRETS_ENCRYPTION:-0}" != "1" ]]; then
    K3S_EXTRA="${K3S_EXTRA} --secrets-encryption"
  fi
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="${K3S_EXTRA}" sh -
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
# Fatal: every CRD must apply. If any fails (404, network glitch, malformed
# yaml), the operator can't reconcile and the rest of the install builds a
# broken cluster silently. Better to die here with a clear pointer.
#
# CRITICAL: this list MUST be a superset of every `kind` in
# operator/watches.yaml. The helm-operator starts one informer per
# watched kind at boot; if a watched kind's CRD isn't registered, the
# manager's cache-sync times out and the whole process exits non-zero →
# the operator Deployment CrashLoopBackOffs forever and `kubectl wait`
# for it times out. This bit a user on v0.18.69: `kusoruns` was added to
# watches.yaml but never added here, so fresh installs never created the
# KusoRun CRD and the operator crashed on every boot. When you add a new
# watched kind, add its CRD plural here too. (kusobuilds is no longer
# watched as of v0.10 but the CRD is still applied for older clusters.)
log "applying kuso CRDs (from ${KUSO_REF})"
for crd in kusoprojects kusoservices kusoenvironments kusoaddons kusobuilds kusocrons kusoruns; do
  url="${KUSO_RAW}/operator/config/crd/bases/application.kuso.sislelabs.com_${crd}.yaml"
  yaml="$(curl -sfL "$url" || true)"
  if [[ -z "$yaml" ]]; then
    die "CRD ${crd} not reachable at ${url} — is KUSO_REF=${KUSO_REF} correct?"
  fi
  if ! printf '%s\n' "$yaml" | kubectl apply -f - >/dev/null; then
    die "CRD ${crd} failed to apply (kubectl error). yaml from ${url}"
  fi
done
# Optional legacy CRD; tolerate 404.
curl -sfL "${KUSO_RAW}/operator/config/crd/bases/application.kuso.dev_kusoes.yaml" \
  | kubectl apply -f - >/dev/null 2>&1 || true

# -------- 8. registry --------
log "deploying in-cluster registry"
kubectl create namespace kuso 2>/dev/null || true
curl -sfL "${KUSO_RAW}/deploy/registry.yaml" | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-registry -n kuso || warn "kuso-registry not yet ready"
REGISTRY_IP=$(kubectl get svc kuso-registry -n kuso -o jsonpath='{.spec.clusterIP}')
ensure_registry_hosts_entry "$REGISTRY_IP"

# -------- 8a2. buildkitd --------
# Long-lived BuildKit daemon. Every kusobuild Job connects via TCP
# instead of spawning its own builder per build, so caches stay
# warm and the rootless-buildkit kernel-syscall dance is avoided
# (the daemon runs privileged once; the build Jobs are unprivileged
# thin clients).
log "deploying in-cluster buildkitd"
curl -sfL "${KUSO_RAW}/deploy/buildkitd.yaml" | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=180s \
  deployment/kuso-buildkitd -n kuso || warn "kuso-buildkitd not yet ready"

# -------- 8b. prometheus --------
# Lightweight prometheus that scrapes traefik:9100 + opted-in pods.
# Required for the dashboard's Metrics tab — without it the
# Requests / Error rate / Response time cards permanently render
# "no data yet" because /api/kubernetes/envs/.../timeseries hits
# kuso-prometheus.kuso.svc.cluster.local:9090. CPU + memory still
# work without prom (they read kube metrics-server).
log "deploying in-cluster prometheus"
curl -sfL "${KUSO_RAW}/deploy/prometheus.yaml" | kubectl apply -f - >/dev/null
kubectl wait --for=condition=Available --timeout=120s \
  deployment/kuso-prometheus -n kuso || warn "kuso-prometheus not yet ready"

# -------- 8c. postgres --------
#
# v0.9 retired SQLite. Postgres is the metadata DB now. Provisions
# a single-replica StatefulSet on the control-plane node and
# generates a unique password on first install (existing kuso-
# postgres-conn Secret values win on re-run, same as kuso-server-
# secrets — see section 9). Operators with a managed Postgres
# (RDS, Crunchy Bridge, Supabase) can pre-create kuso-postgres-conn
# with their own DSN; the install script skips the StatefulSet
# when KUSO_USE_EXTERNAL_POSTGRES=1 is set.
log "provisioning kuso-postgres"
kubectl create namespace kuso 2>/dev/null || true

# kuso-platform PriorityClass — both kuso-postgres and kuso-server
# reference it in their pod specs. The full definition lives in
# deploy/server-go.yaml's bundle, but that gets applied later in
# the script — and a missing PriorityClass at create time produces
# "no PriorityClass with name kuso-platform was found" with no
# useful retry. Eagerly create it here so subsequent applies are
# clean.
kubectl apply -f - <<'EOF' >/dev/null
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: kuso-platform
value: 100000
globalDefault: false
description: "kuso control-plane pods. Survives workload eviction."
EOF

# v0.9.38: install CloudNativePG operator. The bundled Postgres is now
# a CNPG-managed Cluster (3 instances, automatic failover, anti-
# affinity) instead of a single-replica StatefulSet pinned to the
# control-plane node. CNPG is also already required by the HA-Postgres
# *addon* path (docs/ADDON_HA.md) — we just install it eagerly now
# instead of waiting for the user to read the docs.
#
# ~150 MB image, ~64 MiB steady-state. Idempotent — re-applying the
# CNPG release manifest is a no-op when CRDs already exist.
CNPG_VERSION="${KUSO_CNPG_VERSION:-1.24.0}"
CNPG_RELEASE_BRANCH="${KUSO_CNPG_BRANCH:-release-1.24}"
if kubectl get crd clusters.postgresql.cnpg.io >/dev/null 2>&1; then
  log "CloudNativePG already installed; skipping operator install"
else
  log "installing CloudNativePG operator (v${CNPG_VERSION})"
  if ! kubectl apply --server-side -f \
    "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/${CNPG_RELEASE_BRANCH}/releases/cnpg-${CNPG_VERSION}.yaml" \
    >/dev/null; then
    err "CNPG install failed — check network access to raw.githubusercontent.com"
    exit 1
  fi
  log "waiting for CNPG operator to become ready (up to 5 min)"
  kubectl -n cnpg-system wait --for=condition=Available deploy/cnpg-controller-manager --timeout=300s \
    || warn "CNPG operator not yet Available — proceeding (Cluster create may retry)"
fi

if [[ "${KUSO_USE_EXTERNAL_POSTGRES:-0}" == "1" ]]; then
  if ! kubectl get secret -n kuso kuso-postgres-conn >/dev/null 2>&1; then
    err "KUSO_USE_EXTERNAL_POSTGRES=1 set but Secret kuso-postgres-conn missing — create it before re-running install"
    exit 1
  fi
  log "external Postgres mode — using existing kuso-postgres-conn Secret"
else
  # Reuse an existing password if one's already in the cluster (re-
  # run safety). Otherwise mint a fresh one. CNPG bootstrap also
  # reads {username, password} from kuso-postgres-conn — same Secret
  # serves both bootstrap-input + composed-output roles.
  EXISTING_PG_PASS=""
  if kubectl get secret -n kuso kuso-postgres-conn >/dev/null 2>&1; then
    EXISTING_PG_PASS=$(kubectl get secret -n kuso kuso-postgres-conn -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
  fi
  PG_PASS="${EXISTING_PG_PASS:-$(random_string)}"

  # Bootstrap shape — CNPG initdb reads {username, password} from
  # this Secret and creates the role + database. The dsn-stamp Job
  # in deploy/postgres.yaml patches the same Secret with the
  # composed {dsn, host, port, database} fields kuso-server reads.
  #
  # CNPG's bootstrap.initdb.secret expects keys {username, password}
  # (not {user, password}). We provide BOTH name shapes so kuso-server's
  # existing reads of 'user' keep working alongside CNPG's reads of
  # 'username'. The dsn-stamp Job will later add {dsn, host, port}
  # composed-field keys.
  #
  # NOTE: type=Opaque (not kubernetes.io/basic-auth) — basic-auth
  # technically only allows {username, password} keys; some kubelets
  # are lenient about extra keys, but we don't want to depend on it
  # since we add four more keys via the dsn-stamp Job.
  kubectl create secret generic kuso-postgres-conn -n kuso --dry-run=client -o yaml \
    --from-literal=username="kuso" \
    --from-literal=user="kuso" \
    --from-literal=password="$PG_PASS" \
    --from-literal=database="kuso" \
    | kubectl apply -f - >/dev/null

  # PgBouncer fronts Postgres with auth_type=md5, so the kuso role must
  # be stored as an md5 verifier (see deploy/postgres.yaml's
  # postgresql.parameters.password_encryption=md5). CNPG ≥1.29.2/1.30
  # SCRAM-encodes role passwords operator-side BEFORE issuing CREATE/
  # ALTER ROLE, which would override password_encryption and re-break
  # the md5 pooler with `wrong password type (08P01)`. This annotation
  # opts out of operator-side hashing so PostgreSQL honours
  # password_encryption=md5. No-op on the CNPG 1.24 we pin today;
  # present so a future CNPG bump doesn't silently reintroduce the bug.
  kubectl annotate secret kuso-postgres-conn -n kuso \
    cnpg.io/passwordPassthrough=enabled --overwrite >/dev/null 2>&1 || true

  # HA mode: the default manifest ships single-instance (1 primary,
  # no standby) because kuso is single-tenant and typically deployed
  # on one node, where HA-postgres buys nothing. Operators on ≥3-node
  # production clusters who want streaming replication + automatic
  # failover set KUSO_POSTGRES_HA=1, which patches the manifest to
  # `instances: 3` + enables anti-affinity so the standbys land on
  # different nodes than the primary.
  #
  # KUSO_POSTGRES_SINGLE_NODE is honoured for back-compat with older
  # install commands but is a no-op now (single-node is the default).
  PG_MANIFEST=$(curl -sfL "${KUSO_RAW}/deploy/postgres.yaml")
  if [[ "${KUSO_POSTGRES_HA:-0}" == "1" || "${KUSO_POSTGRES_HA:-false}" == "true" ]]; then
    log "KUSO_POSTGRES_HA: scaling kuso-postgres Cluster to 3 instances (primary + 2 standbys)"
    PG_MANIFEST=$(echo "$PG_MANIFEST" | sed 's/^  instances: 1$/  instances: 3/' \
      | sed 's/    enablePodAntiAffinity: false/    enablePodAntiAffinity: true/')
  fi
  echo "$PG_MANIFEST" | kubectl apply -f - >/dev/null

  log "waiting for kuso-postgres Cluster primary to come up (up to 5 min)"
  # CNPG creates the primary pod with a -1 suffix; wait for it to
  # exist first, then for Ready.
  for i in $(seq 1 60); do
    if kubectl -n kuso get pod kuso-postgres-1 >/dev/null 2>&1; then
      break
    fi
    sleep 5
  done
  kubectl -n kuso wait --for=condition=Ready --timeout=300s \
    pod/kuso-postgres-1 || warn "kuso-postgres-1 not yet Ready"

  # Wait for the dsn-stamp Job to complete so kuso-server can find
  # the dsn key in kuso-postgres-conn. The Job is part of the
  # manifest we applied above; if it fails the Cluster bootstrap
  # never produced kuso-postgres-app, which is its own issue.
  log "waiting for kuso-postgres-conn dsn key (up to 5 min)"
  for i in $(seq 1 60); do
    if kubectl -n kuso get secret kuso-postgres-conn -o jsonpath='{.data.dsn}' 2>/dev/null | grep -q .; then
      break
    fi
    sleep 5
  done
  if ! kubectl -n kuso get secret kuso-postgres-conn -o jsonpath='{.data.dsn}' 2>/dev/null | grep -q .; then
    warn "dsn key not yet present in kuso-postgres-conn — kuso-server may need a restart after the Job lands"
  fi

  # Password-alignment guard. The dsn-stamp Job authoritatively
  # writes whatever CNPG actually used during initdb, so in
  # steady-state install.sh's PG_PASS guess might disagree with
  # what's in the Secret. That's correct — we trust CNPG's view.
  # But: surface the divergence so an operator who hand-edited
  # kuso-postgres-conn outside install.sh sees the mismatch instead
  # of debugging "auth failed" with kuso-server.
  if kubectl -n kuso get secret kuso-postgres-app >/dev/null 2>&1 \
     && kubectl -n kuso get secret kuso-postgres-conn >/dev/null 2>&1; then
    APP_PASS=$(kubectl -n kuso get secret kuso-postgres-app -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
    CONN_PASS=$(kubectl -n kuso get secret kuso-postgres-conn -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
    if [[ -n "$APP_PASS" && -n "$CONN_PASS" && "$APP_PASS" != "$CONN_PASS" ]]; then
      warn "kuso-postgres-conn.password diverges from kuso-postgres-app.password — dsn-stamp should have caught this; kuso-server may fail to authenticate. To recover:"
      warn "  kubectl -n kuso delete job kuso-postgres-dsn-stamp && kubectl apply -f deploy/postgres.yaml"
    fi
  fi

  # Recovery hint for the rare case where the Cluster bootstrapped
  # but the dsn-stamp Job fully timed out. Operator can re-run the
  # Job; it's idempotent.
  if kubectl -n kuso get job kuso-postgres-dsn-stamp -o jsonpath='{.status.failed}' 2>/dev/null | grep -q '^[1-9]'; then
    warn "dsn-stamp Job has failures — to retry: kubectl -n kuso delete job kuso-postgres-dsn-stamp && kubectl apply -f deploy/postgres.yaml"
  fi
fi

# -------- 9. server secrets --------
#
# Re-runs of install.sh must NOT regenerate secrets. Rerolling the
# admin password locks the operator out, and rerolling JWT_SECRET
# invalidates every active session. So: if kuso-server-secrets
# already exists, decode its values and reuse them. Explicit env-var
# overrides (KUSO_ADMIN_PASSWORD=...) still win — that's the
# documented "rotate the password" path.
kubectl create namespace kuso 2>/dev/null || true

EXISTING_ADMIN=""
EXISTING_SESSION=""
EXISTING_JWT=""
EXISTING_METRICS_SCRAPE=""
# The admin password moved out of kuso-server-secrets into a dedicated
# `kuso-admin-credentials` Secret mounted as a file. Read from there
# first; fall back to the legacy kuso-server-secrets key for installs
# that haven't been re-run since v0.9.77.
if kubectl get secret -n kuso kuso-admin-credentials >/dev/null 2>&1; then
  EXISTING_ADMIN=$(kubectl get secret -n kuso kuso-admin-credentials -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
fi
if kubectl get secret -n kuso kuso-server-secrets >/dev/null 2>&1; then
  if [[ -z "$EXISTING_ADMIN" ]]; then
    EXISTING_ADMIN=$(kubectl get secret -n kuso kuso-server-secrets -o jsonpath='{.data.KUSO_ADMIN_PASSWORD}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
  fi
  EXISTING_SESSION=$(kubectl get secret -n kuso kuso-server-secrets -o jsonpath='{.data.KUSO_SESSION_KEY}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
  EXISTING_JWT=$(kubectl get secret -n kuso kuso-server-secrets -o jsonpath='{.data.JWT_SECRET}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
  EXISTING_METRICS_SCRAPE=$(kubectl get secret -n kuso kuso-server-secrets -o jsonpath='{.data.KUSO_METRICS_SCRAPE_TOKEN}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
fi

if [[ "${KUSO_INSECURE_SECRETS:-0}" == "1" ]]; then
  log "using INSECURE dev secrets (admin / kuso-admin)"
  ADMIN_PASSWORD="${KUSO_ADMIN_PASSWORD:-kuso-admin}"
  SESSION_KEY="dev-session-key-do-not-use-in-prod-3232"
  JWT_SECRET="dev-jwt-secret-do-not-use-in-prod-32-chars"
  METRICS_SCRAPE_TOKEN="dev-metrics-scrape-token-do-not-use"
else
  # Precedence: explicit env-var override > existing secret > fresh random.
  ADMIN_PASSWORD="${KUSO_ADMIN_PASSWORD:-${EXISTING_ADMIN:-$(random_string)}}"
  SESSION_KEY="${EXISTING_SESSION:-$(random_string)}"
  JWT_SECRET="${EXISTING_JWT:-$(random_string)}"
  METRICS_SCRAPE_TOKEN="${EXISTING_METRICS_SCRAPE:-$(random_string)}"
  if [[ -n "$EXISTING_ADMIN" ]]; then
    log "reusing existing kuso-server-secrets (admin password unchanged)"
  fi
fi

log "applying kuso-server-secrets"
# KUSO_DOMAIN goes in here too so the server can build OAuth callback
# URLs without an admin re-pasting it. NewGithubOAuth() autoderives
# https://${KUSO_DOMAIN}/api/auth/github/callback when it's set.
#
# KUSO_RELEASE_PUBLIC_KEY is the Ed25519 public key (base64) used by
# the updater to verify release.json signatures. Bake it in at install
# time so a compromised GH release can't trick installs into pulling
# a malicious update. Generated with:
#   openssl genpkey -algorithm Ed25519 -out kuso-release.priv
#   openssl pkey -in kuso-release.priv -pubout -outform DER \
#     | tail -c 32 | base64
# Empty is fine for unsigned releases — the updater logs a warn but
# proceeds. Set KUSO_REQUIRE_SIGNATURES=true to refuse unsigned.
KUSO_RELEASE_PUBKEY="${KUSO_RELEASE_PUBLIC_KEY:-}"
KUSO_REQUIRE_SIGS="${KUSO_REQUIRE_SIGNATURES:-false}"
kubectl create secret generic kuso-server-secrets -n kuso --dry-run=client -o yaml \
  --from-literal=KUSO_SESSION_KEY="$SESSION_KEY" \
  --from-literal=JWT_SECRET="$JWT_SECRET" \
  --from-literal=KUSO_DOMAIN="$KUSO_DOMAIN" \
  --from-literal=KUSO_RELEASE_PUBLIC_KEY="$KUSO_RELEASE_PUBKEY" \
  --from-literal=KUSO_REQUIRE_SIGNATURES="$KUSO_REQUIRE_SIGS" \
  --from-literal=KUSO_METRICS_SCRAPE_TOKEN="$METRICS_SCRAPE_TOKEN" \
  | kubectl apply -f - >/dev/null

# Admin password lives in its OWN Secret, mounted as a file inside the
# kuso-server pod (see deploy/server-go.yaml volumes). Splitting it out
# keeps the password off `kubectl describe pod` and `env` — anyone with
# pod-exec rights on kuso-server can still cat the file, but the leak
# surface is much smaller than an env var visible to every pod inspector.
log "applying kuso-admin-credentials"
kubectl create secret generic kuso-admin-credentials -n kuso --dry-run=client -o yaml \
  --from-literal=password="$ADMIN_PASSWORD" \
  | kubectl apply -f - >/dev/null

# Mirror the metrics-scrape token into a dedicated Secret the bundled
# Prometheus mounts. Two reasons for the split: (1) we don't want to
# hand Prometheus the JWT secret + admin password just to scrape;
# (2) rotating the scrape token shouldn't bounce kuso-server, so the
# Secret with that single key is referenced from prometheus.yaml's
# bearer_token_file path. install.sh re-applies on every run so the
# two stay in sync.
#
# We snapshot the prior token (if any) so we can detect a change
# downstream and bounce the Prometheus pod — a SecretVolumeSource
# update propagates eventually, but the kubelet's mount-refresh
# cadence is up to a minute and Prometheus only rereads
# credentials_file at scrape time anyway. A rollout-restart is the
# cheap, deterministic refresh.
PREV_PROM_TOKEN=""
if kubectl get secret -n kuso kuso-prometheus-scrape >/dev/null 2>&1; then
  PREV_PROM_TOKEN=$(kubectl get secret -n kuso kuso-prometheus-scrape -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
fi
kubectl create secret generic kuso-prometheus-scrape -n kuso --dry-run=client -o yaml \
  --from-literal=token="$METRICS_SCRAPE_TOKEN" \
  | kubectl apply -f - >/dev/null
# Restart Prometheus when:
#   - the token rotated (PREV != current), OR
#   - the Secret is brand-new (PREV is empty) AND Prometheus was
#     already running (means we're upgrading from pre-v0.9.38; the
#     existing pod has no scrape-token volume and needs to mount it)
# In both cases the rollout is cheap (Recreate strategy on
# emptyDir storage; ~5s downtime on a 7-day metric retention).
if kubectl -n kuso get deployment kuso-prometheus >/dev/null 2>&1; then
  if [[ -z "$PREV_PROM_TOKEN" ]] || [[ "$PREV_PROM_TOKEN" != "$METRICS_SCRAPE_TOKEN" ]]; then
    log "kicking kuso-prometheus to mount the metrics-scrape token"
    kubectl -n kuso rollout restart deployment kuso-prometheus >/dev/null 2>&1 || true
  fi
fi

# Same logic for kuso-server: env vars are read at process start, so
# a Secret patch alone doesn't propagate to running replicas. If
# kuso-server was already running AND the scrape token changed (or
# didn't exist before), bounce the deployment so the new token
# lands in os.Getenv before the next /metrics scrape comes in.
# Skipped on brand-new installs where the deployment doesn't exist
# yet — server-go.yaml is applied later in this script and starts
# fresh with the right env.
if kubectl -n kuso get deployment kuso-server >/dev/null 2>&1; then
  if [[ -z "$EXISTING_METRICS_SCRAPE" ]] || [[ "$EXISTING_METRICS_SCRAPE" != "$METRICS_SCRAPE_TOKEN" ]]; then
    log "kicking kuso-server to pick up the rotated KUSO_METRICS_SCRAPE_TOKEN"
    kubectl -n kuso rollout restart deployment kuso-server >/dev/null 2>&1 || true
  fi
fi

# k3s node-token Secret. Required so kuso-server can issue agent-join
# commands without being pinned to the control-plane node — pre-this,
# the deploy yaml mounted /var/lib/rancher/k3s/server/node-token via
# hostPath, which broke multi-replica HA. Now the token lives in a
# kube Secret (envFrom: kuso-k3s-token) so any node's kuso-server pod
# can read it.
#
# We read the token at install time on the control-plane host where
# this script runs, write it into the Secret. Re-installs reuse the
# existing Secret if present (rotating the token would invalidate
# every joined node's k3s-agent kubelet auth).
log "applying kuso-k3s-token (Secret)"
if kubectl get secret -n kuso kuso-k3s-token >/dev/null 2>&1; then
  log "  reusing existing kuso-k3s-token (token unchanged)"
else
  K3S_NODE_TOKEN=""
  if [[ -r /var/lib/rancher/k3s/server/node-token ]]; then
    K3S_NODE_TOKEN="$(tr -d '\n' < /var/lib/rancher/k3s/server/node-token)"
  fi
  if [[ -z "$K3S_NODE_TOKEN" ]]; then
    log "  WARNING: /var/lib/rancher/k3s/server/node-token not readable — skipping kuso-k3s-token Secret. Add Node flow will fall back to the hostPath mount on the control-plane node only."
  else
    kubectl create secret generic kuso-k3s-token -n kuso --dry-run=client -o yaml \
      --from-literal=KUSO_K3S_TOKEN="$K3S_NODE_TOKEN" \
      | kubectl apply -f - >/dev/null
  fi
fi

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
  # We surface the App slug + ID so it's obvious *which* App is being used —
  # otherwise a fresh install on a recycled box silently inherits whatever
  # GitHub identity was there before, which is a surprise for both UX
  # (wrong org listed) and security (orphan creds you forgot about).
  EXISTING_SLUG="$(grep -E '^APP_SLUG=' /etc/kuso/github-app.env | cut -d= -f2- || true)"
  EXISTING_ID="$(grep -E '^APP_ID=' /etc/kuso/github-app.env | cut -d= -f2- || true)"
  warn "reusing existing GitHub App at /etc/kuso/github-app.{env,pem}"
  warn "  slug=${EXISTING_SLUG:-?}  id=${EXISTING_ID:-?}"
  warn "  to start fresh: rm /etc/kuso/github-app.{env,pem} and rerun with --github-wizard"
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
# Clean up the pre-v0.9.4 cluster-admin binding if present. The
# narrow RBAC declared in deploy/server-go.yaml is the authoritative
# grant; the legacy `kuso-server-cluster-admin` ClusterRoleBinding
# was silently overriding that with full cluster-admin. Idempotent
# on fresh installs (NotFound is fine).
kubectl delete clusterrolebinding kuso-server-cluster-admin --ignore-not-found 2>/dev/null || true

# The fetched manifest pins a specific tag at the install ref; we
# rewrite to whatever the user requested via --server-version. The
# regex matches `kuso-server-go:vX.Y.Z` regardless of the embedded
# tag so an upgrade-via-install works without surgery on the YAML.
curl -sfL "${KUSO_RAW}/deploy/server-go.yaml" \
  | sed -E "s|kuso-server-go:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-server-go:${KUSO_SERVER_VERSION}|g" \
  | kubectl apply -f - >/dev/null

# kuso-activator: the scale-to-zero request-holding proxy (runs the same
# image in --activator mode). Reuses the kuso-server ServiceAccount, so
# it must apply AFTER server-go.yaml (which declares the SA + RBAC). The
# traefik errors-Middleware it defines is what wakes slept services.
# Best-effort: a cluster without scale-to-zero users still works, the
# activator just sits idle. Same image-tag rewrite as the server.
curl -sfL "${KUSO_RAW}/deploy/kuso-activator.yaml" \
  | sed -E "s|kuso-server-go:v[0-9]+\\.[0-9]+\\.[0-9]+([-A-Za-z0-9.]*)?|kuso-server-go:${KUSO_SERVER_VERSION}|g" \
  | kubectl apply -f - >/dev/null 2>&1 || warn "kuso-activator apply failed (scale-to-zero wake won't work until fixed)"

kubectl apply -f - <<EOF >/dev/null
# kuso-server's ServiceAccount + RBAC are owned by deploy/server-go.yaml
# (the narrow ClusterRole + ClusterRoleBinding declared at the top of
# the bundle). Pre-v0.9.4 this section silently layered a cluster-
# admin ClusterRoleBinding on top, which let any kuso-server bug
# delete arbitrary cluster resources. We delete that binding here on
# upgrade for installs that ran the pre-v0.9.4 path; new installs
# never get it.
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
# HTTPS redirect middleware: any plain-HTTP request hitting traefik
# gets a 308 to the same path on https. Without this, the dashboard
# loads over HTTP first (no warning, no redirect), users sign in over
# plaintext until they manually retype the URL. Traefik's
# RedirectScheme is the standard way; we attach it to the Ingress via
# the traefik.ingress.kubernetes.io/router.middlewares annotation
# below.
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: kuso-https-redirect
  namespace: kuso
spec:
  redirectScheme:
    scheme: https
    permanent: true
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kuso-server
  namespace: kuso
  annotations:
    cert-manager.io/cluster-issuer: ${DEFAULT_ISSUER}
    # Apply the redirect middleware to the HTTP-facing router only.
    # Traefik names entrypoint-specific routers as <ns>-<ingress>-<host>-<idx>@kubernetes
    # but the simpler form below applies to all routers backed by
    # this ingress; the websecure (HTTPS) router won't redirect
    # since its scheme is already correct.
    traefik.ingress.kubernetes.io/router.middlewares: kuso-kuso-https-redirect@kubernetescrd
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

# -------- 11b. auto-flip to LE prod --------
#
# Default is prod (line 62), so this block is normally a no-op. It
# fires only when the user explicitly passed --le-staging and didn't
# set KUSO_LE_AUTO_FLIP=0 — i.e. "iterate on staging, then auto-
# upgrade to a real cert once it works." Without the flip, every
# install that started on staging would ship with a browser warning
# until manually fixed.
if [[ "$KUSO_LE_ENV" == "staging" && "${KUSO_LE_AUTO_FLIP:-1}" == "1" ]]; then
  log "waiting for LE staging cert to validate before flipping to prod"
  STAGING_OK=0
  for i in $(seq 1 30); do
    READY=$(kubectl get certificate -n kuso kuso-server-tls \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
    if [[ "$READY" == "True" ]]; then
      STAGING_OK=1
      break
    fi
    sleep 4
  done
  if [[ "$STAGING_OK" == "1" ]]; then
    log "staging cert valid — flipping to LE prod"
    kubectl -n kuso annotate ingress kuso-server \
      cert-manager.io/cluster-issuer=letsencrypt-prod --overwrite >/dev/null
    kubectl -n kuso delete secret kuso-server-tls --ignore-not-found >/dev/null
    # Wait for the prod cert to land. ACME http-01 + http propagation
    # is usually <30s on a healthy box; bound at 2min.
    PROD_OK=0
    for i in $(seq 1 30); do
      READY=$(kubectl get certificate -n kuso kuso-server-tls \
        -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")
      if [[ "$READY" == "True" ]]; then
        PROD_OK=1
        break
      fi
      sleep 4
    done
    if [[ "$PROD_OK" == "1" ]]; then
      log "LE prod cert active"
      KUSO_LE_ENV="prod"   # so the summary doesn't print the staging warning
    else
      warn "prod cert hasn't validated within 2min — leaving on prod issuer; check 'kubectl describe certificate -n kuso kuso-server-tls'"
    fi
  else
    warn "staging cert didn't validate — leaving everything on staging"
  fi
fi

# -------- 12. summary --------
echo
log "kuso is up"
echo
echo "  UI:        https://${KUSO_DOMAIN}/"
echo "  Admin:     admin"
if [[ -n "$EXISTING_ADMIN" && "$ADMIN_PASSWORD" == "$EXISTING_ADMIN" ]]; then
  echo "  Password:  (unchanged — reused from existing install)"
  echo
  echo "  Forgot the password? Reset it via the UI (Settings → Users)."
  echo "  Lost UI access entirely? Force-reset from the configured secret:"
  echo "    kubectl -n kuso patch secret kuso-admin-credentials --type=merge \\"
  echo "      -p '{\"stringData\":{\"password\":\"<new>\"}}'"
  echo "    kubectl -n kuso set env deployment/kuso-server KUSO_ADMIN_PASSWORD_FORCE_RESET=true"
  echo "    kubectl -n kuso rollout restart deployment/kuso-server"
  echo "    # ...wait for new pod to come up, then:"
  echo "    kubectl -n kuso set env deployment/kuso-server KUSO_ADMIN_PASSWORD_FORCE_RESET-"
else
  echo "  Password:  ${ADMIN_PASSWORD}"
  echo
  echo "  CLI login from your workstation:"
  echo "    kuso login --api https://${KUSO_DOMAIN} -u admin -p '${ADMIN_PASSWORD}'"
fi
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
  GitHub App: not yet configured. Two ways to fix:
    1. Open https://${KUSO_DOMAIN}/settings/github in the dashboard
       and paste your GitHub App credentials.
    2. Re-run install with --github-wizard for an interactive prompt.

  Without it, services still build via 'kuso build trigger' but the
  repo picker on the new-service page stays empty.

EOF
  fi
fi
echo
