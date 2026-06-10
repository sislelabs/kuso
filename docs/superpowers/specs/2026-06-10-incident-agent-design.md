# Autonomous Incident-Response Agent — design

**Date:** 2026-06-10
**Status:** approved, implementing (full feature)

## Goal

When an operational incident fires (pod crash, alert threshold crossing, node down),
kuso automatically spawns an AI agent (`claude -p`, using the operator's Claude Code
subscription) that investigates, posts findings to a Discord thread, takes the
operator's free-text/approve feedback, and — on "go" — implements a fix and opens a
PR to the relevant project repo. Human-in-the-loop throughout; every code change is a
reviewable PR.

Single-tenant scope (one team per cluster). The agent acts with the same blast radius
as the operator debugging by hand.

## What already exists (build on)

- **Incident detection** (all emit `notify.Event`):
  - `health.Watcher` (`internal/health/health.go`) — CrashLoopBackOff / OOMKilled /
    ImagePullBackOff → `EventPodCrashed` + `failures.Classify` (kind, tab hint, summary).
  - `alerts.Engine` (`internal/alerts/engine.go`) — node_cpu/mem/disk + log_match
    thresholds → `EventAlertFired`.
  - `nodewatch.Watcher` — NotReady > 5min → `EventNodeUnreachable`.
- **notify.Dispatcher** (`internal/notify/`) — persists every event + fans out to
  Discord/Slack/webhook/etc. Outbound only. `SetLeaderHook` gates fan-out on the
  leader; we mirror it with a new `SetEventHook`.
- **github.Client** (`internal/github/`, uses go-github) — `MintInstallationToken`,
  `PostPRComment`, `GetFile`, `ResolveBranchSHA`. The GitHub App ALREADY has
  `Contents: Read & write` + `Pull requests: Read & write` (GITHUB_APP_SETUP.md /
  install.sh) — so PR creation needs NO permission change, just new client methods.
- **In-cluster Job spawn pattern** — `pkgupdates.Apply` (`internal/pkgupdates/apply.go`):
  deterministic Job name for dedup, `BatchV1().Jobs().Create`, TTL cleanup,
  leader-gated watcher. The incident runner reuses this verbatim.
- **kuso CLI** — the documented investigation contract (logs/status/env/builds/shell).

## Net-new components

### 1. Incident lifecycle + DB (`internal/incidents/`, new `Incident` table)

State machine, one row per incident:

```
investigating → awaiting_feedback → implementing → pr_open → resolved
                      │  (text feedback loops back to investigating)
                      └→ rejected / dropped
```

`Incident` table:
```
id              text pk
event_type      text   -- pod.crashed | alert.fired | node.unreachable
project         text
service         text   -- or node name for node.unreachable
target_key      text   -- dedup key: eventType|project|service
state           text
title           text
severity        text
context_pack    jsonb  -- the incident payload handed to the agent
findings        text   -- agent's investigation writeup (markdown)
feedback        jsonb  -- array of {at, text} operator messages
discord_thread  text   -- channel/thread id the bot owns
pr_url          text
pr_number       int
investigate_job text   -- Job name (dedup / status)
implement_job   text
created_at, updated_at timestamptz
```

`incidents.Manager`:
- Registers a `notify.SetEventHook(fn)` callback. On the 3 event types, computes
  `target_key`; if an OPEN incident exists for that key → append to `feedback`/refresh
  the Discord thread (no new agent). Else (and under the global concurrency cap, default
  3) → create an Incident row + spawn the investigate Job.
- Leader-gated (only one replica spawns).
- Cooldown: after an incident closes, the same target_key won't auto-spawn for 1h.

### 2. Agent runner + image

- **Image** `ghcr.io/sislelabs/kuso-incident-agent`: Claude Code CLI + kuso CLI +
  kubectl (read-only RBAC SA) + git + gh. Built in `build/incident-agent/`.
- **CC credentials**: the operator's Claude Code OAuth creds stored in a k8s Secret
  `kuso-incident-agent-cc` (key `credentials.json` → mounted at `~/.claude/`). Created
  out-of-band by the operator (a `kuso incident-agent set-credentials` CLI helper reads
  the local `~/.claude/.credentials.json` and uploads it). TRADEOFF: the operator's
  personal CC session token lives in-cluster; rotatable/revocable; documented.
- **Investigate Job** (`incidents.buildInvestigateJob`): name
  `kuso-incident-<id>-investigate`, mounts the CC secret + a per-incident context-pack
  (ConfigMap), runs `claude -p "<investigation prompt>"`. The prompt hands the agent:
  the incident summary + classification, the project/service, and instructions to use
  the kuso CLI (then kubectl) to root-cause, then `POST /api/incidents/{id}/findings`
  with its writeup. RBAC: a dedicated `kuso-incident-agent` SA with read-only cluster
  access (pods/nodes/events get+list+watch, logs) + kuso API token scoped to the
  project.
- **Implement Job** (`incidents.buildImplementJob`): name
  `kuso-incident-<id>-implement`, same image/creds, runs `claude -p "<implement
  prompt + accumulated feedback>"`. The agent clones the repo, writes the fix, and
  calls `POST /api/incidents/{id}/pr` with branch+title+body; the SERVER opens the PR
  via github.Client (agent doesn't hold git push creds — keeps the GitHub App as the
  single push identity). Agent validates locally (build/tests) before requesting the PR.

### 3. github.Client PR methods (`internal/github/pr_create.go`)

New methods (go-github, installation token):
- `CreateBranch(ctx, instID, owner, repo, fromBranch, newBranch) (sha, error)`
- `CommitFiles(ctx, instID, owner, repo, branch, baseSHA, []FileChange{path,content,delete}, message) (commitSHA, error)`
  — via the Git Data API (CreateBlob → CreateTree → CreateCommit → UpdateRef).
- `OpenPR(ctx, instID, owner, repo, head, base, title, body) (url, number, error)`
- `MergePR(ctx, instID, owner, repo, number, method) error`

The implement flow: agent pushes commits to its branch directly with the
installation token (git clone `https://x-access-token:<token>@github.com/...`), then
asks the server to OpenPR. (Simpler than server-side blob commits for a multi-file
change; the token is short-lived + repo-scoped.) `MergePR` fires when the operator
says "go" a second time on the PR.

### 4. Incident HTTP API (`internal/http/handlers/incidents.go`)

```
GET    /api/incidents                      list (UI feed)
GET    /api/incidents/{id}                 detail
POST   /api/incidents/{id}/findings        agent → writeup (state→awaiting_feedback, bot posts)
POST   /api/incidents/{id}/feedback        bot → {text} | {decision:"go"|"reject"}
POST   /api/incidents/{id}/pr              agent → {branch,title,body} → server OpenPR
POST   /api/incidents/{id}/resolve         operator/bot → close
```
Agent-facing endpoints auth via a per-incident bearer token (minted into the Job env,
single-incident scope). Operator endpoints use the normal session + `settings:admin`.

### 5. Discord bot bridge (`build/incident-bot/`, separate deployment)

A small Go (discordgo) bot deployed in-cluster (`deploy/incident-bot.yaml`):
- On incident `findings`: server tells the bot (via an internal endpoint or the bot
  polls `/api/incidents?state=awaiting_feedback`) to create/post in a thread.
- Listens to the incident thread: a plain message → `POST .../feedback {text}`;
  ✅ reaction or "go" → `{decision:"go"}`; ❌ or "reject" → `{decision:"reject"}`.
- Posts agent findings + PR links back into the thread.
- Bot token in a Secret; leader-gated singleton (one bot connection).

## Data flow (happy path)

1. `health.Watcher` emits `EventPodCrashed` → `notify.Emit` persists + calls the
   incidents hook.
2. `incidents.Manager` (leader) creates Incident{state:investigating} + investigate Job.
3. Agent investigates via kuso CLI/kubectl → `POST /findings`. State→awaiting_feedback.
   Bot opens a Discord thread with the findings.
4. Operator replies "actually it's the migration lock" → bot → `POST /feedback{text}`
   → Manager appends feedback, re-spawns investigate Job with the added context.
5. Operator replies "go" → `POST /feedback{decision:go}` → Manager spawns implement Job.
6. Agent clones, fixes, validates, pushes branch, `POST /pr` → server OpenPR → PR link
   to Discord. State→pr_open.
7. Operator merges in GitHub (or "go" again → server MergePR). State→resolved.

## Safety / guardrails

- **Human gate before any write**: no PR is opened without an explicit "go". The
  investigate phase is read-only (RBAC SA has no write verbs; kuso token is viewer+sql).
- **PR, never direct push to default branch**: every fix is a reviewable branch+PR.
- **Concurrency cap + dedup + cooldown**: bounds CC-sub usage and agent swarm.
- **Per-incident scoped tokens**: the agent's kuso API token and incident bearer are
  single-incident; the git token is the short-lived installation token, repo-scoped.
- **Audit**: every state transition + agent action logs via the audit service
  (`incident.*` actions).
- **Kill switch**: `KUSO_INCIDENT_AGENT=true` env gates the whole subsystem off by
  default; a global `enabled` setting + per-event-type toggles in settings.

## Build order (dependency-correct; each lands working)

1. `Incident` table + migrations + `internal/incidents` Manager skeleton (no Job yet) +
   HTTP API + the `notify.SetEventHook`. Unit-tested with a fake hook.
2. `github.Client` PR methods + tests (CLI-exercisable independent of the agent).
3. Agent image (`build/incident-agent/`) + RBAC SA + investigate Job spawn + context-pack
   + `/findings`. End-to-end investigate→findings (no Discord yet; findings visible via API).
4. Discord bot (`build/incident-bot/`) + `/feedback` + thread management. Full read loop.
5. Implement Job + `/pr` (server OpenPR) + MergePR-on-second-go. Full write loop.
6. kuso CLI: `kuso incident list/show/resolve` + `incident-agent set-credentials`.
   Settings UI: enable/disable + per-event toggles + incident feed.

## Out of scope (v1)

Non-Discord chat backends, auto-merge without any human "go", multi-cluster, the agent
modifying kuso's own infra (it only touches project repos + suggests kuso API actions in
findings), learning/memory across incidents.

## Testing

- `incidents.Manager`: dedup (one-open-per-target), cooldown, concurrency cap, state
  transitions — pure/fake-Job unit tests.
- `github` PR methods: against go-github with a recorded/mock transport.
- HTTP handlers: auth gating (agent token scope, operator admin), state-transition
  validation.
- Bot: message→feedback mapping unit-tested; gateway integration manual.
- Live: a real pod-crash incident on the test cluster end-to-end before enabling by
  default.
