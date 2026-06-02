# Guaranteed replica spread across nodes

**Date:** 2026-06-02
**Status:** Design approved, pending spec review

## Problem

A node reboot (host patch, kernel update, hardware event, or a kuso
`apply-updates` with reboot) takes down every pod on that node. For a
multi-replica *stateless* service that's only an outage if **all** its
replicas happen to be on the rebooted node — which is exactly what
happened: tickero-frontend-production has 2 replicas and BOTH landed on
`tickero-node`, so rebooting that node took tickero.bg's frontend fully
down even though there was a second node available to serve from.

The intent ("2 replicas split across nodes so a reboot doesn't down the
app") is already half-built: the env deployment chart has a
topologySpreadConstraint. But it uses `whenUnsatisfiable: ScheduleAnyway`
— a SOFT preference. The scheduler tries to spread, but under any momentary
pressure (a rollout, a briefly-cordoned node, bin-packing) it freely
places both replicas on one node, and they never rebalance afterward.

## Goal

Multi-replica stateless services get their replicas placed on DISTINCT
nodes with a hard guarantee, so rebooting any one node leaves at least
one replica serving — on multi-node clusters. Single-node clusters must
still schedule all replicas (no Pending-forever).

## Non-goals

- HA for single-replica StatefulSet addons (tickero-db-0, cache-0,
  storage-0). They have no second replica; rebooting their node downs
  them regardless. That's an addon-replication topic, out of scope.
- Auto-scaling replica count to node count. The user's requested
  replicaCount is honored as-is; we only control PLACEMENT.
- Changing the reboot/drain mechanics (covered by the pkg-updates spec
  + the d072e56 uncordon fix). This spec is purely about placement so a
  reboot is survivable in the first place.

## Why this approach (rejected alternatives)

- **Server-decides hard-vs-soft at render time (chosen):** kuso-server
  already lists schedulable nodes (placement validation). It stamps a
  `spreadPolicy` onto the env CR — "hard" when >1 schedulable node,
  "soft" on a single node. Self-adjusts as nodes are added/removed.
  Multi-node clusters get real HA; single-node still schedules.
- **Always hard + cap replicas at node count (rejected):** caps the
  user's requested replicaCount (surprising), and needs more chart
  logic. Placement, not count, is the lever we want.
- **Per-service HA toggle (rejected):** another knob most users won't
  find. The DEFAULT behavior is what bit us; the fix should make the
  safe behavior automatic, not opt-in.

## Design

### 1. Env CR carries the policy

Add `SpreadPolicy string` to `KusoEnvironmentSpec`
(`server-go/internal/kube/types.go`), values `"hard" | "soft" | ""`.
Empty means unset → the chart defaults to soft (back-compat: existing
env CRs render exactly as today until kuso-server re-stamps them).

The operator-sdk helm-operator maps a CR's `spec` directly onto chart
`.Values` (spec.replicaCount → .Values.replicaCount, etc.), so the new
field surfaces as `.Values.spreadPolicy` with no extra wiring.

### 2. Server resolves the policy from live node count

`projects.resolveSpreadPolicy(ctx) string`:
- Counts SCHEDULABLE nodes (Ready, not unschedulable/cordoned) — reuse
  the cached node list the placement validator already pulls.
- `> 1` → `"hard"`, else `"soft"`.
- Any error / zero nodes → `"soft"` (fail safe: never strand a replica
  Pending because we couldn't read the node count).

Stamped onto the env CR `spec.spreadPolicy`:
- at `AddEnvironment` (env creation), and
- in the scale-propagation path (`propagateChangedToEnvs`, Scale
  branch) — spread only matters when replicaCount > 1, and that path
  already fires on every scale change. Setting it there means a service
  scaled from 1→2 gets hard spread on the same write that adds the
  replica.

Because it's recomputed on these writes, adding a 2nd node upgrades a
service to hard spread on its next scale/redeploy; dropping to one node
relaxes it. (We don't add a background re-stamp loop — that's YAGNI;
the next env write picks up the change, and a redeploy is the natural
trigger after a topology change.)

### 3. Chart honors the policy

`kusoenvironment/templates/deployment.yaml` topologySpreadConstraints
block (only emitted when replicaCount > 1 and no explicit node-pin,
unchanged):

```yaml
whenUnsatisfiable: {{ if eq (.Values.spreadPolicy | default "soft") "hard" }}DoNotSchedule{{ else }}ScheduleAnyway{{ end }}
```

`maxSkew: 1` + `topologyKey: kubernetes.io/hostname` stay. With
DoNotSchedule + maxSkew 1, two replicas cannot both land on one node
while a second node is available — the hard guarantee. Default "soft"
preserves today's behavior for any env CR kuso-server hasn't re-stamped
yet and for single-node clusters.

## Data flow

```
env write (AddEnvironment / scale change)
  └─ resolveSpreadPolicy(ctx): schedulable-node-count >1 ? "hard" : "soft"
     └─ stamp env CR spec.spreadPolicy
        └─ helm-operator renders deployment with
           whenUnsatisfiable = DoNotSchedule (hard) | ScheduleAnyway (soft)
           └─ scheduler places replicas on distinct nodes (hard)
```

## Edge cases

- **Single node:** resolveSpreadPolicy → "soft"; the 2nd replica
  schedules on the only node (no Pending). Correct.
- **replicaCount > node count** (e.g. 3 replicas, 2 nodes) with hard
  spread: maxSkew 1 allows 2-1 across the nodes; the 3rd schedules
  fine (skew within bound). No Pending. Only a replica that would
  exceed maxSkew with NO eligible node hangs — which can't happen at
  replicas ≤ 2× nodes, the realistic range.
- **Node cordoned for maintenance:** counts as not-schedulable →
  policy may relax to soft on the next write, which is the safe
  direction (don't wedge scheduling during maintenance).
- **Existing env CRs:** spreadPolicy unset → chart defaults soft →
  identical to today until re-stamped by a scale/redeploy. No flag day.

## Components touched

- `server-go/internal/kube/types.go` — `SpreadPolicy` field + getter/
  setter if the pattern needs it.
- `server-go/internal/projects/` — `resolveSpreadPolicy`; set at
  AddEnvironment + scale propagation.
- `operator/helm-charts/kusoenvironment/templates/deployment.yaml` —
  conditional whenUnsatisfiable.
- Ships in BOTH the server image (stamping) AND the operator image
  (chart render). CRD schema unchanged if the field rides under an
  existing preserve-unknown-fields spec; otherwise a CRD apply is
  needed — verify during implementation.

## Testing

- **Chart render** (helm template): spreadPolicy=hard → DoNotSchedule;
  unset/soft → ScheduleAnyway; replicaCount=1 → no constraint block.
- **resolveSpreadPolicy** (fake node list): 2 ready nodes → "hard";
  1 node → "soft"; 1 ready + 1 cordoned → "soft"; list error → "soft".
- **Live:** after the change, scale/redeploy tickero frontend+api and
  confirm the 2 replicas land on separate nodes (and that a single-node
  cluster still schedules both).

## Related / also shipping

- `d072e56` (committed, unshipped): the uncordon-self-heal that stops a
  reboot-apply from leaving a node cordoned — what made the recent
  outage PERSIST. Should ship alongside this.
- Worth a follow-up (not this spec): gate the reboot-apply UI action on
  the control-plane node behind an extra confirmation, since rebooting
  it is uniquely disruptive (kuso-server itself goes down).
