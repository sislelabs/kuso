#!/usr/bin/env bash
# Seed the sandbox with realistic review data. Idempotent: every step checks
# what already exists and skips it, so re-running only fills gaps.
# Talks to kuso only through the sandbox CLI (and `kuso api` for the one call
# the CLI has no flag shape for). See README.md for what it creates.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KUSO="${KUSO:-${dir}/../kuso}"
DEMO_REPO="https://github.com/ivo9999/kuso-demo-todo-api"
# A commit of the demo repo whose binary exits on boot: deploying it and then
# rolling back gives shop/api a realistic "Rolled back" history.
CRASH_REF="64cd286f1547123c1d997d60ccd417e6785bb5ae"
SEED_PASSWORD="${SEED_PASSWORD:-kuso-review-1}"

k() { "$KUSO" "$@"; }
log() { printf '\n==> %s\n' "$*"; }
skip() { printf '    %s (already there)\n' "$*"; }

command -v jq >/dev/null || { echo "error: jq is required" >&2; exit 1; }
k get projects -o json >/dev/null || { echo "error: sandbox CLI can't reach kuso; run 'make sandbox-up'" >&2; exit 1; }

project_exists() { k get projects -o json | jq -e --arg n "$1" 'any(.[]; .metadata.name == $n)' >/dev/null; }
service_exists() { k get services "$1" -o json | jq -e --arg n "$1-$2" 'any(.[]; .metadata.name == $n)' >/dev/null; }
build_count() { k build list "$1" "$2" -o json 2>/dev/null | jq --arg s "${3:-}" '[.[] | select($s == "" or .status == $s)] | length'; }
live_commit() {
  k build list "$1" "$2" -o json | jq -r '[.[] | select(.liveEnvs // [] | index("production"))][0].commitSha // ""'
}

ensure_project() {
  if project_exists "$1"; then skip "project $1"; else k project create "$1" --repo "$DEMO_REPO"; fi
}

# Retry a build trigger once: the shared buildkitd is sometimes busy with
# another stream's build and the first attempt times out.
build() {
  k build trigger "$@" -f || { echo "    build failed, retrying once"; k build trigger "$@" -f; }
}

# ---------------------------------------------------------------- shop
log "shop: storefront with api, web, worker, postgres, redis, cron, staging"
ensure_project shop
k apply "${dir}/shop.kuso.yml"

if k secret list shop api -o json | jq -e '.keys // [] | index("STRIPE_SECRET_KEY")' >/dev/null; then
  skip "secret STRIPE_SECRET_KEY"
else
  k secret set shop api STRIPE_SECRET_KEY "sk_test_51Seed$(openssl rand -hex 12)"
fi

# The worker reuses api's image with its own argv. Created through `kuso api`
# because the script needs commas and quotes that --command can't carry.
if service_exists shop worker; then
  skip "service worker"
else
  jq -n --rawfile script "${dir}/worker.sh" \
    '{name: "worker", runtime: "worker", fromService: "api", command: ["sh", "-c", $script],
      sleep: {enabled: false}}' >"${TMPDIR:-/tmp}/kuso-seed-worker.json"
  k api POST projects/shop/services --data "@${TMPDIR:-/tmp}/kuso-seed-worker.json" >/dev/null
  rm -f "${TMPDIR:-/tmp}/kuso-seed-worker.json"
fi

if [[ "$(build_count shop api succeeded)" -ge 1 ]]; then
  skip "first api build"
else
  build shop api --branch main
fi

# Deploy a commit that crashes on boot, then roll back to the build before it.
if [[ "$(build_count shop api succeeded)" -ge 2 ]]; then
  skip "crashing api build"
else
  build shop api --ref "$CRASH_REF"
  echo "    letting the crashing release run for 45s so restarts show up"
  sleep 45
fi
if [[ "$(live_commit shop api)" == "$CRASH_REF" ]]; then
  k build rollback shop api --previous --yes
else
  skip "rollback"
fi

if k cron list shop -o json | jq -e 'any(.[]; (.metadata.name // .name // "") | test("prune-carts"))' >/dev/null; then
  skip "cron prune-carts"
else
  k cron add shop api --name prune-carts --schedule '0 3 * * *' \
    --cmd 'sh -c "echo pruning carts older than 30 days; sleep 2; echo pruned 0 carts"'
fi

if k environment list shop -o json | jq -e 'any(.[]; .metadata.labels["kuso.sislelabs.com/env"] == "staging")' >/dev/null; then
  skip "staging environment"
else
  k environment add shop api staging --branch staging
fi
if [[ "$(k build list shop api -o json | jq '[.[] | select(.branch == "staging")] | length')" -ge 1 ]]; then
  skip "staging build"
else
  build shop api --env staging
fi

k uptime enable shop >/dev/null

alert_exists() { k alert list -o json | jq -e --arg n "$1" 'any(.[]; .name == $n)' >/dev/null; }
if alert_exists "Worker errors"; then skip "alert Worker errors"; else
  k alert create --kind log_match --name "Worker errors" --project shop --service worker \
    --query "ERROR" --severity error --window 5m --throttle 30m
fi
if alert_exists "Storefront 5xx"; then skip "alert Storefront 5xx"; else
  k alert create --kind http_5xx_rate --name "Storefront 5xx" --project shop --service web \
    --threshold 5 --window 5m
fi

# ---------------------------------------------------------------- blog
log "blog: HedgeDoc from the marketplace"
if project_exists blog; then
  skip "project blog"
else
  k marketplace deploy hedgedoc --project blog --set host=blog.kuso.localhost
fi

# ---------------------------------------------------------------- internal-tools
log "internal-tools: two image services and one failing build"
ensure_project internal-tools
k apply "${dir}/internal-tools.kuso.yml"
if [[ "$(build_count internal-tools reports)" -ge 1 ]]; then
  skip "reports build"
else
  # The branch doesn't exist, so this fails at clone. That's the point.
  k build trigger internal-tools reports --branch feature-csv-export -f || true
fi

# ---------------------------------------------------------------- instance
log "instance: notification channel, users, invite"
if k notifications list -o json | jq -e 'any(.[]; .name == "ops-webhook")' >/dev/null; then
  skip "notification channel ops-webhook"
else
  # hooks.example.com never answers; deliveries show as failing, like a
  # misconfigured receiver would.
  k notifications create webhook --name ops-webhook --url https://hooks.example.com/kuso \
    --events build.failed,build.succeeded,pod.crashed,alert.fired --secret seed-hmac-secret
fi

user_id() { k user list -o json | jq -r --arg u "$1" '.[] | select(.username == $u) | .id'; }

# maya: a viewer who has signed in.
if [[ -n "$(user_id maya)" ]]; then skip "user maya"; else
  k user create --username maya --email maya@cornershop.example --first-name Maya --last-name Petrova \
    --password "$SEED_PASSWORD"
  k user role "$(user_id maya)" --role viewer
  k api POST auth/login -F username=maya -F password="$SEED_PASSWORD" >/dev/null
fi

# dana: an editor account that has never signed in.
if [[ -n "$(user_id dana)" ]]; then skip "user dana"; else
  k user create --username dana --email dana@cornershop.example --first-name Dana --last-name Ilieva \
    --password "$SEED_PASSWORD"
  k user role "$(user_id dana)" --role editor
fi

if k invite list -o json | jq -e 'any(.[]; (.note | if type == "object" then .String else . end) == "Contractor: checkout redesign")' >/dev/null; then
  skip "invite"
else
  k invite create --role editor --expires-in 168h --note "Contractor: checkout redesign" >/dev/null
fi

log "done"
k get projects -o json | jq -r '.[].metadata.name' | sed 's/^/    project /'
