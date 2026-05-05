# Edit Safety

Which spec fields on a running `KusoEnvironment` (or `KusoService`) can you change live, and which require a redeploy or have a blast-radius worth knowing about? This doc is the contract — both for users editing via UI / CLI / `kubectl edit`, and for the UI itself, which should treat each row below the way the table prescribes.

## Heuristic

> If the change rewrites a Deployment podspec, it triggers a rolling restart. If it touches the Ingress, it can break the public URL for ~30s while traefik reloads. If it touches a PVC or Service port, treat as a redeploy. If it touches TLS, it can hit Let's Encrypt rate limits.

## KusoEnvironment

| Field | Live-editable? | What happens | Notes |
| --- | --- | --- | --- |
| `envVars` | ✅ Yes | Operator re-renders Deployment → rolling restart of the pods. ~10–30s of mixed-version traffic. | Editor on the service overlay does this. Each save is a full PUT. |
| `envFromSecrets` | ✅ Yes | Same as above, plus the operator bumps the env-from secret rev. | Used by the addon-conn pattern. |
| `secretsRev` | ⚠️ Server-managed | Bumping forces a pod restart even when no other field changed. | Don't edit by hand; the secrets package owns this. |
| `image.tag` | ✅ Yes | Triggers a pull + rolling restart. The standard "deploy" path. | This is what `kuso build trigger` ends up writing. |
| `replicaCount` | ✅ Yes | Pure scale; no pod restart unless `autoscaling.enabled`. | Ignored when `autoscaling.enabled = true`. |
| `autoscaling.*` | ✅ Yes | HPA gets re-created if the chart re-renders the manifest. Brief HPA gap. | Don't toggle `enabled` on/off rapidly — gives the HPA a moment to settle. |
| `port` | ⚠️ With caveat | Service `targetPort` changes → ~5s where new connections fail until traefik picks up the new endpoint. | Match this to what your app actually listens on. |
| `host` | ⚠️ With caveat | Ingress rewrites the rule. cert-manager re-issues if TLS is on. **LE prod limit: 5 failed validations/hour, 50 certs/week per eTLD+1.** | Don't churn this in a loop. |
| `additionalHosts` | ⚠️ With caveat | Each new host gets its own TLS secret (`<fqn>-tls-extra-<host>`), minted independently. Removing one does not affect the others. | Server managed via `propagateDomainsToEnvs`. UI edits flow through `PatchService`. |
| `tlsEnabled`, `clusterIssuer` | ⚠️ With caveat | Toggling `tlsEnabled` off/on re-issues. Switching `clusterIssuer` (staging↔prod) re-issues on the new issuer. | Use sparingly; same LE rate limit applies. |
| `ingressClassName` | ⚠️ With caveat | Ingress is recreated on the new class. ~30s URL gap while traefik / the new controller picks it up. | Almost always "traefik". |
| `placement` | ⚠️ Triggers reschedule | nodeSelector / affinity changes → pods evicted from non-matching nodes, restarted on the matching ones. | Brief downtime if no matching node has capacity. |
| `volumes` | ❌ Add only | Adding a volume → new PVC + Deployment redeploy. **Removing a volume detaches the PVC and orphans the data**; the PVC is not auto-deleted by the chart but the pod no longer mounts it. | Treat removal as destructive. |
| `runtime` | ❌ Don't change live | Switching between `worker` ↔ `web` rewrites the Deployment, Service, Ingress, and probes. Equivalent to recreating the env. | Better: delete the env and create a new one. |
| `command` | ✅ Yes (worker) | Rolling restart with the new argv. | Ignored unless `runtime = worker`. |

## KusoService (the parent)

Edits to `KusoService` propagate down to every `KusoEnvironment` owned by that service via the server's propagation step. So everything in the table above also applies when triggered from a service-level edit.

| Field | Live-editable? | Notes |
| --- | --- | --- |
| `displayName` | ✅ Yes, free | Pure UI label. No reconcile. |
| `repo` | ✅ Yes | Next build uses the new repo. Existing pods keep running their image. |
| `domains` | ⚠️ See above | Propagates to every env's `additionalHosts`. |
| `envVars` | ✅ Yes | Propagates. Rolling restart of every env. |
| `port` | ⚠️ With caveat | Propagates. Same caveat as env-level. |
| `scale.*` | ✅ Yes | Propagates to env `autoscaling`. |
| `sleep` | ✅ Yes | Operator pauses pods after `afterMinutes`; first request wakes them. |
| `placement` | ⚠️ Triggers reschedule | Propagates to envs that don't have their own override. |
| `volumes` | ❌ Add only | Same as env-level: removal orphans data. |
| `runtime` | ❌ Don't change live | Same as env-level. |
| `previews.disabled` | ✅ Yes | Affects only future PR opens; existing preview envs survive until their TTL. |

## KusoAddon

Most fields are **immutable post-creation** — the helm chart is provisioning a StatefulSet with a PVC, and StatefulSet specs are not in-place editable. Concretely:

| Field | Live-editable? | Notes |
| --- | --- | --- |
| `placement` | ✅ Yes | Pod gets evicted + rescheduled. Brief data-plane gap; clients reconnect. |
| `resources` | ✅ Yes | Rolling restart of the addon pod. |
| `version` | ❌ No | Treat as new addon. Migrate data with `kuso addon backup` → create new → restore. |
| `size`, `ha`, `storageSize` | ❌ No | StatefulSet PVC templates are immutable. |
| `password`, `database` | ❌ No | Changing these orphans the existing data. |

If `kuso addon update` rejects an edit with "immutable", the only path is `backup → delete → create new → restore`. Yes, this is annoying. It's the cost of using StatefulSets honestly instead of pretending they're mutable.

## Cross-cutting hazards

1. **TLS rate limits are the most common foot-gun.** Let's Encrypt prod allows 5 failed challenges per hour and 50 certs per week per registered domain (eTLD+1, e.g. `example.com` not `app.example.com`). Adding 51 distinct hostnames under one root in a week will fail; removing and re-adding `myapp.example.com` six times in an hour will fail. The UI should rate-limit user-driven domain edits and prefer additive changes.
2. **PVC removal orphans data.** Helm's `kusoservice` / `kusoenvironment` charts emit PVCs but do not delete them when a `volumes` entry is removed. This is a deliberate safety choice — the user can recover the PVC via `kubectl` if they edited by mistake. It does mean removing a volume is a "the data is still there but no pod mounts it" state, not a "data is gone" state.
3. **Concurrent edits will lose.** kuso uses PUT-the-whole-spec semantics; if two clients edit the same env simultaneously, last-write-wins. The UI should refresh-before-save where possible. Pure conflict detection via `resourceVersion` is a future improvement; for now, the rule of thumb is: don't edit the same service from two browser tabs.
4. **Editing live during a rolling deploy is fine** but compounds the restart window. The operator does not coalesce — N edits → N rollouts.

## What the UI should enforce

Long-term, the rows above with ⚠️ or ❌ should produce confirmation dialogs in the dashboard:

- **❌ entries** → "this will recreate / orphan data — type the env name to confirm" pattern.
- **⚠️ entries that hit cert issuance** → "this may consume Let's Encrypt budget; continue?" if it's the Nth domain edit in the last hour.
- **⚠️ entries that trigger reschedule** → just a banner "this will restart your pods" once per save.

Until that's wired, this doc is the contract. If you're about to mass-edit live envs from a script, read the table once.
