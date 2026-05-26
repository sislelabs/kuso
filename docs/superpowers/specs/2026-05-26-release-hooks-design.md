# Release hooks (gap 1)

## Status

**Deferred to next session.** Schema field not yet shipped. Pre-shipping the field without the runtime would create a vapor surface (users author `spec.release.command`, deploys ignore it). Worse than not shipping.

## What it solves

Today, services with DB migrations bake them into the entrypoint (`migrate up && exec /app/api`). Two failure modes:

1. **Race**: two API replicas race to apply the same migration on rollout.
2. **Probe thrash**: a 90s migration exceeds the readiness probe deadline; the new pod fails its probe before the migration finishes; deployment thrashes.

A "release phase" — Heroku-style — runs the migration as a separate Job before the new image scales up. Same pattern Render, Fly, Railway all ship.

## Why not chart hooks

`helm-operator` does not honor `helm.sh/hook: pre-install,pre-upgrade` the way `helm install` does — it diffs and applies, skipping hook semantics. Chart-level hooks are out.

## Architecture (correct)

The build poller in `server-go/internal/builds/builds.go:promoteImage` (around line 2060) is the splice point. Currently:

```
build succeeds → patch env.spec.image.tag → operator rolls deployment
```

After:

```
build succeeds
  → if service.spec.release.command set:
       create kube Job from a per-build template (image = new build,
         env = effective env vars + envFromSecrets, command = release.command)
       poll Job up to release.timeoutSeconds (default 900s)
       on success → patch env.spec.image.tag
       on failure → mark build as release-failed, do NOT patch tag,
         emit notify event, do NOT roll the deployment
  → else fall through (today's behavior)
```

Job naming: `<env>-release-<short-image-tag>` so a re-deploy of the same tag is a no-op (Job already exists, succeeded).

## Schema

```yaml
# KusoService
spec:
  release:
    command: ["./bin/migrate"]
    image: ""               # default: use the new build's image
    timeoutSeconds: 900
    envFromSecrets: []      # default: same as KusoEnvironment.envFromSecrets
```

Mirrors down onto `KusoEnvironment` via the existing propagation step.

## What needs touching

1. CRD: both `KusoService` and `KusoEnvironment` get `spec.release`. Closed schema (no preserve-unknown).
2. `server-go/internal/projects/services_ops.go`: include `release` in the propagation list.
3. `server-go/internal/builds/builds.go:promoteImage`: insert the Job-run gate.
4. New package `server-go/internal/releaserun/`: Job template + poller. Mirrors `builds` lifecycle.
5. CLI: `kuso release-log <project> <service> [--env production]` tails the latest release Job.
6. Web: tab on service overlay showing release command + last run status.
7. EDIT_SAFETY.md row: editing `spec.release.command` takes effect on next deploy (no immediate reconcile).
8. Smoke test: configure papelito-web with `release.command: ["echo", "released"]`, trigger a build, confirm Job fires + completes + image patches.

## Effort

Real: 2–3 focused days. The Job poller + failure semantics + edge cases (Job stuck pending due to image pull, Job OOM, Job network-partition from kube apiserver) need real test coverage.

## Workaround until shipped

Users can run migrations via a manually-triggered `KusoRun` (already exists) before scaling up a new deploy. Manual, but functional.
