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
# jq is used to parse the pod list for the drain (awk over flattened JSON
# is too fragile — "name"/"kind" appear in many nested places). Installed
# best-effort; if it's missing a multi-node reboot fails instead of
# rebooting undrained.
command -v jq >/dev/null 2>&1 || apk add --no-cache jq >/dev/null 2>&1 || true

# json_escape for embedding strings into JSON.
json_escape() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/ /g' | tr -d '\n' | tr -d '\r'; }

# api_patch_node <merge-patch-json>
api_patch_node() {
  curl -sf --cacert "$SA/ca.crt" -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/merge-patch+json" -X PATCH \
    "$APISERVER/api/v1/nodes/$NODE" --data "$1" >/dev/null
}

# api_get <path> — GET against the apiserver, echo body on stdout.
api_get() {
  curl -sf --cacert "$SA/ca.crt" -H "Authorization: Bearer $TOKEN" \
    "$APISERVER$1"
}

# ready_node_count — number of nodes whose Ready condition is True.
# Used to decide whether a drain is worthwhile: on a single Ready node
# there's nowhere to evict to, so we skip the drain entirely. Prefer jq
# (exact: count nodes with a Ready=True condition); fall back to a grep
# heuristic if jq somehow isn't present.
#
# Returns non-zero when the count is unknown (API error such as a 403 on
# the nodes list, or an unparseable body). It used to print 0 instead,
# which took the single-node branch and rebooted a multi-node worker
# without draining it.
ready_node_count() {
  body=$(api_get "/api/v1/nodes") || return 1
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$body" | jq -e '[.items[] | select(.status.conditions[]? | .type=="Ready" and .status=="True")] | length'
  else
    printf '%s' "$body" | tr '{' '\n' | grep -c '"type":"Ready","status":"True"' || true
  fi
}

# drain_pod_list — drainable pods currently on this node, "ns/name" per
# line, into $1. Skips DaemonSet-owned and mirror/static pods (owner
# kind=Node) and terminal pods. Never lists ourselves: this Job pod runs
# on the node being drained, and evicting it kills the apply before the
# reboot (BackoffLimit 0). Non-zero when the list can't be read.
drain_pod_list() {
  pods_json=$(api_get "/api/v1/pods?fieldSelector=spec.nodeName=$NODE" 2>/dev/null) || return 1
  printf '%s' "$pods_json" | jq -r --arg self "${POD_NAMESPACE}/${POD_NAME}" '` + drainPodsFilter + `' > /tmp/drain_raw 2>/dev/null || return 1
  sort -u /tmp/drain_raw > "$1"
}

# drain_node — evict every drainable pod on this node via the Eviction
# API, so workloads reschedule onto other Ready nodes BEFORE the host
# reboots, then wait for the evicted pods to be gone. An eviction blocked
# by a PodDisruptionBudget is retried for up to 120s PER POD, then we
# move on (never wedge an apply forever on a stubborn PDB). The node is
# already cordoned by the caller, so nothing reschedules back onto it.
#
# Returns non-zero when the drain can't be done safely (no jq, pod list
# unreadable); the caller then fails the apply instead of rebooting a
# multi-node worker undrained.
drain_node() {
  if ! command -v jq >/dev/null 2>&1; then
    echo "pkg-apply: drain: jq unavailable"
    return 1
  fi
  drain_pod_list /tmp/drain_pods || { echo "pkg-apply: drain: could not list pods on $NODE"; return 1; }

  count=$(wc -l < /tmp/drain_pods 2>/dev/null | tr -d ' ')
  echo "pkg-apply: drain: $count pod(s) to evict from $NODE"
  [ "${count:-0}" -eq 0 ] && return 0

  : > /tmp/evicted
  while read -r nsname; do
    [ -z "$nsname" ] && continue
    ns=${nsname%%/*}; pod=${nsname#*/}
    body="{\"apiVersion\":\"policy/v1\",\"kind\":\"Eviction\",\"metadata\":{\"name\":\"$pod\",\"namespace\":\"$ns\"},\"deleteOptions\":{\"gracePeriodSeconds\":30}}"
    deadline=$(( $(date +%s) + 120 ))
    # Retry a blocked eviction (429 from a PDB) until this pod's deadline.
    # 404 means the pod is already gone.
    while : ; do
      code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$SA/ca.crt" -H "Authorization: Bearer $TOKEN" \
           -H "Content-Type: application/json" -X POST \
           "$APISERVER/api/v1/namespaces/$ns/pods/$pod/eviction" --data "$body" 2>/dev/null)
      case "$code" in
        200|201) echo "pkg-apply: drain: evicted $ns/$pod"; echo "$nsname" >> /tmp/evicted; break ;;
        404) echo "pkg-apply: drain: $ns/$pod already gone"; break ;;
      esac
      if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "pkg-apply: drain: gave up on $ns/$pod (HTTP $code, PDB/blocked); proceeding"; break
      fi
      sleep 5
    done
  done < /tmp/drain_pods

  # Evicted pods still run their grace period. Rebooting under them
  # kills them uncleanly, so wait (bounded) until they've left the node.
  [ -s /tmp/evicted ] || return 0
  wait_deadline=$(( $(date +%s) + 120 ))
  while : ; do
    left=-1
    if drain_pod_list /tmp/drain_now; then
      left=$(grep -Fxc -f /tmp/evicted /tmp/drain_now || true)
    fi
    if [ "$left" = "0" ]; then
      echo "pkg-apply: drain: evicted pods have left $NODE"; break
    fi
    if [ "$(date +%s)" -ge "$wait_deadline" ]; then
      echo "pkg-apply: drain: evicted pods still terminating after 120s ($left left); proceeding"; break
    fi
    sleep 5
  done
  return 0
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

# Drain: on a MULTI-node cluster, evict this node's pods (via the
# Eviction API) so they reschedule onto other Ready nodes before the
# reboot — preserving availability. On a single Ready node there's
# nowhere to go, so skip the drain and rely on the kubelet restarting
# pods after the reboot (current single-node behavior, unchanged).
READY=$(ready_node_count) || fail "could not count Ready nodes (GET /api/v1/nodes failed); refusing to reboot without a drain decision"
echo "pkg-apply: ready node count = $READY"
if [ "${READY:-0}" -gt 1 ]; then
  set_state draining "draining $NODE before reboot ($READY ready nodes)"
  drain_node || fail "patched, but could not drain $NODE safely (see Job log); not rebooting — reboot it by hand after draining"
else
  echo "pkg-apply: single ready node — skipping drain (nowhere to reschedule)"
fi

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

// drainPodsFilter selects the pods drain_node evicts, as "ns/name", from
// a pod list. $self is the apply pod's own "ns/name".
const drainPodsFilter = `
    .items[]
    | select(([.metadata.ownerReferences[]?.kind] | (contains(["DaemonSet"]) or contains(["Node"]))) | not)
    | select(.status.phase != "Succeeded" and .status.phase != "Failed")
    | .metadata.namespace + "/" + .metadata.name
    | select(. != $self)
`
