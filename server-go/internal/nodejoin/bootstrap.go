// Package nodejoin's bootstrap.go implements the v0.10 pull-mode join
// flow. The new VM curls a one-liner from kuso, the script fetches
// K3S_URL + K3S_TOKEN by redeeming the operator-minted bootstrap
// token, then runs the standard k3s install. This is the inverse of
// the SSH-driven Join() in nodejoin.go: kuso never opens an outbound
// connection to the VM, so the flow works behind NAT, on hosts with
// SSH off, and on cloud images without sshd configured.
//
// Design goals (per "super simple, resilient"):
//
//   - One curl on the VM, no flags to remember.
//   - Single-use, 15-minute-TTL token; replays return 410.
//   - Detect facts (arch, distro, hostname, cloud metadata) on the
//     VM and POST them back so the operator doesn't hand-edit labels.
//   - Retry transient network errors with capped backoff; never
//     silently succeed when the install actually failed.
//   - Idempotent: re-running the script on a partially-installed host
//     either resumes (k3s service starts, install no-ops) or surfaces
//     the exact error.

package nodejoin

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"
)

// DefaultTokenTTL is the lifetime of a freshly-minted bootstrap token.
// 5 minutes — short enough that a token pasted into .bash_history
// (the shell-aware hardening below tries to avoid this but the token
// can leak through other vectors: terminal scrollback, tmux logging,
// shell hooks) expires before an attacker can replay it. Operators
// have to mint-and-paste promptly; the dashboard's UX makes that the
// expected flow anyway.
const DefaultTokenTTL = 5 * time.Minute

// MintTokenRequest is what the handler hands the helper to mint a
// bootstrap token. Labels are the kuso-namespaced keys (without the
// kuso.sislelabs.com/ prefix); the bootstrap script bakes them into
// --node-label flags so the joined node lands with the right region/
// tier from the moment kubelet starts.
type MintTokenRequest struct {
	Labels    map[string]string
	NodeName  string
	CreatedBy string // user id, for audit
	TTL       time.Duration
}

// MintedToken is what the handler returns to the operator. The
// OneLiner is the only thing they care about; the rest is for the
// "pending tokens" UI. JTI is the CLEARTEXT token — returned exactly
// once at mint time. JTIPrefix is a short hash prefix that survives
// in subsequent ListPending responses so the operator can correlate
// the cleartext one-liner they captured against a pending row.
type MintedToken struct {
	JTI       string            `json:"jti"`
	JTIPrefix string            `json:"jtiPrefix"`
	ExpiresAt time.Time         `json:"expiresAt"`
	OneLiner  string            `json:"oneLiner"`
	Labels    map[string]string `json:"labels"`
	NodeName  string            `json:"nodeName,omitempty"`
}

// GenerateJTI returns a 128-bit random base64url-encoded identifier.
// 22 chars on the wire, indistinguishable from random — appropriate
// for a bearer token. We don't use UUIDs because they leak structure
// (version/variant bits) and don't add anything for our threat model.
func GenerateJTI() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("nodejoin: rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// BuildOneLiner returns the curl command an operator pastes on the
// new VM. publicURL must be the externally-reachable URL of the kuso
// server (https://kuso.example.com); we don't try to derive it
// because the same kuso install can be reachable on multiple URLs
// (LAN address, public hostname) and only the operator knows which
// the new VM can hit.
//
// The shape -fsSL is intentional: -f makes curl exit non-zero on 4xx
// (so a stale/revoked token surfaces as a script failure instead of
// silently piping an HTML error page into sh), -sS shows errors but
// no progress meter, -L follows redirects in case kuso sits behind a
// reverse proxy.
func BuildOneLiner(publicURL, jti string) string {
	publicURL = strings.TrimRight(publicURL, "/")
	return fmt.Sprintf("curl -fsSL %s/bootstrap?token=%s | sudo sh", publicURL, jti)
}

// ScriptParams carries the values the bootstrap script template
// needs. The kuso server fills these in when the VM curls
// /bootstrap?token=<jti>; the script then runs on the VM and the
// agent calls back to /bootstrap/register-node to actually consume
// the token (and retrieve K3S_URL+K3S_TOKEN, which we deliberately
// do NOT bake into the script for security — see comments in the
// template below).
type ScriptParams struct {
	// PublicURL is kuso's externally-reachable URL (https://...).
	// The script POSTs to PublicURL + "/bootstrap/register-node"
	// to consume the token. Trailing slash stripped.
	PublicURL string
	// JTI is the bootstrap token id. The script passes it back so
	// the server can atomically consume + return the join params.
	JTI string
}

// RenderScript returns the shell script the operator's `sudo sh`
// will execute. Defensive shell:
//
//   - set -euo pipefail; trap any error and print a friendly summary.
//   - Retries POST /register-node up to 5x with capped backoff so a
//     transient blip doesn't burn the token.
//   - Detects facts (uname -m, /etc/os-release, hostname, cloud
//     metadata) and includes them in the registration body — the
//     server uses them to set --node-label for arch / instance-type.
//   - Idempotent: if /usr/local/bin/k3s already exists, we still
//     re-run the install with the new env so a partial install can
//     be resumed. The k3s installer itself is idempotent.
//   - Never logs the K3S_TOKEN. The token comes back from the
//     server in a JSON response we feed straight into the install
//     command's env without echoing.
func RenderScript(p ScriptParams) (string, error) {
	if p.PublicURL == "" {
		return "", errors.New("RenderScript: PublicURL required")
	}
	if p.JTI == "" {
		return "", errors.New("RenderScript: JTI required")
	}
	p.PublicURL = strings.TrimRight(p.PublicURL, "/")
	tpl, err := template.New("bootstrap").Parse(bootstrapScriptTemplate)
	if err != nil {
		return "", fmt.Errorf("RenderScript: parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, p); err != nil {
		return "", fmt.Errorf("RenderScript: execute: %w", err)
	}
	return buf.String(), nil
}

// RegisterRequest is what the bootstrap script POSTs to
// /bootstrap/register-node. The server consumes the token and
// returns the join parameters. Facts are best-effort — empty
// strings are fine.
type RegisterRequest struct {
	Token         string `json:"token"`
	Hostname      string `json:"hostname,omitempty"`
	Arch          string `json:"arch,omitempty"`
	OSID          string `json:"osId,omitempty"`
	OSVersion     string `json:"osVersion,omitempty"`
	CloudProvider string `json:"cloudProvider,omitempty"`
	InstanceType  string `json:"instanceType,omitempty"`
	Region        string `json:"region,omitempty"`
}

// RegisterResponse is what the server returns once the token is
// consumed. The script feeds these straight into the k3s install
// command. We deliberately return the install command pre-built so
// the script doesn't have to know how to escape labels — the server
// owns that via BuildInstallCommand.
//
// We do NOT serialise the raw k3s server token in this response. The
// install command already contains it (shell-escaped), and a script
// error path that prints the raw response body would otherwise leak
// the cluster-wide token to the operator's terminal. The script
// builds K3S_URL+K3S_TOKEN by parsing InstallCommand instead.
type RegisterResponse struct {
	NodeName       string            `json:"nodeName,omitempty"`
	Labels         map[string]string `json:"labels"`
	InstallCommand string            `json:"installCommand"`
	// RegistryHost is the in-cluster image registry's host:port
	// (e.g. "kuso-registry.kuso.svc.cluster.local:5000"). The bootstrap
	// writes a containerd registries.yaml entry for it so the new node
	// can pull build images over plain HTTP. Empty = skip registry setup.
	RegistryHost string `json:"registryHost,omitempty"`
	// RegistryIP is the registry Service's ClusterIP. The new node's
	// containerd resolves RegistryHost from the host netns (NOT cluster
	// DNS), so the bootstrap pins host→IP in /etc/hosts. Empty = skip.
	RegistryIP string `json:"registryIP,omitempty"`
}

// MergeFactLabels takes the operator-supplied label set and the
// facts the agent reported, and returns the merged label set we'll
// pin on the new node. Operator labels win on conflict — facts are a
// convenience, not policy. Labels emitted only when the fact has a
// non-empty value (we don't want `arch=""` taints).
//
// Facts → labels mapping:
//
//	arch          → arch=<amd64|arm64|...>
//	cloudProvider → cloud=<hetzner|aws|...>
//	instanceType  → instance-type=<t3.small|...>
//	region        → region=<value>     (only if operator didn't set one)
func MergeFactLabels(operator map[string]string, req RegisterRequest) map[string]string {
	out := map[string]string{}
	// Facts first, so operator overrides.
	if req.Arch != "" {
		out["arch"] = req.Arch
	}
	if req.CloudProvider != "" {
		out["cloud"] = req.CloudProvider
	}
	if req.InstanceType != "" {
		out["instance-type"] = req.InstanceType
	}
	if req.Region != "" {
		out["region"] = req.Region
	}
	for k, v := range operator {
		if v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// bootstrapScriptTemplate is what the operator's `sudo sh` runs. The
// double curly braces are Go template; everything else is shell.
//
// The script is small enough to read in one screen — favoring
// reliability over flexibility. Three retries on the register call
// (5s, 15s, 45s backoff) cover the realistic failure modes (control
// plane just rebooted, transient network blip, NAT taking a moment to
// settle). After that we bail with a clear error.
const bootstrapScriptTemplate = `#!/bin/sh
# kuso node bootstrap — generated by the kuso server.
# This script consumes a single-use bootstrap token to retrieve the
# k3s join URL + token, then runs the standard k3s agent install.
# Safe to re-run on partial installs; the k3s installer is idempotent.

set -eu

KUSO_URL='{{.PublicURL}}'
KUSO_TOKEN='{{.JTI}}'

log() { printf '\033[1;36m[kuso]\033[0m %s\n' "$*" >&2; }
err() { printf '\033[1;31m[kuso]\033[0m %s\n' "$*" >&2; }

require() {
  for cmd in "$@"; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      err "required command '$cmd' not found — install it (apt/yum/apk install $cmd) and re-run"
      exit 1
    fi
  done
}

require curl sh

# ---------- gather facts ----------
HOSTNAME="$(hostname 2>/dev/null || echo unknown)"
ARCH="$(uname -m 2>/dev/null || echo unknown)"
case "$ARCH" in
  x86_64)  ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  armv7l)  ARCH=arm   ;;
esac

OS_ID=""
OS_VERSION=""
if [ -r /etc/os-release ]; then
  # Source carefully — ID and VERSION_ID are simple shell-safe values.
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-}"
  OS_VERSION="${VERSION_ID:-}"
fi

# Cloud metadata — best-effort, 1s timeout. We probe the IMDS
# endpoints common to AWS / Hetzner / DigitalOcean / GCP. Failure is
# silent; the server can still join a node without provider hints.
CLOUD=""
INSTANCE_TYPE=""
REGION=""
imds() { curl -fsS --max-time 1 "$@" 2>/dev/null || true; }

# AWS IMDSv2 first (token-gated), then v1 fallback. Hetzner Cloud
# advertises the same v1 endpoint shape so the v1 path catches both.
if AWS_TOK=$(curl -fsS -X PUT --max-time 1 -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token 2>/dev/null); then
  if [ -n "$AWS_TOK" ]; then
    INSTANCE_TYPE=$(curl -fsS --max-time 1 -H "X-aws-ec2-metadata-token: $AWS_TOK" http://169.254.169.254/latest/meta-data/instance-type 2>/dev/null || true)
    REGION=$(curl -fsS --max-time 1 -H "X-aws-ec2-metadata-token: $AWS_TOK" http://169.254.169.254/latest/meta-data/placement/region 2>/dev/null || true)
    [ -n "$INSTANCE_TYPE" ] && CLOUD=aws
  fi
fi
if [ -z "$CLOUD" ]; then
  HCLOUD=$(imds http://169.254.169.254/hetzner/v1/metadata/instance-id)
  if [ -n "$HCLOUD" ]; then
    CLOUD=hetzner
    INSTANCE_TYPE=$(imds http://169.254.169.254/hetzner/v1/metadata/instance-type)
    REGION=$(imds http://169.254.169.254/hetzner/v1/metadata/region)
  fi
fi

# ---------- register with kuso ----------
# POST our facts; receive K3S_URL, K3S_TOKEN, and the pre-built
# install command. The token is consumed atomically here — replays
# return 410 and the script exits.

json_escape() { printf '%s' "$1" | tr -d '\000-\037' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

PAYLOAD=$(printf '{"token":"%s","hostname":"%s","arch":"%s","osId":"%s","osVersion":"%s","cloudProvider":"%s","instanceType":"%s","region":"%s"}' \
  "$(json_escape "$KUSO_TOKEN")" "$(json_escape "$HOSTNAME")" "$(json_escape "$ARCH")" \
  "$(json_escape "$OS_ID")" "$(json_escape "$OS_VERSION")" \
  "$(json_escape "$CLOUD")" "$(json_escape "$INSTANCE_TYPE")" "$(json_escape "$REGION")")

REGISTER_URL="$KUSO_URL/bootstrap/register-node"

ATTEMPT=0
MAX_ATTEMPTS=4
RESPONSE=""
HTTP_CODE=""
while [ "$ATTEMPT" -lt "$MAX_ATTEMPTS" ]; do
  ATTEMPT=$((ATTEMPT + 1))
  log "registering with kuso (attempt $ATTEMPT/$MAX_ATTEMPTS)"
  TMPF=$(mktemp)
  # -L follows redirects, including 307/308 which preserve POST. This
  # is belt-and-braces for the case where KUSO_PUBLIC_URL or the host
  # the operator hit ends up baked in as http:// but the cluster's
  # ingress redirects to https://. Server-side fix is in publicBaseURL;
  # this catches the same problem if a sysadmin pins the wrong scheme.
  HTTP_CODE=$(curl -fsSL -o "$TMPF" -w '%{http_code}' \
    -X POST -H 'Content-Type: application/json' \
    --max-time 30 \
    --data "$PAYLOAD" \
    "$REGISTER_URL" 2>/dev/null || echo "000")
  RESPONSE=$(cat "$TMPF" 2>/dev/null || true)
  rm -f "$TMPF"
  case "$HTTP_CODE" in
    2*)
      break
      ;;
    410)
      err "this bootstrap token is already used, expired, or revoked"
      err "ask your kuso admin for a new one"
      exit 2
      ;;
    404)
      err "bootstrap token not recognized — check the URL is correct"
      exit 2
      ;;
    000|5*|502|503|504)
      # Transient — retry with backoff.
      SLEEP=$((5 * ATTEMPT))
      log "register failed (http=$HTTP_CODE), retrying in ${SLEEP}s"
      sleep "$SLEEP"
      ;;
    *)
      # We deliberately do NOT echo $RESPONSE — even on an error
      # path the server may have started writing the install
      # command (which embeds the cluster k3s token) before
      # noticing the request was malformed. Truncating to a
      # generic message keeps the long-lived secret off the
      # operator's terminal.
      err "register failed with http=$HTTP_CODE"
      exit 3
      ;;
  esac
done

if ! echo "$HTTP_CODE" | grep -q '^2'; then
  err "register failed after $MAX_ATTEMPTS attempts (last http=$HTTP_CODE)"
  err "is $KUSO_URL reachable from this VM?"
  exit 4
fi

# Parse the response. We avoid jq (not installed on minimal images);
# the server response shape is small and stable so a few sed greps are
# enough. The install command is pre-built by the server with K3S_URL
# and K3S_TOKEN already shell-escaped inline — the script never sees
# the raw token, so a parse failure can't leak it to the terminal.
extract() {
  printf '%s' "$RESPONSE" | sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1
}

NODE_NAME_VAL=$(extract nodeName)
REGISTRY_HOST=$(extract registryHost)
REGISTRY_IP=$(extract registryIP)
INSTALL_CMD=$(printf '%s' "$RESPONSE" | sed -n 's/.*"installCommand"[[:space:]]*:[[:space:]]*"\(.*\)"[[:space:]]*[},].*/\1/p' | head -n1)
# JSON un-escape \" and \\ in the install command so it's a runnable shell line.
INSTALL_CMD=$(printf '%s' "$INSTALL_CMD" | sed -e 's/\\"/"/g' -e 's/\\\\/\\/g')

if [ -z "$INSTALL_CMD" ]; then
  err "register response missing installCommand — server bug?"
  # No $RESPONSE in the error: it embeds the join token.
  exit 5
fi

# ---------- in-cluster registry wiring ----------
# Build images live in an in-cluster registry served over plain HTTP at
# a ClusterIP. A worker needs two things to pull from it, and the k3s
# agent install does NOT set either up:
#
#   1. /etc/hosts: containerd resolves the registry host from the HOST
#      netns, which has no cluster DNS — so without a hosts entry the
#      pull dies with "lookup …: Try again". Pin host -> ClusterIP.
#   2. registries.yaml: tell containerd to use http:// (not https) and
#      skip TLS verify for that host, else the pull tries https and
#      fails on the plain-HTTP registry.
#
# Both files are idempotent + persist across reboots. Skipped when the
# server didn't advertise a registry (older control planes).
if [ -n "$REGISTRY_HOST" ] && [ -n "$REGISTRY_IP" ]; then
  REG_NAME="${REGISTRY_HOST%%:*}"   # strip :port for the /etc/hosts name
  log "wiring in-cluster registry $REGISTRY_HOST ($REGISTRY_IP)"
  if ! grep -q "[[:space:]]$REG_NAME\$" /etc/hosts 2>/dev/null; then
    printf '%s %s\n' "$REGISTRY_IP" "$REG_NAME" >> /etc/hosts
  fi
  mkdir -p /etc/rancher/k3s
  cat > /etc/rancher/k3s/registries.yaml <<REG_EOF
mirrors:
  "$REGISTRY_HOST":
    endpoint:
      - "http://$REGISTRY_HOST"
configs:
  "$REGISTRY_HOST":
    tls:
      insecure_skip_verify: true
REG_EOF
else
  log "no registry advertised by control plane — skipping registry wiring"
fi

# ---------- kernel tuning ----------
# k3s + workload pods + Promtail + helm operator etc. each allocate
# inotify watchers. Distro defaults (max_user_instances=128) get
# exhausted around ~30 pods, producing silent "failed to create
# fsnotify watcher: too many open files" failures inside pods (config
# reloaders, Next.js servers, etc). Bump before the agent starts so
# the new node lands with workable limits. Persisted to sysctl.d so
# it survives reboot. Matches the same tuning applied on the control
# plane by install.sh.
log "tuning kernel limits (fs.inotify) for k3s + many pods"
cat > /etc/sysctl.d/99-kuso-inotify.conf <<SYSCTL_EOF
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 524288
SYSCTL_EOF
sysctl -p /etc/sysctl.d/99-kuso-inotify.conf >/dev/null 2>&1 || true

# ---------- run k3s install ----------
log "joining cluster as ${NODE_NAME_VAL:-$HOSTNAME}"
if [ -x /usr/local/bin/k3s ]; then
  log "k3s already installed; the installer will reconfigure as agent"
fi

# We never echo the install command verbatim because it contains the
# k3s shared secret. The redacted line below is the most we can show
# without re-extracting K3S_URL from $INSTALL_CMD (which would defeat
# the redaction).
log "running: curl -sfL https://get.k3s.io | K3S_URL=*** K3S_TOKEN=*** INSTALL_K3S_EXEC=... sh -"

if ! sh -c "$INSTALL_CMD"; then
  err "k3s install failed — check the output above for the actual error"
  err "common causes: control-plane URL not reachable on :6443, firewall blocking egress, k3s mirror down"
  exit 6
fi

log "k3s agent installed and started"
log "the new node should appear in the kuso UI within ~30 seconds"
log "done."
`
