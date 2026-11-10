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
#   INCIDENT_TOKEN   per-incident bearer (agent-facing endpoints)  (required;
#                    from the Job's own Secret via secretKeyRef. Used by this
#                    script to post results, never exposed to the agent.)
#   KUSO_TOKEN       project-scoped kuso CLI token (viewer + sql)  (OPTIONAL;
#                    empty in v1 → agent uses kubectl via its read-only SA)
#   LOG_NAMESPACE    the one namespace the agent may read pod logs in
#   CONTEXT_PACK     incident context-pack JSON (the Incident.contextPack
#                    blob). Provided inline OR via CONTEXT_PACK_FILE.
#                    Investigate phase only; fenced as untrusted data.
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
#   GIT_TOKEN        short-lived GitHub App token restricted to REPO_OWNER/
#                    REPO_NAME (contents:write + pull_requests:write), from
#                    the Job's Secret. Used by this script to clone, push and
#                    open the PR; never logged, never in the agent's env.
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

# --- PHASE=refresh: keep-alive token refresh (no incident) ---------------
# Run by the kuso-incident-agent-refresher CronJob. Mounts the CC secret,
# runs a trivial `claude` call (which refreshes the OAuth access token using
# the refresh token), then writes the refreshed ~/.claude/.credentials.json
# back into the secret via kubectl — so the shared secret stays fresh and
# investigate/implement Jobs always mount valid creds. CC_SECRET_NAME +
# KUSO_NAMESPACE are set by the CronJob; the refresher SA can update ONLY
# that one secret.
if [ "${PHASE:-}" = "refresh" ]; then
  : "${CC_SECRET_NAME:=kuso-incident-agent-cc}"
  : "${KUSO_NAMESPACE:=kuso}"
  mkdir -p "${HOME}/.claude"
  [ -f "$CC_CREDS_SRC" ] || die "refresh: creds not mounted at ${CC_CREDS_SRC}"
  install -m 0600 "$CC_CREDS_SRC" "${HOME}/.claude/.credentials.json"
  before="$(md5sum "${HOME}/.claude/.credentials.json" | cut -d' ' -f1)"
  log "refresh: triggering token refresh via claude"
  # A trivial prompt forces CC to validate + (if near expiry) refresh the token.
  claude -p "reply with: ok" >/dev/null 2>&1 || true
  after="$(md5sum "${HOME}/.claude/.credentials.json" | cut -d' ' -f1)"
  if [ "$before" = "$after" ]; then
    log "refresh: token unchanged (still valid) — nothing to write back"
    exit 0
  fi
  log "refresh: token rotated — writing back to secret ${CC_SECRET_NAME}"
  # Write ONLY the claudeAiOauth block back (strip any transient cruft CC added).
  clean="$(jq -c '{claudeAiOauth: .claudeAiOauth}' "${HOME}/.claude/.credentials.json")"
  kubectl create secret generic "$CC_SECRET_NAME" -n "$KUSO_NAMESPACE" \
    --from-literal=credentials.json="$clean" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null \
    && log "refresh: secret updated" || die "refresh: failed to write secret"
  exit 0
fi

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


# Point the kuso CLI at the in-cluster server. The CLI honours these.
export KUSO_API_URL
export KUSO_TOKEN
export INCIDENT_ID

# The agent writes its outputs here; the entrypoint (not the agent) posts
# them back with INCIDENT_TOKEN, and pushes + opens the PR with GIT_TOKEN.
# Neither credential is in the agent's environment.
OUT_DIR="${OUT_DIR:-/tmp/kuso-out}"
mkdir -p "$OUT_DIR"
export OUT_DIR

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
FEEDBACK="${FEEDBACK:-(none)}"

# CONTEXT_PACK carries a crashing pod's log tail and FINDINGS were written by
# an agent that read it: both are attacker-influenced. They go into the
# prompt capped and inside a fence with a per-run random tag the attacker
# can't predict, so injected text can't close the fence early. The prompt
# templates tell the agent the fenced block is data, never instructions.
FENCE_TAG="untrusted-$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
fence() {
  local body
  body="$(printf '%s' "$1" | head -c "$2")"
  body="${body//$FENCE_TAG/}"
  printf '<%s>\n%s\n</%s>' "$FENCE_TAG" "$body" "$FENCE_TAG"
}
CONTEXT_PACK="$(fence "$CONTEXT_PACK" 16000)"
FINDINGS="$(fence "$FINDINGS" 32000)"

# --- 4. Render the phase prompt -----------------------------------------
TEMPLATE="${PROMPTS_DIR}/${PHASE}.txt"
[ -f "$TEMPLATE" ] || die "prompt template not found: ${TEMPLATE}"

# render replaces {{KEY}} with the value of the env-like arg pairs: read the
# template once, then bash string-replace each token globally. The final
# prompt is passed as ONE double-quoted argument (no word-splitting/eval).
render() {
  local text
  text="$(cat "$TEMPLATE")"
  local key val
  for kv in "$@"; do
    key="${kv%%=*}"
    val="${kv#*=}"
    text="${text//\{\{$key\}\}/$val}"
  done
  printf '%s' "$text"
}

INCIDENT_TITLE="${INCIDENT_TITLE:-${TITLE:-incident ${INCIDENT_ID}}}"
LOG_NAMESPACE="${LOG_NAMESPACE:-kuso}"

# CONTEXT_PACK / FINDINGS are substituted LAST so tokens inside them are
# never expanded.
PROMPT="$(render \
  "EVENT_TYPE=${EVENT_TYPE:-}" \
  "PROJECT=${PROJECT:-}" \
  "SERVICE=${SERVICE:-}" \
  "SEVERITY=${SEVERITY:-}" \
  "TITLE=${INCIDENT_TITLE}" \
  "NAMESPACE=${LOG_NAMESPACE}" \
  "OUT_DIR=${OUT_DIR}" \
  "FENCE_TAG=${FENCE_TAG}" \
  "FEEDBACK=${FEEDBACK}" \
  "REPO_OWNER=${REPO_OWNER:-}" \
  "REPO_NAME=${REPO_NAME:-}" \
  "REPO_DEFAULT_BRANCH=${REPO_DEFAULT_BRANCH:-main}" \
  "FINDINGS=${FINDINGS}" \
  "CONTEXT_PACK=${CONTEXT_PACK}" \
)"

post_pr() {
  curl -fsS -X POST \
    -H "Authorization: Bearer ${INCIDENT_TOKEN}" -H "Content-Type: application/json" \
    --data "$1" "${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/pr" >/dev/null 2>&1
}
post_note() {
  post_pr "$(jq -n --arg u "$1" '{prUrl:"", prNumber:0, note:$u}')" || true
}

# --- 5. Implement phase: clone the repo on a fresh branch ----------------
if [ "$PHASE" = "implement" ]; then
  # Fail GRACEFULLY when the repo isn't wired (no KusoService repo / no GitHub
  # App installation for this project): POST a note to the incident so the
  # operator sees WHY no PR was opened, then exit 0 (a non-zero exit would just
  # show as a Job crash with no explanation).
  if [ -z "${REPO_OWNER:-}" ] || [ -z "${REPO_NAME:-}" ] || [ -z "${GIT_TOKEN:-}" ]; then
    log "implement: no repo wired (owner='${REPO_OWNER:-}' name='${REPO_NAME:-}' token=$([ -n "${GIT_TOKEN:-}" ] && echo set || echo empty))"
    post_note "⚠️ Cannot open a PR: this incident's project/service has no repo + GitHub App installation wired in kuso, so there's nothing to push to. Resolve the fix manually, or wire the repo (KusoService.spec.repo + project GitHub installation) and re-run."
    log "implement: reported no-repo to incident; exiting 0"
    exit 0
  fi
  REPO_DEFAULT_BRANCH="${REPO_DEFAULT_BRANCH:-main}"
  FIX_BRANCH="${FIX_BRANCH:-kuso-incident-${INCIDENT_ID}}"
  export FIX_BRANCH
  CLONE_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}.git"

  # GIT_TOKEN is a short-lived token scoped to this ONE repo
  # (contents:write + pull_requests:write). The credential helper reads it
  # from the environment at call time, so it never lands in .git/config or
  # process args, and git run inside the agent (env without GIT_TOKEN) gets
  # no credentials at all.
  git config --global user.name  "kuso-incident-agent"
  git config --global user.email "incident-agent@kuso.local"
  git config --global credential.helper \
    '!f() { [ -n "${GIT_TOKEN:-}" ] || exit 0; echo "username=x-access-token"; echo "password=${GIT_TOKEN}"; }; f'

  log "cloning ${REPO_OWNER}/${REPO_NAME} (branch ${REPO_DEFAULT_BRANCH})"
  # Retried: a fresh pod's egress NetworkPolicy can take a few seconds to
  # be programmed.
  cloned=""
  for attempt in 1 2 3 4 5; do
    if git clone --depth 50 --branch "$REPO_DEFAULT_BRANCH" "$CLONE_URL" repo; then
      cloned=1; break
    fi
    rm -rf repo; sleep $((attempt * 3))
  done
  [ -n "$cloned" ] || die "git clone failed"
  cd repo
  BASE_SHA="$(git rev-parse HEAD)"
  git checkout -b "$FIX_BRANCH"
  log "on branch ${FIX_BRANCH}"

  # KUSO_INCIDENT_CLONE_ONLY: plumbing test. Proves the repo-resolve → token-mint
  # → clone → branch chain works WITHOUT opening a PR. Reports a note + exits 0.
  if [ "${KUSO_INCIDENT_CLONE_ONLY:-}" = "true" ]; then
    head_sha="$(git rev-parse --short HEAD)"
    log "clone-only: cloned ${REPO_OWNER}/${REPO_NAME}@${head_sha}, branch ${FIX_BRANCH} ready — skipping PR"
    post_note "✅ Plumbing test: successfully resolved repo ${REPO_OWNER}/${REPO_NAME}, minted a push token, cloned (${REPO_DEFAULT_BRANCH}@${head_sha}), and created branch ${FIX_BRANCH}. PR creation skipped (KUSO_INCIDENT_CLONE_ONLY)."
    exit 0
  fi
fi

# --- 6. Run the agent ----------------------------------------------------
# No --dangerously-skip-permissions: head-less `claude -p` denies any tool
# not on the explicit allowlist below. Investigate gets read-only kubectl
# verbs plus a Write confined to OUT_DIR; implement gets file edits, local
# git (no push/remote/config), and the common build/test toolchains. Build
# and test commands run repo code, so the implement allowlist is a speed
# bump, not a sandbox: the real bounds are the repo-scoped token (which
# the agent never sees), the egress NetworkPolicy and the PR review.
if [ "$PHASE" = "investigate" ]; then
  ALLOWED_TOOLS=(
    "Read" "Glob" "Grep" "Write(/${OUT_DIR}/**)"
    "Bash(kubectl get:*)" "Bash(kubectl describe:*)" "Bash(kubectl logs:*)"
    "Bash(kubectl top:*)" "Bash(kubectl events:*)" "Bash(kuso:*)"
    "Bash(jq:*)" "Bash(grep:*)" "Bash(head:*)" "Bash(tail:*)" "Bash(sort:*)" "Bash(wc:*)"
  )
else
  ALLOWED_TOOLS=(
    "Read" "Glob" "Grep" "Edit" "MultiEdit" "Write"
    "Bash(git add:*)" "Bash(git commit:*)" "Bash(git diff:*)" "Bash(git status:*)"
    "Bash(git log:*)" "Bash(git show:*)" "Bash(ls:*)"
    "Bash(go build:*)" "Bash(go test:*)" "Bash(go vet:*)"
    "Bash(npm ci:*)" "Bash(npm install:*)" "Bash(npm run:*)" "Bash(npm test:*)"
    "Bash(pnpm install:*)" "Bash(pnpm run:*)" "Bash(pnpm test:*)"
    "Bash(yarn install:*)" "Bash(yarn run:*)" "Bash(yarn test:*)"
    "Bash(pip install:*)" "Bash(pytest:*)" "Bash(python -m pytest:*)"
    "Bash(make:*)" "Bash(cargo build:*)" "Bash(cargo test:*)"
  )
fi

log "running claude (phase=${PHASE}, incident=${INCIDENT_ID})"
set +e
env -u INCIDENT_TOKEN -u GIT_TOKEN -u GH_TOKEN -u GITHUB_TOKEN claude -p "$PROMPT" --add-dir "$OUT_DIR" --allowedTools "${ALLOWED_TOOLS[@]}"
rc=$?
set -e
if [ "$rc" -ne 0 ]; then
  log "claude exited non-zero (rc=${rc})"
fi

# Anything the agent started (dev servers, test watchers) must be gone
# before the entrypoint touches its credentials again.
for d in /proc/[0-9]*; do
  p="${d#/proc/}"
  { [ "$p" = "$$" ] || [ "$p" = "$PPID" ]; } && continue
  [ -O "$d" ] && kill -9 "$p" 2>/dev/null || true
done

# --- 7. Report back -------------------------------------------------------
if [ "$PHASE" = "investigate" ]; then
  if [ -s "$OUT_DIR/findings.md" ]; then
    curl -fsS -X POST \
      -H "Authorization: Bearer ${INCIDENT_TOKEN}" -H "Content-Type: application/json" \
      --data "$(jq -n --rawfile f "$OUT_DIR/findings.md" '{findings:$f}')" \
      "${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/findings" >/dev/null \
      && log "findings posted" || { log "posting findings failed"; exit 1; }
  else
    log "agent wrote no findings to ${OUT_DIR}/findings.md"
    [ "$rc" -eq 0 ] && rc=1
  fi
  exit "$rc"
fi

# implement: validation blocked → report why, no push.
if [ -s "$OUT_DIR/blocked.md" ]; then
  post_note "Agent could not produce a validated fix: $(head -c 4000 "$OUT_DIR/blocked.md")"
  exit 0
fi

# Never run git with the token inside the agent's checkout: its .git/config
# and hooks are agent-writable (core.fsmonitor, url.insteadOf, pushurl …).
# Export the new commits as a bundle with the token stripped from the
# environment, then push them from a fresh clone under a clean HOME.
ahead="$(env -u GIT_TOKEN -u INCIDENT_TOKEN git rev-list --count "${BASE_SHA}..${FIX_BRANCH}" 2>/dev/null || echo 0)"
if [ "$ahead" = "0" ]; then
  post_note "Agent finished without committing a fix (rc=${rc}); nothing to push."
  exit 0
fi
env -u GIT_TOKEN -u INCIDENT_TOKEN git bundle create "$OUT_DIR/fix.bundle" "${BASE_SHA}..${FIX_BRANCH}" \
  || { post_note "Could not bundle the agent's commits; no PR opened."; exit 1; }
cd /
PUSH_HOME="$(mktemp -d)"
export HOME="$PUSH_HOME" GIT_CONFIG_NOSYSTEM=1
git config --global credential.helper \
  '!f() { echo "username=x-access-token"; echo "password=${GIT_TOKEN}"; }; f'
git config --global user.name  "kuso-incident-agent"
git config --global user.email "incident-agent@kuso.local"
git init -q "$PUSH_HOME/push" && cd "$PUSH_HOME/push"
git remote add origin "$CLONE_URL"
git fetch -q --depth 50 origin "$BASE_SHA" \
  || { post_note "Could not fetch the base commit to push from; no PR opened."; exit 1; }
git fetch -q "$OUT_DIR/fix.bundle" "${FIX_BRANCH}:refs/heads/${FIX_BRANCH}" \
  || { post_note "The agent's commit bundle did not apply; no PR opened."; exit 1; }
git push -q origin "refs/heads/${FIX_BRANCH}:refs/heads/${FIX_BRANCH}" \
  || { post_note "Pushing ${FIX_BRANCH} failed; no PR opened."; exit 1; }

PR_TITLE="Fix for kuso incident ${INCIDENT_ID}"
PR_BODY="Generated by kuso's incident agent for human review (incident ${INCIDENT_ID})."
if [ -s "$OUT_DIR/pr.json" ] && jq -e . "$OUT_DIR/pr.json" >/dev/null 2>&1; then
  PR_TITLE="$(jq -r '.title // empty' "$OUT_DIR/pr.json" | head -c 200)"
  PR_BODY="$(jq -r '.body // empty' "$OUT_DIR/pr.json" | head -c 60000)"
  [ -n "$PR_TITLE" ] || PR_TITLE="Fix for kuso incident ${INCIDENT_ID}"
fi
pr_url="$(printf '%s' "$PR_BODY" | GH_TOKEN="$GIT_TOKEN" gh pr create \
  --repo "${REPO_OWNER}/${REPO_NAME}" --base "$REPO_DEFAULT_BRANCH" --head "$FIX_BRANCH" \
  --title "$PR_TITLE" --body-file - 2>/dev/null | tail -n1)"
case "$pr_url" in
  https://github.com/*/pull/*) ;;
  *) post_note "Pushed branch ${FIX_BRANCH} but opening the PR failed; open it by hand."; exit 1 ;;
esac
post_pr "$(jq -n --arg u "$pr_url" --argjson n "${pr_url##*/}" '{prUrl:$u, prNumber:$n}')" \
  && log "PR reported: ${pr_url}" || { log "reporting PR failed"; exit 1; }
exit 0
