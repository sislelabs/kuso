# Host package-update advisory + patch orchestration

**Date:** 2026-06-01
**Status:** Design approved, pending spec review

## Problem

kuso has no visibility into host-OS package updates. A Coolify-style
advisory — "N package updates available, M may require restarts" — is
absent. Operators have no in-platform signal that the underlying nodes
(k3s hosts) have pending security/package updates, and no way to apply
them without manually SSHing each node.

This matters because the kuso nodes run everything: the control-plane
Postgres, the registry, every project's pods. Stale host packages
(containerd, docker, kernel) are a real security + stability gap, and
today nothing surfaces it.

## Goals

1. **Advisory:** detect available host package updates per node and
   surface them (count, restart-needed flag, sample packages,
   last-checked) on the nodes page.
2. **Notify:** emit a notification when a node gains a fresh advisory —
   informational (`warn`), not a page.
3. **Apply:** an admin action to apply the updates on a node, including
   full reboot orchestration (cordon → drain → reboot → rejoin →
   uncordon) when a restart is required.

## Non-goals

- Patching anything other than OS packages (k3s/kuso self-update is the
  existing updater's job).
- Auto-applying updates on a schedule. Apply is always operator-initiated.
- Package managers beyond apt in v1 (structured to add apk/dnf later).
- Pinning/holding specific packages, or partial/selective upgrades —
  apply does a full `apt-get upgrade` (held-back-safe, not dist-upgrade).

## Why these choices (rejected alternatives)

- **DaemonSet probe vs. SSH poll:** kuso deliberately avoids standing
  SSH (nodejoin only SSHes during join/remove). A privileged DaemonSet
  that `nsenter`s the host is pure-k8s, multi-node-native, needs no
  persistent node credentials, and mirrors how real node-exporters
  work. SSH-poll was rejected: it forces kuso to hold node SSH creds
  persistently (a security posture change) and breaks on nodes without
  sshd.
- **Node annotation vs. API POST vs. DB:** the advisory is node-scoped,
  self-cleaning (node deleted → annotation gone), survives kuso-server
  restarts, and needs no probe→server auth. The annotation pattern
  matches nodewatch's existing cordon marker. An API POST would need
  the probe to authenticate + a new endpoint + a DB table for what is
  fundamentally ephemeral per-node state.
- **apt-first, pluggable:** apt is by far the most common k3s host (and
  the production cluster is Ubuntu/Hetzner). The probe detects the pkg
  manager and switches on it; unrecognized OS → reports `unsupported`,
  never a false alarm.

## Architecture

Four components, mirroring the existing node subsystems
(`nodewatch`, `nodemetrics`, `nodeshape`).

### 1. Probe DaemonSet (`deploy/pkg-probe.yaml`)

- One privileged pod per node, pinned via the standard DaemonSet
  spread, tolerating control-plane taints.
- On a timer (default ~6h, configurable), it:
  1. `nsenter`s into the host PID/mount namespace.
  2. Detects the package manager (`apt` in v1; switch structured for
     `apk`/`dnf` later). Unknown → writes `pkgMgr: "unsupported"`.
  3. Runs the upgradable check (apt: `apt-get -s upgrade` parse or
     `/usr/lib/update-notifier/apt-check`; counts upgradable packages).
  4. Reads `/var/run/reboot-required` for the reboot-needed signal.
  5. Writes the result to its OWN node's annotation (the probe knows
     its node via the downward API `spec.nodeName`).
- Writes **only** to its own node — no cluster-wide list, minimal RBAC
  (patch on the one node it runs on; a small Role/ClusterRole gives
  `nodes patch`, scoped as tightly as kube allows).

**Annotation shape** — `kuso.sislelabs.com/pkg-updates`:
```json
{
  "count": 7,
  "rebootRequired": true,
  "pkgMgr": "apt",
  "sample": ["containerd.io 2.2.3→2.2.4", "docker-ce 29.4.3→29.5.2"],
  "checkedAt": "2026-06-01T08:00:00Z"
}
```
Sample capped at ~5 entries (UI shows "…and N more") to keep the node
object small.

### 2. `internal/pkgupdates` (server goroutine + domain service)

- Started by `main`, cancel-on-context like `nodemetrics`.
- Timer (default ~6h) lists nodes, parses the annotation into an
  in-memory per-node view, exposes it via a domain service the HTTP
  handler consumes.
- **Edge-triggered notify:** emits a `warn`-severity notify event only
  when a node's advisory is *newer* than last-seen, deduped on
  `checkedAt`. Last-notified `checkedAt` per node is stored in the
  `Setting` kv table so a kuso-server restart does NOT re-alert an
  already-seen advisory. (This is the explicit fix for the
  per-restart-spam class we just hit with the backup alert: `warn`
  severity → `notify.mentionFor` does not default to `@here`.)
- Severity: a fresh advisory is `warn` (informational). A failed
  *apply* is `error` (`@here`) — a half-patched node is real.

### 3. Nodes-page surface

Per node, read-only:
- update count + a "restart needed" badge when `rebootRequired`.
- sample package list (name old→new), last-checked relative time.
- `unsupported`/`never-checked` states render plainly (no alarm).
- An **Apply patches** action (admin-gated) with a confirm dialog that
  surfaces "this will reboot the node" when a restart is required.

### 4. Apply orchestrator

`POST /api/admin/nodes/{node}/apply-patches` (admin-only). Request
carries `allowReboot: bool`.

Vehicle: a one-shot **privileged Job** pinned to the target node
(`nodeName` + toleration), `nsenter`-ing the host — NOT an exec into
the standing DaemonSet pod, because the operation must outlive the API
(the node may reboot under it).

Steps:
1. **Pre-flight:** re-check upgradable; nothing to do → exit clean.
   Snapshot `dpkg --get-selections` for the audit trail.
2. **Patch:** `apt-get -y upgrade` (held-back-safe; NOT dist-upgrade).
   Capture output.
3. **Reboot branch** — only if `/var/run/reboot-required` exists after
   patching AND `allowReboot` is true:
   - kuso-server **cordons** the node before launching the Job (so
     scheduling stops immediately), stamping the ownership marker
     `kuso.sislelabs.com/cordoned-by-pkgupdates`.
   - **drain:** evict pods respecting PDBs, skip DaemonSets. Multi-node
     → workloads reschedule. **Single-node → drain is best-effort /
     skipped** (nowhere to move; documented; the reboot is not blocked
     on an impossible drain).
   - **reboot:** `nsenter … systemctl reboot`, fired **detached**
     (`setsid`) so the Job pod dying with the node doesn't abort it.
     The apply-state annotation is set to `rebooting` BEFORE the reboot.
   - On **single-node**, kuso-server itself goes down here. All state
     lives in node annotations the kubelet re-reports on boot; the UI
     shows "rebooting…" and reconnects when the API returns.
   - **rejoin + uncordon:** the `pkgupdates` goroutine reconciles — a
     node in `rebooting` state that is `Ready` again → uncordon (ONLY
     if we own the cordon marker) → state `done` → notify.
4. If `allowReboot` is false and a reboot is needed: patch, set
   `rebootRequired`, stop (leave the reboot to a deliberate second
   action).

**Apply-state annotation** — `kuso.sislelabs.com/pkg-apply-state`:
`{"phase":"running|rebooting|done|failed","at":"...","log":"...tail..."}`
so the UI can poll status across a reboot.

**Guardrails:**
- At most one apply Job per node (annotation lock; refuse a second).
- Cordon ownership marker so we never uncordon a node an operator (or
  nodewatch) cordoned for another reason.
- No auto-reboot without explicit `allowReboot: true`.
- Any step fails → state `failed` + captured log + an `error` notify.

## Data flow summary

```
probe DaemonSet --(nsenter host, ~6h)--> node annotation pkg-updates
                                              |
kuso-server pkgupdates goroutine --(read, ~6h, edge-dedup via Setting)-->
   - domain service -> nodes-page surface
   - warn notify on fresh advisory

admin clicks Apply -> POST apply-patches
   -> kuso-server cordons (marker) -> launches per-node privileged Job
   -> Job: preflight -> apt upgrade -> [reboot branch, detached]
   -> annotation pkg-apply-state tracks phase across reboot
   -> goroutine reconciles rebooting+Ready -> uncordon -> done -> notify
```

## Storage

- No new DB table.
- Two node annotations (`pkg-updates`, `pkg-apply-state`) + one cordon
  ownership marker annotation.
- One `Setting` kv key for per-node last-notified `checkedAt`
  (restart-safe edge dedup).

## Testing

- **Pure functions (table tests):** apt output → upgradable parse;
  pkg-manager detection; advisory severity; edge-dedup decision.
- **Orchestrator state machine (fake clients):** cordon-ownership,
  single-vs-multi-node drain decision, `rebooting`+`Ready` →
  uncordon-only-if-owned, concurrency lock. Tested against the
  dynamic+typed fakes like the existing reconciler tests.
- **Live smoke (test cluster):** advisory appears; apply with no reboot;
  then the reboot path (patch → cordon → reboot → rejoin → uncordon).

## Phasing

Large build; ship incrementally, each phase independently useful:

1. **Probe + annotation:** DaemonSet writes the advisory annotation.
   Verify on the live cluster (it has real apt updates pending).
2. **Advisory + notify + UI:** `pkgupdates` goroutine, warn notify with
   restart-safe dedup, nodes-page read-only surface.
3. **Apply (no reboot):** the apply Job for the patch-only path +
   `allowReboot:false` behavior.
4. **Apply + reboot orchestration:** cordon/drain/detached-reboot/
   rejoin/uncordon, single-node edge case, failure handling.

## Open risks

- **Single-node self-reboot:** kuso-server goes down mid-apply. Mitigated
  by detached reboot + annotation-based state, but the UI necessarily
  shows a gap until the API returns. Acceptable for an
  operator-initiated action with an explicit "this reboots the node"
  confirm.
- **Privileged DaemonSet blast radius:** the probe runs privileged +
  host-nsenter. Mitigated by: tightest-possible RBAC (own-node patch),
  read-only probe (apply is a separate Job), and the probe never
  executing operator-supplied input.
- **apt parse drift:** apt output format can vary by version. Mitigated
  by preferring the machine-readable `apt-check`/`-s` simulate path and
  table-testing the parser against captured real output.
