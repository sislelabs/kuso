# Edit Safety

Which spec fields on a running `KusoEnvironment` (or `KusoService`) can you change live, and which require a redeploy or have a blast-radius worth knowing about? This doc is the contract — both for users editing via UI / CLI / `kubectl edit`, and for the UI itself, which should treat each row below the way the table prescribes.

## Heuristic

> If the change rewrites a Deployment podspec, it triggers a rolling restart. If it touches the Ingress, it can break the public URL for ~30s while traefik reloads. If it touches a PVC or Service port, treat as a redeploy. If it touches TLS, it can hit Let's Encrypt rate limits.

## KusoEnvironment

| Field | Live-editable? | What happens | Notes |
| --- | --- | --- | --- |
| `envVars` | ✅ Yes | Operator re-renders Deployment → rolling restart of the pods. ~10–30s of mixed-version traffic. | Server-managed: written by service propagation and the per-env scoped editor (names pinned there are listed in `envOverrides` and survive propagation). |
| `envFromSecrets` | ✅ Yes | Changing the list rewrites the podspec → rolling restart. Changing a referenced Secret's *contents* does not restart anything by itself; the server bumps `secretsRev` for that. | Used by the addon-conn pattern. Server-derived from `subscribedAddons` / `sharedEnvKeys`; hand edits are overwritten on the next propagation. |
| `secretsRev` | ⚠️ Server-managed | Rendered as the `kuso.sislelabs.com/secrets-rev` pod annotation, so bumping it forces a rolling restart even when no other field changed. | Don't edit by hand; the secrets package owns this. |
| `image.tag` | ✅ Yes | Triggers a pull + rolling restart. The standard "deploy" path. | This is what `kuso build trigger` ends up writing. |
| `replicaCount` | ✅ Yes | Pure scale; no pod restart. | Ignored when `autoscaling.enabled = true` (the chart omits `replicas` so the HPA owns it), when `stopped` is set (pinned to 0), and while `image.repository` is empty (0). |
| `autoscaling.*` | ✅ Yes | HPA gets re-created if the chart re-renders the manifest. Brief HPA gap. | Don't toggle `enabled` on/off rapidly — gives the HPA a moment to settle. |
| `port` | ⚠️ With caveat | Rewrites the container port, the `PORT` env var and the probes (rolling restart) plus the Service `targetPort` → ~5s where new connections fail until traefik picks up the new endpoint. | Match this to what your app actually listens on. |
| `host` | ⚠️ With caveat | Ingress rewrites the rule. cert-manager re-issues (into the same `<fqn>-tls` secret) if TLS is on and the host is TLS-eligible (`tlsHosts`). **LE prod limit: 5 failed validations/hour, 50 certs/week per eTLD+1.** | Don't churn this in a loop. |
| `additionalHosts` | ⚠️ With caveat | Each new host gets its own TLS secret (`<fqn>-tls-extra-<host>`), minted independently. Removing one does not affect the others. | Server managed via `propagateDomainsToEnvs`. UI edits flow through `PatchService`. Only hosts the server lists in `tlsHosts` get a cert; the rest are HTTP-only. Wildcard hosts live in `wildcardDomains` with a pre-provisioned secret and never hit cert-manager. |
| `tlsEnabled`, `clusterIssuer` | ⚠️ With caveat | Toggling `tlsEnabled` off/on re-issues. Switching `clusterIssuer` (staging↔prod) re-issues on the new issuer. | Use sparingly; same LE rate limit applies. |
| `ingressClassName` | ⚠️ With caveat | Ingress is recreated on the new class. ~30s URL gap while traefik / the new controller picks it up. | Almost always "traefik". |
| `placement` | ⚠️ Triggers reschedule | nodeSelector / affinity changes → pods evicted from non-matching nodes, restarted on the matching ones. | Brief downtime if no matching node has capacity. |
| `volumes` | ❌ Add only | Adding a volume → new PVC + Deployment redeploy. **Removing a volume detaches the PVC and orphans the data**; the PVC is not auto-deleted by the chart but the pod no longer mounts it. An env with any ReadWriteOnce volume deploys with `strategy: Recreate`: the old pod stops before the new one starts, so **every deploy has a brief outage**. A PVC can't shrink, and one whose StorageClass lacks `allowVolumeExpansion` (local-path) can't grow either; the API refuses those sizes with a 409, because the apiserver would reject the PVC and fail the env's whole helm release. A volume sent without `sizeGi`, or re-added after removal, keeps its existing disk's size. | Treat removal as destructive. Expect a gap on deploy for RWO-volume services. |
| `runtime` | ❌ Don't change live | Switching between `worker` and any web runtime (`dockerfile`/`nixpacks`/`buildpacks`/`static`/`image`) adds or drops the Service, Ingress and probes. Equivalent to recreating the env. | Better: delete the env and create a new one. |
| `command` | ✅ Yes | Rolling restart with the new argv. | Set for `runtime = worker`; the chart renders it whenever present. |
| `resources`, `securityContext` | ✅ Yes | Rewrites the container spec → rolling restart. A memory limit below the app's working set OOM-kills the new pods; the rollout stalls and the old pods keep serving. | Propagated from the service. |
| `healthcheck` | ⚠️ Rolling restart | See the service-level row. | Propagated from the service. |
| `internal` | ⚠️ With caveat | `true` deletes the Ingress (and activator route): the public URL stops answering immediately. Pods are untouched. | Propagated from the service (`--internal=on`). |
| `privateEgress`, `platformApiEgress` | ✅ Yes | Flips a pod label → rolling restart; NetworkPolicies then deny (or allow) public-internet / kuso-API egress. An app that calls external APIs breaks under `privateEgress`. | Propagated from the service. |
| `stopped` | ⚠️ Downtime by design | Pins replicas to 0, removes the HPA, and routes the Ingress to the activator, which serves a "service stopped" page and refuses to wake. | Propagated from the service (`kuso project service stop/start`). |
| `spreadPolicy`, `autoSleep`, `envScope`, `tlsHosts` | ⚠️ Server-managed | Recomputed by the server/scaledown watcher on every write; hand edits are overwritten. `spreadPolicy` and `envScope` rewrite the pod template (rolling restart). | Don't edit by hand. |

## KusoService (the parent)

Edits to `KusoService` propagate down to every `KusoEnvironment` owned by that service via the server's propagation step. So everything in the table above also applies when triggered from a service-level edit.

| Field | Live-editable? | Notes |
| --- | --- | --- |
| `displayName` | ✅ Yes, free | Pure UI label. No reconcile. |
| `repo` | ✅ Yes | Next build uses the new repo. Existing pods keep running their image. |
| `domains` | ⚠️ See above | Propagates to every env's `additionalHosts`. |
| `envVars` | ✅ Yes | Propagates. Rolling restart of every env. |
| `port` | ⚠️ With caveat | Propagates. Same caveat as env-level. |
| `scale.*` | ✅ Yes | Propagates to env `autoscaling`, including the speed keys (`scaleUpStabilizationSeconds`, `scaleUpPods`, `scaleUpPercent`, `scaleDownStabilizationSeconds`). |
| `requestLimits.*` | ✅ Yes | Re-renders the service's Traefik Middleware and the Ingress annotation. No pod restart. Lowering `maxConcurrent` below current traffic starts returning 429 at once. |
| `sleep` | ⚠️ Yes, with caveat | Scale-down watcher pauses pods after `afterMinutes` of zero traffic; the first request hits the activator, which **holds the live connection and proxies the original request through** once a pod is Ready (default budget 60s, `KUSO_ACTIVATOR_HOLD_SECONDS`). So a woken request — including a webhook POST body — is delivered, not dropped, *as long as cold-start fits the hold budget*. The residual risk is a **cold image pull** on a node that doesn't have the image: if wake exceeds the budget the activator returns 503+Retry-After and a webhook sender that doesn't retry loses that event. For services taking third-party webhooks/payment callbacks (Stripe, ePay, GitHub, Slack, etc.), use **`sleep.wakeOn.excludePaths`** to keep the deployment warm (forces min-1 replica and excludes it from scale-down — whole-deployment, not per-path routing). Sleep with no excludePaths is safe for backoffice tools, internal dashboards, and preview environments. **Which envs sleep:** production sleeps only when the service sets `sleep.enabled`. Every other env (named envs like staging, env-group clones, PR previews) sleeps **by default** after 30 idle minutes, even if the service never set `sleep.enabled`; opt out with `sleep.nonProduction: off` (`kuso project service sleep <p> <s> --non-production off`). So a staging env that receives webhooks needs `excludePaths` or `nonProduction: off` too. Envs that never sleep: workers, internal services, services with `excludePaths`, stopped services, and projects with `alwaysOn`. `sleep` is a service-level field with no per-env override. **In-cluster callers:** server-side calls through `${{ svc.URL }}` / `HOST` (any `<env>.<ns>.svc` value in another env's vars) bypass the activator and can't wake a slept env. kuso therefore keeps a referenced env awake while an env that calls it is awake and not itself going to sleep; mutually-calling idle envs sleep together, and waking a caller through the activator wakes the slept envs it references. A hard-coded in-cluster address that doesn't appear in env vars (config files, DB rows) is invisible to this; such a target needs `nonProduction: off` / no `sleep.enabled`. Open WebSocket/SSE connections through the activator count as activity for as long as they stay open. |
| `release` | ✅ Yes, on next deploy | Pre-deploy hook (`spec.release.command`) takes effect at next build. Existing pods keep their image until the release Job succeeds; on Job failure the image is NOT promoted. Removing the field disables the hook on next deploy. |
| `buildArgs` | ✅ Yes, on next build | Non-secret build-time constants (v0.23.1+): flow to `--build-arg` (dockerfile) / build-time `ENV` (nixpacks). No effect on running pods — takes effect only when the next build runs, so trigger a rebuild to apply. Plain literals only; `secretKeyRef` values are dropped server-side (never persisted in a layer). Not for secrets — use a release hook or a `--mount=type=secret` RUN. |
| `snapshotBeforeDeploy` | ✅ Yes, no restart | Only affects future deploys. When true AND a release hook exists, the poller snapshots subscribed postgres addons (a normal manifested backup, `trigger=pre_deploy`) before the migration runs; on migration failure the build is annotated with the snapshot keys for a one-click restore. No effect on running pods; no data impact from toggling. Snapshot infra-failure blocks that deploy. |
| `uptime` | ✅ Yes, no restart | `disabled` and `path` are read by the uptime loop on its next tick (within a minute). Nothing propagates to envs and no pod restarts. Opting out while a service is down closes that outage without a "recovered" message. |
| `healthcheck` | ⚠️ Rolling restart | Propagates to every env and rewrites the probes → rolling restart. With a `path` set, pods get an HTTP readiness AND liveness probe: a path that doesn't answer 2xx/3xx keeps the new pods from going ready and restarts them after `failureThreshold × periodSeconds`; the rollout stalls and the old pods keep serving. Empty `path` = back to TCP probes, no liveness. |
| `placement` | ⚠️ Triggers reschedule | Propagates to envs that don't have their own override. |
| `volumes` | ❌ Add only | Same as env-level: removal orphans data. |
| `runtime` | ❌ Don't change live | Same as env-level. |
| `autoDeployPaused` | ✅ Yes, no restart | Read by the push dispatcher on the next GitHub push. True skips push builds for this service; manual deploys, deploy hooks and PR previews still run. No effect on running pods. `kuso project service set <p> <s> --auto-deploy off`. |
| `previews.disabled` | ✅ Yes | Affects only future PR opens; existing preview envs survive until their TTL. |
| `resources`, `securityContext` | ✅ Yes | Propagates. Rolling restart of every env (see env-level row). |
| `internal`, `privateEgress`, `platformApiEgress`, `stopped` | ⚠️ See env-level rows | Propagates to every env. `internal` and `stopped` take the public URL down for every env at once. |
| `subscribedAddons`, `sharedEnvKeys` | ⚠️ Rolling restart | Rewrites every env's `envFromSecrets` / per-key env entries → rolling restart. Unsubscribing an addon removes its connection vars from the pods immediately. `nil` (legacy) mounts everything; an empty list mounts nothing. |
| `publicEnv` | ✅ Yes | Propagates; changes the `KUSO_PUBLIC_ENV` env var → rolling restart. Names only matter if the image was built with the matching `__KUSO_RUNTIME_<KEY>__` placeholders. |

## KusoAddon

Most fields are **immutable post-creation** — the helm chart is provisioning a StatefulSet with a PVC, and StatefulSet specs are not in-place editable. Concretely:

| Field | Live-editable? | Notes |
| --- | --- | --- |
| `placement` | ⚠️ With caveat | Pod gets evicted + rescheduled. Brief data-plane gap; clients reconnect. **On local-path storage the PVC is bound to the node it was created on**: a placement that excludes that node leaves the pod Pending (the data does not move). The server only checks that some schedulable node matches. Set via `kuso project addon placement set`. |
| `resources` | ✅ Yes | Rolling restart of the addon pod. An explicit `resources` block overrides the size-derived requests/limits. CR-only; no API/CLI field. |
| `version` | ❌ No | The server refuses with 409. Treat as new addon. Migrate data with `kuso addon-backup download` → create new → restore. |
| `size` | ⚠️ Only when storage doesn't move | Size sets the pod's requests/limits and, when `storageSize` is empty, the volume size (small=5Gi, medium=20Gi, large=100Gi). The server accepts a size change when `storageSize` is pinned or when the new size maps to the same storage; otherwise it refuses with 409, because the StatefulSet PVC template is immutable. An accepted change restarts the addon pod. |
| `ha`, `storageSize` | ❌ No | Refused with 409. StatefulSet PVC templates are immutable, and flipping `ha` switches to a different StatefulSet with an empty volume. |
| `passwordSecret`, `database` | ❌ No | Changing these orphans the existing data (`database` is refused with 409; `passwordSecret` is CR-only and not checked). There is no plaintext `password` field on the CR; generated passwords live in `<name>-conn`. |
| `backup.*` | ✅ Yes, no restart | Re-renders the backup CronJob only. Schedule must be 5-field cron; `retentionDays` 0..3650. |
| `pooler`, `publicTCP`, `webUI` | ⚠️ With caveat | Add or remove side resources (PgBouncer, Traefik TCP route, web UI); the DB pod and data are untouched, but apps pointed at the pooler/public endpoint lose it when it is turned off. |
| `tls` (postgres) | ⚠️ With caveat | Only touches the pod template + conn secret, so the flip is safe for the data: DB pod restarts serving (or dropping) TLS and `<name>-conn` re-renders with the matching `sslmode`. BUT consuming app pods resolve `envFrom` at container start — they keep the stale `sslmode` until restarted. Flip the addon, then restart every subscribed env. Exposed on the API, CLI, and kuso.yml since v0.18.123: `kuso project addon update <p> <addon> --tls require\|disable`. node-postgres rejects the self-signed cert that `require` serves; Go pgx/libpq accept it. |

If `kuso project addon update` rejects an edit with a 409 ("not supported" / "immutable"), the only path is `backup → delete → create new → restore`. Yes, this is annoying. It's the cost of using StatefulSets honestly instead of pretending they're mutable.

## Cross-cutting hazards

1. **TLS rate limits are the most common foot-gun.** Let's Encrypt prod allows 5 failed challenges per hour and 50 certs per week per registered domain (eTLD+1, e.g. `example.com` not `app.example.com`). Adding 51 distinct hostnames under one root in a week will fail; removing and re-adding `myapp.example.com` six times in an hour will fail. The UI should rate-limit user-driven domain edits and prefer additive changes.
2. **PVC removal orphans data.** The `kusoenvironment` chart emits one PVC per volume with `helm.sh/resource-policy: keep`, so helm does not delete it when a `volumes` entry is removed (the `kusoservice` chart renders only a marker, no PVCs). This is a deliberate safety choice — the user can recover the PVC via `kubectl` if they edited by mistake. It does mean removing a volume is a "the data is still there but no pod mounts it" state, not a "data is gone" state.
3. **Concurrent edits can lose.** `PATCH /api/projects/{p}/services/{s}` replaces whole list fields (`envVars`, `domains`, …); if two clients patch the same list simultaneously, last-write-wins. The delta endpoints (`POST …/domains`, `DELETE …/domains/{host}`, `PUT/DELETE …/env-vars/{name}`) serialise per service, so single-item adds/removes from two clients both land. There is no `resourceVersion` conflict detection on the API; the rule of thumb is still: don't edit the same service from two browser tabs.
4. **Editing live during a rolling deploy is fine** but compounds the restart window. The operator does not coalesce — N edits → N rollouts. To batch, use `POST …/services/{s}/changes` (staged variable writes + a spec patch applied together, so each env CR is written and rolled once).

## What the UI should enforce

Long-term, the rows above with ⚠️ or ❌ should produce confirmation dialogs in the dashboard:

- **❌ entries** → "this will recreate / orphan data — type the env name to confirm" pattern.
- **⚠️ entries that hit cert issuance** → "this may consume Let's Encrypt budget; continue?" if it's the Nth domain edit in the last hour.
- **⚠️ entries that trigger reschedule** → just a banner "this will restart your pods" once per save.

Until that's wired, this doc is the contract. If you're about to mass-edit live envs from a script, read the table once.
