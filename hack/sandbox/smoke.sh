#!/usr/bin/env bash
#
# End-to-end check of the deploy lifecycle against a running sandbox:
# project → dockerfile + nixpacks builds → live HTTP → postgres addon via
# an env ref → rebuild → rollback → delete.
#
# Only ever talks to the sandbox: the API URL comes from lib.sh, not the
# environment, and the CLI runs through ./kuso (its own HOME).
#
#   KEEP=1 smoke.sh    leave the project behind for inspection

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

K="${SANDBOX_DIR}/kuso"
PROJECT="smoke"
REPO_URL="https://github.com/ivo9999/kuso-demo-todo-api"
SERVICES=(docker nix)

node_running || die "the sandbox is not running; run 'make sandbox-up'"

step() { printf '\n\033[1;34m[%s]\033[0m %s\n' "$((++STEP))" "$*"; }
STEP=0

# svc_curl <service> <path> [curl args]: request through traefik. Goes to
# 127.0.0.1 with a Host header so it doesn't depend on *.localhost resolving.
svc_curl() {
  local svc="$1" path="$2"; shift 2
  curl -sS --max-time 10 -H "Host: ${svc}.${PROJECT}.${SANDBOX_DOMAIN}" \
    "http://127.0.0.1:${KUSO_SANDBOX_PORT}${path}" "$@"
}

# wait_http <service> <path> <seconds>: until the service answers 200.
wait_http() {
  local svc="$1" path="$2" deadline=$((SECONDS + $3)) code=""
  while (( SECONDS < deadline )); do
    code="$(svc_curl "$svc" "$path" -o /dev/null -w '%{http_code}' 2>/dev/null || true)"
    [[ "$code" == "200" ]] && return 0
    sleep 3
  done
  "$K" service pods "$PROJECT" "$svc" >&2 || true
  "$K" logs "$PROJECT" "$svc" 2>&1 | tail -30 >&2 || true
  die "${svc}${path} did not return 200 within $3s (last status: ${code:-none})"
}

# live_image <service>: the image the production environment runs.
live_image() {
  node_kubectl get kusoenvironments -A -o json \
    | jq -r --arg n "${PROJECT}-$1-production" '.items[] | select(.metadata.name==$n) | "\(.spec.image.repository):\(.spec.image.tag)"'
}

command -v jq >/dev/null || die "jq is required"

step "log in to the sandbox"
"$K" login --api "$SANDBOX_API_URL" -u admin -p "$SANDBOX_ADMIN_PASSWORD" >/dev/null

if "$K" get projects -o json | jq -e --arg p "$PROJECT" '.[] | select(.metadata.name==$p or .name==$p)' >/dev/null 2>&1; then
  step "remove the project left by an earlier run"
  "$K" project delete "$PROJECT" --yes --purge-data
  sleep 10
fi

step "create project ${PROJECT}"
"$K" project create "$PROJECT" --repo "$REPO_URL" --branch main

step "add a dockerfile service and a nixpacks service"
"$K" service add "$PROJECT" docker --runtime dockerfile --port 8080
"$K" service add "$PROJECT" nix --runtime nixpacks --port 8080

step "add a postgres addon and reference it from both services"
"$K" project addon add "$PROJECT" db --kind postgres --version 16 --size small
for svc in "${SERVICES[@]}"; do
  "$K" env set "$PROJECT" "$svc" 'DATABASE_URL=${{ db.DATABASE_URL }}' 'MIGRATE_ON_BOOT=1'
done

step "build both services"
for svc in "${SERVICES[@]}"; do
  "$K" build trigger "$PROJECT" "$svc" --follow \
    || { "$K" build why "$PROJECT" "$svc" >&2 || true; die "build of ${svc} failed"; }
done

step "both services answer through traefik"
for svc in "${SERVICES[@]}"; do
  wait_http "$svc" /healthz 240
  echo "  ${svc}: $(svc_curl "$svc" /healthz)"
done

step "the env ref reaches the database"
# Pods restart once the addon's connection secret is mounted, so the first
# attempts can still hit a pod without DATABASE_URL.
created=""
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
  created="$(svc_curl docker /api/todos -X POST -H 'Content-Type: application/json' -d '{"title":"smoke-test"}' 2>/dev/null || true)"
  grep -q 'smoke-test' <<<"$created" && break
  sleep 5
done
grep -q 'smoke-test' <<<"$created" || die "writing through docker failed: ${created:-no response}"
echo "  POST via docker: ${created}"
listed="$(svc_curl nix /api/todos)"
echo "  GET via nix:     ${listed}"
grep -q 'smoke-test' <<<"$listed" || die "the row written through one service is not visible through the other"

step "rebuild, then roll back"
first_image="$(live_image docker)"
[[ -n "$first_image" ]] || die "could not read the live image of ${PROJECT}/docker"
"$K" build trigger "$PROJECT" docker --follow || die "second build of docker failed"
second_image="$(live_image docker)"
[[ "$second_image" != "$first_image" ]] || die "the second build did not change the live image (${first_image})"
"$K" build rollback "$PROJECT" docker --previous --yes
rolled_image="$(live_image docker)"
[[ "$rolled_image" == "$first_image" ]] || die "rollback left ${rolled_image}, expected ${first_image}"
wait_http docker /healthz 180
echo "  ${first_image} -> ${second_image} -> ${rolled_image}"

if [[ "${KEEP:-0}" == "1" ]]; then
  printf '\n\033[1;32mSMOKE PASSED\033[0m (KEEP=1: project %s left in place)\n' "$PROJECT"
  exit 0
fi

step "delete the project"
"$K" project delete "$PROJECT" --yes --purge-data
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
  left="$(node_kubectl get kusoservices,kusoenvironments,kusoaddons -A --no-headers 2>/dev/null | grep -c "${PROJECT}-" || true)"
  [[ "$left" == "0" ]] && break
  sleep 5
done
[[ "$left" == "0" ]] || { node_kubectl get kusoservices,kusoenvironments,kusoaddons -A >&2; die "project resources remain after delete"; }

printf '\n\033[1;32mSMOKE PASSED\033[0m\n'
