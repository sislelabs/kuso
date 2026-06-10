#!/usr/bin/env bash
#
# kuso-incident-agent entrypoint.
#
# Runs inside the per-incident Job (built by internal/incidents jobs.go).
# It wires the operator's Claude Code credentials into ~/.claude, renders a
# phase-specific prompt from /opt/incident-agent/prompts/, and runs
# `claude -p`. The agent itself POSTs its findings / PR request back to the
# kuso API with the per-incident bearer token.
#
# ENV CONTRACT (the Go Job-builder MUST set these — keep in sync with
# README.md and internal/incidents jobs.go):
#
#   PHASE            investigate | implement          (required)
#   INCIDENT_ID      incident id (inc-xxxx)           (required)
#   KUSO_API_URL     base URL of the kuso server      (required)
#                    e.g. http://kuso-server.kuso.svc.cluster.local
#   INCIDENT_TOKEN   per-incident bearer (agent-facing endpoints)  (required)
#   KUSO_TOKEN       project-scoped kuso CLI token (viewer + sql)  (OPTIONAL;
#                    empty in v1 → agent uses kubectl via its read-only SA)
#   CONTEXT_PACK     incident context-pack JSON (the Incident.contextPack
#                    blob). Provided inline OR via CONTEXT_PACK_FILE.    (required)
#   CONTEXT_PACK_FILE  path to a mounted file holding the context pack
#                    (a ConfigMap mount). Takes precedence over CONTEXT_PACK.
#   EVENT_TYPE, PROJECT, SERVICE, SEVERITY, INCIDENT_TITLE   (required; for
#                    prompt substitution + the kuso CLI invocations)
#   FINDINGS         investigate-phase writeup, fed into the implement prompt
#                    (implement phase only; may also arrive via FINDINGS_FILE)
#   FEEDBACK         accumulated operator feedback as a JSON array or plain
#                    text block (optional; substituted into both prompts)
#
#   implement phase only (repo wiring):
#   REPO_OWNER, REPO_NAME, REPO_DEFAULT_BRANCH   (required for implement)
#   GIT_TOKEN        short-lived, repo-scoped GitHub installation token used
#                    to clone + push. NEVER logged. (required for implement)
#   FIX_BRANCH       branch name to create for the fix (default:
#                    kuso-incident-${INCIDENT_ID})
#
# VOLUME CONTRACT:
#   /cc/credentials.json   the operator's Claude Code OAuth creds, projected
#                          from Secret kuso-incident-agent-cc (key
#                          credentials.json), read-only. Copied into
#                          ~/.claude/.credentials.json at startup.
#   (optional) a ConfigMap mounted at CONTEXT_PACK_FILE for large packs.
#
# The pod runs as the read-only kuso-incident-agent ServiceAccount: kubectl
# uses the mounted SA token (in-cluster config) automatically.

set -euo pipefail

log()  { printf '[incident-agent] %s\n' "$*" >&2; }
die()  { log "FATAL: $*"; exit 1; }

require() {
  local name="$1"
  if [ -z "${!name:-}" ]; then die "required env $name is unset"; fi
}

PROMPTS_DIR="${PROMPTS_DIR:-/opt/incident-agent/prompts}"
CC_CREDS_SRC="${CC_CREDS_SRC:-/cc/credentials.json}"

# --- 1. Validate the common env contract ---------------------------------
require PHASE
require INCIDENT_ID
require KUSO_API_URL
require INCIDENT_TOKEN
# KUSO_TOKEN is OPTIONAL in v1. When empty, the agent investigates via kubectl
# (its read-only ServiceAccount token) instead of the kuso CLI's /api surface.
# It is deliberately NOT the incident bearer (least privilege). A future
# release mints a project-scoped viewer+sql token and sets this.

case "$PHASE" in
  investigate|implement) ;;
  *) die "PHASE must be 'investigate' or 'implement', got '${PHASE}'" ;;
esac

# --- 2. Mount Claude Code credentials into ~/.claude ---------------------
# `claude -p` reads OAuth creds from ~/.claude/.credentials.json. The Secret
# is mounted read-only at /cc; copy it into a writable HOME location (the CC
# CLI may rewrite/refresh the token file, which it can't do on a RO mount).
mkdir -p "${HOME}/.claude"
if [ -f "$CC_CREDS_SRC" ]; then
  install -m 0600 "$CC_CREDS_SRC" "${HOME}/.claude/.credentials.json"
  log "claude credentials mounted from ${CC_CREDS_SRC}"
else
  die "Claude Code credentials not found at ${CC_CREDS_SRC} (is the kuso-incident-agent-cc Secret mounted?)"
fi

# Point the kuso CLI + API at the in-cluster server. The CLI honours these.
export KUSO_API_URL
export KUSO_TOKEN
# Exported so the agent's curl calls in the prompts resolve them.
export INCIDENT_ID INCIDENT_TOKEN

# --- 3. Resolve the context pack ----------------------------------------
if [ -n "${CONTEXT_PACK_FILE:-}" ] && [ -f "${CONTEXT_PACK_FILE}" ]; then
  CONTEXT_PACK="$(cat "${CONTEXT_PACK_FILE}")"
fi
CONTEXT_PACK="${CONTEXT_PACK:-}"
[ -z "$CONTEXT_PACK" ] && CONTEXT_PACK='{}'
# Sanity: it should be JSON. Don't hard-fail — a malformed pack still lets
# the agent investigate from the other fields — but log it.
if ! printf '%s' "$CONTEXT_PACK" | jq -e . >/dev/null 2>&1; then
  log "WARN: CONTEXT_PACK is not valid JSON; passing through verbatim"
fi

# Resolve findings (implement phase reads the investigate writeup).
if [ -n "${FINDINGS_FILE:-}" ] && [ -f "${FINDINGS_FILE}" ]; then
  FINDINGS="$(cat "${FINDINGS_FILE}")"
fi
FINDINGS="${FINDINGS:-}"
# SECURITY NOTE: FEEDBACK (operator/Discord free-text) and CONTEXT_PACK (which
# includes a crashing pod's log tail) are UNTRUSTED content rendered into the
# claude -p prompt. This is a prompt-injection surface — a crafted log line or
# feedback message could try to steer the agent. This is inherent to LLM agents
# and partially mitigated: POST /feedback is admin-gated, the investigate SA is
# read-only, and no write (PR/merge) happens without an explicit human "go".
# The values are NOT a SHELL-injection vector: every {{TOKEN}} substitution goes
# through sed_escape below and the final prompt is passed as ONE double-quoted
# argument to claude -p (no word-splitting/glob/eval).
FEEDBACK="${FEEDBACK:-(none)}"

# --- 4. Render the phase prompt -----------------------------------------
# Substitute {{TOKENS}} in the prompt template with env values. We use a
# perl-free approach: build the prompt by reading the template and replacing
# each known placeholder via bash parameter-less here-strings is awkward, so
# use a small awk-free sed with a delimiter unlikely to appear. Values can
# contain slashes/newlines, so we feed them through a function that escapes
# for sed's replacement.
TEMPLATE="${PROMPTS_DIR}/${PHASE}.txt"
[ -f "$TEMPLATE" ] || die "prompt template not found: ${TEMPLATE}"

# render replaces {{KEY}} with the value of the env-like arg pairs. To keep
# multi-line / special-char values intact we do it in Python-free pure bash:
# read the template once, then bash string-replace each token globally.
render() {
  local text
  text="$(cat "$TEMPLATE")"
  local key val
  for kv in "$@"; do
    key="${kv%%=*}"
    val="${kv#*=}"
    # Global literal replace of {{KEY}} → val using bash ${var//search/repl}.
    text="${text//\{\{$key\}\}/$val}"
  done
  printf '%s' "$text"
}

INCIDENT_TITLE="${INCIDENT_TITLE:-${TITLE:-incident ${INCIDENT_ID}}}"

PROMPT="$(render \
  "EVENT_TYPE=${EVENT_TYPE:-}" \
  "PROJECT=${PROJECT:-}" \
  "SERVICE=${SERVICE:-}" \
  "SEVERITY=${SEVERITY:-}" \
  "TITLE=${INCIDENT_TITLE}" \
  "CONTEXT_PACK=${CONTEXT_PACK}" \
  "FINDINGS=${FINDINGS}" \
  "FEEDBACK=${FEEDBACK}" \
  "REPO_OWNER=${REPO_OWNER:-}" \
  "REPO_NAME=${REPO_NAME:-}" \
  "REPO_DEFAULT_BRANCH=${REPO_DEFAULT_BRANCH:-main}" \
)"

# --- 5. Implement phase: clone the repo on a fresh branch ----------------
if [ "$PHASE" = "implement" ]; then
  # Fail GRACEFULLY when the repo isn't wired (no KusoService repo / no GitHub
  # App installation for this project): POST a note to the incident so the
  # operator sees WHY no PR was opened, then exit 0 (a non-zero exit would just
  # show as a Job crash with no explanation).
  if [ -z "${REPO_OWNER:-}" ] || [ -z "${REPO_NAME:-}" ] || [ -z "${GIT_TOKEN:-}" ]; then
    log "implement: no repo wired (owner='${REPO_OWNER:-}' name='${REPO_NAME:-}' token=$([ -n "${GIT_TOKEN:-}" ] && echo set || echo empty))"
    note="⚠️ Cannot open a PR: this incident's project/service has no repo + GitHub App installation wired in kuso, so there's nothing to push to. Resolve the fix manually, or wire the repo (KusoService.spec.repo + project GitHub installation) and re-run."
    curl -fsS -X POST \
      -H "Authorization: Bearer ${INCIDENT_TOKEN}" -H "Content-Type: application/json" \
      --data "$(jq -n --arg u "$note" '{prUrl:"", prNumber:0, note:$u}')" \
      "${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/pr" >/dev/null 2>&1 || true
    log "implement: reported no-repo to incident; exiting 0"
    exit 0
  fi
  REPO_DEFAULT_BRANCH="${REPO_DEFAULT_BRANCH:-main}"
  FIX_BRANCH="${FIX_BRANCH:-kuso-incident-${INCIDENT_ID}}"
  export FIX_BRANCH

  # Clone over the short-lived, repo-scoped installation token. The
  # x-access-token user is GitHub's convention for App installation tokens.
  # Configure a credential helper that injects the token so it never lands
  # in .git/config or process args (where it could leak to logs).
  git config --global user.name  "kuso-incident-agent"
  git config --global user.email "incident-agent@kuso.local"
  git config --global credential.helper \
    '!f() { echo "username=x-access-token"; echo "password=${GIT_TOKEN}"; }; f'

  CLONE_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}.git"
  log "cloning ${REPO_OWNER}/${REPO_NAME} (branch ${REPO_DEFAULT_BRANCH})"
  git clone --depth 50 --branch "$REPO_DEFAULT_BRANCH" "$CLONE_URL" repo \
    || die "git clone failed"
  cd repo
  git checkout -b "$FIX_BRANCH"
  log "on branch ${FIX_BRANCH}"

  # KUSO_INCIDENT_CLONE_ONLY: plumbing test. Proves the repo-resolve → token-mint
  # → clone → branch chain works WITHOUT opening a PR. Reports a note + exits 0.
  if [ "${KUSO_INCIDENT_CLONE_ONLY:-}" = "true" ]; then
    head_sha="$(git rev-parse --short HEAD)"
    log "clone-only: cloned ${REPO_OWNER}/${REPO_NAME}@${head_sha}, branch ${FIX_BRANCH} ready — skipping PR"
    note="✅ Plumbing test: successfully resolved repo ${REPO_OWNER}/${REPO_NAME}, minted a push token, cloned (${REPO_DEFAULT_BRANCH}@${head_sha}), and created branch ${FIX_BRANCH}. PR creation skipped (KUSO_INCIDENT_CLONE_ONLY)."
    curl -fsS -X POST \
      -H "Authorization: Bearer ${INCIDENT_TOKEN}" -H "Content-Type: application/json" \
      --data "$(jq -n --arg u "$note" '{prUrl:"", prNumber:0, note:$u}')" \
      "${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/pr" >/dev/null 2>&1 || true
    exit 0
  fi
fi

# --- 6. Run the agent ----------------------------------------------------
# Head-less, non-interactive. --dangerously-skip-permissions because there is
# no human at a TTY to approve each tool call; the SANDBOX is the read-only
# RBAC SA (investigate) and the per-branch repo clone (implement), not CC's
# interactive gate. The blast radius is bounded by RBAC + the PR-only flow,
# per the design's safety section.
log "running claude (phase=${PHASE}, incident=${INCIDENT_ID})"
set +e
claude -p "$PROMPT" --dangerously-skip-permissions
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  log "claude exited non-zero (rc=${rc})"
fi
exit "$rc"
