#!/usr/bin/env bash
# kuso-updater entrypoint. Runs inside the Job that the self-updater
# kicks off (server-go/internal/updater/applyJob). Reads env vars
# set by the server, walks the upgrade in clear phases, and writes
# JSON status to the ConfigMap so the UI can render progress without
# tailing pod logs.

set -euo pipefail

NS="${KUSO_NAMESPACE:-kuso}"
CM="${KUSO_STATUS_CONFIGMAP:-kuso-update-status}"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

write_status() {
  local phase="$1"
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

STARTED_AT="$(now)"
write_status "applying-crds" "downloading ${KUSO_CRDS_URL}"

TMP_CRDS=$(mktemp)
curl -fsSL "$KUSO_CRDS_URL" -o "$TMP_CRDS"

# Server-side dry-run BEFORE the real apply. A CRD schema that
# retroactively adds a required field, a tightened pattern, or a CEL
# validation rule can make already-stored CRs unwritable — the apiserver
# rejects them at admission. If that lands blind, every subsequent
# reconcile/write against existing resources fails and there's no clean
# recovery path. --dry-run=server runs the real admission/validation
# against the live apiserver without persisting, so we catch the
# rejection here and abort with a clear status instead of bricking.
if ! dryrun_out=$(kubectl apply --dry-run=server -f "$TMP_CRDS" 2>&1); then
  echo "==> CRD server-side dry-run FAILED — refusing to apply:"
  echo "$dryrun_out" | sed 's/^/    /'
  write_status "failed" "CRD validation (dry-run) failed — not applied: $(echo "$dryrun_out" | tr '\n' ' ' | cut -c1-300)"
  exit 1
fi
kubectl apply -f "$TMP_CRDS" >/dev/null

# Apply the release's non-workload platform manifests (RBAC,
# ServiceAccounts, PriorityClasses, NetworkPolicies, PDBs) before
# rolling images, so new server/operator code picks up RBAC/policy
# changes instead of drifting. Absent on releases that predate the
# bundle — skip cleanly so old release.json payloads still upgrade.
#
# BEST-EFFORT BY DESIGN: this Job runs as the kuso-server
# ServiceAccount, which deliberately CANNOT mutate ClusterRoles /
# RoleBindings / NetworkPolicies / PriorityClasses (granting the
# control plane rbac-escalate power would be a worse hole than the
# drift it fixes). So a forbidden/partial apply must NOT abort the
# upgrade — we log it to status and roll images anyway. An operator
# whose release genuinely needs new RBAC applies the bundle manually
# (or re-runs install.sh), exactly as before this bundle existed.
# The alternative — hard-failing here — would brick self-update on
# every cluster the moment a release touched RBAC, with no recovery
# path, since the fix ships inside the very Job that's failing.
if [ -n "${KUSO_MANIFESTS_URL:-}" ]; then
  write_status "applying-manifests" "downloading ${KUSO_MANIFESTS_URL}"
  TMP_MANIFESTS=$(mktemp)
  if curl -fsSL "$KUSO_MANIFESTS_URL" -o "$TMP_MANIFESTS"; then
    # server-side apply, non-fatal: capture output, warn on failure.
    if apply_out=$(kubectl apply -f "$TMP_MANIFESTS" 2>&1); then
      echo "==> applied upgrade manifests"
    else
      echo "==> WARNING: upgrade-manifests apply incomplete (continuing with image roll):"
      echo "$apply_out" | sed 's/^/    /'
      write_status "applying-manifests" "partial — some platform manifests need manual apply (see updater logs); continuing"
    fi
  else
    echo "==> WARNING: could not download upgrade-manifests bundle; continuing with image roll"
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
# Wrap rollout status so a timeout/crashloop on the new image triggers a
# revert instead of aborting under `set -e` with the deploy stuck. The
# explicit `if !` keeps this robust to set -euo pipefail.
if ! kubectl rollout status -n "$NS" deploy/kuso-server --timeout=180s; then
  echo "==> kuso-server rollout to ${KUSO_SERVER_IMAGE} did NOT become ready"
  if [ -n "$PRIOR_SERVER_IMAGE" ] && [ "$PRIOR_SERVER_IMAGE" != "${KUSO_SERVER_IMAGE}" ]; then
    echo "==> reverting kuso-server to ${PRIOR_SERVER_IMAGE}"
    write_status "failed" "server image ${KUSO_SERVER_IMAGE} failed to roll out; reverting to ${PRIOR_SERVER_IMAGE}"
    kubectl set image -n "$NS" deploy/kuso-server "server=${PRIOR_SERVER_IMAGE}" >/dev/null || true
    if kubectl rollout status -n "$NS" deploy/kuso-server --timeout=180s; then
      write_status "failed" "server image ${KUSO_SERVER_IMAGE} failed; reverted to working ${PRIOR_SERVER_IMAGE}"
    else
      write_status "failed" "server image ${KUSO_SERVER_IMAGE} failed AND revert to ${PRIOR_SERVER_IMAGE} did not stabilise — manual intervention required"
    fi
  else
    # No prior image to revert to (fresh install) or same image — nothing
    # safe to roll back to. Report failure and leave state for an operator.
    write_status "failed" "server image ${KUSO_SERVER_IMAGE} failed to roll out and no prior image to revert to — manual intervention required"
  fi
  exit 1
fi

# The activator runs the SAME kuso-server-go image in `--activator` mode
# (deploy/kuso-activator.yaml). Roll it in lockstep with the server so
# activator-side changes (scale-to-zero / stopped-page / stopped-env
# routing) actually reach self-updating clusters — otherwise it stays
# pinned to the OLD image forever. Guard on presence: older installs
# predate the activator, so a missing deployment must not fail the Job.
if kubectl get -n "$NS" deploy/kuso-activator >/dev/null 2>&1; then
  write_status "rolling-activator" "${KUSO_SERVER_IMAGE}"
  kubectl set image -n "$NS" deploy/kuso-activator "activator=${KUSO_SERVER_IMAGE}" >/dev/null
  kubectl rollout status -n "$NS" deploy/kuso-activator --timeout=180s
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
  write_status "rolling-operator" "${KUSO_OPERATOR_IMAGE}"
  OP_NS="${KUSO_OPERATOR_NS:-kuso-operator-system}"
  for d in kuso-operator-controller-manager kuso-operator; do
    if kubectl get -n "$OP_NS" "deploy/$d" >/dev/null 2>&1; then
      kubectl set image -n "$OP_NS" "deploy/$d" "manager=${KUSO_OPERATOR_IMAGE}" >/dev/null \
        || kubectl set image -n "$OP_NS" "deploy/$d" "*=${KUSO_OPERATOR_IMAGE}" >/dev/null
      kubectl rollout status -n "$OP_NS" "deploy/$d" --timeout=180s
      break
    fi
  done
fi

write_status "done" "upgraded to ${KUSO_TARGET_VERSION}"
echo "==> upgrade to ${KUSO_TARGET_VERSION} complete"
