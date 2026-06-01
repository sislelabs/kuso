package pkgupdates

// applyScript is the body of the per-node apply Job. It runs in an
// alpine container, nsenters PID 1 to act on the host, and patches the
// node's apply-state annotation via the in-cluster API (pod SA token).
//
// Flow:
//  1. preflight — install curl, set up API patch, snapshot selections.
//  2. patch — apt-get -y upgrade (held-back-safe; NOT dist-upgrade).
//  3. if reboot NOT required → done.
//  4. if reboot required AND ALLOW_REBOOT=false → done, but flag
//     rebootRequired in the apply-state (operator reboots deliberately).
//  5. if reboot required AND ALLOW_REBOOT=true → cordon (mark ours) →
//     drain (best-effort; single-node has nowhere to go) → set state
//     rebooting → DETACHED reboot (survives the Job pod dying with the
//     node). The kuso-server rejoin reconcile uncordons + finalizes.
//
// Any failure → apply-state phase=failed + the log tail, and a non-zero
// exit so the Job records Failed.
const applyScript = `
set -u
NODE="${NODE_NAME}"
ALLOW_REBOOT="${ALLOW_REBOOT:-false}"
APPLY_ANNOTATION="${APPLY_ANNOTATION}"
CORDON_ANNOTATION="${CORDON_ANNOTATION}"
HOST="nsenter --target 1 --mount --uts --ipc --net --pid --"
SA=/var/run/secrets/kubernetes.io/serviceaccount
APISERVER="https://${KUBERNETES_SERVICE_HOST}:${KUBERNETES_SERVICE_PORT}"
TOKEN=$(cat "$SA/token")

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1 || true

# json_escape for embedding strings into JSON.
json_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/ /g' | tr -d '\n' | tr -d '\r'; }

# api_patch_node <merge-patch-json>
api_patch_node() {
  curl -sf --cacert "$SA/ca.crt" -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/merge-patch+json" -X PATCH \
    "$APISERVER/api/v1/nodes/$NODE" --data "$1" >/dev/null
}

# set_state <phase> <logtail>
set_state() {
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  inner="{\"phase\":\"$1\",\"at\":\"$now\",\"log\":\"$(json_escape "$2")\"}"
  einner=$(json_escape "$inner")
  api_patch_node "{\"metadata\":{\"annotations\":{\"${APPLY_ANNOTATION}\":\"${einner}\"}}}" || true
}

fail() { echo "pkg-apply: FAILED: $1" >&2; set_state failed "$1"; exit 1; }

echo "pkg-apply: node=$NODE allowReboot=$ALLOW_REBOOT"

# --- patch ---
if [ "${PKG_MGR}" != "apt" ]; then fail "unsupported pkg manager: ${PKG_MGR}"; fi
$HOST apt-get update -qq >/dev/null 2>&1 || true
LOG=$($HOST sh -c 'DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confold upgrade' 2>&1) || fail "apt upgrade failed: $(echo "$LOG" | tail -c 1500)"
echo "$LOG" | tail -5

# --- reboot decision ---
if ! $HOST test -f /var/run/reboot-required 2>/dev/null; then
  set_state done "patched; no reboot required"
  echo "pkg-apply: done (no reboot)"; exit 0
fi

if [ "$ALLOW_REBOOT" != "true" ]; then
  set_state done "patched; REBOOT REQUIRED — rerun with reboot allowed to finish"
  echo "pkg-apply: done (reboot required, not allowed)"; exit 0
fi

# --- reboot orchestration (phase 4) ---
# Cordon, marking the cordon as ours so the server only uncordons what
# we cordoned. (kuso-server also cordons before launch; this is a
# belt-and-suspenders so the marker is set even if launched directly.)
api_patch_node "{\"spec\":{\"unschedulable\":true},\"metadata\":{\"annotations\":{\"${CORDON_ANNOTATION}\":\"true\"}}}" || true

# Drain best-effort: evict non-DaemonSet pods on this node. On a single
# node there's nowhere to reschedule, so failures here are expected and
# non-fatal — we proceed to the reboot regardless (documented behavior).
# (kubectl isn't guaranteed in-container; we rely on the kubelet to
# restart pods after reboot. A future enhancement can add eviction via
# the API here. For now the reboot itself is the operation.)

set_state rebooting "patched; rebooting node to finish (reboot-required)"
echo "pkg-apply: rebooting $NODE (detached)"
# Detached reboot: schedule it slightly in the future and fully detach so
# the Job pod dying with the node doesn't abort the reboot. systemctl
# reboot via the host's init.
$HOST sh -c 'setsid sh -c "sleep 3; systemctl reboot" >/dev/null 2>&1 < /dev/null &' || \
  $HOST sh -c 'setsid sh -c "sleep 3; reboot" >/dev/null 2>&1 < /dev/null &' || \
  fail "could not initiate reboot"
echo "pkg-apply: reboot scheduled"
# Give the detached reboot a moment to take; the pod will be killed by
# the node going down.
sleep 30
exit 0
`
