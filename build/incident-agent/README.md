# kuso-incident-agent

The container image the autonomous incident-response agent runs in. One
short-lived Job per incident phase (`investigate` / `implement`), spawned by
`server-go/internal/incidents`. See the design at
`docs/superpowers/specs/2026-06-10-incident-agent-design.md`.

## What's in the image

Base `node:20-bookworm-slim` (Claude Code is an npm package + needs real Node):

- **Claude Code CLI** (`@anthropic-ai/claude-code`) — the agent runtime,
  driven head-less with `claude -p`.
- **kuso CLI** (`/usr/local/bin/kuso`) — the documented investigation contract
  (logs/status/env/builds). Baked from the GitHub release artifact
  `kuso-linux-<arch>` (or a local build, see below).
- **kubectl** — read-only fallback (the investigate SA has read-only RBAC).
- **git + gh + curl + jq + bash** — repo clone/push (implement phase) and the
  curl/jq the prompts use to POST findings / PR requests.

Runs as the unprivileged `node` user (uid 1000) with `HOME=/home/node`.

## Build

```sh
# Default: download the kuso CLI from the GitHub release matching the tag.
docker build \
  --build-arg KUSO_CLI_VERSION=v0.18.54 \
  --build-arg TARGETARCH=amd64 \
  -t ghcr.io/sislelabs/kuso-incident-agent:v0.18.54 \
  build/incident-agent

# Dev: bake a locally cross-built CLI instead of downloading. Stage the
# binary somewhere under the build context and point KUSO_CLI_LOCAL at it
# (path is relative to the context root):
( cd cli && GOOS=linux GOARCH=amd64 go build -o ../build/incident-agent/kuso-linux-amd64 ./cmd )
docker build \
  --build-arg KUSO_CLI_LOCAL=kuso-linux-amd64 \
  -t ghcr.io/sislelabs/kuso-incident-agent:dev \
  build/incident-agent
```

`make incident-agent-image` (repo-root Makefile) is the canonical build:
it cross-builds the CLI into the context, then `docker buildx build --push`
for linux/amd64 + linux/arm64. The manual invocations above are for
one-off debugging.

## Claude Code credentials secret

The agent authenticates to Claude with the **operator's** Claude Code OAuth
session (the design's deliberate tradeoff: the operator's personal CC token
lives in-cluster; it is rotatable/revocable). Stored in a Secret the Job
projects read-only at `/cc/credentials.json`; the entrypoint copies it into
`~/.claude/.credentials.json` (writable, so CC can refresh the token).

Create it from the operator's local creds (the `kuso incident-agent
set-credentials` CLI helper automates this — it reads
`~/.claude/.credentials.json` and uploads it):

```sh
kubectl create secret generic kuso-incident-agent-cc \
  -n kuso \
  --from-file=credentials.json="$HOME/.claude/.credentials.json"
```

Secret name `kuso-incident-agent-cc`, key `credentials.json`. Rotate by
re-creating the Secret; revoke by deleting it (the next Job then fails fast at
startup with "credentials not found").

## RBAC

`deploy/incident-agent-rbac.yaml` defines the `kuso-incident-agent`
ServiceAccount (namespace `kuso`) + a **read-only** ClusterRole/Binding
(pods, pods/log, nodes, events — get/list/watch only). The investigate Job
runs as this SA; its read-only-ness is the structural half of the "human gate
before any write" guardrail. Apply it on the cluster (the image auto-updater
does NOT apply RBAC):

```sh
kubectl apply -f deploy/incident-agent-rbac.yaml
```

The **implement** Job does not need cluster write access — it changes a
project repo via git, not the cluster — so it can run as the same read-only SA
(or `default`); the Go Job-builder decides.

## Env + volume contract (what the Go Job-builder MUST provide)

The entrypoint (`entrypoint.sh`) reads the following. Keep this table in sync
with `internal/incidents` `jobs.go` — it is the wire contract between the
Job-builder and the image.

### Common (both phases)

| Env                  | Required | Meaning |
| -------------------- | -------- | ------- |
| `PHASE`              | yes      | `investigate` or `implement`. |
| `INCIDENT_ID`        | yes      | Incident id (`inc-xxxx`). Used in prompts + the POST URLs. |
| `KUSO_API_URL`       | yes      | Base URL of kuso-server, e.g. `http://kuso-server.kuso.svc.cluster.local`. |
| `INCIDENT_TOKEN`     | yes      | Per-incident bearer for the agent-facing endpoints (`/findings`, `/pr`). Single-incident scope. Source: `Incident.agentToken`. |
| `KUSO_TOKEN`         | yes      | Project-scoped kuso CLI token (viewer + sql). The CLI + the curl calls read it. |
| `EVENT_TYPE`         | yes      | `pod.crashed` \| `alert.fired` \| `node.unreachable`. |
| `PROJECT`            | yes      | Incident project. |
| `SERVICE`            | yes      | Incident service (or node name for `node.unreachable`). |
| `SEVERITY`           | yes      | Incident severity. |
| `INCIDENT_TITLE`     | yes      | Human title (falls back to `TITLE`). |
| `CONTEXT_PACK`       | yes\*    | The `Incident.contextPack` JSON, inline. |
| `CONTEXT_PACK_FILE`  | no       | Path to a mounted file holding the context pack (ConfigMap). Takes precedence over `CONTEXT_PACK`; use this for large packs to avoid env-size limits. |
| `FEEDBACK`           | no       | Accumulated operator feedback (JSON array or text block). Substituted into both prompts. Defaults to `(none)`. |
| `PROMPTS_DIR`        | no       | Override prompt dir (default `/opt/incident-agent/prompts`). |
| `CC_CREDS_SRC`       | no       | Override CC creds path (default `/cc/credentials.json`). |

\* Provide `CONTEXT_PACK` inline **or** `CONTEXT_PACK_FILE`; at least one.

### Implement phase only

| Env                   | Required | Meaning |
| --------------------- | -------- | ------- |
| `FINDINGS`            | yes\*\*  | The investigate-phase writeup (fed into the implement prompt). |
| `FINDINGS_FILE`       | no       | Path to a mounted file holding the findings (precedence over `FINDINGS`). |
| `REPO_OWNER`          | yes      | GitHub repo owner. |
| `REPO_NAME`           | yes      | GitHub repo name. |
| `REPO_DEFAULT_BRANCH` | no       | Base branch to branch from / PR against (default `main`). |
| `GIT_TOKEN`           | yes      | Short-lived, repo-scoped GitHub **installation** token. Used to clone + push over a git credential helper (never logged, never in `.git/config`). Mint via `github.Client.MintInstallationToken`. |
| `FIX_BRANCH`          | no       | Branch name for the fix (default `kuso-incident-${INCIDENT_ID}`). |

\*\* Provide `FINDINGS` inline **or** `FINDINGS_FILE`.

### Volumes

| Mount                            | Required | Source |
| -------------------------------- | -------- | ------ |
| `/cc/credentials.json`           | yes      | Secret `kuso-incident-agent-cc`, key `credentials.json`, **read-only**. |
| `CONTEXT_PACK_FILE` path         | optional | ConfigMap (per-incident context pack), read-only. |
| `FINDINGS_FILE` path             | optional | ConfigMap (implement phase findings), read-only. |

### ServiceAccount

- **investigate Job**: run as `kuso-incident-agent` (read-only RBAC). kubectl
  picks up the mounted SA token automatically (in-cluster config).
- **implement Job**: no cluster mutation needed; `kuso-incident-agent` or
  `default` both work.

## Agent-facing API endpoints (what the prompts POST to)

Authenticated with `INCIDENT_TOKEN` (single-incident bearer):

- `POST ${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/findings` — body
  `{"findings": "<markdown>"}`. Investigate phase; server moves the incident
  to `awaiting_feedback` and the bot posts to Discord.
- `POST ${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/pr` — body
  `{"branch": "...", "title": "...", "body": "..."}`. Implement phase; the
  SERVER opens the PR via the GitHub App (the agent already pushed the branch
  with `GIT_TOKEN`).

## Safety notes

- **Investigate is physically read-only** — enforced by the
  `kuso-incident-agent` RBAC, not by trusting the prompt.
- **No direct push to the default branch** — the implement phase only ever
  commits/pushes a fresh `FIX_BRANCH`; the PR is opened server-side and merged
  by a human (or a second explicit "go").
- **`GIT_TOKEN` is short-lived + repo-scoped** and injected via a git
  credential helper so it never lands in args, `.git/config`, or logs.
- **`claude -p` runs with `--dangerously-skip-permissions`** because there is
  no interactive approver in a Job; the sandbox is the read-only SA
  (investigate) + the per-branch clone + PR-only flow (implement), per the
  design's guardrails — NOT CC's interactive gate.
