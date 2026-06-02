# Preview release hook — run the service's release Job against the preview DB

**Date:** 2026-06-02
**Status:** approved, ready for implementation plan

## Problem

A PR preview clones the production database (`<addon>-pr-N`, seeded via
`pg_dump --clean | psql` from the source addon) and points the preview pod at
it. But the preview never runs the service's **release hook** (`spec.release`,
e.g. tickero's `migrate up`). Production envs carry `spec.release`; the kuso
release-Job machinery (`internal/releaserun`, driven from the build poller at
image-promote) runs it against the env's own `$DATABASE_URL` and gates the app
rollout on success. Preview envs are built in `internal/github/dispatcher.go`
and **`spec.release` is never stamped on them**.

Consequence: any PR that adds a DB migration boots its preview app against a
cloned-but-un-migrated schema. Observed on tickero PR-36 — the app added
migration `000032` (adds `events.spotify_url`); the preview API returned HTTP
500 `column e.spotify_url does not exist (SQLSTATE 42703)`, so event cards
rendered with no prices and wrong images. Recovered manually by exec-ing
`migrate up` in the preview pod. We want this to happen automatically.

## Approach

Propagate the parent service's `spec.release` onto the preview
`KusoEnvironment.Spec.Release`, exactly where `EnvVars` / `EnvFromSecrets` /
`Runtime` / `Command` are already cloned. The existing release-Job machinery
then does the rest:

- The build poller (`internal/builds/builds.go`, ~line 2222) already runs the
  release Job for **any** env whose `spec.Release` is set — including previews —
  before patching the image tag, and skips promote on failure. No poller change.
- The release Job runs the **PR's image** (`migrate up` and the migration files
  live in that image), so the PR's own migrations apply.
- The release Job reads the preview's `$DATABASE_URL`, which the dispatcher has
  already swapped to the per-PR clone-conn secret
  (`swapPGCloneSecrets` / `swapPGCloneSecretRefsInEnvVars`). So migrate runs
  against the clone, not prod.

**No CRD change.** `KusoEnvironmentSpec.Release` (`internal/kube/types.go:275`)
and the `kusoenvironments` CRD (`spec.release`, line 214) already exist for
production envs. The schema-drift guard is satisfied. The golden test does not
change.

### Ordering: seed must finish before migrate

The seed (`pg_dump --clean | psql`) **drops and recreates** tables. It runs
**async, concurrent with the build** (`EnsurePRAddons` kicks off `seedAsync` in
a goroutine; the build is triggered right after the env is created). The release
Job's existing `wait-for-addons` init only TCP-waits that Postgres *accepts
connections* — it does not know whether the seed has finished. If the build is
fast and the seed is slow, `migrate up` can run first and then the seed's
`--clean` wipes the migration; or they interleave.

**Fix (option A — enforce ordering inside the release Job):** extend the release
Job's init phase to also wait for the **seed Job to complete** before running
migrate, but only when this env actually has a cloned DB.

- The seed Job is created per-PR by `previewdb`, named
  `<cloneFQN>-seed-from-<src>-<unix>`, and labelled
  `kuso.sislelabs.com/project=<project>` and
  `kuso.sislelabs.com/env=preview-pr-N`.
- Add a second init container (or extend the wait script) to the release Job
  that, **for preview envs with a clone**, polls for a seed Job matching this
  env's `kuso.sislelabs.com/env` label and waits for its `Complete` condition
  (bounded by the Job's `activeDeadlineSeconds`, same as the addon wait). A
  non-preview env, or a preview with no clone (e.g. a frontend preview that
  subscribes to no DB addon), skips the seed-wait entirely.
- Because the seed-wait needs kube API access (to find + watch the seed Job),
  it is cleanest to do this **server-side in `releaserun.Run`** before creating
  the release Job — i.e. when `env.Spec.Kind == "preview"` and the env has a
  pg-clone conn secret, block on the seed Job's completion (poll with the
  client) up to a bounded timeout, then create the release Job as today. This
  keeps kube credentials out of the Job pod (which has
  `automountServiceAccountToken: false`).

Rejected alternatives:
- **B — serialize the build behind the seed:** delays every preview build by the
  seed time even when the seed would have finished first, and couples the
  dispatcher to seed-Job polling. Worse latency, more coupling.
- **C — accept the race:** flaky "works most of the time" preview behaviour;
  the exact failure mode we are fixing. Rejected.

## Changes

1. **`internal/github/dispatcher.go`** — in the preview env build:
   - Capture the parent service's `Release` alongside the existing
     `svcRuntime` / `svcCommand` capture (the `GetKusoService` block, ~line 644).
   - Stamp it onto the preview env: `env.Spec.Release = parentSvc.Spec.Release`
     (or the baseEnv's release as a fallback, mirroring how `SharedEnvKeys` /
     `SubscribedAddons` fall back to the baseEnv).
   - **Re-stamp on the update path** (`existing != nil`, ~line 800): the update
     branch currently only carries over `EnvFromSecrets`. It must also set
     `env.Spec.Release` so a later push to the PR doesn't drop the release hook.

2. **`internal/releaserun/releaserun.go`** — in `Run` (before `buildJob` /
   create):
   - When `env.Spec.Kind == "preview"` **and** the env's `EnvFromSecrets`
     contains a per-PR clone-conn secret (detect via the `-pr-N` suffix
     convention, or a clone marker), wait for the matching seed Job
     (`kuso.sislelabs.com/env == env's preview-pr-N` label) to reach the
     `Complete` condition, bounded by a timeout (reuse/parallel the addon-wait
     budget, ~2–3 min). If no seed Job is found within a short grace window,
     proceed (best-effort — matches the seed's own best-effort contract; a
     preview with no clone never has a seed Job).
   - Non-preview envs are unaffected (skip the seed-wait).

## Testing

- **Unit (`releaserun`):** seed-wait helper returns promptly when (a) env is not
  a preview, (b) preview has no clone conn; blocks until Complete when a seed
  Job exists; respects the timeout. Fake clientset with a seed Job flipped to
  Complete.
- **Unit (`dispatcher`):** preview env build stamps `Spec.Release` from the
  parent service on both create and update paths; a service with nil release
  yields a nil preview release (no-op, unchanged behaviour).
- **Live e2e (tickero, via the kuso CLI per CLAUDE.md):** open/refresh a PR that
  adds a migration; confirm the preview's release Job runs `migrate up` against
  the clone after the seed, the app promotes, and
  `curl https://api-pr-N.<base>/api/v1/events` returns 200 with prices — no
  manual `migrate up`. Confirm a frontend-only preview (no DB clone) still
  promotes without blocking on a non-existent seed Job.

## Scope / non-goals

- Single-tenant assumptions unchanged. No new CRD, no new field.
- Does not change production release behaviour.
- Does not fold migrations into the seed image (the seed image
  `kuso-backup:latest` lacks the PR's migration files — that's exactly why the
  PR image's own release hook is the right vehicle).
