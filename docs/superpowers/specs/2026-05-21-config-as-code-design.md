# Config-as-Code (kuso.yaml) — Design

**Date:** 2026-05-21
**Status:** Approved (user delegated full implementation)

## Problem

kuso already has a working declarative-apply engine — `spec.Reconciler`,
`spec.File` (the `kuso.yml` YAML schema), `spec.PlanFor` (dry-run diff), and the
`POST /api/projects/{project}/apply` endpoint. But it is **not usable as
config-as-code** for three reasons:

1. **Nothing reads `kuso.yaml` from the repo.** The apply endpoint exists, but
   a user must POST the YAML by hand. Railway/Coolify's value is that the
   config lives *in the repo* and applies on push.
2. **No CLI.** Apply is HTTP-only — no `kuso apply`, no `kuso project export`.
3. **The schema is thin.** `spec.File` covers `project / baseDomain / services
   / addons` with ~8 service fields. It cannot express sleep, placement,
   `internal`, `privateEgress`, addon HA/pooler/backup/external, or crons —
   so the YAML is far less capable than the dashboard.

This design closes all three: a repo-resident `kuso.yaml` applied on git push,
a CLI (`kuso apply` / `kuso project export`), and a full-parity schema.

## Goals

- **`kuso.yaml` in the repo root**, applied automatically on push to the
  project's default branch — before the build is kicked off, so new env vars /
  addons exist when the build's image deploys.
- **Full-parity schema** (`apiVersion: kuso/v1`): the YAML can express every
  field the dashboard can set, for services, addons, and crons.
- **CLI:** `kuso apply [file] [--dry-run]` and `kuso project export <project>`
  (emits a `kuso.yaml` reconstructed from live state).
- **Declarative semantics** — the YAML is the source of truth: fields absent
  from the YAML are reset to chart defaults; resources absent from the YAML are
  candidates for deletion.
- **Safe deletes** — destructive changes (resource deletion) require an
  explicit `prune: true` at the top of the file. Without `prune`, apply does
  everything *except* delete; it records "would delete X" in the result.
- **UI** — a "Config" tab on the project view: shows the current `kuso.yaml`
  (exported from live state), lets the user paste/edit and apply with a dry-run
  diff shown first.

## Non-goals (YAGNI)

- Two-way sync / continuous reconciliation. Apply is push-triggered and
  CLI/UI-triggered only — kuso does not poll the repo or fight UI edits between
  pushes.
- Transactional all-or-nothing apply. Resources are created one per kube write;
  a mid-apply failure leaves partial state. This is documented; the per-step
  `ApplyResult.Errors[]` model surfaces every failure in one round-trip.
- `kuso.toml` — YAML only. One format.
- Templating / variable interpolation inside `kuso.yaml` beyond the existing
  `${{ <addon>.KEY }}` env-var references (which are resolved server-side at
  spec apply, unchanged).
- Per-environment overrides (`environments.<name>` blocks). The `kuso.yaml`
  describes the production shape; preview envs are derived as today.

## Architecture

Five layers, each independently testable:

### 1. Schema — `server-go/internal/spec/spec.go`

`File` gains an `APIVersion string` (`kuso/v1`; empty tolerated as v1 for
backwards-compat with the current thin files) and a `Prune bool`. `ServiceSpec`
and `AddonSpec` are expanded to full parity; a new `CronSpec` is added.

```yaml
apiVersion: kuso/v1
project: my-product
baseDomain: my-product.example.com
prune: false            # when true, apply may delete resources absent here

services:
  - name: api
    repo: https://github.com/me/api
    branch: main
    path: ""
    runtime: dockerfile
    port: 8080
    internal: false
    privateEgress: false
    domains:
      - host: api.my-product.example.com
        tls: true
    env:
      LOG_LEVEL: info
      DATABASE_URL: "${{ db.DATABASE_URL }}"
    scale: { min: 1, max: 5, targetCPU: 70 }
    sleep: { enabled: true, afterMinutes: 30 }
    placement:
      labels: { region: eu }
      nodes: []
    volumes:
      - { name: data, mountPath: /var/lib/api, sizeGi: 5 }
    static: { buildCmd: "", outputDir: "" }
    buildpacks: { builder: "" }

addons:
  - name: db
    kind: postgres
    version: "16"
    size: small
    ha: false
    pooler: { enabled: true }
    storageSize: 10Gi
    backup: { schedule: "0 3 * * *", retentionDays: 14 }
    placement: { labels: {}, nodes: [] }
    # external / useInstanceAddon are mutually exclusive with the above
    external: { secretName: "" }
    useInstanceAddon: ""

crons:
  - name: nightly-rollup
    kind: service          # service | http | command
    schedule: "0 2 * * *"
    service: api           # for kind=service
    command: ["node", "scripts/rollup.js"]
    url: ""                # for kind=http
    image: ""              # for kind=command
    suspend: false
```

`spec.Parse` validates: `apiVersion` is empty or `kuso/v1`; runtimes are in the
known set; cron schedules are 5-field; `external`/`useInstanceAddon` are not
both set on one addon. Unknown YAML keys are **rejected** (`yaml.Decoder` with
`KnownFields(true)`) so a typo'd field surfaces as an error, not a silent
no-op.

### 2. Plan — `server-go/internal/spec/plan.go`

`PlanFor` already diffs desired vs live for services + addons. It is extended
to:
- diff **crons** (`CronsToCreate/Update/Delete`).
- diff **per-field service/addon changes** so the plan can show *what* changes
  on an update, not just "update service:api". The plan's update entries gain a
  `Fields []string` list (changed field names) for the UI diff.
- respect `prune`: when `prune` is false, `*ToDelete` slices are still computed
  but moved into a separate `WouldDelete` section the apply skips.

### 3. Reconciler — `server-go/internal/spec/apply.go`

`Apply` is extended to:
- apply the **full** service/addon field set (today it only maps
  `runtime/port/scale/domains/env`). `serviceCreateReq` / `servicePatchReq`
  gain sleep, placement, internal, privateEgress, volumes, static, buildpacks.
  `addonCreateReq` gains version/size/ha/pooler/storageSize/backup/placement/
  external/useInstanceAddon.
- apply **crons** via the existing `crons.Service`.
- skip the `WouldDelete` set unless `prune` is true.
- **declarative reset:** on update, a field absent from the YAML is set to its
  zero/default value (not left as-is). This is the "YAML wins" contract. The
  patch requests already exist; the reconciler passes explicit zero values
  rather than omitting fields.

### 4. Git-push trigger — `server-go/internal/github/dispatcher.go`

**Important constraint:** the build pod clones the repo inside an init
container; the kuso server never has the repo working tree on disk during
webhook handling. So `kuso.yaml` is fetched via the **GitHub Contents API**,
not read from a filesystem.

`Dispatcher.onPush` already resolves the push to a `KusoProject` and enqueues
builds. A new step runs **before** the builds are enqueued, for pushes to the
project's default branch:

- The dispatcher already holds a `*github.Client`. Call the GitHub Contents
  API — `GET /repos/{owner}/{repo}/contents/kuso.yaml?ref={sha}` — to fetch
  `kuso.yaml` (fall back to `kuso.yml`) at the pushed commit. A `404` (no file)
  is the common case → silently skip, no error.
- If found, base64-decode the content, `spec.Parse` it, and call
  `Reconciler.Apply`. The project name in the file must match the project the
  dispatcher resolved (mismatch → skip + audit warning, never apply to a
  different project).
- Apply runs with `prune` honored from the file. Result is recorded as a
  `NotificationEvent` (`config.applied` / `config.apply_failed`) and audit row.
- A parse error or apply error does **not** block the builds — they still run
  against the (possibly stale) infra; the failure is surfaced via notification.
  Rationale: a broken `kuso.yaml` shouldn't wedge deploys.
- Gated by a project setting `spec.configAsCode.enabled` (default **true** for
  new projects; the dispatcher simply no-ops on a `404`, so default-on is
  safe). When the flag is explicitly false, the Contents API call is skipped
  entirely.
- The dispatcher gains a `Reconciler *spec.Reconciler` field (nil on kube-less
  installs → step skipped, same guard as the HTTP handler).

### 5. CLI + UI

**CLI** (`cli/cmd/kusoCli/apply.go`, `export.go`):
- `kuso apply [file]` — defaults to `./kuso.yaml`; POSTs to the apply endpoint;
  `--dry-run` adds `?dryRun=1` and prints the plan; prints per-step errors.
- `kuso project export <project>` — GETs a new
  `GET /api/projects/{project}/spec` endpoint that reconstructs a `kuso.yaml`
  from live CRs; writes to stdout or `-o <file>`.

**Server** — one new endpoint: `GET /api/projects/{project}/spec` returns the
live state rendered as a `spec.File` (YAML). The apply endpoint already exists.

**UI** — a "Config" tab on the project overlay (`web/src/components/project/`):
a code view of the exported `kuso.yaml`, an editable textarea, a "Dry run"
button (shows the plan: creates/updates/deletes/would-delete), and an "Apply"
button. Reuses the existing apply + new spec endpoints.

## Data flow

```
git push ──> GitHub webhook ──> Dispatcher.onPush ──> resolve project
                                      │
                          GitHub Contents API: GET kuso.yaml?ref=<sha>
                                      │ 200 (404 → skip)
                          spec.Parse ──> spec.PlanFor ──> Reconciler.Apply
                                      │                        │
                          audit + NotificationEvent     kube writes
                                      │
                          builds enqueued (unchanged)

kuso apply kuso.yaml ──> POST /api/projects/{p}/apply ──> same Reconciler
kuso project export   ──> GET  /api/projects/{p}/spec  ──> live CRs → File → YAML
UI Config tab         ──> same two endpoints
```

## Error handling

- **Parse errors** (bad YAML, unknown field, invalid runtime/cron) → `400` from
  the API; on the webhook path → audit warning + `config.apply_failed` event,
  build proceeds.
- **Plan errors** (kube unreachable) → `503`, unchanged.
- **Per-step apply errors** → collected in `ApplyResult.Errors[]`, returned
  `200` with the error list (one round-trip to see all failures). Webhook path
  emits `config.apply_failed` if any step errored.
- **Project mismatch** (file's `project:` ≠ resolved project) → never applied;
  audit warning. Prevents a webhook from mutating the wrong project.
- **`prune` guard** — deletions only when `prune: true`. Otherwise the plan's
  `WouldDelete` is reported but not executed.

## Testing

- **Schema:** `spec.Parse` table tests — full-parity file round-trips; unknown
  field rejected; bad cron/runtime rejected; `apiVersion` empty vs `kuso/v1` vs
  bad; `external`+`useInstanceAddon` conflict rejected.
- **Plan:** `PlanFor` tests — create/update/delete diff for services, addons,
  crons; `Fields` populated on updates; `prune=false` routes deletes to
  `WouldDelete`.
- **Reconciler:** `Apply` tests against a fake projects/addons/crons service —
  full field application; declarative reset (omitted field → default); prune
  on/off; per-step error collection.
- **Export:** `GET /spec` round-trips — export a project, re-parse the YAML,
  assert it plans as a no-op against the same project.
- **CLI:** `kuso apply --dry-run` prints a plan; `kuso project export` emits
  parseable YAML.
- **E2e (live, via the kuso CLI):** export `papelito` to `kuso.yaml`, change a
  field, `kuso apply --dry-run` shows the diff, `kuso apply` applies it,
  re-export shows the change landed. Push a `kuso.yaml` to the test repo,
  confirm the webhook applied it (audit row + notification).

## Rollout

- No CRD schema change for the core feature — `spec.File` is a YAML contract,
  not a CRD. One new field on `KusoProject` (`spec.configAsCode.enabled`)
  *is* a CRD change → needs `kubectl apply` of the updated kusoprojects CRD.
- New endpoint `GET /api/projects/{project}/spec` — server-only, picked up on
  the next release.
- Ship via `make ship`.

## Decisions (locked)

- **Scope:** both `kuso.yaml`-in-repo auto-apply AND the CLI.
- **Schema:** full parity, versioned `apiVersion: kuso/v1`.
- **Drift:** declarative — YAML wins, omitted fields reset to defaults.
- **Deletes:** require explicit `prune: true`; otherwise reported, not executed.
- **Trigger:** push to the default branch, apply before the build; broken
  `kuso.yaml` never blocks the build.
- **Unknown YAML keys:** rejected (fail fast on typos).
