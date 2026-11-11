#!/usr/bin/env bash
#
# End-to-end checks against a running sandbox, in scenario groups:
#
#   core       project → dockerfile + nixpacks builds → live HTTP → postgres
#              addon via an env ref → rebuild → rollback → delete (default)
#   isolation  cross-project and staging→production traffic is refused,
#              same-env traffic works
#   backup     postgres backup now → restore in place and into a sibling;
#              s3 addon snapshot → restore
#   preview    staging env + signed PR webhook → preview built on staging's
#              config; PR close → reclaimed
#   sleep      idle env scales to zero; a request wakes it (timed)
#   registry   anonymous push/pull refused; a build pushes; digest on the env
#
#   SMOKE=isolation,backup make sandbox-smoke     pick groups
#   SMOKE=all make sandbox-smoke                  every group
#   KEEP=1 ...                                    leave what a group made
#
# Each group cleans up after itself and prints PASS/FAIL per check. Only
# ever talks to the sandbox: the API URL comes from lib.sh, not the
# environment, and the CLI runs through ./kuso (its own HOME).

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
source "${SANDBOX_DIR}/smoke/common.sh"

ALL_GROUPS=(core isolation backup preview sleep registry)
groups="${SMOKE:-core}"
[[ "$groups" == "all" ]] && groups="$(IFS=,; echo "${ALL_GROUPS[*]}")"

node_running || die "sandbox ${KUSO_SANDBOX_NAME} is not running; run 'make sandbox-up'"
command -v jq >/dev/null || die "jq is required"
"$K" login --api "$SANDBOX_API_URL" -u admin -p "$SANDBOX_ADMIN_PASSWORD" >/dev/null

for g in ${groups//,/ }; do
  [[ -f "${SANDBOX_DIR}/smoke/${g}.sh" ]] || die "unknown smoke group: ${g} (have: ${ALL_GROUPS[*]})"
done

for g in ${groups//,/ }; do
  run_group "$g"
done

summary
