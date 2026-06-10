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

# api_get <path> — GET against the apiserver, echo body on stdout.
api_get() {
  curl -sf --cacert "$SA/ca.crt" -H "Authorization: Bearer $TOKEN" \
    "$APISERVER$1"
}

# ready_node_count — number of nodes whose Ready condition is True.
# Used to decide whether a drain is worthwhile: on a single Ready node
# there's nowhere to evict to, so we skip the drain entirely.
ready_node_count() {
  api_get "/api/v1/nodes" 2>/dev/null \
    | tr '{' '\n' \
    | grep -c '"type":"Ready","status":"True"' 2>/dev/null \
    || echo 0
}

# drain_node — evict every non-DaemonSet, non-mirror pod scheduled on
# this node via the Eviction API, so workloads reschedule onto other
# Ready nodes BEFORE the host reboots. Best-effort + bounded: an eviction
# blocked by a PodDisruptionBudget is retried for a while, then we proceed
# to the reboot regardless (the reboot is the operation; we never wedge an
# apply forever on a stubborn PDB). The node is already cordoned by the
# caller, so nothing reschedules back onto it mid-drain.
drain_node() {
  # Pods on THIS node, all namespaces, via fieldSelector.
  pods_json=$(api_get "/api/v1/pods?fieldSelector=spec.nodeName=$NODE" 2>/dev/null) || {
    echo "pkg-apply: drain: could not list pods; skipping drain"; return 0; }

  # Extract "namespace/name|ownerKind" tuples. We parse with a tiny awk
  # state machine over the flattened JSON: each pod object carries its
  # metadata.namespace, metadata.name, and ownerReferences[].kind. We
  # skip DaemonSet-owned pods (they're tolerated/replaced per-node, not
  # drainable) and mirror pods (static, kind=Node owner).
  echo "$pods_json" \
    | tr ',{}[]' '\n\n\n\n\n' \
    | awk '
        /"namespace":/ { gsub(/.*"namespace":"|".*/,""); ns=$0 }
        /"name":/ && name=="" { v=$0; gsub(/.*"name":"|".*/,"",v); name=v }
        /"kind":"DaemonSet"/ { daemon=1 }
        /"kind":"Node"/      { mirror=1 }
        /"phase":"/ { p=$0; gsub(/.*"phase":"|".*/,"",p); phase=p
                      if (ns!="" && name!="" && daemon==0 && mirror==0 && phase!="Succeeded" && phase!="Failed")
                        print ns"/"name
                      ns=""; name=""; daemon=0; mirror=0; phase="" }
      ' 2>/dev/null | sort -u > /tmp/drain_pods || true

  count=$(wc -l < /tmp/drain_pods 2>/dev/null | tr -d ' ')
  echo "pkg-apply: drain: $count pod(s) to evict from $NODE"
  [ "${count:-0}" -eq 0 ] && return 0

  # Evict each pod. The Eviction API respects PodDisruptionBudgets.
  deadline=$(( $(date +%s) + 120 ))
  while read -r nsname; do
    [ -z "$nsname" ] && continue
    ns=${nsname%%/*}; pod=${nsname#*/}
    body="{\"apiVersion\":\"policy/v1\",\"kind\":\"Eviction\",\"metadata\":{\"name\":\"$pod\",\"namespace\":\"$ns\"},\"deleteOptions\":{\"gracePeriodSeconds\":30}}"
    # Retry a blocked eviction (429 from a PDB) until the deadline.
    while : ; do
      if curl -sf --cacert "$SA/ca.crt" -H "Authorization: Bearer $TOKEN" \
           -H "Content-Type: application/json" -X POST \
           "$APISERVER/api/v1/namespaces/$ns/pods/$pod/eviction" --data "$body" >/dev/null 2>&1; then
        echo "pkg-apply: drain: evicted $ns/$pod"; break
      fi
      if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "pkg-apply: drain: gave up on $ns/$pod (PDB/blocked); proceeding"; break
      fi
      sleep 5
    done
  done < /tmp/drain_pods
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
READY=$(ready_node_count)
echo "pkg-apply: ready node count = $READY"
if [ "${READY:-0}" -gt 1 ]; then
  set_state draining "draining $NODE before reboot ($READY ready nodes)"
  drain_node
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
