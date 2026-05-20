# Kuso `RefreshEnvSecrets` Per-Service & Per-Env Secret Drop — Design

**Date:** 2026-05-20
**Status:** Approved
**Repo:** kuso (`/Users/sisle/code/work/kubero-setup/kuso`)

## Problem

Companion to the v0.13.11 `envFromSecrets` fix. `addons.RefreshEnvSecrets`
— invoked on every addon add/remove — rebuilds each project's
`envFromSecrets` from scratch and merge-patches **every** `KusoEnvironment`
CR with one shared list. v0.13.11 made it keep `<project>-shared` and
`kuso-instance-shared`. It still **drops** two more secret kinds:

- **Per-service secret** — `<project>-<service>-secrets`. Holds keys set
  via `kuso secret set <project> <service> KEY VALUE` at shared (env="")
  scope: `AUTH_SECRET`, `NEXTAUTH_SECRET`, `DISCORD_CLIENT_ID`, etc.
- **Per-env secret** — `<project>-<service>-<env>-secrets`. Holds keys set
  at a specific env scope (e.g. preview-PR overrides).

After any addon operation, every service loses both from its running
pods. This is the cause of distill's recurring `MissingSecret` /
`?error=Configuration` failures after env-CR reconciles.

## Root Cause

`RefreshEnvSecrets` builds **one** `secrets` slice and applies it to all
envs. Per-service and per-env secrets are by definition *different per
env* — they cannot live in a single shared slice. The fix computes them
**per env, inside the loop**, from labels each env CR already carries.

## Design

### How the per-env secret names are derived

Every kuso-created env CR carries two labels (confirmed at all
env-creation sites in `services_ops.go` / `env_groups.go`):
- `kuso.sislelabs.com/service` (`kube.LabelService`) — the **short**
  service name (e.g. `web`).
- `kuso.sislelabs.com/env` (`kube.LabelEnv`) — the env name (e.g.
  `production`, or a PR/custom env name).

The `secrets` package's `Name(project, service, env)` produces:
- `env == ""` → `<project>-<service>-secrets`
- `env != ""` → `<project>-<service>-<sanitized-env>-secrets`, where the
  env name is lowercased and `[^a-z0-9-]` is replaced with `-`.

So `RefreshEnvSecrets`, per env in its loop, can derive BOTH names from
that env's two labels — **no Secret-list call needed**.

### Change 1 — name helpers in `kube` (single source of truth)

`addons.go` cannot import the `secrets` package directly without risking
an import cycle. Add two helpers to `server-go/internal/kube/selectors.go`
beside the existing `SharedSecretNames`:

```go
// ServiceSecretName returns the service-scoped shared secret name:
// <project>-<service>-secrets. Holds keys set via
// `kuso secret set <project> <service>` with no --env scope.
func ServiceSecretName(project, service string) string {
	return project + "-" + service + "-secrets"
}

// EnvSecretName returns the env-scoped secret name:
// <project>-<service>-<sanitized-env>-secrets. The env name is
// lowercased and any character outside [a-z0-9-] becomes "-", so the
// result is a valid Kubernetes resource-name segment.
func EnvSecretName(project, service, env string) string {
	safe := envSecretNameSanitize(env)
	return project + "-" + service + "-" + safe + "-secrets"
}
```

`envSecretNameSanitize` reproduces the regex `[^a-z0-9-]` → `-` on the
lowercased env name (move the existing `envSafeRE` regex, or an
equivalent, into `kube`).

To guarantee these never drift from the canonical naming, **refactor
`secrets.Name`** (`server-go/internal/secrets/secrets.go`) to delegate:
`env == ""` returns `kube.ServiceSecretName(project, service)`, otherwise
`kube.EnvSecretName(project, service, env)`. The `secrets` package
already imports `kube`, and `kube` does not import `secrets`, so this is
cycle-safe. After the refactor there is exactly one implementation of the
naming logic.

### Change 2 — fix `RefreshEnvSecrets`

In `server-go/internal/addons/addons.go`:

1. Keep the **project-wide** part computed once before the loop —
   `baseSecrets = addon-conn-secrets + kube.SharedSecretNames(project)`.
2. **Inside** the per-env `for` loop:
   - Read `svc := envs[i].Labels[kube.LabelService]` and
     `envName := envs[i].Labels[kube.LabelEnv]`.
   - Build a per-env slice: `perEnv := slices.Clone(baseSecrets)` (an
     independent copy so envs don't share backing array).
   - If `svc != ""`: append `kube.ServiceSecretName(project, svc)`; and
     if `envName != ""`, also append
     `kube.EnvSecretName(project, svc, envName)`.
   - Call `buildEnvFromSecretsPatch(perEnv)` for **that** env (the patch
     is now per-env instead of shared).
3. If an env CR has no `kube.LabelService` label, skip both per-X
   appends for it (do not build a malformed `<project>--secrets`); the
   env still receives addon-conn + shared secrets. This guards only
   hand-created CRs — every kuso-created env has the label.

Both per-service and per-env entries are attached **unconditionally**
(not gated on the Secret existing). `envFromSecrets` entries are
`optional: true` in the `kusoenvironment` chart, so a not-yet-created
Secret is harmless — identical to how `<project>-shared` is handled.

## Testing

- **`kube` helper unit tests** — `ServiceSecretName` returns
  `<project>-<service>-secrets`; `EnvSecretName` returns
  `<project>-<service>-<env>-secrets` and sanitizes a mixed-case /
  punctuated env name (e.g. `preview/PR-7` → `preview-pr-7`).
- **`secrets.Name` test** — confirm it still returns the same strings
  after delegating (the existing tests, if any, must stay green; add one
  if none cover it).
- **`RefreshEnvSecrets` test** — after the fan-out, a `web` production
  env's `envFromSecrets` contains `<project>-web-secrets` AND
  `<project>-web-production-secrets`, alongside the addon-conn and shared
  secrets. Two services in one project each receive their own
  per-service secret with no cross-contamination. An env CR missing
  `LabelService` is patched without a malformed name.

## Rollout

1. Implement changes 1–2, all tests green.
2. Cut release v0.13.12; `kuso upgrade` the cluster.
3. distill's env CRs are currently held by a manual `kubectl patch`; the
   release makes the per-service secret durable — the next addon
   operation's `RefreshEnvSecrets` will no longer drop it.

## Risks & Mitigations

- **Per-env patch:** the loop now builds a distinct slice + patch per
  env instead of reusing one. Negligible cost; correctness requires it.
- **Sanitization drift:** the per-env name must exactly match
  `secrets.Name`'s sanitization, or the fan-out attaches a name that
  doesn't match the real Secret. Mitigated by making `secrets.Name`
  delegate to the shared `kube` helper — one implementation.
- **Label trust:** the fix trusts `kube.LabelService` / `kube.LabelEnv`.
  A hand-created env CR missing them degrades gracefully (skips the
  per-X entry). Every kuso-created env CR has both.
- **Out of scope:** nothing further — this completes the
  `envFromSecrets` correctness story: addon-conn + project-shared +
  instance-shared + per-service + per-env.

## Success Criteria

- After an addon add/remove, every env CR's `envFromSecrets` still
  contains its `<project>-<service>-secrets` and
  `<project>-<service>-<env>-secrets`.
- `secrets.Name` and the `kube` helpers produce identical strings (one
  implementation via delegation).
- Unit tests for the helpers and `RefreshEnvSecrets` pass; full
  `server-go` suite passes.
- After release + cluster upgrade, distill's services keep their
  service-scoped secrets (`AUTH_SECRET` etc.) through an addon operation.
