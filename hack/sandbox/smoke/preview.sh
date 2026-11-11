# shellcheck shell=bash
# preview: PR previews from signed webhook deliveries (hack/sandbox/gh-event).
# A project on the public demo API repo with a postgres addon, a staging
# env group, and per-env markers. PR #1 opened → a preview env built from
# the PR head that carries staging's config, not production's. PR closed
# → the preview env and its per-PR database are reclaimed.

PV_PROJECT="pv"
PV_REPO="ivo9999/kuso-demo-todo-api"
PV_PR=1

pv_envs() {
  node_kubectl get kusoenvironments -A -o json \
    | jq -c --arg p "$PV_PROJECT" '[.items[] | select(.spec.project==$p)]'
}
pv_preview() { pv_envs | jq -r --arg n "pr-${PV_PR}" '.[] | select(.metadata.name | endswith("-" + $n)) | .metadata.name' | head -1; }
pv_has_preview() { [[ -n "$(pv_preview)" ]]; }
pv_no_preview() { [[ -z "$(pv_preview)" ]]; }
pv_ready() { [[ "$(node_kubectl -n "$PV_NS" get deploy "$1" -o jsonpath='{.status.readyReplicas}' 2>/dev/null)" -ge 1 ]]; }

# pv_env_value <env CR> <KEY>: the value the env's running pod sees.
pv_env_value() {
  local pod
  pod="$(node_kubectl -n "$PV_NS" get pods -l "app.kubernetes.io/instance=$1" \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')"
  node_kubectl -n "$PV_NS" exec "$pod" -c app -- printenv "$2"
}
pv_pr_addons() {
  node_kubectl -n "$PV_NS" get kusoaddons -o json \
    | jq -r --arg p "$PV_PROJECT" --arg n "pr-${PV_PR}" '.items[] | select(.metadata.name | startswith($p + "-")) | select(.metadata.name | contains($n)) | .metadata.name'
}
pv_no_pr_addons() { [[ -z "$(pv_pr_addons)" ]]; }

preview_main() {
  step "project ${PV_PROJECT}: api + postgres, staging env, previews on"
  fresh_project "$PV_PROJECT" --repo "https://github.com/${PV_REPO}" --branch main
  PV_NS="$(project_ns "$PV_PROJECT")"
  "$K" project addon add "$PV_PROJECT" db --kind postgres --version 16 --size small >/dev/null
  "$K" service add "$PV_PROJECT" api --runtime dockerfile --port 8080 --no-build >/dev/null
  "$K" env set "$PV_PROJECT" api 'DATABASE_URL=${{ db.DATABASE_URL }}' 'MIGRATE_ON_BOOT=1' >/dev/null
  "$K" environment add "$PV_PROJECT" api staging --branch staging >/dev/null
  "$K" env set "$PV_PROJECT" api --env production SMOKE_MARKER=production >/dev/null
  "$K" env set "$PV_PROJECT" api --env staging SMOKE_MARKER=staging >/dev/null
  "$K" project update "$PV_PROJECT" --previews=on >/dev/null
  require "production build succeeded" "$K" build trigger "$PV_PROJECT" api --follow

  step "PR #${PV_PR} (re)opened"
  # The server remembers closed PRs in memory and skips a later "opened" for
  # them; "reopened" clears that, so the group can run more than once per
  # server lifetime. Both actions take the same deploy path.
  require "webhook accepted" "${SANDBOX_DIR}/gh-event" pr reopened "$PV_REPO" "$PV_PR"
  if eventually 120 pv_has_preview; then
    pass "preview env $(pv_preview) created"
  else
    node_kubectl -n kuso logs deploy/kuso-server --since=5m 2>/dev/null | grep -iE 'github|preview' | tail -20 >&2 || true
    fail "preview env created"; exit 1
  fi
  local pv; pv="$(pv_preview)"
  if eventually 600 pv_ready "$pv"; then
    pass "preview build deployed and the pod is ready"
  else
    "$K" build list "$PV_PROJECT" api >&2 || true
    fail "preview build deployed and the pod is ready"; exit 1
  fi
  local marker db_prod db_pv
  marker="$(pv_env_value "$pv" SMOKE_MARKER || true)"
  check "preview carries staging's config (SMOKE_MARKER=${marker:-unset})" test "$marker" == "staging"
  db_prod="$(pv_env_value "$(pv_envs | jq -r '.[] | select(.metadata.name | endswith("-production")) | .metadata.name')" DATABASE_URL || true)"
  db_pv="$(pv_env_value "$pv" DATABASE_URL || true)"
  check "preview DATABASE_URL is not production's" test -n "$db_pv" -a "$db_pv" != "$db_prod"
  echo "  preview addons: $(pv_pr_addons | xargs)"
  local host
  host="$(node_kubectl -n "$PV_NS" get ingress "$pv" -o jsonpath='{.spec.rules[0].host}' 2>/dev/null || true)"
  check "preview answers through traefik at ${host}" eventually 120 http_200 "$host" /healthz

  step "PR #${PV_PR} closed"
  require "webhook accepted" "${SANDBOX_DIR}/gh-event" pr closed "$PV_REPO" "$PV_PR"
  check "preview env reclaimed" eventually 180 pv_no_preview
  check "per-PR database reclaimed" eventually 180 pv_no_pr_addons
}

preview_cleanup() { delete_project "$PV_PROJECT"; }
