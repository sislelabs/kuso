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
#   rbac       editors can't set code-loading env names or the command;
#              another project's apex host can't be claimed
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

ALL_GROUPS=(core isolation backup preview sleep registry rbac)
groups="${SMOKE:-core}"
[[ "$groups" == "all" ]] && groups="$(IFS=,; echo "${ALL_GROUPS[*]}")"

node_running || die "sandbox ${KUSO_SANDBOX_NAME} is not running; run 'make sandbox-up'"
command -v jq >/dev/null || die "jq is required"
"$K" login --api "$SANDBOX_API_URL" -u admin -p "$SANDBOX_ADMIN_PASSWORD" >/dev/null

for g in ${groups//,/ }; do
  [[ -f "${SANDBOX_DIR}/smoke/${g}.sh" ]] || die "unknown smoke group: ${g} (have: ${ALL_GROUPS[*]})"
done

# A node under pressure evicts pods and schedules none, so every check
# after it would sit out its full timeout. Stop before the group instead.
node_pressure() {
  node_kubectl get nodes -o jsonpath='{range .items[*].spec.taints[*]}{.key}{"\n"}{end}' 2>/dev/null |
    grep -E 'node.kubernetes.io/(disk|memory|pid)-pressure' | sort -u
}

for g in ${groups//,/ }; do
  if p="$(node_pressure)" && [[ -n "$p" ]]; then
    die "sandbox node is tainted ${p//$'\n'/, }; free Docker disk (make sandbox-doctor) and rerun SMOKE=${g}"
  fi
  run_group "$g"
done

summary
