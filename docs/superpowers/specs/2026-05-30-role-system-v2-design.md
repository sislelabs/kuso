# Role System v2 — Design

**Date:** 2026-05-30
**Status:** Approved (pending spec review)
**Author:** ivilthe69 + Claude

## Goal

Simplify kuso's authorization model to **three roles** — `viewer`, `editor`, `admin` —
grantable to **both users and groups**, with project-level visibility that is
**closed by default** for non-admins.

This replaces the current model:
- Instance roles: `admin / member / viewer / billing / pending` → **`viewer / editor / admin`**
- Project roles: `owner / deployer / viewer` → **`viewer / editor`** (admin is instance-only)

## The three roles

| Role | Capabilities |
|------|-------------|
| **viewer** | Read-only on everything they can see. |
| **editor** | Full project read **and write**, including **writing** env vars. **Cannot read env var values** and **cannot open a pod shell / exec**. No instance-admin powers (no user/group/role management, no billing, no system update, no audit log). |
| **admin** | Full access to everything, all projects, all instance settings. Sees every project by default. |

### Editor env restriction — exact scope

Editors are blocked from two surfaces (decided explicitly):
1. **Env var read** — the env list/get API returns 403 for editors (they can still
   PUT/POST to set env vars blind, but cannot read current values).
2. **Pod shell / exec** — the terminal/exec websocket is admin-only for the env-hiding
   to be meaningful (`printenv` would otherwise defeat it).

**Accepted residual leak paths (NOT closed in this iteration):**
- Addon `-conn` secrets / `get addons -o json` still expose `DATABASE_URL` etc. to editors.
- Application logs may print secrets at boot; editors retain log access.

These are conscious scope lines, documented so they are not mistaken for oversights.
Closing them is a possible follow-up.

## Two-layer hybrid model

Two distinct concepts:

1. **Instance role** — the *default access level* a principal carries. Set on a
   **user directly** OR on a **group**. Does NOT grant project visibility on its own,
   except `admin` (admins see all).

2. **Project grant** — what makes a project *visible* to a non-admin. A project has
   an access list; each entry is a **user or a group**, with an **optional role override**.
   - No override → the principal acts on that project at their **instance role** (inherited).
   - Override set → the principal acts at the overridden role on that project (up or down).

### Effective-role resolution

For user `U` acting on project `P`:

1. If `U` is `admin` (directly, or via any group) → **admin on every project**; `P` always visible.
2. Otherwise, gather all grants applying to `P`:
   - direct user-grants where grantee == `U`
   - group-grants where grantee is a group `U` belongs to
3. If **no** applicable grant → **`P` is invisible to `U`** (filtered from lists; 403 on direct access).
4. For each applicable grant, compute its level:
   - `override` if the grant sets one, else `U`'s instance role (inherited default).
   - If the inherited instance role is also absent (NULL), the grant defaults to **`viewer`**
     — being explicitly added to a project always confers at least read access.
5. **Effective role on `P` = highest** of those levels (`admin` > `editor` > `viewer`).

`U`'s own instance role (`viewer`/`editor`) thus takes effect only on projects where a
grant exists without an override. A non-admin with zero project grants sees nothing.

### Instance role of a user

A user's effective **instance role** = highest of:
- their direct instance role (new), and
- the instance role of every group they belong to.

(`admin` > `editor` > `viewer`. Absence = no instance role = `viewer`-equivalent floor
for *level*, but still no visibility without a grant.)

## Data model changes

The control plane is **PostgreSQL** (`lib/pq`), schema applied idempotently from
`server-go/internal/db/schema.sql` on boot (each `ALTER … ADD COLUMN IF NOT EXISTS`
/ `CREATE TABLE IF NOT EXISTS` is safe to re-run). Migration syntax must be Postgres.

### 1. Direct user instance role

Add a column to `User`:
```sql
ALTER TABLE "User" ADD COLUMN IF NOT EXISTS "instanceRole" TEXT;  -- null = none
```

### 2. Project access list (users + groups, optional override)

New table replacing the JSON `projectMemberships` blob with first-class rows that can
reference either a user or a group:
```sql
CREATE TABLE IF NOT EXISTS "ProjectGrant" (
    "id"          TEXT PRIMARY KEY,
    "project"     TEXT NOT NULL,              -- KusoProject name
    "userId"      TEXT,                       -- exactly one of userId / groupId set
    "groupId"     TEXT,
    "roleOverride" TEXT,                      -- null = inherit instance role
    "createdAt"   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "ProjectGrant_user_fk"  FOREIGN KEY ("userId")  REFERENCES "User"("id")      ON DELETE CASCADE,
    CONSTRAINT "ProjectGrant_group_fk" FOREIGN KEY ("groupId") REFERENCES "UserGroup"("id") ON DELETE CASCADE,
    CONSTRAINT "ProjectGrant_one_grantee" CHECK (("userId" IS NOT NULL) <> ("groupId" IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS "ProjectGrant_project_idx" ON "ProjectGrant"("project");
CREATE UNIQUE INDEX IF NOT EXISTS "ProjectGrant_user_uq"  ON "ProjectGrant"("project","userId")  WHERE "userId"  IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS "ProjectGrant_group_uq" ON "ProjectGrant"("project","groupId") WHERE "groupId" IS NOT NULL;
```

### 3. Group instance role

Keep the existing `UserGroup."instanceRole"` column; only the **allowed values** change
(`viewer/editor/admin`). The `projectMemberships` JSON column is **deprecated** — superseded
by `ProjectGrant`. It is left in place (unused) to avoid a destructive drop; reads stop
referencing it.

## Migration — wipe and re-grant

A one-shot, marker-guarded migration (runs once, idempotent):

1. **Bootstrap admin survives.** The existing admin group (`UserGroup` where
   `instanceRole='admin'`, e.g. `grp-bootstrap-admins`) and its members keep `admin`.
2. **Everyone else → no access.** All non-admin groups have `instanceRole` set to NULL/`viewer`
   and `projectMemberships` cleared; no `ProjectGrant` rows are created.
3. `User."instanceRole"` starts NULL for all (admins get it via their admin group; no need
   to stamp users directly at migration time).
4. Guarded by a marker row in a `SchemaMarker`/`KusoMigration` table (or an existing
   migration-tracking mechanism) so it never re-wipes on subsequent boots.

Net effect: after migration, only admins can see/do anything; admins re-grant
viewer/editor access to users and groups, and add users/groups to projects, in the new UI.

## Permission matrix (code)

`auth.Compute(tenancy, project)` becomes project-aware (or a sibling
`ComputeForProject`) and emits the permission set per the effective role:

| Permission | viewer | editor | admin |
|-----------|:------:|:------:|:-----:|
| `project:read`, `services:read`, `addons:read` | ✓ | ✓ | ✓ |
| `project:write`, `services:write`, `addons:write` | | ✓ | ✓ |
| `secrets:write` (set env, blind) | | ✓ | ✓ |
| `secrets:read` (read env values) | | | ✓ |
| `shell:exec` (pod shell/terminal) | | | ✓ |
| `sql:read` | | ✓ | ✓ |
| instance: `settings:admin`, `user:write`, `audit:read`, `system:update`, `billing:read` | | | ✓ |

(`shell:exec` is a **new** permission gating the terminal/exec websocket — previously
ungated-by-perm or tied to project access.)

Instance-level `viewer`/`editor` carry NO permissions on their own; permissions are
always computed in the context of a project the user can see, at their effective role
there. Instance pages (settings, users, audit, billing) require `admin`.

## API / handler changes

- `auth.Compute` → project-aware resolution; `ProjectsAccessible` reads `ProjectGrant`
  (admins → nil/all; others → distinct projects with an applicable grant).
- `ProjectRoleFor(tenancy, project)` → returns effective `viewer/editor/admin` per the
  resolution rules.
- `requireProjectAccess(minRole)` keeps its shape; role constants change to the 3-role set.
- **New** `secrets:read` gate on env-var **read** endpoints (list/get) — `Require(PermSecretsRead)`.
  Env **write** endpoints gate on `secrets:write` (editor-allowed).
- **New** `shell:exec` gate on `terminal_ws.go` / exec websocket — admin-only.
- Group/user management endpoints: set instance role on a user (new) or group (existing,
  values change); add/remove `ProjectGrant` rows (users or groups) with optional override.
- DB layer: replace `Get/SetGroupTenancy` JSON-membership logic + `ListUserTenancy` union
  to read from `ProjectGrant` and the new `User.instanceRole`; add CRUD for grants and
  direct user instance role.

## UI changes (web)

- **Role pickers**: everywhere the old roles appear, collapse to viewer/editor/admin.
- **Users/Groups admin page**: set a user's or group's instance role (viewer/editor/admin).
- **Project access panel** (per project): list of grantees (users + groups), each with an
  optional role-override dropdown defaulting to "inherited (<their instance role>)".
- **Project list**: non-admins see only granted projects (already filtered server-side;
  UI just renders what the API returns). Empty state for users with no grants.
- **Env vars tab**: hidden / values masked for editors (403 → "env values are admin-only").
- **Shell/terminal action**: hidden for non-admins.

## Out of scope (YAGNI)

- Closing the addon-conn-secret and log leak paths for editors (documented residual).
- Custom roles beyond the three / per-permission custom grants.
- Billing as a distinct role (folded into admin; `billing:read` is admin-only now).
- A separate "pending/awaiting-access" instance role — replaced by "no access until granted"
  (a logged-in user with no instance role and no grants simply sees an empty/awaiting state).

## Testing

- **Unit (auth pkg, pure):** `Compute`/resolution table — every (instance role × grant override ×
  group-vs-user × admin) combination → expected permission set + visibility. This is the
  highest-value test surface and must be exhaustive.
- **Unit (db pkg):** `ProjectGrant` CRUD, `ListUserTenancy` union (highest-wins across
  direct + group, override vs inherit), migration wipe-and-re-grant idempotency.
- **Handler-level:** `secrets:read` 403 for editor on env read; `secrets:write` 200 for editor;
  `shell:exec` 403 for non-admin; project invisibility (404/filtered) for ungranted non-admin.
- **Migration:** apply against a seeded old-model DB; assert only admins retain access.

## Rollout

- Pure server + web change; **no CRD schema change** → ships via `make ship VERSION=vX.Y.Z`,
  instances self-update. The DB migration runs on the new server's boot.
- This is an **authz change** — verify on the test cluster (papelito/tickero) that a freshly
  re-granted editor cannot read env vars or open a shell, and that an ungranted user sees no
  projects, before considering it done.
