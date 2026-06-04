# Shared PgBouncer for the cluster DB (auth_query)

**Date:** 2026-06-04
**Status:** approved, implementing

## Problem

kuso's cluster DB (`kuso-instance-pg`, a KusoAddon kind=postgres) is direct-only.
Per-project provisioning writes a direct `kuso-instance-pg:5432` DSN, and the
kusoaddon chart's pooler is gated off for cluster-DB consumers. The Coolify
migration wants apps to connect through a PgBouncer (pooling across ~16 apps on
one shared Postgres), matching Coolify's topology. The conn secret's
`POOLER_HOST/PORT/URL` keys are empty.

## Approach

Put ONE shared PgBouncer in front of `kuso-instance-pg`, rendered by the
EXISTING kusoaddon pooler template with a new **auth_query** branch (the cluster
pooler serves N rotating per-project users, unlike the existing single-user
poolers). Route per-project `DATABASE_URL` through it by default.

### auth_query (the one new capability)

The existing pooler renders a static single-user `userlist.txt`. The cluster
pooler instead authenticates clients dynamically:

- `auth_type = scram-sha-256` (matches the instance PG; confirmed).
- `auth_user = kuso` (the instance superuser; confirmed it can read pg_shadow).
- `auth_query = SELECT usename, passwd FROM pg_shadow WHERE usename=$1`.
- `[databases] * = host=<instance-pg> port=5432` — pass-through; client picks db.
- `userlist.txt` holds ONLY the auth_user credential (rendered from the instance
  conn secret by the existing init container), so PgBouncer can log in to run
  the auth_query.
- Result: a new per-project user works instantly — NO pooler restart/reload on
  project opt-in. Transaction pool mode. Single replica (instance PG is the SPOF).

The pooler template branches on an `authMode` value: `query` (instance/shared)
vs the existing static-userlist path (dedicated single-addon, unchanged).

## Changes

1. **`operator/helm-charts/kusoaddon/templates/postgres-pooler.yaml`** — add the
   `authMode: query` branch: emit `auth_user`/`auth_query` + a userlist with only
   the auth user; gate the existing multi-user/static path under the default.
   Lift the render so `useInstanceAddon` no longer blocks it when the addon IS
   the instance PG (it sets `pooler.enabled` + `instancePooler: true`).
2. **`operator/helm-charts/kusoaddon/values.yaml`** — `pooler.authMode` (default
   the existing static behaviour) + `pooler.instancePooler`.
3. **`internal/instancepg/instancepg.go`** — set `pooler: {enabled: true,
   authMode: query, instancePooler: true}` on the instance-pg CR it creates.
4. **conn-secret POOLER_***: the chart's `<name>-conn` secret (instance PG) gets
   `POOLER_HOST/PORT/URL` populated (mirror the HA chart's existing block).
5. **`internal/addons/instance_provisioner.go`** — per-project `<addon>-conn`
   `DATABASE_URL` points at the pooler (`<pooler>:6432/<project>_<addon>`), with
   `POSTGRES_HOST` kept direct as a fallback + `POOLER_*` exposed too.
6. **CRD**: `pooler` already x-kubernetes-preserve-unknown or explicit? If the
   KusoAddon CRD needs `spec.pooler.authMode`/`instancePooler` fields, add them
   (schema-drift guard) + refresh goldens. Apply the CRD to the live cluster.

### DATABASE_URL routing decision (approved)

Cluster-DB projects get `DATABASE_URL` → pooler by default (the explicit ask).
`POSTGRES_HOST`/direct still in the secret as fallback.

## Testing

- Go TDD: instancepg sets pooler values on the CR; provisioner writes a
  pooler-routed DATABASE_URL + POOLER_*.
- `helm template` golden: pooler renders auth_query config for instancePooler;
  dedicated-addon pooler unchanged (static userlist).
- CRD golden refresh if schema fields added.
- Live: re-provision/patch cluster DB → pooler pod Ready → a throwaway project
  opts into the cluster DB → its DATABASE_URL is `:6432` → connects + SELECT 1
  succeeds through the pooler with the per-project user.

## Non-goals

- Per-project poolers (one shared pooler only).
- HA pooler (single replica; instance PG is the SPOF anyway).
- Changing dedicated single-addon pooler behaviour.
