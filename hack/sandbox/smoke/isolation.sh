# shellcheck shell=bash
# isolation: NetworkPolicy between projects and between a project's
# staging and production envs. Two image services (no builds): iso-a has
# web + peer, plus a staging env group; iso-b has web.

ISO_A="iso-a"
ISO_B="iso-b"
# Unprivileged nginx: listens on 8080, ships busybox wget for the probes.
ISO_IMAGE_REPO="nginxinc/nginx-unprivileged"
ISO_IMAGE_TAG="1.29-alpine"

# iso_env <project> <service> <env>: the env CR name (also its Service name).
iso_env() {
  node_kubectl get kusoenvironments -A -o json \
    | jq -r --arg p "$1" --arg s "$1-$2" --arg e "$3" '.items[]
        | select(.spec.project==$p and .spec.service==$s)
        | select((.metadata.labels["kuso.sislelabs.com/env"] // .spec.kind // "") == $e
                 or (.metadata.name | endswith("-" + $e)))
        | .metadata.name' | head -1
}
iso_ns() {
  node_kubectl get kusoenvironments -A -o json \
    | jq -r --arg n "$1" '.items[] | select(.metadata.name==$n) | .metadata.namespace' | head -1
}

# iso_reach <from env CR> <to env CR>: an HTTP request from a pod of the
# first env to the second env's Service succeeds.
iso_reach() {
  local from="$1" to="$2" ns pod tns
  ns="$(iso_ns "$from")"; tns="$(iso_ns "$to")"
  pod="$(node_kubectl -n "$ns" get pods -l "app.kubernetes.io/instance=${from}" \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "$pod" ]] || return 2
  node_kubectl -n "$ns" exec "$pod" -- wget -q -T 3 -O /dev/null "http://${to}.${tns}.svc.cluster.local/"
}

iso_ready() {
  local ns
  ns="$(iso_ns "$1")"
  [[ -n "$ns" ]] && [[ "$(node_kubectl -n "$ns" get deploy "$1" -o jsonpath='{.status.readyReplicas}' 2>/dev/null)" -ge 1 ]]
}

isolation_main() {
  step "projects ${ISO_A} (web, peer, staging env) and ${ISO_B} (web)"
  local p s
  for p in "$ISO_A" "$ISO_B"; do
    fresh_project "$p" --repo https://github.com/ivo9999/kuso-demo-todo-api --branch main
  done
  for s in web peer; do
    "$K" service add "$ISO_A" "$s" --runtime image --image-repo "$ISO_IMAGE_REPO" --image-tag "$ISO_IMAGE_TAG" --port 8080 >/dev/null
  done
  "$K" service add "$ISO_B" web --runtime image --image-repo "$ISO_IMAGE_REPO" --image-tag "$ISO_IMAGE_TAG" --port 8080 >/dev/null
  "$K" environment add "$ISO_A" web staging --branch main >/dev/null

  local a_web a_peer a_stg b_web
  a_web="$(iso_env "$ISO_A" web production)"
  a_peer="$(iso_env "$ISO_A" peer production)"
  b_web="$(iso_env "$ISO_B" web production)"
  eventually 120 test -n "$(iso_env "$ISO_A" web staging)" || true
  a_stg="$(iso_env "$ISO_A" web staging)"
  echo "  envs: a/web=${a_web} a/peer=${a_peer} a/web staging=${a_stg} b/web=${b_web}"
  require "all four envs resolved" test -n "$a_web" -a -n "$a_peer" -a -n "$a_stg" -a -n "$b_web"
  local e
  for e in "$a_web" "$a_peer" "$a_stg" "$b_web"; do
    require "${e} is ready" eventually 240 iso_ready "$e"
  done

  step "reachability (kube-router programs policy a few seconds after a pod starts)"
  # Positive checks retry through the policy lag; negative ones must hold
  # for the whole window, after the positive ones have passed.
  check "same project, same env: ${a_web} -> ${a_peer}" eventually 90 iso_reach "$a_web" "$a_peer"
  check "cross project: ${b_web} -> ${a_web} refused" never 20 iso_reach "$b_web" "$a_web"
  check "cross project: ${a_web} -> ${b_web} refused" never 20 iso_reach "$a_web" "$b_web"
  check "staging -> production: ${a_stg} -> ${a_peer} refused" never 20 iso_reach "$a_stg" "$a_peer"
  # Env isolation is one-way (internal/envisolation): it keeps non-production
  # pods off production. Reported, not asserted.
  if iso_reach "$a_peer" "$a_stg" >/dev/null 2>&1; then
    echo "  info: production -> staging (${a_peer} -> ${a_stg}) is allowed (by design)"
  else
    echo "  info: production -> staging (${a_peer} -> ${a_stg}) is refused"
  fi
  check "through traefik: web.${ISO_A} answers" eventually 60 http_200 "web.${ISO_A}.${SANDBOX_DOMAIN}" /
}

isolation_cleanup() {
  delete_project "$ISO_A"
  delete_project "$ISO_B"
}
