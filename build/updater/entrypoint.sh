#!/usr/bin/env bash
# kuso-updater entrypoint. Runs inside the Job that the self-updater
# kicks off (server-go/internal/updater/applyJob). Reads env vars
# set by the server, walks the upgrade in clear phases, and writes
# JSON status to the ConfigMap so the UI can render progress without
# tailing pod logs.

set -Eeuo pipefail

NS="${KUSO_NAMESPACE:-kuso}"
CM="${KUSO_STATUS_CONFIGMAP:-kuso-update-status}"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# Bounded downloads: a stalled connection must fail the step, not hang
# the Job until its deadline.
CURL_OPTS=(-fsSL --connect-timeout 15 --max-time 60 --retry 2 --retry-delay 5)

CURRENT_PHASE="starting"
write_status() {
  local phase="$1"
  CURRENT_PHASE="$phase"
  local message="${2:-}"
  local started="${STARTED_AT:-$(now)}"
  local payload
  payload=$(jq -n \
    --arg phase   "$phase" \
    --arg message "$message" \
    --arg started "$started" \
    --arg updated "$(now)" \
    '{phase:$phase, message:$message, started:$started, updated:$updated}')

  kubectl create configmap "$CM" -n "$NS" \
    --from-literal=status="$payload" \
    --dry-run=client -o yaml \
  | kubectl apply -f - >/dev/null
}

# verify_sha256 <file> <expected-hex> <label>: the expected digest comes
# from the signed release.json (via the server), so a mismatch means the
# release asset was replaced after signing. Refuse to apply it.
#
# An EMPTY expected digest is a hard failure: either the release predates
# release digests, or the launching server (v0.31.0 and earlier) doesn't
# pass them. The server sets KUSO_ALLOW_UNVERIFIED_ASSETS=1 only when an
# admin explicitly asked (kuso upgrade --allow-unverified). A server too
# old to know that flag upgrades in two steps: first to a v0.32+ release,
# then on to this one.
UNVERIFIED=""
verify_sha256() {
  local file="$1" want="$2" label="$3" got
  if [ -z "$want" ]; then
    if [ "${KUSO_ALLOW_UNVERIFIED_ASSETS:-}" != "1" ]; then
      echo "==> no sha256 for ${label}; refusing to apply an unverified asset"
      write_status "failed" "no sha256 for ${label} in the release digests, so it can't be verified — nothing applied. Re-run with 'kuso upgrade --allow-unverified' to accept it, or (server older than v0.32) upgrade to an intermediate release first"
      exit 1
    fi
    echo "==> WARNING: no sha256 for ${label}; applying unverified (--allow-unverified)"
    UNVERIFIED="${UNVERIFIED:+${UNVERIFIED}, }${label}"
    return 0
  fi
  got=$(sha256sum "$file" | awk '{print $1}')
  if [ "$got" != "$want" ]; then
    echo "==> ${label} sha256 mismatch: got ${got}, release.json says ${want}"
    write_status "failed" "${label} does not match the signed release digest (got ${got}, want ${want}) — nothing applied"
    exit 1
  fi
  echo "==> ${label} sha256 verified (${got})"
}

# Any unguarded command failing under set -e used to exit with the
# ConfigMap still on an in-progress phase, so the UI showed the update
# running forever. Explicit failure paths write their own status and
# `exit 1`, which does not fire ERR.
on_err() {
  trap - ERR
  write_status "failed" "updater aborted at line $1 during ${CURRENT_PHASE} — see the kuso-update Job logs" || true
}
trap 'on_err $LINENO' ERR

# The server and operator images are rolled under the kuso-server
# ServiceAccount; a bare tag would run whatever was last pushed to it.
for img_var in KUSO_SERVER_IMAGE KUSO_OPERATOR_IMAGE; do
  img="${!img_var:-}"
  if [ -n "$img" ] && [[ "$img" != *@sha256:* ]] && [ "${KUSO_ALLOW_UNVERIFIED_ASSETS:-}" != "1" ]; then
    echo "==> ${img_var}=${img} is not pinned by digest; refusing"
    write_status "failed" "${img} is not pinned by digest in the signed release — nothing applied. Re-run with 'kuso upgrade --allow-unverified' to accept it"
    exit 1
  fi
done

STARTED_AT="$(now)"

# KUSO_SKIP_CRDS=1 is set by the server on a downgrade: the older
# release's CRDs would drop newer spec fields and the apiserver would
# prune them from every stored CR. The installed CRDs are a superset the
# older server tolerates, so they stay.
if [ "${KUSO_SKIP_CRDS:-}" = "1" ]; then
  write_status "applying-crds" "downgrade to ${KUSO_TARGET_VERSION} — keeping the installed (newer) CRDs"
  echo "==> downgrade: skipping CRD apply so newer spec fields are not pruned"
else
  write_status "applying-crds" "downloading ${KUSO_CRDS_URL}"

  TMP_CRDS=$(mktemp)
  curl "${CURL_OPTS[@]}" "$KUSO_CRDS_URL" -o "$TMP_CRDS"
  verify_sha256 "$TMP_CRDS" "${KUSO_CRDS_SHA256:-}" "crds.yaml"

  # Server-side dry-run BEFORE the real apply. This validates the CRD
  # objects themselves (a structurally invalid schema, an illegal type
  # change the apiserver refuses) and aborts with a clear status instead
  # of a half-applied bundle.
  #
  # It does NOT re-validate stored CRs against the new schema. A tightened
  # pattern/maxLength, a new CEL rule or required field, or a dropped
  # x-kubernetes-preserve-unknown-fields all pass this dry-run and then
  # break (or silently prune) existing CRs on their next write. Those are
  # caught at ship time by hack/crd-guard.py, not here.
  if ! dryrun_out=$(kubectl apply --dry-run=server -f "$TMP_CRDS" 2>&1); then
    echo "==> CRD server-side dry-run FAILED — refusing to apply:"
    echo "$dryrun_out" | sed 's/^/    /'
    write_status "failed" "CRD validation (dry-run) failed — not applied: $(echo "$dryrun_out" | tr '\n' ' ' | cut -c1-300)"
    exit 1
  fi
  kubectl apply -f "$TMP_CRDS" >/dev/null
fi

# Apply the release's non-workload platform manifests (RBAC,
# ServiceAccounts, PriorityClasses, NetworkPolicies, PDBs) before
# rolling images, so new server/operator code picks up RBAC/policy
# changes instead of drifting. Absent on releases that predate the
# bundle — skip cleanly so old release.json payloads still upgrade.
#
# BEST-EFFORT BY DESIGN: this Job runs as the kuso-server
# ServiceAccount. It may write RBAC objects, but Kubernetes' escalation
# prevention refuses any Role/ClusterRole granting a permission
# kuso-server doesn't itself hold cluster-wide (pods/eviction, pods/log
# watch, deployments/scale, …), so a changed role of that kind comes back
# Forbidden. A forbidden/partial apply must NOT abort the upgrade —
# hard-failing would brick self-update with no recovery path, since the
# fix ships inside the very Job that's failing. Instead the warning is
# carried through to the final "done" status (MANIFESTS_WARNING) so the
# UI and `kuso upgrade` show that a manual apply is still owed.
MANIFESTS_WARNING=""
# KUSO_SKIP_MANIFESTS=1 is set by the server on a downgrade, for the same
# reason as KUSO_SKIP_CRDS: the older bundle carries older RBAC/policy
# (and may predate the sha256 field), while the installed newer bundle
# is a superset the older server runs fine under.
if [ "${KUSO_SKIP_MANIFESTS:-}" = "1" ]; then
  echo "==> downgrade: keeping the installed (newer) platform manifests"
elif [ -n "${KUSO_MANIFESTS_URL:-}" ]; then
  write_status "applying-manifests" "downloading ${KUSO_MANIFESTS_URL}"
  TMP_MANIFESTS=$(mktemp)
  if curl "${CURL_OPTS[@]}" "$KUSO_MANIFESTS_URL" -o "$TMP_MANIFESTS"; then
    # A mismatch is fatal even though the apply itself is best-effort:
    # CRDs are already applied at this point, but no RBAC from an
    # unverified bundle and no image roll happens.
    verify_sha256 "$TMP_MANIFESTS" "${KUSO_MANIFESTS_SHA256:-}" "upgrade-manifests.yaml"
    # server-side apply, non-fatal: capture output, warn on failure.
    if apply_out=$(kubectl apply -f "$TMP_MANIFESTS" 2>&1); then
      echo "==> applied upgrade manifests"
    else
      echo "==> WARNING: upgrade-manifests apply incomplete (continuing with image roll):"
      echo "$apply_out" | sed 's/^/    /'
      forbidden=$(echo "$apply_out" | { grep -iE 'forbidden|error' || true; } | head -3 | tr '\n' ' ' | cut -c1-300)
      MANIFESTS_WARNING="some platform manifests (RBAC/policy) were NOT applied and need a manual kubectl apply as cluster-admin: ${forbidden}"
      write_status "applying-manifests" "partial — ${MANIFESTS_WARNING}; continuing"
    fi
  else
    echo "==> WARNING: could not download upgrade-manifests bundle; continuing with image roll"
    MANIFESTS_WARNING="could not download the upgrade-manifests bundle; platform RBAC/policy changes were not applied"
  fi
else
  echo "==> no upgrade-manifests bundle in this release; skipping (pre-bundle release)"
fi

write_status "rolling-server" "${KUSO_SERVER_IMAGE}"
# Snapshot the CURRENT server image BEFORE we change it, so a
# crashlooping new image can be reverted. This Job outlives the server
# pod (the server is killed mid-roll), so the revert has to live here —
# the in-app updater can't recover an API that's down, and BackoffLimit:0
# means the Job won't retry. jsonpath returns "" if the container/deploy
# is missing; we guard on that below before attempting a revert.
PRIOR_SERVER_IMAGE=$(kubectl get -n "$NS" deploy/kuso-server \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="server")].image}' 2>/dev/null || true)

kubectl set image -n "$NS" deploy/kuso-server "server=${KUSO_SERVER_IMAGE}" >/dev/null

revert_server() {
  local why="$1"
  if [ -n "$PRIOR_SERVER_IMAGE" ] && [ "$PRIOR_SERVER_IMAGE" != "${KUSO_SERVER_IMAGE}" ]; then
    echo "==> reverting kuso-server to ${PRIOR_SERVER_IMAGE}"
    write_status "failed" "server image ${KUSO_SERVER_IMAGE} ${why}; reverting to ${PRIOR_SERVER_IMAGE}"
    kubectl set image -n "$NS" deploy/kuso-server "server=${PRIOR_SERVER_IMAGE}" >/dev/null || true
    if kubectl rollout status -n "$NS" deploy/kuso-server --timeout=180s; then
      # The failed image may have migrated the database before it died;
      # the reverted server reports that on /readyz (checks.schema).
      schema=$(curl -s --connect-timeout 5 --max-time 10 "http://kuso-server.${NS}.svc:80/readyz" 2>/dev/null \
        | jq -r '.checks.schema // empty' 2>/dev/null || true)
      if [ -n "$schema" ]; then
        write_status "failed" "server image ${KUSO_SERVER_IMAGE} ${why}; reverted to ${PRIOR_SERVER_IMAGE}, but the database keeps the newer migrations (${schema}) — check the server logs for SQL errors"
      else
        write_status "failed" "server image ${KUSO_SERVER_IMAGE} ${why}; reverted to working ${PRIOR_SERVER_IMAGE}"
      fi
    else
      write_status "failed" "server image ${KUSO_SERVER_IMAGE} ${why} AND revert to ${PRIOR_SERVER_IMAGE} did not stabilise — manual intervention required"
    fi
  else
    # No prior image to revert to (fresh install) or same image — nothing
    # safe to roll back to. Report failure and leave state for an operator.
    write_status "failed" "server image ${KUSO_SERVER_IMAGE} ${why} and no prior image to revert to — manual intervention required"
  fi
  exit 1
}

# Wrap rollout status so a timeout/crashloop on the new image triggers a
# revert instead of aborting under `set -e` with the deploy stuck. The
# explicit `if !` keeps this robust to set -euo pipefail.
if ! kubectl rollout status -n "$NS" deploy/kuso-server --timeout=180s; then
  echo "==> kuso-server rollout to ${KUSO_SERVER_IMAGE} did NOT become ready"
  revert_server "failed to roll out"
fi

# Leader gate. The new pod comes up as a non-leader (the old pod still
# holds the leases), and a non-leader's /readyz skips the build-poller
# check, so `rollout status` passes even when the leader-only loops are
# broken on the new version. Wait for the new pod to take the lease and
# report its poller. Only an explicit "stalled" reverts: an unreachable
# endpoint or a slow lease handover is a warning, never a rollback of a
# working server.
LEADER_GATE_SECONDS="${KUSO_LEADER_GATE_SECONDS:-150}"
leader_gate_deadline=$(( $(date +%s) + LEADER_GATE_SECONDS ))
poller_state=""
while [ "$(date +%s)" -lt "$leader_gate_deadline" ]; do
  readyz=$(curl -s --connect-timeout 5 --max-time 10 "http://kuso-server.${NS}.svc:80/readyz" 2>/dev/null || true)
  observed=$(printf '%s' "$readyz" | jq -r '.checks.buildPoller // empty' 2>/dev/null || true)
  if [ -n "$observed" ]; then
    poller_state="$observed"
  fi
  if [ "$poller_state" = "ok" ]; then
    echo "==> new kuso-server leads and its build poller is beating"
    break
  fi
  sleep 5
done
case "$poller_state" in
  ok) ;;
  stalled)
    echo "==> new kuso-server took the leader lease but its build poller is stalled"
    revert_server "took the leader lease with a stalled build poller"
    ;;
  *)
    echo "==> WARNING: could not confirm the new kuso-server's build poller within ${LEADER_GATE_SECONDS}s; continuing"
    ;;
esac

# The activator runs the SAME kuso-server-go image in `--activator` mode
# (deploy/kuso-activator.yaml). Roll it in lockstep with the server so
# activator-side changes (scale-to-zero / stopped-page / stopped-env
# routing) actually reach self-updating clusters — otherwise it stays
# pinned to the OLD image forever. Guard on presence: older installs
# predate the activator, so a missing deployment must not fail the Job.
if kubectl get -n "$NS" deploy/kuso-activator >/dev/null 2>&1; then
  write_status "rolling-activator" "${KUSO_SERVER_IMAGE}"
  # Same snapshot-and-revert as the server: the activator runs the image
  # with a stricter securityContext, so it can fail where the server didn't.
  PRIOR_ACTIVATOR_IMAGE=$(kubectl get -n "$NS" deploy/kuso-activator \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="activator")].image}' 2>/dev/null || true)
  kubectl set image -n "$NS" deploy/kuso-activator "activator=${KUSO_SERVER_IMAGE}" >/dev/null
  if ! kubectl rollout status -n "$NS" deploy/kuso-activator --timeout=180s; then
    echo "==> kuso-activator rollout to ${KUSO_SERVER_IMAGE} did NOT become ready"
    if [ -n "$PRIOR_ACTIVATOR_IMAGE" ] && [ "$PRIOR_ACTIVATOR_IMAGE" != "${KUSO_SERVER_IMAGE}" ]; then
      echo "==> reverting kuso-activator to ${PRIOR_ACTIVATOR_IMAGE}"
      kubectl set image -n "$NS" deploy/kuso-activator "activator=${PRIOR_ACTIVATOR_IMAGE}" >/dev/null || true
      if kubectl rollout status -n "$NS" deploy/kuso-activator --timeout=180s; then
        write_status "failed" "activator failed on ${KUSO_SERVER_IMAGE}; activator reverted to ${PRIOR_ACTIVATOR_IMAGE}. Server is already on ${KUSO_TARGET_VERSION}; operator not rolled"
      else
        write_status "failed" "activator failed on ${KUSO_SERVER_IMAGE} AND revert to ${PRIOR_ACTIVATOR_IMAGE} did not stabilise — manual intervention required. Server is on ${KUSO_TARGET_VERSION}"
      fi
    else
      write_status "failed" "activator failed on ${KUSO_SERVER_IMAGE} and there is no different prior image to revert to — manual intervention required"
    fi
    exit 1
  fi
else
  write_status "rolling-activator" "no kuso-activator deployment — skipping (pre-activator install)"
  echo "==> no kuso-activator deployment found; skipping activator roll"
fi

# Guard against a manifest with no operator image. Under `set -e` an empty
# KUSO_OPERATOR_IMAGE would make `set image …manager=` fail and abort the
# whole Job AFTER the server already rolled — leaving a half-applied upgrade.
# The server-side fetchVersion now falls back to a version-tagged default so
# this should never be empty, but skip-with-status here is the safety net.
if [ -z "${KUSO_OPERATOR_IMAGE:-}" ]; then
  write_status "rolling-operator" "no operator image in manifest — skipping operator roll"
  echo "==> WARNING: no operator image in manifest; server rolled, operator left unchanged"
else
  # Operator health gate + rollback. This used to be a goroutine in the
  # kuso-server process, but this Job replaces that process (above)
  # before touching the operator, so the watchdog died with the old pod
  # on every real upgrade. The Job outlives both rolls, so the gate
  # lives here, mirroring the server revert.
  write_status "rolling-operator" "${KUSO_OPERATOR_IMAGE}"
  OP_NS="${KUSO_OPERATOR_NS:-kuso-operator-system}"
  OP_TIMEOUT="${KUSO_OPERATOR_ROLLOUT_TIMEOUT:-300s}"
  for d in kuso-operator-controller-manager kuso-operator; do
    if ! kubectl get -n "$OP_NS" "deploy/$d" >/dev/null 2>&1; then
      continue
    fi
    PRIOR_OPERATOR_IMAGE=$(kubectl get -n "$OP_NS" "deploy/$d" \
      -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}' 2>/dev/null || true)
    OP_CONTAINER=manager
    if [ -z "$PRIOR_OPERATOR_IMAGE" ]; then
      OP_CONTAINER='*'
      PRIOR_OPERATOR_IMAGE=$(kubectl get -n "$OP_NS" "deploy/$d" \
        -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)
    fi
    kubectl set image -n "$OP_NS" "deploy/$d" "${OP_CONTAINER}=${KUSO_OPERATOR_IMAGE}" >/dev/null
    if ! kubectl rollout status -n "$OP_NS" "deploy/$d" --timeout="$OP_TIMEOUT"; then
      echo "==> operator rollout to ${KUSO_OPERATOR_IMAGE} did NOT become ready within ${OP_TIMEOUT}"
      if [ -n "$PRIOR_OPERATOR_IMAGE" ] && [ "$PRIOR_OPERATOR_IMAGE" != "${KUSO_OPERATOR_IMAGE}" ]; then
        echo "==> reverting operator to ${PRIOR_OPERATOR_IMAGE}"
        kubectl set image -n "$OP_NS" "deploy/$d" "${OP_CONTAINER}=${PRIOR_OPERATOR_IMAGE}" >/dev/null || true
        if kubectl rollout status -n "$OP_NS" "deploy/$d" --timeout="$OP_TIMEOUT"; then
          write_status "rolled-back" "operator ${KUSO_OPERATOR_IMAGE} did not become Ready within ${OP_TIMEOUT}; operator reverted to ${PRIOR_OPERATOR_IMAGE}. Server + CRDs are at ${KUSO_TARGET_VERSION}"
        else
          write_status "rollback-failed" "operator ${KUSO_OPERATOR_IMAGE} did not become Ready AND revert to ${PRIOR_OPERATOR_IMAGE} did not stabilise — manual intervention required"
        fi
      else
        write_status "failed" "operator ${KUSO_OPERATOR_IMAGE} did not become Ready within ${OP_TIMEOUT} and there is no different prior image to revert to — manual intervention required"
      fi
      exit 1
    fi
    break
  done
fi

if [ -n "$UNVERIFIED" ]; then
  MANIFESTS_WARNING="${MANIFESTS_WARNING:+${MANIFESTS_WARNING}; }applied without a release digest check: ${UNVERIFIED}"
fi

if [ -n "$MANIFESTS_WARNING" ]; then
  write_status "done" "upgraded to ${KUSO_TARGET_VERSION} with warnings: ${MANIFESTS_WARNING}"
  echo "==> upgrade to ${KUSO_TARGET_VERSION} complete WITH WARNINGS: ${MANIFESTS_WARNING}"
else
  write_status "done" "upgraded to ${KUSO_TARGET_VERSION}"
  echo "==> upgrade to ${KUSO_TARGET_VERSION} complete"
fi
