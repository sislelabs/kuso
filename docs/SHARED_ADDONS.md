# Shared addons

Three ways a kuso project can attach to a database (or any addon kind). Pick the model that matches your operational reality.

## The three models

| Model | What kuso provisions | Where the data lives | Who manages it | Why pick it |
| --- | --- | --- | --- | --- |
| **Native** (default) | StatefulSet + PVC + Secret | In your kuso cluster, on a node | kuso operator | One-click, isolated per project, native backups via cronjob |
| **External** (`spec.external`) | Mirrored Secret only | An existing instance you already run (managed Postgres, RDS, on-prem, anywhere) | You | You already have a database, want kuso to wire conn-secrets without taking over |
| **Instance-shared** (`spec.useInstanceAddon`) | A new database + dedicated user on a shared server, plus a mirrored Secret | In your kuso cluster (or wherever the registered shared server lives) | kuso server creates the DB; admin manages the shared server | Multiple projects share one Postgres instance — saves resources, keeps the conn-secret pattern |

The first two are obvious. The third is the new one — that's what `feat(addons): instance dropdown lists registered shared servers` (v0.7.54) shipped, and it's the model this doc focuses on.

## Instance-shared addons (the model the v0.7.54 dropdown surfaces)

### What it is

An admin registers a shared Postgres server with kuso once, by handing the server a **superuser DSN** that has `CREATE DATABASE` and `CREATE ROLE` permissions:

```bash
kuso instance-addon register \
  --name primary-pg \
  --dsn 'postgres://kuso_super:secret@pg-shared.internal:5432/postgres'
```

The DSN is stored in the cluster-scoped `kuso-instance-shared` Secret under the key `INSTANCE_ADDON_PRIMARY_PG_DSN_ADMIN`. Only kuso admins can read or modify it; the password never leaves the cluster.

After registration, every project's "Add addon" dialog shows a dropdown with `primary-pg` as an option. When a developer picks it for a new addon called `myapp-db`:

1. kuso connects to `primary-pg` as the super user.
2. Creates a fresh database (`<project>_<addon>`) and a dedicated role with a generated password.
3. Grants the role full ownership of just that database.
4. Writes the per-project DSN into the project's `<addon>-conn` Secret (e.g. `myapp-db-conn`), which `envFromSecrets` consumes the same way it does for native addons.

The developer's services see `DATABASE_URL` exactly as if they had a native Postgres addon. No StatefulSet is rendered for the addon CR — the chart no-ops when `spec.useInstanceAddon` is set.

### Why this exists

Native addons are great for isolation but expensive: every project that wants Postgres burns a StatefulSet, a PVC, and a long-lived process. For an operator running 10 small side-projects (or 50 internal tools), "50 Postgres instances on one cluster" is wasteful — they fit in one Postgres with 50 databases.

`useInstanceAddon` keeps the kuso programming model intact (services attach to an "addon" by name; conn-secret pattern works) while letting the operator amortise one shared instance across many projects.

### What gets stored where

```
cluster-scope:
  Secret/kuso-instance-shared
    INSTANCE_ADDON_PRIMARY_PG_DSN_ADMIN   # superuser DSN, admin-only

project namespace (per addon):
  KusoAddon/<addon-name>
    spec.kind: postgres
    spec.useInstanceAddon: primary-pg     # references the registration
    # no StatefulSet rendered

  Secret/<addon-name>-conn
    DATABASE_URL    # per-project DSN — pooler (:6432) when the instance has a PgBouncer, else direct
    DIRECT_URL      # per-project DSN, ALWAYS un-pooled/direct (:5432) — use for Prisma migrations
    POSTGRES_HOST, POSTGRES_PORT, POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB
    POOLER_HOST, POOLER_PORT, POOLER_URL   # populated when the instance runs a pooler, else empty
```

The per-project Secret is just like the native-addon conn-secret. Services that did `envFromSecrets: [myapp-db-conn]` continue working unchanged whether the addon is native or instance-shared.

### Prisma migrations — use `DIRECT_URL`, not `DATABASE_URL`

The shared cluster PG fronts every per-project database with **PgBouncer in `transaction` pooling mode**, and `DATABASE_URL` routes through it by default (`<host>-pooler:6432`). That's correct for app runtime traffic, but it **breaks schema migrations**: Prisma's migration engine takes a session-scoped advisory lock (`pg_advisory_lock(72707369)`). Under transaction pooling, PgBouncer hands each transaction a different backend, so the lock is acquired on one server connection and leaked when that connection returns to the pool — the next migration attempt blocks, times out after 10s (`Timed out trying to acquire a postgres advisory lock`), and the pod `CrashLoopBackOff`s until the leaked backend is recycled.

Point Prisma's `directUrl` at the always-direct `DIRECT_URL` key so migrations run on a sticky, un-pooled session:

```prisma
datasource db {
  provider  = "postgresql"
  url       = env("DATABASE_URL")   // pooled — app runtime queries
  directUrl = env("DIRECT_URL")     // direct :5432 — migrations only
}
```

Wire it in the service's env vars (the `${{ <addon>.KEY }}` ref resolves to a `secretKeyRef` against `<addon>-conn`):

```
DATABASE_URL = ${{ <addon>.DATABASE_URL }}   # or just envFromSecrets the conn secret
DIRECT_URL   = ${{ <addon>.DIRECT_URL }}
```

`DIRECT_URL` is emitted by the native StatefulSet chart, the HA chart, and the instance-shared provisioner alike, so the same wiring works on every postgres-addon backing path. Same applies to any migration tool that relies on a session-scoped lock (golang-migrate's `pg_advisory_lock`, Flyway, Liquibase) — run migrations against `DIRECT_URL`.

### Lifecycle

| Operation | What happens on the shared server |
| --- | --- |
| Project creates `KusoAddon` with `useInstanceAddon: primary-pg` | `CREATE DATABASE`, `CREATE ROLE`, `GRANT`. Per-project conn-secret written. |
| Project updates the addon's spec (e.g. backup schedule) | No-op against the shared server; only the conn-secret may rewrite. |
| Project deletes the addon | **No automatic `DROP DATABASE`.** The kuso server clears `<addon>-conn` and the KusoAddon CR; the database on the shared server is left in place. **The admin must drop it manually** if they want to reclaim space. This is intentional — accidental cascading deletes lose data. |
| Admin unregisters the shared server | Refused if any project's KusoAddon currently references it. The UI surfaces "in use by N projects." Force-unregistering an in-use shared server orphans the per-project DSNs. |
| Shared server goes down | Every dependent project's database goes down with it. Same blast radius as a native StatefulSet failure, just shared. |

### Failure modes

1. **Superuser DSN expires or rotates.** Re-run `kuso instance-addon register --name primary-pg --dsn '<new>'`. Existing per-project DSNs aren't affected — they use their own roles, not the super DSN.
2. **Shared server out of disk.** Every project on it stops accepting writes. The shared model concentrates this risk; pick a host with monitoring + alerting before sharing.
3. **Bad DSN at registration.** kuso parses the URL but doesn't dial the server until the first project tries to use it. A typo'd registration looks fine in the dropdown until someone picks it.
4. **Project deletion leaves orphan DBs.** As above. We may add an admin-side "list orphan DBs on this server" verb in a future release; for now the shared-server admin is responsible for housekeeping.
5. **Cross-project namespace collision.** kuso names per-project DBs `<project>_<addon>`. Two projects in different namespaces can't pick the same name pair, but they CAN pick names that resolve to identical DB names if the underlying server is case-insensitive. We normalize lowercase before sending — collisions are rare but not impossible. The CREATE DATABASE call is what ultimately fails; the addon creation surfaces a 400 with the Postgres error string.

### What's not supported (yet)

- Only `kind: postgres`. MySQL/Mongo/Redis instance-sharing would each need their own `provisionInstanceAddonDB` implementation.
- No automatic `DROP DATABASE` on addon delete. Deliberate.
- No backup schedule per project — the shared server's backup is the admin's responsibility, not kuso's. Per-project pg_dump-shaped backup is on the table for a future release.
- No connection pooling layer (PgBouncer / RDS Proxy) — every per-project role connects directly. Fine up to a few dozen projects; PgBouncer in front is the next mitigation when you fan out further.

## When to pick which

| Situation | Pick |
| --- | --- |
| One project, want isolation, accept the resource cost | **Native** |
| Already have a Postgres you trust (RDS, managed, on-prem) | **External** |
| Many small kuso projects on one cluster, want to share one Postgres | **Instance-shared** |
| Compliance / regulated workload — projects must not share infra | **Native**, never instance-shared |
| Project is critical and shared server's blast radius is unacceptable | **Native** |

## See also

- `cli/cmd/kusoCli/instance_addons.go` — CLI surface
- `server-go/internal/instancesecrets/instance_addons.go` — registration storage
- `server-go/internal/addons/addons.go` — `provisionInstanceAddonDB` + `writeInstanceAddonConnSecret`
- `docs/EDIT_SAFETY.md` — addon edit semantics across all three models
