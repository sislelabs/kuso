# kuso-incident-agent

The container image the autonomous incident-response agent runs in. One
short-lived Job per incident phase (`investigate` / `implement`), spawned by
`server-go/internal/incidents`. See the design at
`docs/superpowers/specs/2026-06-10-incident-agent-design.md`.

## What's in the image

Base `node:24-bookworm-slim` (Claude Code is an npm package + needs real Node):

- **Claude Code CLI** (`@anthropic-ai/claude-code`) — the agent runtime,
  driven head-less with `claude -p`.
- **kuso CLI** (`/usr/local/bin/kuso`) — the documented investigation contract
  (logs/status/env/builds). Baked from the GitHub release artifact
  `kuso-linux-<arch>` (or a local build, see below).
- **kubectl** — read-only fallback (the investigate SA has read-only RBAC).
- **git + gh + curl + jq + bash** — repo clone/push/PR (implement phase) and the
  curl/jq the entrypoint uses to POST findings / PR results.

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
for linux/amd64. The manual invocations above are for
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
| `PHASE`              | yes      | `investigate` or `implement` (`refresh` is the keep-alive mode the `kuso-incident-agent-refresher` CronJob runs). |
| `INCIDENT_ID`        | yes      | Incident id (`inc-xxxx`). Used in prompts + the POST URLs. |
| `KUSO_API_URL`       | yes      | Base URL of kuso-server, e.g. `http://kuso-server.kuso.svc.cluster.local`. |
| `INCIDENT_TOKEN`     | yes      | Per-incident bearer for the agent-facing endpoints (`/findings`, `/pr`). Single-incident scope. Source: `Incident.agentToken`. Arrives as the file `$SECRETS_DIR/incident-token` (the Job's init container copies it from the per-Job Secret; the entrypoint reads then deletes it), so it is never in the container env. The env form is still accepted from older Jobs. |
| `KUSO_TOKEN`         | no       | Project-scoped kuso CLI token (viewer + sql). Empty in v1 (`jobs.go` sets it to `""`), so the agent investigates via kubectl with its read-only SA. |
| `LOG_NAMESPACE`      | no       | The one namespace the agent may read pod logs in (default `kuso`). |
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
| `GIT_TOKEN`           | yes      | Short-lived, repo-scoped GitHub **installation** token. Used to clone + push over a git credential helper (never logged, never in `.git/config`). Mint via `github.Client.MintInstallationToken`. Arrives as `$SECRETS_DIR/git-token`, like `INCIDENT_TOKEN`. |
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

## Agent-facing API endpoints (posted by the entrypoint, not the agent)

The agent writes its output to files in `$OUT_DIR` (`/tmp/kuso-out`); the
entrypoint posts them with `INCIDENT_TOKEN` (single-incident bearer) after
`claude` exits. Neither `INCIDENT_TOKEN` nor `GIT_TOKEN` is in the agent's
environment or in the container's (`/proc/1/environ`). The agent's `git` is
`git-guard.sh`: no system/global config, hooks/fsmonitor/credential helpers
pinned off, and it refuses to run in a checkout whose `.git/config` holds
anything beyond what `git clone` writes.

The operator's Claude credentials (`~/.claude/.credentials.json`) are still
readable by any process the agent starts, since `claude` itself needs them
under the same uid. That is why `KUSO_INCIDENT_AGENT_RUN_REPO_CODE` stays off.

- `POST ${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/findings` — body
  `{"findings": "<markdown>"}` from `findings.md`. Investigate phase; the
  server moves the incident to `awaiting_feedback` and the bot posts to Discord.
- `POST ${KUSO_API_URL}/api/incidents/${INCIDENT_ID}/pr` — body
  `{"prUrl": "...", "prNumber": N}` after the entrypoint pushes the fix
  branch and opens the PR with `gh` (title/body from `pr.json`), or
  `{"note": "..."}` when there is nothing to push (`blocked.md`, no commits,
  no repo wired).

## Safety notes

- **Investigate is read-only** — enforced by the `kuso-incident-agent` RBAC,
  not by trusting the prompt. Pods and their logs are readable only in the
  incident's namespace: kuso-server binds `kuso-incident-agent-logs` there at
  spawn time and the incident reaper unbinds it once no agent needs it.
  `kubectl` is a wrapper that refuses `--server`, `--kubeconfig`, `--token`,
  `--raw` and the other flags that would send the SA token elsewhere. The
  implement phase gets no ServiceAccount token at all.
- **Explicit tool allowlist** — `claude -p` runs with `--allowedTools`
  (read-only kubectl verbs for investigate; file edits and local git for
  implement), never `--dangerously-skip-permissions`. The Claude credentials,
  the SA token and `/proc` are on `--disallowedTools`. Build/test toolchains
  (`npm run`, `make`, `go test`, ...) are added to the implement allowlist
  only when kuso-server runs with `KUSO_INCIDENT_AGENT_RUN_REPO_CODE=true`:
  they run repo code as the user that can read the Claude credentials.
- **Untrusted input is fenced** — the context pack (which carries a crashing
  pod's log tail) is capped server-side, stripped of control characters, and
  wrapped in a per-run random tag the prompt declares as data. The implement
  phase never sees the context pack, only the approved findings (also fenced).
- **`GIT_TOKEN` is short-lived and scoped to the incident's one repo**
  (`contents:write` + `pull_requests:write`), delivered through a per-Job
  Secret owned by the Job. The entrypoint pushes from a fresh clone under a
  clean `HOME`, so agent-written `.git/config` or hooks never run with it.
  Build/test commands run repo code as the same user, so this bounds rather
  than eliminates what a hijacked agent can reach.
- **Egress is restricted** — a per-Job NetworkPolicy allows DNS, kuso-server,
  the kube-apiserver (:6443, investigate only) and HTTPS to public addresses
  only (no RFC1918, CGNAT or link-local). If the policy can't be created the
  Job is deleted and the agent never starts.
- **No direct push to the default branch** — only a fresh `FIX_BRANCH` is
  pushed; a human merges the PR.
