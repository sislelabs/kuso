# shellcheck shell=bash
# sleep: scale-to-zero end to end. An image service with sleep on and the
# shortest idle window (1 minute) goes to 0 replicas once Prometheus has
# seen no traffic; the next request through traefik wakes it via the
# activator. Takes 3-5 minutes: the scaledown watcher ticks every minute
# and needs one tick to route the env through the activator first.

SL_PROJECT="sl"

sl_env() {
  node_kubectl -n "$SL_NS" get kusoenvironments -o json \
    | jq -r --arg p "$SL_PROJECT" '.items[] | select(.spec.project==$p) | .metadata.name' | head -1
}
sl_replicas() { node_kubectl -n "$SL_NS" get deploy "$1" -o jsonpath='{.spec.replicas}' 2>/dev/null; }
# Asleep = scaled to 0 AND the old pod is gone; while it terminates it can
# still answer, and the request would not exercise the wake path.
sl_asleep() {
  [[ "$(sl_replicas "$1")" == "0" ]] && [[ -z "$(node_kubectl -n "$SL_NS" get pods -l "app.kubernetes.io/instance=$1" -o name 2>/dev/null)" ]]
}
sl_ready() { [[ "$(node_kubectl -n "$SL_NS" get deploy "$1" -o jsonpath='{.status.readyReplicas}' 2>/dev/null)" -ge 1 ]]; }

sleep_main() {
  local host="web.${SL_PROJECT}.${SANDBOX_DOMAIN}" env t0 code
  step "project ${SL_PROJECT}: image service with sleep on --after 1m"
  fresh_project "$SL_PROJECT" --repo https://github.com/ivo9999/kuso-demo-todo-api --branch main
  SL_NS="$(project_ns "$SL_PROJECT")"
  "$K" service add "$SL_PROJECT" web --runtime image --image-repo nginxinc/nginx-unprivileged \
    --image-tag 1.29-alpine --port 8080 >/dev/null
  "$K" project service sleep "$SL_PROJECT" web on --after 1m >/dev/null
  env="$(sl_env)"
  require "env ${env} is ready" eventually 240 sl_ready "$env"
  require "web answers before sleeping" eventually 60 http_200 "$host" /

  step "wait for the idle env to scale to zero (up to 8 minutes)"
  t0=$SECONDS
  if eventually 480 sl_asleep "$env"; then
    pass "env scaled to 0 after $((SECONDS - t0))s idle"
  else
    node_kubectl -n kuso logs deploy/kuso-server --since=10m 2>/dev/null | grep -i scaledown | tail -10 >&2 || true
    fail "env scaled to 0 within 8 minutes"; exit 1
  fi

  step "a request wakes it"
  local out secs
  out="$(CURL_TIMEOUT=120 svc_curl "$host" / -o /dev/null -w '%{http_code} %{time_total}' 2>/dev/null || true)"
  code="${out%% *}"; secs="${out#* }"
  check "first request after sleep returns 200 (got ${code} after ${secs}s)" test "$code" == "200"
  check "deployment is back to 1 replica" eventually 30 sl_ready "$env"
  echo "  wake time: ${secs}s"
}

sleep_cleanup() { delete_project "$SL_PROJECT"; }
