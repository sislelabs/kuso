# shellcheck shell=bash
# Which deploy/*.yaml the sandbox applies and which it leaves out.
# check-drift.sh fails when a file in deploy/ is in neither list, so a new
# manifest forces a decision here.

SANDBOX_APPLIED=(
  registry
  buildkitd
  operator
  server-go
  kuso-activator
)

SANDBOX_SKIPPED=(
  postgres                  # CNPG cluster; the sandbox runs a plain postgres (postgres.yaml here)
  postgres-backup           # needs S3
  prometheus                # optional; only feeds the request/error metric cards
  cluster-issuer            # no cert-manager, everything is plain HTTP
  pkg-probe                 # privileged hostPID DaemonSet that runs apt on the host
  incident-agent-rbac       # incident agent is not part of the sandbox
  incident-agent-refresher
  incident-bot
  review-ingress            # not applied by install.sh either
  hello-world               # sample CRs, not platform
)
