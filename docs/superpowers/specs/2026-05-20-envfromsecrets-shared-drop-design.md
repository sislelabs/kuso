# Kuso `envFromSecrets` Shared-Secret Drop — Design

**Date:** 2026-05-20
**Status:** Approved
**Repo:** kuso (`/Users/sisle/code/work/kubero-setup/kuso`)

## Problem

A `KusoEnvironment`'s `spec.envFromSecrets` lists the Kubernetes Secrets
injected (via `envFrom`) into the service's pods. It must contain three
kinds of entry:
- the project's addon connection secrets — `<project>-<addon>-conn`
- the project-shared secret — `<project>-shared`
- the instance-shared secret — `kuso-instance-shared`

`RefreshEnvSecrets` in `server-go/internal/addons/addons.go` — invoked on
every addon add/remove — recomputes the list as **addon connection secrets
only**, then calls `buildEnvFromSecretsPatch` which issues a merge-patch that
*replaces* `spec.envFromSecrets` with exactly that list. Because a JSON
merge-patch replaces an array wholesale, `<project>-shared` and
`kuso-instance-shared` are **silently dropped**.

Effect: every kuso project loses all of its shared secrets (auth tokens,
Stripe keys, Discord bot tokens, API keys — anything set via
`kuso secret set` at shared scope) from its running pods the moment any
addon operation fans out. This was discovered when the `distill` project's
Discord bot crash-looped with Discord gateway close code `4004
Authentication failed` — `DISCORD_BOT_TOKEN` lives in `distill-shared` and
never reached the bot pod. The same drop also breaks Stripe (the API
service's `STRIPE_SECRET_KEY`) and Discord OAuth.

distill's live env CRs were manually repaired (`kubectl patch` re-adding
`distill-shared` to each `envFromSecrets`); that repair is temporary — the
next addon operation re-runs the broken fan-out and drops it again. A
durable fix in kuso is required.

## Root Cause

Three code paths construct or patch `envFromSecrets`:

| Path | File | Behavior |
|------|------|----------|
| Env creation (service add) | `projects/services_ops.go` (×2) | **Correct** — appends `<project>-shared`, `kuso-instance-shared` |
| Env-group env creation | `projects/env_groups.go` | **Correct** — also appends them |
| Addon fan-out | `addons/addons.go` `RefreshEnvSecrets` | **Wrong** — addon conn-secrets only |

Three independent constructions of "the `envFromSecrets` list"; one drifted.

## Fix

Single source of truth for the shared-secret entries, applied at all three
sites.

### Change 1 — shared helper in `kube`

Both the `addons` and `projects` packages already import
`kuso/server/internal/kube`, so `kube` is the neutral home. Add an exported
function:

```go
// SharedSecretNames returns the two always-present shared-secret entries
// every KusoEnvironment's envFromSecrets must carry: the project-shared
// secret and the instance-shared secret. Both are marked optional:true by
// the kusoenvironment chart, so a pod boots cleanly even when the Secret
// has not been created yet.
func SharedSecretNames(project string) []string {
	return []string{project + "-shared", "kuso-instance-shared"}
}
```

Place it beside the other name-builder/constant code in the `kube` package
(e.g. in the file that already defines connection-secret naming, or a
small dedicated `secretnames.go`).

### Change 2 — fix `RefreshEnvSecrets` (the bug)

In `server-go/internal/addons/addons.go`, `RefreshEnvSecrets` builds
`secrets` from addon connection secrets. After that loop, before
`buildEnvFromSecretsPatch`, append the shared entries:

```go
secrets = append(secrets, kube.SharedSecretNames(project)...)
```

This both fixes the fan-out AND self-heals every existing broken env CR:
the next addon add/remove on any project re-runs `RefreshEnvSecrets`, which
now writes the complete list including the shared secrets.

### Change 3 — route the two correct paths through the helper

In `projects/services_ops.go` (both env-creation sites) and
`projects/env_groups.go`, replace the inline literal
`append(..., project+"-shared", "kuso-instance-shared")` with
`append(..., kube.SharedSecretNames(project)...)`. Behavior is identical;
this removes the drift risk so a future edit can't desync the three sites.

No CRD change, no Helm chart change, no operator change. `spec.envFromSecrets`
already exists and the `kusoenvironment` chart already consumes it. This is
a pure server-side logic fix.

## Self-Healing

Per the approved approach, there is **no separate migration or repair pass**.
Existing env CRs with a missing `<project>-shared` self-heal the next time
any addon operation triggers `RefreshEnvSecrets` for that project — which
now writes the complete list. distill's live CRs are already manually
repaired; the kuso fix guarantees the next addon op will not re-break them.

## Testing

- A unit test for `kube.SharedSecretNames` — returns
  `["<project>-shared", "kuso-instance-shared"]`.
- A unit test for `RefreshEnvSecrets` asserting the patched
  `envFromSecrets` contains BOTH the addon connection secrets AND
  `<project>-shared` + `kuso-instance-shared`. Mirror the construction
  style of the nearest existing `addons` package test.

## Rollout

1. Implement changes 1–3, tests green.
2. Cut a patch release (v0.13.11) and `kuso upgrade` the cluster.
3. distill is already manually patched and the bot is running; the release
   makes the fix durable so the next addon op cannot re-drop
   `distill-shared`.

## Risks & Mitigations

- **Merge-patch still replaces the array.** The fix keeps the
  replace-style merge-patch; it works only because the list passed is
  always complete. The `kube.SharedSecretNames` helper guarantees
  completeness at all three known write sites. This is acceptable: today
  nothing else writes `spec.envFromSecrets`. If a future code path adds a
  fourth kind of entry outside these three sites, `RefreshEnvSecrets` would
  drop it — that is a known, documented limitation, not addressed here
  (YAGNI).
- **Out of scope:** no operator-side reconcile loop for `envFromSecrets`,
  no explicit cluster-wide repair job.

## Success Criteria

- After an addon add/remove, every affected env CR's `envFromSecrets`
  still contains `<project>-shared` and `kuso-instance-shared`.
- All three `envFromSecrets` construction sites use
  `kube.SharedSecretNames`.
- Unit tests for the helper and `RefreshEnvSecrets` pass.
- The full `server-go` test suite passes.
- After release + cluster upgrade, distill's bot stays running through an
  addon operation (shared secret not re-dropped).
