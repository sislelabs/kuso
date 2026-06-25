#!/usr/bin/env bash
# verify-parity.sh — heuristic CLI/API surface check.
#
# Lists every route mounted by server-go handlers (chi `r.Get/Post/...`)
# and every cobra `Use:` command path on the CLI side. Compares the
# resource-noun stems and prints API routes whose noun isn't reachable
# via any CLI command. Not airtight (the noun mapping is fuzzy and
# CLI side often groups multiple endpoints under one command), but
# loud enough to catch "added /api/notify/test, forgot to wire it"
# before review.
#
# Exit code 0 always — output is informational. Wire as a `make verify`
# helper, not a hard CI gate, until we have a typed registry both
# sides import.
#
# Tunables: env var KUSO_PARITY_VERBOSE=1 prints the matched pairs too.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# --- 1. server routes ---------------------------------------------
# Capture lines like:
#   r.Get("/api/projects/{project}/services", h.List)
# and reduce them to noun stems: "projects", "services", "kubernetes",
# etc. The first /api/<noun> segment is the bucket.
api_nouns="$(
  grep -rh -E 'r\.(Get|Post|Put|Patch|Delete)\("/api/' \
    server-go/internal/http/handlers/ 2>/dev/null \
    | sed -E 's|.*"/api/([a-zA-Z][a-zA-Z0-9_-]*).*|\1|' \
    | sort -u
)"

# --- 2. CLI commands ---------------------------------------------
# cobra commands declare `Use: "<word> ..."`; we want the first word.
cli_nouns="$(
  grep -rh -E 'Use:\s*"' cli/cmd/kusoCli/ 2>/dev/null \
    | sed -E 's|.*Use:[[:space:]]*"([a-zA-Z][a-zA-Z0-9_-]*).*|\1|' \
    | sort -u
)"

# --- 3. Manual-mapping aliases ------------------------------------
# A handful of API nouns map onto a CLI noun with a different name.
# Normalise here so the "missing" report doesn't yell about them.
declare -A alias_map=(
  ["projects"]="get apply"
  ["services"]="get apply"
  ["addons"]="addon"
  ["crons"]="cron"
  ["secrets"]="env"
  ["kubernetes"]="status nodes"
  ["instance-secrets"]="instance"
  ["instance-addons"]="instance"
  ["project-secrets"]="env"
  ["users"]="login"
  ["auth"]="login"
  ["tokens"]="login"
  ["health"]="status"
  ["healthz"]="status"
  ["audit"]="status"
  ["release.json"]="upgrade"
  ["system"]="upgrade"
  ["logs"]="logs"
  ["builds"]="build"
  ["alerts"]="status"
  ["github"]="github"
  ["invites"]="login"
  ["admin"]="login"
  ["notify"]="status"
  ["notifications"]="status"
  ["templates"]="apply"
  ["openapi"]="status"
  ["docs"]="status"
  ["spec"]="apply"
)

# --- 4. report ----------------------------------------------------
missing=()
matched=()
for noun in $api_nouns; do
  candidates="${alias_map[$noun]:-$noun}"
  hit=""
  for cand in $candidates; do
    if echo "$cli_nouns" | grep -qx "$cand"; then
      hit="$cand"
      break
    fi
  done
  if [[ -z "$hit" ]]; then
    missing+=("$noun")
  else
    matched+=("$noun → $hit")
  fi
done

if [[ "${KUSO_PARITY_VERBOSE:-0}" == "1" ]]; then
  echo "=== matched API nouns → CLI commands ==="
  printf '  %s\n' "${matched[@]}"
  echo
fi

if [[ ${#missing[@]} -gt 0 ]]; then
  echo "verify-parity: API nouns with no CLI mapping:"
  printf '  - /api/%s\n' "${missing[@]}"
  echo
  echo "These may be fine (handlers without user-facing surface) but check"
  echo "before merging. To suppress, add an alias to hack/verify-parity.sh."
else
  echo "verify-parity: every /api/<noun> has a plausible CLI command."
fi

# --- 5. VERB parity (the create-but-can't-delete class) -----------
# The noun check above passes as long as SOME command exists under a
# noun — it's blind to a resource you can create but not delete/list.
# That's exactly how `environment` (add but no top-level delete/list),
# `env-groups` and `grants` (full API, zero CLI) all slipped through.
#
# Here we curate the user-managed resources and the verbs that matter,
# and check the CLI exposes each verb (as a cobra `Use:` word OR an
# alias anywhere — delete is satisfied by delete OR rm). Curated rather
# than auto-derived because "DELETE /api/.../grants/{id}" → which
# command? needs a human noun→command mapping; this table is the
# registry until a typed one exists. Add a row per user-managed resource.
verb_specs=(
  "environment|add delete list"
  "env-group|create delete list"
  "project grant|add list remove"
  "run|cancel delete"
  "addon|add"
  "cron|add delete"
  "domain|add remove"
)

cli_use_words="$(
  grep -rh -E 'Use:\s*"' cli/cmd/kusoCli/ 2>/dev/null \
    | sed -E 's|.*Use:[[:space:]]*"([a-zA-Z][a-zA-Z0-9_-]*).*|\1|'
)"
cli_alias_words="$(
  grep -rhoE 'Aliases:[[:space:]]*\[\]string\{[^}]*\}' cli/cmd/kusoCli/ 2>/dev/null \
    | grep -oE '"[a-zA-Z][a-zA-Z0-9_-]*"' | tr -d '"'
)"
all_cli_words="$(printf '%s\n%s\n' "$cli_use_words" "$cli_alias_words" | sort -u)"

has_word() { echo "$all_cli_words" | grep -qx "$1"; }
verb_satisfied() {
  case "$1" in
    delete) has_word delete || has_word rm ;;
    remove) has_word remove || has_word rm ;;
    *) has_word "$1" ;;
  esac
}

verb_gaps=()
for spec in "${verb_specs[@]}"; do
  IFS='|' read -r label verbs <<<"$spec"
  for v in $verbs; do
    if ! verb_satisfied "$v"; then
      verb_gaps+=("$label: missing '$v' verb")
    fi
  done
done

echo
if [[ ${#verb_gaps[@]} -gt 0 ]]; then
  echo "verify-parity: CLI verb gaps (create-but-can't-X class):"
  printf '  - %s\n' "${verb_gaps[@]}"
  echo
  echo "A managed resource is missing a key verb. Wire it, or update the"
  echo "verb_specs table in hack/verify-parity.sh if intentionally unsupported."
else
  echo "verify-parity: all curated resources expose their key verbs."
fi
