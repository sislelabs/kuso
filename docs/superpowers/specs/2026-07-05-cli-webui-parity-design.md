# CLI ⇄ Web-UI feature parity

**Date:** 2026-07-05
**Branch:** `feat/cli-webui-parity`
**Status:** approved, implementing

## Goal

Bind every web-UI capability that makes sense in a terminal to a `kuso` CLI
command, so the CLAUDE.md rule "always go through the kuso CLI, not raw kubectl"
holds without web-UI fallbacks. One PR. Verified live against `kuso.sislelabs.com`.

## Non-goals

Browser-native UX that is nonsensical or unsafe as a CLI command is explicitly
**out of scope**:

- Avatar upload (binary multipart), icon / render byte streams.
- Review-token magic-link approval flow (`GET/POST /api/reviews/{token}`).
- UI personalization: `me/project-prefs`, `me/folders/rename`.
- SSE / sparkline **timeseries** render loops (the point-in-time metric reads ARE in scope; the streaming feeds are not).
- SQL **row mutation** grid (`POST/PATCH/DELETE .../sql/rows`) — destructive, grid-shaped, belongs in the browser. Read + ad-hoc `query` only.

No server, operator, web, or CRD changes. Pure CLI: three layers per command —
client method in `cli/pkg/kusoApi/<area>.go`, cobra command in
`cli/cmd/kusoCli/<area>.go`, registration in the parent `init()`. Ships in the
CLI binary via `make ship`; no `kubectl apply`.

## Architecture

Follows the existing CLI idiom exactly (no new patterns):

1. **Client method** — thin resty wrapper returning `(*resty.Response, error)`,
   paths escaped with `esc()`, auth auto-injected by the shared client
   (`pkg/kusoApi/main.go`). Model on `EnablePublicTCP` / `AddonSecret`.
2. **Cobra command** — `RunE`, `checkRespErr(resp, err)` for status→error
   mapping, `-o json|table` on reads (mirror `get.go`).
3. **Registration** — `AddCommand` in the parent's `init()`.

## Endpoint → command map

### New: `kuso role` (+ fix broken help)  — HIGH

| Endpoint | Command |
|---|---|
| `GET /api/roles` (AdminHandler, slim) | `kuso role list`, `kuso get roles` |
| `GET /api/roles/full` (perms inlined) | `kuso role list --full`, `kuso role get <id>` |
| `POST /api/roles` `{name,description,permissions[]}` | `kuso role create --name … [--description …] [--permission …]` |
| `PUT /api/roles/{id}` | `kuso role edit <id> …` |
| `DELETE /api/roles/{id}` | `kuso role delete <id>` |

**Bug fix:** `cli/cmd/kusoCli/user.go` `user create --help` tells the user to run
`kuso get roles` to obtain a `--role-id`, but that command does not exist today.
This PR makes `kuso get roles` real, resolving the self-contradiction.

Role request shape (from `roles.go`): `{name, description, permissions:
[]PermissionInput}`. `--permission` is repeatable; permission-input shape read
from `db.PermissionInput` during implementation.

### Extend `kuso instance-config`  — podsizes CRUD, rest read-only

| Endpoint | Command | Notes |
|---|---|---|
| `GET/POST /api/config/podsizes`, `PUT/DELETE .../{id}` | `instance-config podsize list/create/edit/delete` | full CRUD |
| `GET/DELETE /api/config/runpacks[/{id}]` | `instance-config runpack list/delete` | **no create/edit route** — list + delete only |
| `GET /api/config/templates` | `instance-config templates` | read-only |
| `GET /api/config/banner` | `instance-config banner` | read-only; write via existing top-level `/config` blob |
| `GET /api/config/clusterissuer` | `instance-config clusterissuer` | read-only |
| `GET /api/config/registry` | `instance-config registry` | read-only |

### Extend `kuso backup` / admin settings

| Endpoint | Command |
|---|---|
| `GET/PUT /api/admin/backup-settings` | `backup settings get/set` |
| `GET /api/admin/backup-health` | `backup health` |
| `GET /api/admin/db/stats` | `backup db-stats` |
| `GET/PUT /api/admin/settings/build` | `instance-config build-settings get/set` |
| `GET/PUT /api/admin/settings/session` | `instance-config session-settings get/set` |

### Extend `kuso addon` — placement

| Endpoint | Command |
|---|---|
| `PUT .../addons/{addon}/placement` | `addon placement set <proj> <addon> --label k=v …` |
| (read `spec.placement` from existing `get addons -o json`) | `addon placement show <proj> <addon>` |

### Diagnostic / cluster reads (`get`, `node`, `service`, `build`)

| Endpoint | Command |
|---|---|
| `GET .../services/{s}/pods` | `get pods <proj> <svc>` (or `service pods`) |
| `GET .../services/{s}/errors` | `service errors <proj> <svc>` |
| `GET .../builds/latest` | `build latest <proj>` |
| `GET /api/kubernetes/storageclasses` *(verify path)* | `get storageclasses` |
| `GET /api/kubernetes/domains` *(verify path)* | `get domains` |
| `GET /api/kubernetes/events` *(verify path)* | `node events` |
| node/env point-in-time metrics *(verify path)* | `node metrics <name> -o json` |

*Paths marked (verify) will be confirmed against `router.go` during
implementation; if a path or method differs, the command adapts. If an endpoint
turns out not to exist, it is dropped and noted in the PR.*

### `kuso db` — SQL browser (read + query)

| Endpoint | Command |
|---|---|
| `GET .../sql/tables` | `db tables <proj> <addon>` |
| `GET .../sql/columns` | `db columns <proj> <addon> <table>` |
| `POST .../sql/query` | `db sql <proj> <addon> "<SELECT …>"` |
| `GET .../sql/rows` | `db rows <proj> <addon> <table>` |
| `POST/PATCH/DELETE .../sql/rows` | **skipped** (destructive grid ops) |

### Remaining extras (`incident`, `github`, `user`, `invite`, `notifications`)

| Endpoint | Command |
|---|---|
| `GET/POST /incidents/{id}/findings`, `/pr`, `/thread` | `incident findings/pr/thread <id>` (reads) |
| `POST /incidents/{id}/feedback` | `incident feedback <id> …` |
| `POST /github/check-repo`, `/detect-runtime`, `/scan-addons` | `github check-repo/detect-runtime/scan-addons …` |
| `GET/PUT /users/profile` (not avatar) | `user profile [set …]` |
| `GET /invites/lookup` *(verify)* | `invite lookup <token>` |
| `GET /notifications/my-feed`, `/outbox-stats` | `notifications my-feed`, `notifications outbox-stats` |

## Testing

Live cluster (`kuso.sislelabs.com`, already logged in) is the contract, per
CLAUDE.md. After `go build`, exercise every new command against real data:

- **Reads** — run and confirm non-empty / well-formed output (`role list`,
  `instance-config podsize list`, `backup health`, `db tables scubatony
  scubatony-db`, `get pods scubatony scubatony`, etc.).
- **Writes** — dry-run-safe where possible; for `role create` / `podsize
  create`, create a throwaway then delete it. `placement set` tested on a
  disposable addon only. No writes to production project data.
- Confirm `-o json` parses (`| jq .`) on every read command.
- `checkRespErr` maps 401/403/404/409 to readable messages (test by hitting a
  non-existent id).

## Docs

- Update CLAUDE.md CLI table with the high-value new commands (`role`, `get
  pods`, `db sql`, `addon placement`, `backup settings`).
- Remove the "known CLI gaps" note that was about to be added — the gaps are now
  closed.
- SKILL.md: add `kuso db sql` and `kuso get roles` if they help the operating
  guide; otherwise leave.

## Risk / blast radius

Additive only. Existing commands unchanged except the `user create` help-text
fix. Worst case a new command has a wrong path/shape → it errors visibly and is
fixed; it cannot affect existing flows. All writes tested against throwaway
resources.
