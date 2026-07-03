# Image-path release trigger

**Date:** 2026-07-03
**Status:** Draft — pending review

## Problem

The pre-deploy release hook (`spec.release.command` — runs DB migrations as a
Job before promoting a rollout) only fires from the **build poller**
(`builds.go:2587`), which processes `KusoBuild` completions. `runtime: image`
services skip the build pipeline entirely (services_ops.go:585) — no
`KusoBuild` is ever created — so their release hook **never runs**. Every
marketplace app is `runtime: image`, so a template that ships an empty DB and
migrates via the hook (Plausible: `relation "salts" does not exist`) crashes
on first boot.

## Goal

Make the release hook run for `runtime: image` deploys with the **same
semantics** the build path already provides: the migration Job runs against
the target image BEFORE the app pod serves it, and on failure the image is
NOT promoted (old pods keep running, failure surfaced via notify).

## Decisions (confirmed with user)

1. **Gate model: withhold the image until the release passes.** Mirror the
   build path. When a `runtime: image` env has a release hook AND a not-yet-
   released image, do NOT stamp `env.spec.image`; run the release Job; stamp
   the image only on success. Failed migration → image never goes live →
   old pods keep serving.
2. **Home: a new leader-elected `imagerelease` watcher package** (sibling of
   `scaledown`/`nodewatch`), reusing `releaserun.Runner`. Not bolted into the
   build poller (which is `KusoBuild`-centric).

## Architecture

### The withholding mechanism

Today `AddService`/image-patch stamps `env.spec.image` directly. New rule:

- **If the service has NO release hook** (`spec.release` empty): behave exactly
  as today — stamp `env.spec.image` immediately (no regression, no watcher
  involvement, no extra latency).
- **If the service HAS a release hook**: create/patch the env with
  `spec.image = nil` and record the desired image in a new field
  **`spec.pendingImage *KusoImage`**. With `image` nil the chart renders no
  pod (or the previous image's pod keeps running on an update), so nothing
  serves the un-migrated image.

`pendingImage` is a new field on `KusoEnvironmentSpec` (CRD + Go type). It is
the image-path analogue of "the build hasn't promoted its tag yet."

### The watcher

`internal/imagerelease/watcher.go` — leader-elected loop (tick ~15s):

```
every tick:
  list KusoEnvironments where spec.pendingImage != nil
  for each env with a release hook:
    run releaserun.Runner.Run(ctx, ns, env, env.spec.pendingImage)
      • idempotent: releaserun.JobName is per-(env, image-tag), so a
        re-tick while a Job is mid-flight is a no-op (Job exists)
      • Runner already waits for addons via its init container + polls
    on OutcomeSucceeded:
      CAS-promote: set spec.image = spec.pendingImage, clear pendingImage
      (RMW with retry, mirroring promoteEnvImageCAS's conflict handling)
    on OutcomeFailed / OutcomeTimedOut:
      leave pendingImage set (image stays withheld), stamp a
      release-failed status/annotation on the env, fire a notify event.
      Do NOT re-run immediately — the per-(env,tag) Job name means the
      failed Job blocks re-runs of the SAME tag until the user changes
      the image or explicitly retries (delete the Job / bump).
```

Envs whose release hook is empty never carry `pendingImage`, so the watcher
ignores them.

### Why a watcher (not inline in AddService)

A migration can take minutes; blocking the API call / CLI on it is poor UX and
a server restart mid-call orphans state. The watcher is crash-safe: state
lives on the env CR (`pendingImage`), so a restart just re-reconciles. This
matches how the build poller already works (async, CR-state-driven).

## Preview envs

`shouldRunRelease` already excludes `kind: preview` (preview migrations are
owned by the seed path). The watcher applies the same exclusion — a preview
env never carries `pendingImage`; image previews stamp their image directly.
(Preview of an image service is rare, but the rule keeps parity.)

## Failure surfacing

Reuse the existing `markReleaseFailed` shape (notify event + status) so an
image-service migration failure shows up in the bell feed + deployments tab
exactly like a build-service one. The env stays on its previous image (or has
no image yet on first deploy — the pod simply doesn't start, and the failure
is visible rather than a crash-loop).

## What changes

- `KusoEnvironmentSpec.PendingImage *KusoImage` (Go type + both CRD schemas).
- `AddService` + the image-patch path: when `spec.release` is set, write
  `pendingImage` instead of `image`; else unchanged.
- New `internal/imagerelease/` package: `Watcher` struct + `Run(ctx)` loop +
  the promote-on-success CAS. Reuses `releaserun.New(kc)`.
- Wire the watcher into `main.go`'s leader-gated `startSingletons` (like
  `scaledown`).
- The `promoteEnvImageCAS` logic is build-poller-private; extract a shared
  helper or replicate the small RMW loop in the watcher (replicate — it's
  ~15 lines and the build poller's version is entangled with build state).

## Non-goals

- No change to built runtimes (dockerfile/nixpacks/buildpacks) — they keep
  using the build poller.
- No new user-facing config — the release hook UI/CLI/kuso.yaml already
  exist; this just makes them effective for image services.
- No retry UI for a failed image-service migration in v1 (changing the image
  or deleting the failed Job re-triggers — same as build path).

## Testing

- Unit: watcher promotes on success (fake releaserun returns Succeeded →
  env.spec.image set, pendingImage cleared); withholds on failure (image
  stays nil, pendingImage retained, notify fired). AddService writes
  pendingImage (not image) when a release hook is present, and image
  directly when not.
- CRD: pendingImage accepted by both schemas.
- Live smoke: `kuso marketplace deploy plausible` → release Job runs
  createdb+migrate → env.spec.image gets stamped → app pod comes up Ready,
  no "relation does not exist". Then re-verify the 3 already-working DB apps
  (n8n/gitea/metabase) still deploy (they have no release hook → unchanged
  path).
