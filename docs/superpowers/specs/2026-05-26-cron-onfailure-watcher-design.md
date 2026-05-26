# Cron onFailure watcher

## Status

CRD field `KusoCron.spec.onFailure` shipped in v0.15.0. **Watcher implementation deferred** — schema is forward-compatible so users can author it now.

## What ships next session

A `cronwatch.Watcher` goroutine in `server-go/internal/cronwatch/`, started from `cmd/server/main.go` alongside `nodewatch.Watcher` and `nodemetrics.Sampler`.

## Mechanics

1. Watch `batch/v1` `Jobs` cluster-wide (filtered by label `kuso.sislelabs.com/cron`).
2. On Job → `.status.conditions[Failed]=True`, look up the parent `KusoCron` by the `kuso.sislelabs.com/cron` label.
3. If `spec.onFailure.webhookURL` is set:
   - Build payload: `{project, service, cron, jobName, exitCode, startedAt, finishedAt, logsURL}`
   - If `secretRef` set: HMAC-SHA256 sign with the resolved secret value, attach as `X-Kuso-Signature: sha256=<hex>`
   - POST with 5s timeout, 3 retries (1s/4s/9s backoff)
4. Mirror the event into the notify dispatcher as a `cron.failed` event so the bell icon + Slack/Discord channels light up.
5. Idempotency: store last-seen Job UID per cron in an in-memory map. Failed Jobs are pruned by `failedJobsHistoryLimit` so the map self-bounds.

## Auth

`secretRef.name` follows the `<addon>-conn` admission pattern — same as `envVars[].valueFrom.secretKeyRef`. Tenants can't pick `kuso-server-secrets`. Workaround for "I need to sign with a custom secret": add a `KusoAddon kind=raw-secret` (out of scope for this gap).

## Tests

- Unit: payload builder + HMAC signer + retry policy.
- Integration: fake clientset, synthesize Job-failed event, assert webhook is POSTed once with correct headers.
- E2E: tail a real failing cron on the test cluster, point at requestcatcher.com, eyeball the payload.

## Why deferred

The Watcher pattern is real new code (~300 lines) with its own lifecycle, observability, and shutdown semantics. Doing it right means mirroring `nodewatch.Watcher`'s telemetry + reconcile-after-restart logic. That's a focused 1-day build, not a YOLO drop.

The CRD field shipping today means users can:
- Author it in `kuso.yaml` immediately
- Get schema validation
- Be on the upgrade path the day the watcher lands

When the watcher ships, no spec migration is needed.
