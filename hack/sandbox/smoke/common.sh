# shellcheck shell=bash
# Helpers for smoke.sh groups. Sourced after lib.sh.

K="${SANDBOX_DIR}/kuso"
RESULTS="${STATE_DIR}/smoke-results"
mkdir -p "$STATE_DIR"
: > "$RESULTS"
CURRENT_GROUP=""

step() { printf '\n\033[1;34m[%s]\033[0m %s\n' "$CURRENT_GROUP" "$*"; }
pass() { printf '  \033[1;32mPASS\033[0m %s\n' "$*"; echo "PASS ${CURRENT_GROUP}: $*" >> "$RESULTS"; }
fail() { printf '  \033[1;31mFAIL\033[0m %s\n' "$*"; echo "FAIL ${CURRENT_GROUP}: $*" >> "$RESULTS"; }

# check <description> <command...>: PASS when the command succeeds. A
# FAIL is recorded and the group carries on.
check() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then pass "$desc"; else fail "$desc"; fi
  return 0
}

# require <description> <command...>: like check, but a FAIL ends the
# group (later checks depend on it).
require() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then pass "$desc"; else fail "$desc"; exit 1; fi
}

# eventually <seconds> <command...>: retry until it succeeds.
eventually() {
  local deadline=$((SECONDS + $1)); shift
  while (( SECONDS < deadline )); do
    "$@" >/dev/null 2>&1 && return 0
    sleep 3
  done
  return 1
}

# never <seconds> <command...>: succeeds only if the command keeps
# failing for the whole window.
never() {
  local deadline=$((SECONDS + $1)); shift
  while (( SECONDS < deadline )); do
    "$@" >/dev/null 2>&1 && return 1
    sleep 3
  done
  return 0
}

# run_group <name>: source smoke/<name>.sh and run <name>_main in a
# subshell, then <name>_cleanup unless KEEP=1. An abort (set -e, die) is
# recorded as a FAIL instead of ending the whole run.
run_group() {
  local g="$1" rc=0 before
  before="$(grep -c '^FAIL' "$RESULTS" || true)"
  printf '\n\033[1;35m=== %s ===\033[0m\n' "$g"
  # Not `( ... ) || rc=$?`: that would switch errexit off inside the group.
  set +e
  (
    set -e
    CURRENT_GROUP="$g"
    source "${SANDBOX_DIR}/smoke/${g}.sh"
    if [[ "${KEEP:-0}" != "1" ]] && declare -F "${g}_cleanup" >/dev/null; then
      trap '"${CURRENT_GROUP}_cleanup" >/dev/null 2>&1 || true' EXIT
    fi
    "${g}_main"
  )
  rc=$?
  set -e
  if (( rc != 0 )) && [[ "$(grep -c '^FAIL' "$RESULTS" || true)" == "$before" ]]; then
    CURRENT_GROUP="$g" fail "group aborted (exit ${rc})"
  fi
}

summary() {
  local p f
  p="$(grep -c '^PASS' "$RESULTS" || true)"
  f="$(grep -c '^FAIL' "$RESULTS" || true)"
  printf '\n\033[1;35m=== summary ===\033[0m\n'
  sed 's/^/  /' "$RESULTS"
  if [[ "$f" == "0" ]]; then
    printf '\n\033[1;32mSMOKE PASSED\033[0m (%s checks)\n' "$p"
  else
    printf '\n\033[1;31mSMOKE FAILED\033[0m (%s passed, %s failed)\n' "$p" "$f"
    exit 1
  fi
}

# svc_curl <host> <path> [curl args]: through traefik on the instance port.
svc_curl() {
  local host="$1" path="$2"; shift 2
  curl -sS --max-time "${CURL_TIMEOUT:-10}" -H "Host: ${host}" "http://127.0.0.1:${KUSO_SANDBOX_PORT}${path}" "$@"
}

http_code() { svc_curl "$1" "$2" -o /dev/null -w '%{http_code}' 2>/dev/null || true; }
http_200() { [[ "$(http_code "$1" "$2")" == "200" ]]; }

project_exists() {
  "$K" get projects -o json 2>/dev/null | jq -e --arg p "$1" '.[] | select(.metadata.name==$p or .name==$p)' >/dev/null
}

# delete_project <name>: delete and wait for its CRs to go.
delete_project() {
  local p="$1" ns
  project_exists "$p" || return 0
  ns="$(project_ns "$p")"
  "$K" project delete "$p" --yes --purge-data >/dev/null 2>&1 || true
  eventually 180 project_gone "$p" || true
  # A re-create of the same name fails while the old namespace terminates.
  [[ -n "$ns" && "$ns" != "kuso" ]] && { eventually 180 ns_gone "$ns" || true; }
  return 0
}
ns_gone() { ! node_kubectl get ns "$1" >/dev/null 2>&1; }
project_gone() {
  ! node_kubectl get kusoservices,kusoenvironments,kusoaddons -A --no-headers 2>/dev/null | grep -q " ${1}-"
}

# fresh_project <name> [create args]: remove a leftover, then create.
fresh_project() {
  local p="$1"; shift
  if project_exists "$p"; then
    step "remove ${p} left by an earlier run"
    delete_project "$p"
  fi
  "$K" project create "$p" "$@" >/dev/null
}

# sandbox_s3 <mc args>: mc against the backup MinIO (alias s), e.g.
# `sandbox_s3 ls -r s/kuso-backups`.
sandbox_s3() {
  node_kubectl -n kuso-sandbox exec deploy/sandbox-s3 -- sh -c \
    'mc alias set s http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && mc "$@"' _ "$@"
}

# project_ns <project>: the namespace its workloads and addons run in
# (each project gets its own, kuso-<project> by default).
project_ns() {
  "$K" get projects -o json 2>/dev/null \
    | jq -r --arg p "$1" '.[] | select(.metadata.name==$p or .name==$p) | .spec.namespace // empty' | head -1
}

# env_pod <namespace> <env CR name>: a Running pod of that env.
env_pod() {
  node_kubectl -n "$1" get pods -l "app.kubernetes.io/instance=$2" \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

# deploy_ready <namespace> <deployment>: at least one ready replica.
deploy_ready() {
  [[ "$(node_kubectl -n "$1" get deploy "$2" -o jsonpath='{.status.readyReplicas}' 2>/dev/null)" -ge 1 ]]
}
