# shellcheck shell=bash
# registry: the in-cluster registry refuses anonymous pull and push when
# auth is on, a build pushes to it, and the env records the pushed digest
# and runs the image by that digest.

RG_PROJECT="rg"
RG_PROBE="registry-probe"

# rg_curl <args...>: curl from a throwaway pod (the registry is only
# reachable in-cluster). Prints the HTTP status code.
rg_curl() {
  node_kubectl -n kuso-sandbox run "$RG_PROBE-$RANDOM" --rm -i --quiet --restart=Never \
    --image=curlimages/curl:8.17.0 --command -- \
    curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$@" 2>/dev/null | tr -d '\r' | tail -c 3
}

rg_auth() {
  local u p
  u="$(node_kubectl -n kuso get secret kuso-registry-auth -o jsonpath='{.data.username}' | base64 -d)"
  p="$(node_kubectl -n kuso get secret kuso-registry-auth -o jsonpath='{.data.password}' | base64 -d)"
  echo "${u}:${p}"
}

rg_env_json() {
  node_kubectl -n "$RG_NS" get kusoenvironments -o json \
    | jq --arg p "$RG_PROJECT" '[.items[] | select(.spec.project==$p)][0]'
}

rg_auth_not_flagged() {
  local rep
  rep="$("$K" health -o json)" || return 1
  ! jq -e '.issues[] | select(.kind=="registry_auth_disabled")' <<<"$rep" >/dev/null
}

rg_has_digest() { rg_env_json | jq -e '.spec.image.digest // "" | startswith("sha256:")' >/dev/null; }

registry_main() {
  local reg="http://kuso-registry.kuso.svc.cluster.local:5000" code auth
  auth="$(rg_auth)"

  step "anonymous access is refused"
  code="$(rg_curl "${reg}/v2/_catalog")"
  check "anonymous catalog read refused (HTTP ${code})" test "$code" == "401"
  code="$(rg_curl -X POST "${reg}/v2/smoke/anon/blobs/uploads/")"
  check "anonymous blob upload refused (HTTP ${code})" test "$code" == "401"
  code="$(rg_curl -u "$auth" "${reg}/v2/_catalog")"
  check "authenticated catalog read allowed (HTTP ${code})" test "$code" == "200"

  step "a build pushes and the env pins its digest"
  fresh_project "$RG_PROJECT" --repo https://github.com/ivo9999/kuso-demo-todo-api --branch main
  RG_NS="$(project_ns "$RG_PROJECT")"
  "$K" service add "$RG_PROJECT" api --runtime dockerfile --port 8080 --no-build >/dev/null
  if "$K" build trigger "$RG_PROJECT" api --follow >/dev/null; then
    pass "dockerfile build succeeded"
  else
    "$K" build why "$RG_PROJECT" api >&2 || true
    fail "dockerfile build succeeded"; exit 1
  fi

  local env repo tag digest deployed
  require "env records an image digest" eventually 120 rg_has_digest
  env="$(rg_env_json)"
  repo="$(jq -r '.spec.image.repository' <<<"$env")"
  tag="$(jq -r '.spec.image.tag' <<<"$env")"
  digest="$(jq -r '.spec.image.digest' <<<"$env")"
  echo "  env image: ${repo}:${tag}@${digest}"
  check "image lives in the in-cluster registry" grep -q 'kuso-registry' <<<"$repo"
  local path="${repo#*/}"
  code="$(rg_curl -u "$auth" -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' "${reg}/v2/${path}/manifests/${digest}")"
  check "the recorded digest exists in the registry (HTTP ${code})" test "$code" == "200"
  code="$(rg_curl "${reg}/v2/${path}/manifests/${tag}")"
  check "anonymous pull of the built image refused (HTTP ${code})" test "$code" == "401"
  deployed="$(node_kubectl -n "$RG_NS" get deploy "$(jq -r '.metadata.name' <<<"$env")" -o jsonpath='{.spec.template.spec.containers[0].image}')"
  check "the Deployment runs the image by that digest (${deployed##*@})" grep -qF "@${digest}" <<<"$deployed"

  step "registry rebuild plans the deployed commit"
  local plan
  plan="$("$K" registry rebuild "$RG_PROJECT" --dry-run 2>&1 || true)"
  echo "  ${plan}"
  check "rebuild plan names the production env and a full commit" \
    grep -qE "would rebuild ${RG_PROJECT}/api env=production from commit [0-9a-f]{40}" <<<"$plan"
  check "kuso health reports registry auth as on" \
    bash -c "! \"$K\" health -o json | jq -e '.issues[] | select(.kind==\"registry_auth_disabled\")' >/dev/null"
}

registry_cleanup() {
  delete_project "$RG_PROJECT"
}
