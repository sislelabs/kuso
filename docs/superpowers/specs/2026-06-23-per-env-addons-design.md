# Per-environment addon provisioning — design

**Date:** 2026-06-23
**Status:** approved

## Problem

`kuso environment add <project> <service> <name> --branch <b>` creates a named
long-lived env (staging, qa, …) but the env **shares the project's addons** with
production. `AddEnvironment` attaches `AddonConnSecrets(project)` (the shared
`<project>-<addon>-conn` secrets) to the new env's `EnvFromSecrets`, filtered only
by the service's `SubscribedAddons` (which is per-service, not per-env). So a
staging env's `DATABASE_URL` resolves to the **production** database — staging
migrations/writes hit prod data. There is no per-named-env addon isolation.

(Per-PR **preview** envs already get their own addons via `previewdb.EnsurePRAddons`
— this design generalizes that to named envs.)

## Goal

`kuso environment add` provisions a new named env with **its own addons** by
default: postgres → an isolated clone (empty by default), redis → a fresh empty
instance, s3 → its own bucket/prefix. The env's `EnvFromSecrets` point at the
clones, so `${{ db.URL }}`-style varrefs and app code need no change. Production
addons are untouched.

## Decisions (from brainstorming)

1. **Empty by default; opt-in seed.** A named env's new postgres DB starts empty
   (schema comes from the env's own release-hook migrations on first deploy).
   `--seed-from <env>` copies a `pg_dump | psql` snapshot from the named source
   (reusing the preview seed Job). Redis/s3 are never seeded.
2. **Full isolation now.** Clone/provision postgres + redis + s3 in this change.
   Postgres → real isolated clone; redis → fresh empty instance; s3 → own
   bucket/prefix (no object copy). Other kinds: skipped (unchanged).
3. **On by default, opt-out.** New named envs get their own addons by default;
   `--share-addons` restores the old shared-addon behavior. Existing envs untouched.

## Architecture

### Generalize `previewdb` from PR-keyed to env-scope-keyed

`EnsurePRAddons(project, prNumber)` already: lists project addons, skips
env-scoped ones (`LabelEnv != ""`), clones each postgres addon as
`<short>-pr-N` with `LabelEnv: preview-pr-<N>`, returns `<clone>-conn` names, and
kicks off seed Jobs. The delete sweep + migrate Jobs key off `kube.LabelEnv`.

Extract an env-scope core:

```
EnsureEnvAddons(ctx, project, envScope string, opts EnvAddonOpts) ([]string, error)
  opts: Kinds []string            // {"postgres","redis","s3"} — which to clone/provision
        SeedFromConn string       // source conn-secret for a pg_dump seed; "" = empty
```

- `envScope` is the `LabelEnv` value: `staging`, `qa`, … (preview path passes
  `preview-pr-<N>`, unchanged).
- Clone naming: `<short>-<envScope>` → CR `<project>-<short>-<envScope>`, conn
  `<clone>-conn`. (Matches the existing `-pr-N` convention; `-pr-N` stays a valid
  scope.) No collision with preview clones: `AddEnvironment` already reserves
  `production` and `pr-*` env names, so a named env's scope can never be `pr-N`.
- Per kind:
  - **postgres** → `addons.Add` a clone (HA=false, carry version/size/storage/
    database/UseInstanceAddon), label `LabelEnv: <envScope>`. Seed only if
    `SeedFromConn != ""` (reuse `seedAsync`); else leave empty.
  - **redis** → `addons.Add` a fresh instance (own StatefulSet/PVC), label
    `LabelEnv`. Never seeded.
  - **s3** → `addons.Add` a fresh bucket/prefix, label `LabelEnv`. No object copy.
  - other → skip.
- Idempotent: existing clone (by CR name) → reuse, re-issue seed only if requested.
- Returns the clone conn-secret names.

`EnsurePRAddons` becomes a thin wrapper:
`EnsureEnvAddons(project, "preview-pr-"+N, {Kinds: postgresOnly, SeedFromConn: sourceConn})`
— preserving today's preview behavior exactly (postgres-only, seeded from source).

### Wire into `AddEnvironment` (`projects/services_ops.go`)

After computing `envFromSecrets` (the shared project addon conn-secrets) and
before building the `KusoEnvironment`:

- If `req.ShareAddons` → keep current behavior (shared addons). Done.
- Else:
  1. `clones := EnsureEnvAddons(project, req.Name, {Kinds: <stateful kinds present in project>, SeedFromConn: connFor(req.SeedFrom)})`.
  2. Remove the project's addon conn-secrets from `envFromSecrets` (the
     `<project>-<addon>-conn` ones — keep shared/instance/per-service/foo-conn
     secrets), and append the `clones`. Reuse the same project-addon-set detection
     as `filterEnvFromForSubscription`.
  3. The env CR's `EnvFromSecrets` now points at the clones; `${{ db.URL }}`
     resolves through them automatically.

### Deletion

`DeleteEnvironment` already sweeps addons for preview envs by the `preview-pr`
label. Generalize the sweep to delete every addon with `LabelEnv == <envName>`
for the env being deleted (so deleting `staging` drops `*-staging` addons + PVCs).
Production env deletion is already blocked.

## CLI (`environment add`)

New flags:
- `--share-addons` — opt out; the env shares the project's addons (old behavior).
- `--seed-from <env>` — copy a postgres snapshot from `<env>` (default: empty DB).
- `--addons <kind,...>` — override which stateful kinds to provision (default:
  every stateful kind the project has — postgres, redis, s3).

Help + examples updated:
```
kuso environment add scubatony internal-system staging --branch staging
  → staging gets its own scubatony-db-staging (empty) + own redis + own s3
kuso environment add scubatony internal-system staging --branch staging --seed-from production
  → same, but the staging DB is seeded from production
kuso environment add scubatony internal-system staging --branch staging --share-addons
  → old behavior: staging shares the production addons
```

## Error handling

- Addon clone/provision failure → **fail env creation** with a clear error (don't
  leave a half-wired env whose `DATABASE_URL` points at nothing). Clone creation is
  idempotent, so a retry is safe.
- `--seed-from <env>` where `<env>` doesn't exist / has no postgres conn →
  validation error **before** provisioning.
- redis/s3 provision failure → fail (full isolation was chosen; silently falling
  back to shared prod redis/s3 defeats the purpose).

## Testing

- **Unit (`previewdb`)**: `EnsureEnvAddons` — clone naming, `LabelEnv` stamping,
  kind filtering, empty-vs-seed, idempotent reuse, skip already-env-scoped addons.
- **Regression**: `EnsurePRAddons` (now a wrapper) behaves identically — existing
  `previewdb`/`migrate` tests stay green.
- **Wire-in (`projects`)**: `AddEnvironment` removes project addon conn-secrets and
  appends clone conn-secrets for a non-prod env; `--share-addons` keeps the old set.
  Extend the `env_domains_test.go` style.
- **Delete sweep**: deleting a named env removes its `LabelEnv`-scoped addons.
- **Manual (against the test cluster / scubatony)**:
  `kuso environment add scubatony internal-system staging --branch staging` →
  `kuso get addons scubatony` shows `scubatony-db-staging` (+ redis/s3) labeled
  `env=staging`, the staging service's DATABASE_URL resolves to the clone, and
  `kuso db connect scubatony db-staging` tunnels to the isolated DB.

## Non-goals

- No data copy for redis/s3 (fresh instances only).
- No change to PR-preview behavior (the wrapper preserves it).
- No per-env override of `SubscribedAddons` (orthogonal; not needed here).
- Migrating EXISTING shared-addon envs to isolated addons is out of scope (this
  changes the default for NEW envs only).
