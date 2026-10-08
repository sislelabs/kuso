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
| `requestLimits` | `{maxConcurrent, ratePerSecond, burst}` | Project default for its services' [ingress limits](#ingress-limits). Leave the block out to keep whatever is set in the UI or CLI. |
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
| `uptime` | object | Uptime check settings: `disabled` (bool) opts the service out, `path` (string) is the path the check requests. Leave the block out to keep whatever is set in the UI or CLI. |
| `command` | list of strings | Overrides the image's CMD. |
| `domains` | list | `{host, tls, tlsSecret}`. `tlsSecret` is only for wildcard hosts (`*.example.com`) and is required there. |
| `env` | map | See [Env values](#env-values). |
| `scale` | `{min, max, targetCPU, scaleUpStabilizationSeconds, scaleUpPods, scaleUpPercent, scaleDownStabilizationSeconds}` | If you leave it out, it resets to min 1, max 5, targetCPU 70. `min: 0` means scale to zero. The last four set how fast the autoscaler moves; see [Autoscaling speed](#autoscaling-speed). |
| `requestLimits` | `{maxConcurrent, ratePerSecond, burst}` | This service's [ingress limits](#ingress-limits). Leave the block out to keep whatever is set in the UI or CLI. |
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

### Autoscaling speed

A service autoscales when `scale.max` is greater than `scale.min`. Four optional keys under `scale` control how fast it adds and removes pods. Leaving one out of the file resets it to its default.

| Key | Range | Default | Notes |
|---|---|---|---|
| `scaleUpStabilizationSeconds` | 0-3600 | 120 | How long CPU has to stay over `targetCPU` before pods are added. `0` adds them on the first reading. |
| `scaleUpPods` | 1-100 | 1 | Pods added per 60 seconds. |
| `scaleUpPercent` | 1-1000 | off | Also allows adding this percent of the current pods per 60 seconds. The autoscaler uses whichever of the two allows more. |
| `scaleDownStabilizationSeconds` | 0-3600 | 300 | How long load has to stay low before pods are removed. Removal is always 1 pod per 60 seconds. |

With the defaults, a service at min 2, max 8 takes roughly 8 minutes of sustained load to reach 8 pods: a 120 second wait, then one pod a minute. The defaults are slow on purpose. During a deploy, new pods report no CPU metrics for up to a minute, and a short scale-up window lets that gap add pods the service doesn't need.

For traffic that arrives all at once (a ticket on-sale), ask for a faster ramp:

```yaml
scale:
  min: 2
  max: 8
  targetCPU: 70
  scaleUpStabilizationSeconds: 0
  scaleUpPods: 4
```

Under enough load this goes from 2 to 8 pods in about two minutes, plus the time the pods take to start. If the spike is scheduled, raising `min` beforehand is faster still.

A faster ramp is only accepted on a service whose pods have a memory limit. That means `scaleUpPods` above 1, any `scaleUpPercent`, or a `scaleUpStabilizationSeconds` under 60. Without a limit, each new pod could grow until its node runs out of memory, and the autoscaler would be adding several a minute. With one, the most the service can use is `max` times the limit. Set it first with a pod size (`kuso project service set <project> <service> --size medium`, or Settings → Scale); apply fails with an error naming this rule otherwise. A service that was already scaling fast without a limit keeps running, and gets the error the next time its `scale` or resources are edited.

CPU is not capped per pod: pod sizes set a CPU request and no CPU limit. Under contention each pod gets CPU in proportion to its request, so an autoscaled service competes with `max` times its request, not with every core on the node.

The same keys are accepted by the service PATCH API, by `kuso project service set` (`--scale-up-stabilization`, `--scale-up-pods`, `--scale-up-percent`, `--scale-down-stabilization`) and in the web UI under Settings → Scale. In a PATCH or CLI call, `-1` resets a stabilization window to its default and `0` resets `scaleUpPods` or `scaleUpPercent`.

### Ingress limits

Every public service on a cluster is served by the same Traefik pods. `requestLimits` caps what one service can ask of them, so a flood against one hostname is answered with `429 Too Many Requests` for that service while the others keep being served.

| Key | Default | Notes |
|---|---|---|
| `maxConcurrent` | 1000 | Requests in flight at once. Past it, new requests get 429 until others finish. `-1` removes the cap. A websocket or SSE stream holds a slot for as long as it is open, so raise this for a service with more than a thousand long-lived connections per ingress replica. |
| `ratePerSecond` | none | Requests per second. `-1` removes a limit inherited from the project. |
| `burst` | `ratePerSecond` | Requests allowed above the rate in one burst. |

The numbers are counted per hostname and per Traefik replica. A standard install runs two replicas, so the cluster-wide ceiling for a service is twice the number.

A service's block overrides the project's top-level `requestLimits` one key at a time, and the project's overrides the defaults above. Preview and staging environments get the same limits as production.

```yaml
project: shop
requestLimits: { maxConcurrent: 500 }        # default for every service
services:
  - name: api
    requestLimits: { maxConcurrent: 2000, ratePerSecond: 300, burst: 600 }
  - name: stream
    requestLimits: { maxConcurrent: -1 }     # long-lived connections, no cap
```

The same settings are in the service PATCH API (`{"requestLimits":{"maxConcurrent":2000}}`), the project PATCH API, `kuso project service set` and `kuso project update` (`--max-concurrent`, `--rate-limit`, `--rate-burst`), and the web UI under service Settings → Networking and Project Settings. In a PATCH or CLI call, `0` clears a key so it is inherited again.

The limits are enforced by Traefik and need the operator to be allowed to create Traefik Middlewares. Installs made before this feature get it when `deploy/operator.yaml` from the new release is applied; until then no limits are rendered and kuso-server logs a warning at boot.

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
