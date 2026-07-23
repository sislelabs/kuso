# Surface `<service>-secrets` keys as first-class env vars — design

**Date:** 2026-07-23
**Status:** approved
**Author:** ivo (with Claude)

## Problem

kuso mounts the whole kuso-managed `<service>-secrets` Secret into a service's pod
via `envFrom` (bulk mount). But the env editor only lists `spec.envVars`. Any key
that lives *only* in `<service>-secrets` — with no matching `secretKeyRef` entry in
`spec.envVars` — is **invisible in the UI** even though the pod has it.

Concrete case that surfaced this (scubatony/internal-system, v0.20.2):
- Secret `scubatony-internal-system-secrets` (kuso-managed — carries
  `secrets.kuso.sislelabs.com/generated-*` annotations) holds `INTERNAL_JWT_SECRET`,
  `WETRAVEL_API_KEY`, `WETRAVEL_WEBHOOK_TOKEN`.
- `spec.envVars` (11 entries) has none of those three — only the literal `WETRAVEL_ENV`.
- Pod gets all three via the `envFrom` bulk mount → visible to `env` in a shell, but
  the env editor can't show them.

Origin: these keys were injected by the Coolify import (or a direct write), not by
kuso's env editor. kuso's `SetEnvVar` only ever writes `spec.envVars` (a literal
`value` or a `secretKeyRef` *pointer`), so it has no concept of storing a secret
*value* in `<service>-secrets` — hence the orphaned keys and the invisibility.

## Goal

Make keys in the kuso-managed `<service>-secrets` Secret **first-class, read+write
env vars** in the editor, treated as normal kuso env secrets — names always visible,
values masked for non-admins, writes landing in that secret (which the pod already
envFrom-mounts). No change to how the pod consumes env.

## Non-goals

- Do NOT change the pod's envFrom wiring.
- Do NOT backfill `secretKeyRef` entries into the CR (enumerate-on-read is enough;
  avoids double-listing a key that's both envFrom-mounted and secretKeyRef'd).
- Do NOT touch addon-conn secrets (`<addon>-conn`) — those already show as
  `secretKeyRef` entries and are managed by the addon lifecycle.
- Not a multi-tenant/security-model change — single-tenant, same authz as today.

## The `<service>-secrets` Secret

Naming: `<project>-<service>-secrets` (e.g. `scubatony-internal-system-secrets`),
env-scoped variants `<project>-<service>-<env>-secrets`. kuso-managed: carries
`secrets.kuso.sislelabs.com/generated-<KEY>` annotations for generated values. It is
listed in the env's `envFromSecrets` and bulk-mounted into the pod. This is the
authoritative store for secret *values* going forward.

## Design

### 1. Read (enumerate)

`ListEnv` / `GetService` / `GetEnvironment` additionally read the kuso-managed
service-secrets Secret(s) and merge their keys into the returned env list. To avoid
guessing the exact name, resolve which secret to read from the env's `envFromSecrets`
list, restricted to the kuso-managed service-secrets entries — i.e. the entry(ies)
matching `<project>-<service>-secrets` / `<project>-<service>-<env>-secrets` (NOT the
`<addon>-conn` entries, which are addon-owned and already shown as secretKeyRefs).
Merge their keys into the returned env list as secret-backed vars, with these rules:
- **Dedup:** skip any key already represented by a `secretKeyRef` in `spec.envVars`
  pointing at that same secret+key (avoid double-listing).
- **Tag:** mark each merged entry `source: "managed-secret"` (new field on the env
  DTO) so the UI renders it as an editable secret value (distinct from a literal or a
  secretKeyRef pointer to an addon conn).
- **Mask:** value returned only to `callerCanReadSecrets`; otherwise the existing mask
  sentinel. Names always returned.
- Skip kuso-internal/generated bookkeeping keys only if they shouldn't be user-facing
  — default is to show all keys (INTERNAL_JWT_SECRET included; it's a real env var the
  app uses).

### 2. Write

Extend `SetEnvVarRequest` with a third mode `SecretValue *string`:
- Exactly one of `Value`, `SecretRef`, `SecretValue` must be set (extend the current
  XOR check).
- `SecretValue` upserts `name → value` into `<service>-secrets` (create the Secret if
  absent, else patch the one key). Preserves existing keys + the
  `secrets.kuso.sislelabs.com/generated-*` annotations (patch, don't replace).
- Does NOT add a `spec.envVars` entry — the pod already envFrom-mounts the secret, and
  enumerate-on-read (part 1) surfaces it. This keeps the CR clean.
- Ensure `<service>-secrets` is in the env's `envFromSecrets` (it already is for
  existing services; guard for the case where it's missing so a first secret-value
  write also wires the mount).
- Bump the secret rev / trigger the same rolling-restart path a secret change already
  uses so the pod picks up the new value.

`UnsetEnvVar` for a `managed-secret` key removes the key from `<service>-secrets`
(and, if it becomes empty, leaves the empty Secret — don't delete it, other keys/the
mount may depend on it).

### 3. Masking + safety

- Reuse `callerCanReadSecrets(ctx, DB, project)` — identical gate to existing secret
  vars. Names visible to all who can read the service; values admin/secret-reader only.
- On write, preserve the Secret's `generated-*` annotations and all other keys (patch
  semantics, never full-replace).
- `<service>-secrets` is already `helm.sh/resource-policy: keep`-adjacent (kuso-managed,
  not helm-owned) — confirm writes don't fight the operator.

## Components

- `server-go/internal/projects/services_deltas.go` — `SetEnvVarRequest.SecretValue`,
  the XOR validation, the `<service>-secrets` upsert on write, unset removal.
- `server-go/internal/projects/` (env read path) — a helper
  `mergeManagedSecretKeys(ctx, ns, project, service, env, envVars) []EnvVar` that reads
  the secret and merges keys (dedup + source tag).
- `server-go/internal/http/handlers/projects.go` — call the merge in
  ListEnv/GetService/GetEnvironment before masking; the existing mask then applies to
  the merged entries uniformly.
- `api/apiv1` + `cli/pkg/kusoApi` + `cli/cmd/kusoCli/env.go` — `secretValue` on the
  set-env wire shape + a `kuso env set --secret KEY=VALUE` flag; `kuso env list` shows
  the `managed-secret` source.
- `web/src/components/service/EnvVarsEditor.tsx` — render `managed-secret` rows as
  editable secret values (masked), writable via the new mode.

## Testing

- Unit: `mergeManagedSecretKeys` dedups a key that's both in the secret and a
  secretKeyRef; surfaces an orphaned key; masks for non-admin; tags source.
- Unit: `SetEnvVar` with `SecretValue` upserts into `<service>-secrets`, preserves
  other keys + generated annotations, XOR rejects value+secretValue.
- Integration (fake clientset): set a secret value → key appears in the secret and in
  a subsequent env list; unset removes it.
- Regression: an existing secretKeyRef var (DATABASE_URL) is NOT double-listed.

## Rollout

- Server-code only + web + CLI. No CRD change, no chart change. Ships in the next
  release; enumerate-on-read means the 3 orphaned scubatony keys appear immediately
  once the instance updates — no migration.
- For the LIVE scubatony instance (still v0.20.2), the keys are already correct in the
  pod; this fix just makes them visible/editable once the instance rolls to the release
  carrying it. No urgent operational action — the app works today.
