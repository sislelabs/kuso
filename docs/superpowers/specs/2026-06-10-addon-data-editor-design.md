# Addon Data Browser/Editor — design

**Date:** 2026-06-10
**Status:** approved, implementing

## Goal

Extend the existing read-only SQL surface on the Postgres **addon overlay** into a
full data **browser + editor**: list tables, page/sort/filter rows in a grid,
inline type-aware cell editing, insert and delete rows. Plus the existing raw SQL
runner (kept read-only). Scoped to one addon, reusing the conn secret kuso already
holds. Inspired by `db-masterclass` (dbmaster), but native to kuso's tenancy/auth.

Single-tenant scope: no separate server connections, no branching, no its-own RBAC
— kuso's project roles + `sql:read` admin gate already cover access.

## What already exists (build on, don't replace)

- **Backend** (`server-go/internal/http/handlers/backups.go`):
  - `GET  /api/projects/{project}/addons/{addon}/sql/tables` → `SQLTables`
  - `POST /api/projects/{project}/addons/{addon}/sql/query` → `SQLQuery` (raw SQL,
    enforced read-only inside a `BeginTx{ReadOnly:true}` + `SET LOCAL
    statement_timeout='5s'`, `blockedSQLBuiltin` denylist, audit-logged).
  - `pgConn(ctx, project, addon)` helper: per-request `*sql.DB` to the addon's
    **direct** Postgres (POSTGRES_HOST/PORT/USER/PASSWORD/DB from the `-conn`
    secret), `MaxOpenConns=1`. Reused as-is for all new endpoints.
  - Gating: `requireProjectAccess(…, ProjectRoleViewer)` + `callerCanRunSQL`
    (`sql:read`, admin-only). Reused as-is.
- **Frontend**: `web/src/components/addon/overlay/SQLTab.tsx` (table list + query
  box + results table), shown only for postgres addons. `features/projects/api.ts`
  has `listSQLTables` + `runSQL`.

## New backend endpoints

All under the existing `/sql/...` prefix, same handler (`BackupsHandler`), same
`pgConn`, same `sql:read` admin gate, same audit logging.

| Method | Path | Purpose |
|---|---|---|
| GET | `/sql/tables` | **extend**: add `rowCount` + `size` per table from `pg_class`/`pg_total_relation_size` (estimate via `reltuples` to stay cheap). |
| GET | `/sql/columns?schema=&table=` | **new**: columns `{name, dataType, udtName, nullable, default, ordinal, isEnum, enumValues[]}` + `primaryKey: []string`. |
| GET | `/sql/rows?schema=&table=&limit=&offset=&orderBy=&dir=&filter=` | **new**: paginated/sorted/filtered single-table read. |
| POST | `/sql/rows` | **new — insert** `{schema, table, values: {col: {value, isNull}}}`. |
| PATCH | `/sql/rows` | **new — update** `{schema, table, pk: {col: {value,isNull}}, set: {col:{value,isNull}}}`. |
| DELETE | `/sql/rows` | **new — delete** `{schema, table, pk: {col:{value,isNull}}}`. |

### Read response shape (rows)

```
{
  "columns": ["id","email","meta"],
  "rows":    [["1","a@b.co","{...}"], ...],   // stringifyCell text, as today
  "nulls":   [[false,false,true], ...],       // per-cell isNull bitmap
  "total":   1234,                            // COUNT for pagination (capped/estimated)
  "truncated": false,
  "elapsed": "12ms"
}
```

## Safety model (consistent with the existing read-only design)

1. **No raw-SQL writes.** The raw runner (`/sql/query`) keeps its read-only tx
   untouched. ALL mutations go through the structured insert/update/delete
   endpoints, which build **parameterized** statements — no user string is ever
   interpolated into SQL.
2. **Identifier validation.** `schema`, `table`, `orderBy`, and every column name
   in a write are validated against the table's **actual** column/PK list fetched
   from `information_schema` for that request. An identifier not in the set →
   400, never interpolated. Identifiers are then quoted with `pq.QuoteIdentifier`.
3. **PK-targeted writes only.** UPDATE/DELETE require the table to have a primary
   key and the `pk` map to cover exactly the PK columns; the statement is
   `… WHERE <pk_col> = $n AND …`. Server verifies the write affected **exactly one
   row** (else rolls back → 409). Tables without a PK are **read-only** in the grid.
4. **NULL vs empty is explicit.** Values cross the wire as `{value, isNull}`.
   `isNull:true` binds SQL NULL; `isNull:false` binds the value (incl. `""`, `0`,
   `false`). Postgres does final type coercion via the column type — Go does not
   re-parse pg types.
5. **Same gate + audit.** Every write requires `sql:read` (admin) and logs
   `addon.sql_write` with `{table, pk, op}`. `blockedSQLBuiltin` still guards the
   raw runner.
6. **Bounded.** `limit` capped at 1000 (default 100); `statement_timeout='5s'` on
   reads; `total` COUNT capped (e.g. `SELECT count(*) … LIMIT`-style or estimate)
   so a huge table doesn't hang pagination.

## Type-aware editing (frontend)

pg type → input + coercion:
- text/varchar/char/uuid → text
- int*/numeric/float* → number (sent as string; pg casts)
- bool → tri-state true/false/null
- json/jsonb → textarea, validate JSON on blur
- timestamp(tz)/date/time → text, ISO, validated
- enum (udtName, `isEnum`) → dropdown of `enumValues`
- else → text

NULL is a first-class, dim italic token in the grid (distinct from `''`); nullable
columns get a "set NULL" affordance. Edits send `{value, isNull}`.

## Frontend changes

- `SQLTab.tsx` becomes two sub-modes (segmented control): **Browse** (default) and
  **SQL** (the existing raw runner).
- **Browse**: left = table list (exists, + row counts); right = data grid for the
  selected table — header with type, sortable columns, pagination footer, inline
  cell editing (double-click), an "+ insert row" affordance, per-row delete. PK-less
  tables render read-only with a banner.
- New `features/projects/api.ts` functions: `getSQLColumns`, `getSQLRows`,
  `insertSQLRow`, `updateSQLRow`, `deleteSQLRow`; React Query hooks for each.
- Grid component lives under `web/src/components/addon/overlay/data/` (TableGrid,
  CellEditor, types) to keep `SQLTab.tsx` a thin shell.

## Out of scope (v1)

MySQL/ClickHouse adapters (postgres + postgres-ha only), branching, query history,
EXPLAIN viz, CSV import, FK hover-preview, multi-row bulk edit, transactions across
edits. The API is addon-scoped so a future top-level "Database" view can reuse it.

## Testing

- Go: table-driven tests for identifier validation (reject non-existent
  schema/table/column/orderBy), PK-guard (no-PK table → write refused; multi-row
  WHERE → 409), NULL vs empty binding, and the parameterized-statement builder.
- Reuse the existing pg test patterns; the builders are pure (no kube) where
  possible so they unit-test without a live DB.
