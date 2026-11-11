# shellcheck shell=bash
# core: the deploy lifecycle. project → dockerfile + nixpacks builds →
# live HTTP → postgres addon via an env ref → rebuild → rollback → delete.

CORE_PROJECT="smoke"
CORE_REPO="https://github.com/ivo9999/kuso-demo-todo-api"
CORE_SERVICES=(docker nix)

core_host() { echo "$1.${CORE_PROJECT}.${SANDBOX_DOMAIN}"; }

core_live_build() {
  "$K" build list "$CORE_PROJECT" "$1" -o json \
    | jq -r '[.[] | select((.liveEnvs // []) | index("production"))][0].id // empty'
}

core_dump() {
  "$K" service pods "$CORE_PROJECT" "$1" >&2 || true
  "$K" logs "$CORE_PROJECT" "$1" 2>&1 | tail -30 >&2 || true
}

core_main() {
  step "create project ${CORE_PROJECT} with a dockerfile and a nixpacks service"
  fresh_project "$CORE_PROJECT" --repo "$CORE_REPO" --branch main
  "$K" service add "$CORE_PROJECT" docker --runtime dockerfile --port 8080 >/dev/null
  "$K" service add "$CORE_PROJECT" nix --runtime nixpacks --port 8080 >/dev/null

  step "postgres addon referenced from both services"
  "$K" project addon add "$CORE_PROJECT" db --kind postgres --version 16 --size small >/dev/null
  local svc
  for svc in "${CORE_SERVICES[@]}"; do
    "$K" env set "$CORE_PROJECT" "$svc" 'DATABASE_URL=${{ db.DATABASE_URL }}' 'MIGRATE_ON_BOOT=1' >/dev/null
  done

  step "build both services"
  for svc in "${CORE_SERVICES[@]}"; do
    if "$K" build trigger "$CORE_PROJECT" "$svc" --follow; then
      pass "${svc} build succeeded"
    else
      "$K" build why "$CORE_PROJECT" "$svc" >&2 || true
      fail "${svc} build succeeded"; exit 1
    fi
  done

  step "both services answer through traefik"
  for svc in "${CORE_SERVICES[@]}"; do
    if eventually 240 http_200 "$(core_host "$svc")" /healthz; then
      pass "${svc} /healthz returns 200"
    else
      core_dump "$svc"; fail "${svc} /healthz returns 200"; exit 1
    fi
  done

  step "the env ref reaches the database"
  # Pods restart once the addon's connection secret is mounted, so the
  # first attempts can still hit a pod without DATABASE_URL.
  local created="" deadline=$((SECONDS + 180))
  while (( SECONDS < deadline )); do
    created="$(svc_curl "$(core_host docker)" /api/todos -X POST -H 'Content-Type: application/json' -d '{"title":"smoke-test"}' 2>/dev/null || true)"
    grep -q 'smoke-test' <<<"$created" && break
    sleep 5
  done
  require "write a row through docker" grep -q 'smoke-test' <<<"$created"
  check "the row is visible through nix" grep -q 'smoke-test' <<<"$(svc_curl "$(core_host nix)" /api/todos)"

  step "rebuild, then roll back"
  # Both builds are of the same commit, so they share an image tag; what
  # moves is which build the production environment counts as live.
  local first second rolled
  first="$(core_live_build docker)"
  require "a live build is recorded for docker" test -n "$first"
  "$K" build trigger "$CORE_PROJECT" docker --follow >/dev/null || { fail "second docker build succeeded"; exit 1; }
  second="$(core_live_build docker)"
  check "the second build became live" test "$second" != "$first"
  "$K" build rollback "$CORE_PROJECT" docker --previous --yes >/dev/null
  rolled="$(core_live_build docker)"
  check "rollback made the first build live again (${first} -> ${second} -> ${rolled})" test "$rolled" == "$first"
  check "docker answers after the rollback" eventually 180 http_200 "$(core_host docker)" /healthz

  [[ "${KEEP:-0}" == "1" ]] && return 0
  step "delete the project"
  "$K" project delete "$CORE_PROJECT" --yes --purge-data >/dev/null
  check "every project CR is gone after delete" eventually 180 project_gone "$CORE_PROJECT"
}

core_cleanup() { delete_project "$CORE_PROJECT"; }
