# Preview migrate-after-seed — run migrations coupled to the seed, not the build

**Date:** 2026-06-03
**Status:** approved approach, ready for implementation
**Supersedes the preview half of:** 2026-06-02-preview-release-hook-design.md

## Problem (root cause, confirmed by live logs)

v0.18.9 propagated the service's `spec.release` onto preview envs and ran the
migration inside the **build poller's promote path** (`releaserun.Run`, gated by
a seed-wait). Live close→reopen of tickero PR-36 proved this is the wrong layer:

1. **Close→reopen never builds.** The dispatcher takes the
   `stampExistingBuildImage` path — it patches the already-succeeded SHA's image
   straight onto the env CR via a kube `Patch`, bypassing the build poller
   entirely. `releaserun.Run` (and its seed-wait) **never executes**. No
   migration runs.

2. **Even on a real build, the ordering is inverted.** The seed is async and
   fire-and-forget: `EnsurePRAddons` spawns `seedAsync` in a goroutine that runs
   *after* the env is created/promoted. Live timeline showed env promoted at
   21:03:49, seed completing at 21:03:59 — 10s later. The seed
   (`pg_dump --clean`) drops+recreates tables and is the **last writer**, so it
   wipes any promote-time migration.

3. **Idempotency makes it worse.** `releaserun.Run` keys its Job on
   `(env, image-tag)` and fast-paths to "observe existing" if that Job exists.
   On reopen (same env + same tag) it would find the prior run, see it
   succeeded, and **skip** — even though the DB was just re-wiped. The migration
   is idempotent per image; the **DB reset is not** — every re-seed needs a
   fresh migrate.

Net: the migration was coupled to *build promote*, but the destructive event is
the *seed*. They are unordered, and the seed always wins.

## Approach

Couple the migration to the **seed**, inside the seed flow
(`previewdb.seedAsync`), which runs on every PR lifecycle event (open / reopen /
synchronize) because it's triggered by clone-create, independent of builds.

Sequence, per clone, after the clone is ready:
1. Run the seed Job (`pg_dump --clean | psql`) as today — but **wait for it to
   complete** (currently `seedAsync` only *creates* the Job and logs "seeded"
   prematurely; add a completion wait).
2. After the seed completes, run a **post-seed migrate** against the clone.

### Finding what to migrate (clone → service join)

A clone is per-**addon**; the release command is per-**service**; the app image
(which carries the migration files) is per-**service build**. The join table
already exists on the cluster: the preview **env CRs** for this PR. Each
`KusoEnvironment` (e.g. `tickero-api-pr-36`) carries, by the time the seed
finishes:
- `spec.release.command` — propagated in v0.18.9 (verified present),
- `spec.image` — the PR app image (stamped on the env CR),
- `spec.envFromSecrets` — including this clone's conn secret.

So after the seed completes, `previewdb` lists the preview env CRs for this PR
(`kuso.sislelabs.com/env=preview-pr-<N>`), selects those whose `envFromSecrets`
contains **this clone's** conn secret AND that have a non-empty `spec.release` +
`spec.image`, and runs one migrate Job per such env.

### The migrate Job

Reuse `releaserun`'s Job *rendering* (wait-for-addons init container + the
release command + the env's `envFrom` → `DATABASE_URL` resolves to the clone),
so there is ONE release-Job shape, two triggers (production build-promote, and
preview post-seed). But the preview migrate Job is keyed to the **seed run**,
not `(env, image-tag)`:
- Name includes a per-seed nonce (reuse the seed Job's `nowUnix`, or a short
  hash of it) so a re-seed always produces a fresh migrate Job and never
  fast-paths to a stale prior run.
- No skip-if-exists fast path for this trigger.
- One-shot (`backoffLimit: 0`), TTL-reaped (24h), owned by the clone addon CR so
  it cascades on PR-close.

### What changes in the v0.18.9 release-hook path

- **Keep** `spec.release` propagation onto preview envs (v0.18.9) — the env CR
  is now the join table the seed path reads. Verified working.
- **Stop running the preview migration from the build poller.** For preview
  envs, `releaserun.Run` should no longer be the thing that migrates (the seed
  path owns it). Options: (a) the poller skips the release Job for `Kind ==
  "preview"` envs and lets the seed path own it; or (b) leave the poller path
  but make it a harmless no-op for previews. Choose (a) — single owner, no
  double-run, no idempotency trap. Production envs are unaffected (they have no
  seed; the poller path remains their migration trigger).
- The `releaserun` seed-wait added in v0.18.9 becomes unnecessary for the
  preview path (the seed path runs migrate *after* the seed by construction) and
  is removed along with the preview branch of the poller. Keep `releaserun`'s
  core (production) untouched.

## Changes

1. **`internal/previewdb/previewdb.go`**
   - `seedAsync`: after `runSeedJob`, **wait for the seed Job to reach
     JobComplete** (bounded), then call a new `migrateAfterSeed`.
   - New `migrateAfterSeed(ctx, ns, project, prNumber, cloneFQN)`:
     - List preview env CRs for the PR; filter to those whose `envFromSecrets`
       contains `ConnSecretName(cloneFQN)` and that have `spec.release` +
       `spec.image`.
     - For each, render + create a one-shot migrate Job (seed-nonce-keyed)
       against the clone, wait for completion, log success/failure (best-effort:
       a failure is logged + surfaced, doesn't crash the goroutine).
   - The migrate Job rendering reuses `releaserun`'s builder (export a
     `BuildMigrateJob`-style helper, or move the shared rendering into a small
     internal func both packages call) to avoid duplicating the wait-for-addons
     init + security context.

2. **`internal/builds/builds.go`** (poller release gate)
   - Skip the release Job when `e.Spec.Kind == "preview"` — the seed path owns
     preview migrations now. Production/other envs unchanged.

3. **`internal/releaserun/`**
   - Remove the preview seed-wait (`seedwait.go` + its wiring in `Run`) added in
     v0.18.9 — no longer needed once previews don't migrate via the poller.
     Keep the production release-Job path intact. (Retain the real-cluster
     label-shape knowledge in the new previewdb code's tests.)

## Testing

- **previewdb unit:** `migrateAfterSeed` selects only env CRs whose
  `envFromSecrets` references this clone AND that have release+image; renders a
  seed-nonce-keyed Job (distinct names across two seed runs → re-migrate on
  reopen); skips envs without a release (frontend). Fake clientset.
- **previewdb unit:** `seedAsync` waits for seed-Job completion before
  migrating (ordering guarantee).
- **builds unit:** poller release gate is skipped for `Kind=="preview"`,
  still runs for production.
- **Live e2e (the real test):** **close → reopen** tickero PR-36; with NO manual
  intervention, confirm a migrate Job runs after the re-seed, applies migration
  32, and `curl https://api-pr-36.tickero.bg/api/v1/events` returns 200 with
  prices. Also confirm a frontend-only preview (no DB) still comes up.

## Scope / non-goals

- No CRD change. Single-tenant assumptions unchanged.
- Production release behaviour unchanged (still poller-driven).
- Does not fold migrate into the seed image (seed image lacks migration files —
  the migrate Job uses the PR app image, same as before).
