# kuso.yml reference

`kuso.yml` (or `kuso.yaml`) is kuso's config-as-code file. `kuso apply` sends it to the server, which plans the changes and applies them to one project. `kuso project export <project>` writes the live state out in this format, and that is the quickest way to start a file.

Source of truth: `server-go/internal/spec/spec.go` (schema and parser) and `apply.go` (what apply does with each field). If this page and the code disagree, the code wins. Please fix this page.

## Rules that apply to the whole file

- **Strict parsing.** Unknown keys are an error, not a no-op, and so are unknown keys inside an `env` mapping. A typo fails `kuso apply` instead of being silently ignored.
- **The file wins.** Every apply resets each service to exactly what the file says. If you leave a field out, the live value goes back to its default rather than keeping its old value. Edits made in the UI get overwritten on the next apply. Exceptions are listed per field below.
- **One project per file.** `project` has to match the project you apply to.
- **Deletes are opt-in.** Services, addons and project crons that exist live but are missing from the file are deleted only when `prune: true`. Without it they are listed as "not pruned". With prune on, the CLI shows what it will delete and asks first. `--yes` skips the question, and you need it when not running on a TTY.
- `kuso apply --dry-run` prints the plan and writes nothing. Run it first.

## Top level

| Field | Type | Notes |
|---|---|---|
| `apiVersion` | string | `kuso/v1`, or leave it out (legacy). Any other value is rejected. |
| `project` | string | Required. |
| `baseDomain` | string | Written by `project export`. Accepted, but apply does not change the project's base domain. |
| `prune` | bool | Default `false`. See above. |
| `services` | list | See [Services](#services). |
| `addons` | list | See [Addons](#addons). |
| `crons` | list | Project crons only. See [Crons](#crons). |

## Services

| Field | Type | Notes |
|---|---|---|
| `name` | string | Required. Short name (the CR is `<project>-<name>`). |
| `repo` | string | Git URL. A `#subdir` suffix sets `path`. There is no project-level default repo in this file. |
| `branch` | string | Branch production tracks. |
| `path` | string | Subdirectory inside the repo (monorepos). |
| `runtime` | string | `dockerfile`, `nixpacks`, `buildpacks`, `static`, `image` or `worker`. |
| `port` | int | The port the app listens on. kuso sets `$PORT` to this. |
| `internal` | bool | No public ingress. |
| `privateEgress` | bool | Allow egress to private ranges. |
| `platformApiEgress` | bool | Let pods call the kuso API over in-cluster DNS. |
| `waitForCI` | bool | Hold push/preview builds until the commit's GitHub checks are green. |
| `command` | list of strings | Overrides the image's CMD. |
| `domains` | list | `{host, tls, tlsSecret}`. `tlsSecret` is only for wildcard hosts (`*.example.com`) and is required there. |
| `env` | map | See [Env values](#env-values). |
| `scale` | `{min, max, targetCPU}` | If you leave it out, it resets to min 1, max 5, targetCPU 70. `min: 0` means scale to zero. |
| `sleep` | `{enabled, afterMinutes, nonProduction}` | If you leave it out, it resets to enabled false, afterMinutes 30. `nonProduction: off` keeps non-production envs awake. `sleep.wakeOn` is **not** a kuso.yml field. Set it with a PATCH; apply leaves it alone. |
| `placement` | `{labels: map, nodes: list}` | If you leave it out, the project default applies. |
| `volumes` | list | `{name, mountPath, sizeGi}`. |
| `static` | `{buildCmd, outputDir}` | For `runtime: static`. |
| `buildpacks` | `{builder}` | Builder image for `runtime: buildpacks`. |
| `image` | `{repository, tag, pullSecret}` | For `runtime: image`. `repository` has no tag. `tag` defaults to `latest`. `pullSecret` names a registry login (`kuso registry login`). |
| `release` | `{command, timeoutSeconds}` | Pre-deploy hook (migrations). It runs once per build, before promotion, and a failure blocks the rollout. If you leave it out, or leave `command` empty, the hook is cleared. The server's default timeout is 900s. |
| `buildArgs` | map | `--build-arg KEY=VAL`, the same in every environment. Don't put secrets here. |
| `publicEnv` | list of strings | Env vars inlined into the build output (for example `NEXT_PUBLIC_*`) that still differ per environment. The value is substituted at pod start. |
| `securityContext` | `{capabilities: {add: [...]}, allowPrivilegeEscalation}` | Opt-in. If you leave it out, the container drops all capabilities and can't escalate. |
| `size` | string | Pod-size preset (`small`/`medium`/`large` or an admin-defined one). Used **only when the service is created**. Later applies ignore it. |
| `watchPaths` | list of globs | Repo-root globs that gate push builds. The default is `path/**` when `path` is set. |

Not expressible in kuso.yml: addon subscriptions (`kuso project addon subscribe`), shared-secret subscriptions (`kuso env share`), per-environment overrides, and named environments.

### Env values

```yaml
env:
  LOG_LEVEL: info                      # literal
  API_URL: ${{ api.URL }}              # reference, still a literal string here
  DATABASE_URI: ${{ db.DATABASE_URL }} # addon key alias
  SESSION_SECRET: { generate: hex32 }  # minted once, stored in the service Secret
  STRIPE_SECRET_KEY: { secret: true }  # value set out-of-band; apply never touches it
  OTHER: { value: "x" }                # same as a plain literal
```

- `env` replaces the whole list. Leaving it out clears every literal env var on the service.
- `generate` accepts `hex16`, `hex32` or `hex64` (N random bytes, hex-encoded). The value is created on the first apply and stored in the per-service Secret, not in the CR. Later applies don't rotate it unless you pass `kuso apply --rotate-secrets`.
- `{secret: true}` records that a key exists in the managed Secret (set with `kuso secret set` or `kuso env set --secret`). Export emits this form so the key doesn't get lost on a round trip.
- If a value is the mask placeholder `••••••••` (an export made without `secrets:read`), apply keeps the stored value for that key. On a new service there is nothing stored, so the create is refused.
- `${{ }}` must be the whole value. Service keys: `URL`/`INTERNAL_URL` (`http://<svc>-<env>.<ns>.svc.cluster.local`, no port), `HOST`, `PORT` (always `80`), `PUBLIC_HOST`, `PUBLIC_URL`.

## Addons

| Field | Type | Notes |
|---|---|---|
| `name` | string | Required. |
| `kind` | string | Required. `postgres`, `redis`, `valkey`, `mongodb`, `mysql`, `rabbitmq`, `s3`, `mailpit`, `nats`, `meilisearch`, `clickhouse` or `redpanda`. |
| `version` | string | For example `"16"`. Quote it. |
| `size` | string | `small`, `medium` or `large`. |
| `ha` | bool | Replicated or clustered, where the kind supports it. |
| `storageSize` | string | For example `10Gi`. |
| `database` | string | Database name, where it applies. |
| `pooler` | `{enabled}` | PgBouncer in front of postgres. |
| `backup` | `{schedule, retentionDays}` | 5-field cron plus retention. |
| `placement` | `{labels, nodes}` | Node pinning. |
| `external` | `{secretName}` | Use an existing Secret (PlanetScale, Neon, RDS…) instead of running a datastore. Can't be combined with `useInstanceAddon`. |
| `useInstanceAddon` | string | Attach to an instance-shared addon. |
| `tls` | string | Postgres only: `disable` (default) or `require`. |

**Addons are create-only in apply.** Changing an existing addon's fields in the file doesn't update it. The plan reports the drift as "not applied". Change live addons with `kuso project addon update` (version, size, ha and storageSize can't be changed on a live addon).

## Crons

Only project crons (`kind: http` or `kind: command`) are managed here. Service crons, which run on a service's image, are created with `kuso cron add` and are neither listed nor pruned by apply. The parser rejects `kind: service`.

| Field | Type | Notes |
|---|---|---|
| `name` | string | Required. |
| `kind` | string | `http` or `command`. |
| `schedule` | string | Required. A 5-field cron expression (month and day-of-week accept names like `JAN`, `MON-FRI`) or one of `@yearly`, `@annually`, `@monthly`, `@weekly`, `@daily`, `@midnight`, `@hourly`. `@every`, `@reboot`, `?` and a seconds field are rejected. |
| `url` | string | Required for `http`. |
| `image` | string | `repo:tag`. Required for `command`. The tag defaults to `latest`. |
| `command` | list of strings | Required for `command`. |
| `suspend` | bool | |
| `pinImage` | bool | |

Cron failure webhooks (`onFailure`) are not a kuso.yml field. Set them with a PATCH on the cron.

## Example

```yaml
apiVersion: kuso/v1
project: shop
prune: false
services:
  - name: api
    repo: https://github.com/me/shop#api
    runtime: dockerfile
    port: 8080
    env:
      LOG_LEVEL: info
      SESSION_SECRET: { generate: hex32 }
    release:
      command: [./bin/migrate]
    volumes:
      - { name: data, mountPath: /var/lib/api, sizeGi: 5 }
addons:
  - { name: db, kind: postgres, version: "16" }
crons:
  - { name: ping, kind: http, schedule: "*/5 * * * *", url: https://shop.example.com/health }
```
