# shellcheck shell=bash
# rbac: a project editor can't choose code that runs with a service's
# secrets through an env var name (NODE_OPTIONS, NIXPACKS_*_CMD) or the
# service command, ordinary env stays editor-writable, and a typed
# "<x>-<project>" instance host needs a real env group (it is another
# project's apex otherwise). One image service, no builds.

RB_P="rbac-p"
RB_USER="rbac-editor"
RB_PASS="rbac-editor-Pass-4821"
RB_IMAGE_REPO="nginxinc/nginx-unprivileged"
RB_IMAGE_TAG="1.29-alpine"

rb_token() {
  curl -fsS -X POST "${SANDBOX_API_URL}/api/auth/login" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}')" | jq -r '.access_token // empty'
}

# rb_code <token> <method> <path> [json body]: the HTTP status.
rb_code() {
  local args=(-s -o /dev/null -w '%{http_code}' -X "$2" -H "Authorization: Bearer $1" "${SANDBOX_API_URL}$3")
  [[ $# -ge 4 ]] && args+=(-H 'Content-Type: application/json' -d "$4")
  curl "${args[@]}"
}
rb_is() { [[ "$(rb_code "${@:2}")" == "$1" ]]; }

rb_user_id() {
  "$K" api GET "users/username/${RB_USER}" 2>/dev/null | jq -r '.id // empty' 2>/dev/null
}

rbac_main() {
  step "project ${RB_P} (web) and editor ${RB_USER}"
  fresh_project "$RB_P" --repo https://github.com/ivo9999/kuso-demo-todo-api --branch main
  "$K" service add "$RB_P" web --runtime image --image-repo "$RB_IMAGE_REPO" --image-tag "$RB_IMAGE_TAG" --port 8080 >/dev/null
  local admin uid editor svc="/api/projects/${RB_P}/services/web"
  admin="$(rb_token admin "$SANDBOX_ADMIN_PASSWORD")"
  require "admin token" test -n "$admin"
  uid="$(rb_user_id)"
  if [[ -z "$uid" ]]; then
    rb_code "$admin" POST /api/users "$(jq -nc --arg u "$RB_USER" --arg p "$RB_PASS" '{username:$u,password:$p,email:($u+"@example.com")}')" >/dev/null
    uid="$(rb_user_id)"
  fi
  require "editor user exists" test -n "$uid"
  rb_code "$admin" POST "/api/projects/${RB_P}/grants" "$(jq -nc --arg id "$uid" '{userId:$id,role:"editor"}')" >/dev/null
  editor="$(rb_token "$RB_USER" "$RB_PASS")"
  require "editor token" test -n "$editor"

  step "code-loading env names and the command need project admin"
  check "editor sets NODE_OPTIONS: 403" rb_is 403 "$editor" PUT "${svc}/env-vars/NODE_OPTIONS" '{"value":"--import=data:text/javascript,1","auto":true}'
  check "editor sets NIXPACKS_START_CMD as a secret: 403" rb_is 403 "$editor" PUT "${svc}/env-vars/NIXPACKS_START_CMD" '{"secretValue":"env"}'
  check "editor stages LD_PRELOAD: 403" rb_is 403 "$editor" POST "${svc}/changes" '{"envSet":[{"name":"LD_PRELOAD","value":"/x.so"}]}'
  check "editor changes command: 403" rb_is 403 "$editor" PATCH "$svc" '{"command":["sh","-c","printenv"]}'
  check "editor sets a plain var: 200" rb_is 200 "$editor" PUT "${svc}/env-vars/API_URL" '{"value":"https://example.com","auto":true}'
  check "admin sets NODE_OPTIONS: 200" rb_is 200 "$admin" PUT "${svc}/env-vars/NODE_OPTIONS" '{"value":"--max-old-space-size=256","auto":true}'
  check "exec env name list is served" bash -c "curl -fsS -H 'Authorization: Bearer ${editor}' '${SANDBOX_API_URL}/api/config/exec-env-names' | jq -e '.names | index(\"NODE_OPTIONS\")'"

  step "instance-domain hosts"
  check "editor claims another project's future apex: 409" rb_is 409 "$editor" POST "${svc}/domains" "{\"host\":\"victim-${RB_P}.${SANDBOX_DOMAIN}\"}"
  check "editor claims a host in the project's subtree: 200" rb_is 200 "$editor" POST "${svc}/domains" "{\"host\":\"extra.${RB_P}.${SANDBOX_DOMAIN}\"}"
}

rbac_cleanup() {
  delete_project "$RB_P"
  local uid
  uid="$(rb_user_id)"
  [[ -n "$uid" ]] && "$K" api DELETE "users/id/${uid}" >/dev/null 2>&1
  return 0
}
