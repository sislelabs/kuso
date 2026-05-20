# Opt-in PgBouncer for addon Postgres

**Date:** 2026-05-20
**Status:** Approved — ready for implementation plan

## Problem

The instance control-plane Postgres (`deploy/postgres.yaml`) already ships
PgBouncer: a 2-replica transaction-pool Deployment that the `kuso-server` DSN
routes through. Addon Postgres (`KusoAddon` with `kind: postgres`) has no
pooler — every consumer app connects straight to the StatefulSet (single-node)
or the CNPG `-rw` Service (HA) on `:5432`. A project that scales its app
horizontally can exhaust the addon Postgres `max_connections` ceiling with no
multiplexing layer in front.

This spec adds an **opt-in** connection pooler to addon Postgres. The instance
Postgres is out of scope — it is already done.

## Goals

- Per-addon opt-in PgBouncer for `kind: postgres` addons.
- Off by default; enabling or disabling it never touches the database itself
  (no DB restart, no data risk — it only adds/removes a pooler Deployment).
- Non-destructive to existing consumers: `DATABASE_URL` keeps pointing direct
  at the database. Pooling is reached via new, additive `POOLER_*` secret keys.
- One idiomatic path per topology: a self-rendered PgBouncer Deployment for
  single-node addons, the CNPG-native `Pooler` CRD for HA addons.

## Non-goals (YAGNI)

- Pooler for non-Postgres addons (redis, etc.).
- Configurable pool mode / pool size / replica count from the CR spec. The
  chart bakes fixed transaction-mode defaults.
- Auto-rewiring `DATABASE_URL` through the pooler. Apps opt in explicitly.
- Multi-replica pooler for single-node addons (see Decisions).

## CRD + Go type changes

### CRD
`operator/config/crd/bases/application.kuso.sislelabs.com_kusoaddons.yaml` —
add to `spec`:

```yaml
pooler:
  type: object
  properties:
    enabled:
      type: boolean
      description: >-
        When true, render a PgBouncer connection pooler in front of this
        Postgres addon. Single-node addons get a self-managed PgBouncer
        Deployment; HA (CNPG) addons get a CNPG-native Pooler CRD. Only
        meaningful for kind=postgres. Reaching the pooler is via the
        additive POOLER_HOST/POOLER_PORT/POOLER_URL keys in the addon's
        <name>-conn Secret; DATABASE_URL is unaffected.
```

### Go type
`server-go/internal/kube/types.go` — extend `KusoAddonSpec`:

```go
// Pooler enables an opt-in PgBouncer connection pooler in front of a
// kind=postgres addon. Nil or {Enabled:false} = no pooler. See the
// addon helm chart's postgres-pooler.yaml / postgres-ha.yaml.
Pooler *KusoAddonPooler `json:"pooler,omitempty"`
```

```go
// KusoAddonPooler is the opt-in connection-pooler block on KusoAddonSpec.
type KusoAddonPooler struct {
	Enabled bool `json:"enabled,omitempty"`
}
```

`crds.go` needs no change: the addon spec round-trips through the generic
unstructured map (confirmed — `HA` has no bespoke getter/setter either).

## Helm changes

The kusoaddon chart is polymorphic. Pooler rendering is gated on
`kind == "postgres"` AND `pooler.enabled`, and branches on `ha`.

### Single-node path — `templates/postgres-pooler.yaml` (new)

Rendered when `kind == postgres`, `pooler.enabled`, `not ha`, `not external`,
`not useInstanceAddon`. Near-copy of the instance-PG pooler stanza, scoped to
addon labels:

- **ConfigMap** `<name>-pooler-config` — `pgbouncer.ini`:
  - `[databases] * = host=<name> port=5432` (points at the addon StatefulSet
    Service).
  - `pool_mode = transaction`, `auth_type = md5`,
    `auth_file = /etc/pgbouncer/userlist.txt`.
  - `max_client_conn = 200`, `default_pool_size = 25`, `reserve_pool_size = 5`,
    `reserve_pool_timeout = 3`, `query_wait_timeout = 30`,
    `server_idle_timeout = 600`.
  - `ignore_startup_parameters = extra_float_digits,search_path`.
  - `admin_users = kuso`.
- **Deployment** `<name>-pooler` — **1 replica** (single-node addon; a 2nd
  pooler replica buys nothing in front of one DB). `image:
  edoburu/pgbouncer:v1.25.1-p0`. Init container `render-userlist` reads
  `POSTGRES_USER`/`POSTGRES_PASSWORD` from `<name>-conn`, writes
  `"user" "md5<hash>"` (`md5(password+user)`) to an emptyDir, `chmod 0444`.
  `command: ["/usr/bin/pgbouncer"]`, `args: ["/etc/pgbouncer/pgbouncer.ini"]`.
  tcp `:6432` readiness + liveness probes. Resources `requests
  {cpu:50m,mem:64Mi} limits {cpu:500m,mem:256Mi}`. `priorityClassName:
  kuso-platform`. Respects `spec.placement` the same way the addon
  StatefulSet does (nodeSelector + nodeAffinity), so the pooler lands with the
  DB.
- **Service** `<name>-pooler` — ClusterIP, port `6432` → `6432`.
- No PodDisruptionBudget (1 replica — a PDB with `minAvailable:1` would block
  node drains).

The userlist uses the *current* `<name>-conn` password. Password rotation
(`kuso project addon repair-password`) requires a pooler pod restart to
re-render the userlist — acceptable, rotation is rare and operator-driven,
matching the instance-PG pooler's behaviour.

### HA path — `templates/postgres-ha.yaml` (extend)

When `kind == postgres`, `pooler.enabled`, `ha`, render a CNPG `Pooler`:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Pooler
metadata:
  name: <name>-pooler
spec:
  cluster:
    name: <name>            # the CNPG Cluster this chart renders
  instances: 1
  type: rw
  pgbouncer:
    poolMode: transaction
```

CNPG manages the PgBouncer Deployment + a `<name>-pooler` Service and follows
primary failover natively. Auth is handled by CNPG (it injects the cluster
credentials) — no userlist init container needed on this path.

### Conn Secret — additive keys (both paths)

The `<name>-conn` Secret (rendered in `postgres.yaml` and `postgres-ha.yaml`)
gains three keys **only when `pooler.enabled`**:

| Key           | Value                                                              |
|---------------|--------------------------------------------------------------------|
| `POOLER_HOST` | `<name>-pooler`                                                    |
| `POOLER_PORT` | `6432`                                                             |
| `POOLER_URL`  | `postgres://kuso:<pw urlquery>@<name>-pooler:6432/<db>?sslmode=disable` |

`DATABASE_URL`, `POSTGRES_HOST`, `POSTGRES_PORT` are **unchanged** — still
direct at the database `:5432`. Existing consumers are unaffected. When
`pooler.enabled` is false the three keys are absent, so disabling the pooler
cleanly removes them.

### values.yaml

Add to `operator/helm-charts/kusoaddon/values.yaml`:

```yaml
# pooler: opt-in PgBouncer in front of a kind=postgres addon. Reach it
# via the POOLER_HOST/POOLER_PORT/POOLER_URL keys in <name>-conn;
# DATABASE_URL stays direct. Ignored for non-postgres kinds.
pooler:
  enabled: false
```

## Consumer wiring

No change to the `${{ <addon>.KEY }}` env-var rewriter is required: it
resolves *any* key in the `<addon>-conn` Secret to a `secretKeyRef`, so
`${{ mydb.POOLER_URL }}` works the moment the key exists. **Implementation
must verify this** against `server-go/internal/secrets` (the rewriter) — if
the rewriter has a key allowlist, `POOLER_HOST/POOLER_PORT/POOLER_URL` must be
added to it.

## UI

`web/src/components/addons/` settings drawer — add a "Connection pooling
(PgBouncer)" toggle bound to `spec.pooler.enabled`, shown only for
`kind == postgres` addons. One-line helper text: *"Adds a PgBouncer pooler.
Point apps at `${{ <addon>.POOLER_URL }}` to use it."* No restart warning —
toggling it only adds/removes a Deployment, the database is untouched.

CLI (`cli/`) — surface `pooler.enabled` in `kuso get addons -o json` output
(it falls out of the spec passthrough automatically) and accept a
`--pooler` flag on the addon create/update command if one exists; otherwise
the UI toggle is sufficient for v1.

## Testing

**Helm:**
- `helm template` kusoaddon with `kind=postgres,pooler.enabled=true,ha=false`
  — assert `<name>-pooler` Deployment (1 replica) + Service + ConfigMap render
  and the conn Secret has `POOLER_*` keys.
- Same with `ha=true` — assert a CNPG `Pooler` renders, no self-managed
  Deployment, conn Secret has `POOLER_*` keys.
- With `pooler.enabled=false` (or unset) — assert no pooler resources and no
  `POOLER_*` keys, for both `ha` values.

**E2e (via `kuso` CLI, per CLAUDE.md — CLI is the contract):**
- Create a single-node Postgres addon, enable the pooler, reconcile.
- `kuso get addons <project> -o json` — confirm `pooler.enabled: true` and the
  conn Secret exposes `POOLER_URL`.
- Confirm the `<name>-pooler` pod is Running and `:6432` is reachable
  (`kuso shell` into a project pod, `pg_isready -h <name>-pooler -p 6432`, or a
  one-shot psql through `POOLER_URL`).
- Disable the pooler, reconcile — confirm the Deployment/Service are gone and
  the `POOLER_*` keys drop from the conn Secret.

## Decisions (locked with the user)

- **Trigger:** opt-in `spec.pooler.enabled` flag, off by default.
- **Routing:** `DATABASE_URL` stays direct; pooling via additive `POOLER_*`
  keys.
- **HA pooler:** CNPG-native `Pooler` CRD for `ha=true`; self-rendered
  Deployment for single-node.
- **Config surface:** minimal — `enabled` only; mode/sizes fixed in the chart.
- **Replicas:** single-node pooler is **1 replica**. It is a SPOF in front of
  a single-node DB anyway; a pooler pod restart briefly drops connections,
  which is acceptable for this opt-in convenience.
- **No UI restart warning** — enabling the pooler does not restart the DB.

## Rollout

- CRD schema change: must `kubectl apply` the updated kusoaddons CRD to the
  live cluster (the release auto-updater only flips image tags, per CLAUDE.md).
- Operator picks up the new chart on its next reconcile after the image bump.
- Existing addons have no `spec.pooler` block → `Pooler` is nil → no pooler
  rendered → zero behaviour change until a user opts in.
