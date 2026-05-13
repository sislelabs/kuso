# Correctness & Bug Review — 2026-05-13

**Scope:** Post-v0.10.0, Go build controller freshly landed.
**Reviewer:** Claude Sonnet 4.6 (automated)
**Files reviewed:** `server-go/internal/buildcontroller/`, `server-go/internal/buildreaper/`, `server-go/internal/builds/`, `server-go/internal/migration/`, `server-go/internal/projects/`, `server-go/internal/kube/`, `server-go/cmd/kuso-server/main.go`, `coolify/`

Already-fixed issues from prior reviews are explicitly excluded.

---

## P0 — Will produce wrong behaviour in production today

### P0-1 · `buildcontroller`: dedup map entries survive across leader re-elections; permanently-stuck builds possible

**File:** `server-go/internal/buildcontroller/buildcontroller.go:111-143`

The `running` map inside `buildcontroller.Service` is populated when the leader's `Start()` is called and never cleared. When the leader lease is lost and then re-acquired (the `RunWhenLeader` loop in `main.go:581-588` explicitly calls `cfg.Run` a second time for re-elected leaders), `Start()` is called again on the **same** `&buildcontroller.Service` struct, adding a second informer handler. But the `running` map from the previous tenure is never reset.

Consequence: any KusoBuild CR that was present in `running` when the leader lost the lease — whether it finished or not — will be permanently ignored by the new handler. A build that failed mid-reconcile and was removed from `running` on error path is fine; a build that was successfully enqueued (added to `running`) but whose SA/Job creation subsequently timed out never retries, because the key stays in the map and the early-return guard fires on every subsequent informer event. The user sees the build stuck in `Pending` forever.

**Secondary issue:** Two informer handlers are registered on re-election. Both call `reconcile()`. The first dedup check prevents double-Job creation, but the second handler adds redundant goroutine overhead and log noise.

**Fix sketch:** In `Start()`, reset `s.running = make(map[string]struct{})` before attaching the event handler, and track registered handler count to guard against double-registration (or construct a fresh `buildcontroller.Service` per leader tenure).

---

### P0-2 · `kube.update`: spec writes silently discard concurrent spec changes from operators on conflict retry

**File:** `server-go/internal/kube/crds.go:213-241`

The `update()` helper is called for `UpdateKusoEnvironment`, `UpdateKusoService`, `UpdateKusoAddon`, etc. On a 409 Conflict it fetches the latest resourceVersion and retries the **same unstructured object** (the caller's intended write). This is only correct when the only conflicting writer is helm-operator's status-subresource patches (which don't touch spec). 

In practice other writers exist: `propagateChangedToEnvs` may call `UpdateKusoEnvironment` on the same env from a concurrent domain-delta and a scale-delta on different request goroutines. The first 409 retry correctly bumps rv; if a second concurrent write has arrived in the meantime (to a different spec field), the retry clobbers it. The comment at line 212 explicitly acknowledges this assumption ("helm-operator only writes status") but does not defend against server-go's own concurrent propagation paths.

The most dangerous case: two near-simultaneous `PatchService` calls that change different fields. Both read the env CR (phase 1), both apply their delta, the second sees a conflict and retries with its own full object — silently overwriting the first writer's successful spec patch.

**Fix sketch:** Use `UpdateKusoEnvironment` with a `updateWithRetry`-style callback that re-applies the specific delta on each retry, the same pattern already used in `UpdateKusoServiceWithRetry`. The env path has enough concurrent writers (propagation, build promotion, sleep-wake, GitHub dispatcher) to warrant it.

---

### P0-3 · `buildcontroller.Start` called from singleton worker, but `buildreaper.Start` also called from a **separate** singleton worker, both sharing the same `kube.Cache` informer

**File:** `server-go/cmd/kuso-server/main.go:540-551`

`buildcontroller.Service.Start(workCtx)` and `buildreaper.Service.Start(workCtx)` both call `inf.AddEventHandler` on the same `KusoBuild` shared informer. That is documented as safe. **However**, both are called from the `startSingletons` closure, which is called once per leader tenure. On re-election both register a **second** event handler on the same informer — AddEventHandler on a SharedIndexInformer is cumulative, not idempotent.

After the first re-election the reaper fires twice per KusoBuild event; after two re-elections it fires three times, etc. Each reap attempt lists and deletes helm release Secrets, which is idempotent at the kube level but doubles/triples the apiserver list calls per event. At scale (hundreds of completed builds in history) this becomes a steady hammering of the Secrets API from a single pod.

**Fix sketch:** Guard `Start()` with a `sync.Once` or `atomic.Bool` in both services so the handler registration is idempotent regardless of how many times the leader wins the lease.

---

## P1 — Will produce wrong behaviour under realistic conditions

### P1-1 · `buildcontroller`: `ActiveDeadlineSeconds` is hard-coded to 60 minutes; nixpacks on a cold cache legitimately takes 65+ min

**File:** `server-go/internal/buildcontroller/buildcontroller.go:86`, `render.go:58`

`jobActiveBudgetMins = int32(60)` means `ActiveDeadlineSeconds = 3600`. A nixpacks build on a cold Nix store (first build, new cache PVC) can easily take 70-80 minutes on a resource-constrained node. The kubelet kills the Job with reason `DeadlineExceeded`; `extractTerminatedReason` maps this to an OOMKilled/Error message (not DeadlineExceeded), so the user sees a confusing failure.

The kusobuild helm chart (prior path) had the same constant. This is not a regression, but the Go controller is a natural place to promote this to an admin-configurable setting.

**Fix sketch:** Add a `BuildDeadlineMinutes int` field to `BuildSettingsView`/the Settings UI and read it in `renderJob`. Default 120 min for nixpacks, 60 min for dockerfile.

---

### P1-2 · `migration.importApp`: `SetEnv` is called on a service that already exists (409 on `AddService`) without checking if the re-run would clobber newer env vars

**File:** `server-go/internal/migration/migration.go:228-261`

When `AddService` returns `ErrConflict` (the service was created on a prior import run), `importApp` falls through and calls `c.ListApplicationEnvs` + `s.Projects.SetEnv`. `SetEnv` performs a **full replacement** of the service's env var list (it calls `UpdateKusoService` with the new slice). This means a re-run of the Coolify import wizard wipes any env vars the operator may have set manually between runs and replaces them with whatever Coolify currently reports.

This is the exact "silent overwrite on re-run" footgun that `SetEnvWithOpts` was designed to prevent for the delta path. The migration path should either skip env sync when the service already exists, or use a merge strategy.

**Fix sketch:** When `AddService` 409s, skip `SetEnv` (idempotent first-run only) or, if update-on-re-run is desired, switch to the delta path with an explicit "replace env vars" user prompt.

---

### P1-3 · `migration.importOneProject`: addons are created **after** services; services that reference addon env vars via `${{ pg.DATABASE_URL }}` get unresolved placeholders

**File:** `server-go/internal/migration/migration.go:161-173`

The loop at line 161 first creates all apps (services) for a project, then at line 165 creates all databases (addons). If any app's env vars contain `${{ pg.DATABASE_URL }}`-style kuso references (which a Coolify migration wizard user would type if they were doing a partial migration with manual cross-wiring), `SetEnv` is called before the addon exists. `RewriteEnvVarsWithOpts` with `AllowPending: false` (the default used in `importApp`) would reject these, but that's opaque to the operator — the `SetEnv` error is swallowed by `out.Errors` and the env vars are simply not set. Worse, if the Coolify `RealValue` field already carried a resolved connection URL (not a kuso-style ref), it gets shipped verbatim — which is correct for the Coolify-migrated value but silently prevents the user from ever using kuso's addon wiring.

**Fix sketch:** Either (a) create addons before services within a project, or (b) pass `AllowPending: true` to `SetEnv` in the migration path so ref-style values survive the import and resolve once the addon boots.

---

### P1-4 · `kube.update` retry on `UpdateKusoEnvironment` in `propagateChangedToEnvs` can lose field changes from another concurrent propagation on different env CRs in the same batch

**File:** `server-go/internal/projects/services_ops.go:1545`, `server-go/internal/kube/crds.go:213-241`

`propagateChangedToEnvs` iterates over envs in list order and calls `UpdateKusoEnvironment` for each. The `update()` helper retries on 409 by bumping resourceVersion against the **same caller-supplied mutation**. If a concurrent `propagateChangedToEnvs` call from a different field change (e.g., a domain delta arriving concurrently with a scale delta) is writing to the same env CR, the retry will overwrite the concurrent write's successful patch rather than merge them. This is because the retry only bumps `resourceVersion`; it does not re-apply the caller's delta on top of the latest live object.

This is a narrowed version of P0-2 focused on the environment-propagation-specific blast radius.

**Symptom:** After rapid successive edits to different service fields (scale then domain, or vice versa), the production env may end up with only one of the two changes, with no error surfaced to the user.

**Fix sketch:** Introduce `UpdateKusoEnvironmentWithRetry` that accepts a `mutate func(*KusoEnvironment) error` callback and call it from `propagateChangedToEnvs` instead of the simple `Update` path.

---

### P1-5 · `builds.Poller.archiveLogs`: `stream.Close()` is called but the `io.ReadCloser` is not drained before closing; kubelet connection may hang or log tail gets truncated

**File:** `server-go/internal/builds/builds.go:2272-2288`

The log-streaming loop at line 2275 reads until `rerr != nil`, then calls `stream.Close()`. This is correct for normal reads. However, when the context is canceled mid-stream (e.g. the 10s archive timeout fires), the loop exits on the context error and `stream.Close()` is called on a not-fully-drained connection. HTTP/2 streams to the kubelet that aren't drained before `Close()` may cause the kubelet to log RST_STREAM errors and can interfere with subsequent log requests from the same connection pool to the same kubelet.

More practically: when the 10s timeout is hit while streaming a slow nixpacks build's 10MB log, the archive saves a truncated snapshot with no indication that truncation occurred, so the deployments tab shows a partial log that looks complete.

**Fix sketch:** After the read loop exits, check whether it exited due to deadline/cancel (via `lctx.Err()`) and log at warn "archive truncated". The drain concern for HTTP/2 is handled by the net/http transport's Close path; `stream.Close()` is already correct — the logging is the actionable fix.

---

### P1-6 · `buildcontroller.ensureJob`: SA and Job have an OwnerReference to the KusoBuild CR, but `BlockOwnerDeletion` is left nil (defaults to true); CR deletion blocks if the Job finishes but TTL hasn't expired

**File:** `server-go/internal/buildcontroller/buildcontroller.go:217-236`

The comment at line 222 says `BlockOwnerDeletion` is left false intentionally so the CR outlives the Job. But looking at `renderJob` (render.go:44-67), `ownerRef` is constructed with `BlockOwnerDeletion` unset (nil pointer). In Kubernetes, nil `BlockOwnerDeletion` on an `OwnerReference` with `Controller: true` defaults to **true** in some versions of the garbage collector. This is version-dependent, but on k3s (which kuso targets), the default may effectively be `true`, meaning a `kubectl delete kusobuild <name>` could be blocked until the Job and SA finish their own foreground deletion.

The comment's intent is correct; the implementation does not match.

**Fix sketch:** Explicitly set `BlockOwnerDeletion: ptrFalse()` on the `ownerRef` in `ensureJob`, matching the comment's intent.

---

### P1-7 · `builds.List` does unbounded LIST against the dynamic client (bypasses cache); called on every deployments tab render

**File:** `server-go/internal/builds/builds.go:1005-1033`

`Service.List` issues `s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).List(...)` directly to the live apiserver, bypassing the informer cache. For a busy cluster with 1000 KusoBuild CRs across 20 services, every deployments-tab render from every open browser tab hits the apiserver with a full-list request. The cache in `kube/cache.go` already tracks KusoBuild CRs via `GVRBuilds` informer.

**Symptom:** On clusters with many historical builds, the apiserver's watch cache gets hammered; this was the exact class of issue the informer cache was introduced to fix for other read paths.

**Fix sketch:** Route `List` through the `list[KusoBuild]` generic helper in `crds.go` (which already checks the cache) or call `kube.ListFromCache(GVRBuilds, ns, sel)` directly. The per-service label selector ensures the response is still scoped.

---

## P2 — Correctness issue but has a narrow trigger window or low impact

### P2-1 · `buildcontroller.reconcile`: `running` map can permanently wedge a build if `ensureJob` succeeds at SA creation but fails at Job creation

**File:** `server-go/internal/buildcontroller/buildcontroller.go:179-208`

When `ensureJob` fails, the key is removed from `running` so the next event retries. But if `ensureJob` succeeds (SA created, Job created successfully, returns nil) and the **next** informer event fires before the Job is running (e.g., the poller patches annotations, triggering an Update event), `reconcile` hits the early-return at line 181 because the key is still in `running`. This is correct and intentional.

However: if the build is then cancelled via `builds.Service.Cancel` (which stamps `spec.done=true` and deletes the Job), the `DeleteFunc` handler at line 129 fires only when the **CR** is deleted, not when the Job is deleted. The CR remains, `running` still contains the key, and any subsequent update event from the annotation-stamp during cancel is silently skipped. The build controller correctly ignores done CRs (`b.Spec.Done` check at line 163) but the key stays in `running` forever for this process lifetime, consuming a small amount of memory.

**Symptom:** Low-severity memory leak for cancelled builds — approximately one map entry per cancelled build per process lifetime. Mitigated by the fact that kuso restarts during rolling updates.

**Fix sketch:** Add a `spec.done` check in the `UpdateFunc` handler: if the object is now done, delete from `running` immediately (same as the `DeleteFunc`). The `reconcile` call already guards on `b.Spec.Done` with an early return, so the worst case is an extra no-op call; adding the running-map cleanup before calling `reconcile` is cleaner.

---

### P2-2 · `drift.compareDeploymentToEnv`: pod env comparison skips `PORT` and `HOSTNAME` but not `PORT` variations like `SERVER_PORT`, and does not account for `valueFrom.fieldRef` entries the chart injects

**File:** `server-go/internal/projects/drift.go:332-341`

The pod env comparison at line 332 skips `PORT` and `HOSTNAME`. The kusoenvironment helm chart injects additional `valueFrom.fieldRef` entries (pod IP, node name) and possibly `valueFrom.resourceFieldRef` (limit/request values) that won't appear in `env.Spec.EnvVars`. These are represented in the pod as `envFrom` entries and may collide with the `specEnv` map. A service with zero user-defined env vars may still show `envMismatch=true` if any chart-injected runtime env var isn't excluded.

**Symptom:** False-positive "restart needed" badge on services with chart-injected envs.

**Fix sketch:** The `specEnv` map correctly represents what the user configured; the pod env filter should exclude any key whose value is derived from `valueFrom` on the **pod side** (where `valueFrom` is non-nil on `e.ValueFrom`). The current code already collapses pod-side `valueFrom` entries to `"<from>"` for comparison, but if the spec has fewer `valueFrom` entries than the pod (because the chart injects additional ones), the maps won't be equal.

The actual bug: the comparison should only flag mismatch if a key in `specEnv` is absent or has the wrong value in `podEnv` — not if `podEnv` has extra keys. Using a loop over `specEnv` rather than `reflect.DeepEqual` on the full maps would eliminate false positives from chart-injected extras.

---

### P2-3 · `builds.List` does not paginate; on large clusters the kube apiserver returns a 410 Gone if the resource version is too old

**File:** `server-go/internal/builds/builds.go:1010`

`builds.List` calls `List(ctx, metav1.ListOptions{LabelSelector: ...})` with no `Limit` field. The Kubernetes API paginates at 500 items by default but a response without `continue` in the metadata means the list is complete. However, if the label selector is broad (project only, no service filter) and a project has 500+ builds, the list continues to work correctly BUT if the apiserver's resource-version window expires (apiserver restarts, etcd compaction), a request without `ResourceVersion: "0"` can receive `410 Gone`. The `Dynamic.Resource().List()` call in this case returns an error and `builds.List` propagates it as a 500 to the deployments tab, wiping the build history view until a page refresh.

**Fix sketch:** Set `ResourceVersion: "0"` in the ListOptions to allow the apiserver to serve from cache, which is immune to the 410 scenario and consistent with the informer pattern.

---

### P2-4 · `migration`: `EffectiveValue()` passes through unresolved Coolify-style `{{ VAR }}` interpolation refs if `RealValue` is empty

**File:** `server-go/internal/migration/migration.go:252-253`, `coolify/types.go:119-124`

`e.EffectiveValue()` returns `e.RealValue` if non-empty, otherwise `e.Value`. Coolify stores template-style references in the `value` field (e.g. `{{ POSTGRES_PASSWORD }}`). When the Coolify API token doesn't have write scope, `real_value` comes back empty and `value` contains the raw template string. These template strings are then passed verbatim to `SetEnv`, which writes them to the KusoService spec. The pod environment will literally contain `{{ POSTGRES_PASSWORD }}` instead of the resolved password.

**Symptom:** After a Coolify import with a read-only token, some env vars in kuso contain unresolved `{{ ... }}` placeholders. The app crashloops with connection errors.

**Fix sketch:** After calling `EffectiveValue()`, check for the `{{ ... }}` pattern. If found and `real_value` is empty, skip the variable (stamp as `out.Skipped` with reason "unresolved Coolify template — re-import with write-scope token") rather than writing the raw template.

---

### P2-5 · `buildreaper.maybeReap`: `reaped` map is never cleared; on long-running kuso-server pods the map grows one entry per ever-completed build

**File:** `server-go/internal/buildreaper/buildreaper.go:56-66`

The `reaped` map accumulates one entry per completed KusoBuild CR for the lifetime of the process. On a cluster that runs 20 builds/day for a year, this map has ~7300 entries. Each entry is a small string ("ns/name" key, empty struct value), so the absolute memory cost is low (~250 KB). However, the map also prevents the reaper from re-running on a build that was previously reaped but whose helm Secret was somehow re-created (e.g., by an operator that wasn't fully stopped before the rollover). This is an edge case but can occur during the v0.9→v0.10 migration window explicitly mentioned in the buildreaper package doc.

**Fix sketch:** Add a size cap: when `len(reaped) > 10000`, clear the map. Since the reaper is idempotent at the kube level, re-reaped builds just cause extra NotFound calls.

---

### P2-6 · `propagateChangedToEnvs`: `UpdateKusoEnvironment` (via `update`) re-encodes the env CR from an unstructured that may have stale status from the list response; if the operator writes status between the list and the update, the update 409s repeatedly

**File:** `server-go/internal/projects/services_ops.go:1507-1548`

`propagateChangedToEnvs` decodes each env from the list response (which includes `resourceVersion` at list time), mutates the spec, and calls `UpdateKusoEnvironment`. The `update()` helper retries by bumping `resourceVersion`. However, the update loop iterates over *all* envs sequentially; for a service with 5 envs (production + 4 preview branches), the 5th update is racing against at least 2-3s of helm-operator activity since the initial list. On a busy cluster this reliably produces at least one conflict per propagation call, adding ~250ms of latency for each retried env.

This is a latency issue, not a correctness issue, because the retry logic is correct. But the compound effect — list once, then update N times with increasing conflict probability — produces visible lag in the "Saving…" spinner on multi-env services.

**Fix sketch:** Use `updateWithRetry`-style per-env updates (fetch-then-mutate-then-write) so each individual update is resilient to concurrent writes without the batch degrading. The placement pre-fetch (line 1500) should remain to avoid N+1 project reads.

---

### P2-7 · `buildcontroller.renderStaticPlanContainer`: `BUILD_CMD` is passed via env and run with `sh -c "$BUILD_CMD"`, which is one shell invocation deep but still passes the user's arbitrary string through a shell

**File:** `server-go/internal/buildcontroller/render.go:586-609`

The comment at line 582 says "We pass buildCmd via env to avoid shell-injection." This is partially correct: `BUILD_CMD` is set as an env var, not interpolated inline into the script string. But `sh -c "$BUILD_CMD"` still evaluates `$BUILD_CMD` as a shell command. This is not injection-in-the-Go-string sense, but a privileged admin who can set `spec.static.buildCmd` via the API can run arbitrary commands in the build container (e.g., `curl ... | sh`). 

Since the service spec is admin-only (kuso is single-tenant, the user IS the admin), this is within the threat model. Document it explicitly — there should be no expectation that `buildCmd` is sandboxed.

**No fix required.** The existing comment says "The user is supposed to set it to a build command; running it via `sh -c "$BUILD_CMD"` evaluates one shell context regardless of the value's content." This is accurate but the security implication should be in `EDIT_SAFETY.md` for completeness.

---

### P2-8 · `SetEnvWithOpts` swallows propagation errors silently; callers using the return value to confirm success will believe the pods are updated when propagation may have failed

**File:** `server-go/internal/projects/services_ops.go:944-948`

```go
if err := s.propagateChangedToEnvs(...); err != nil {
    // Logged via the caller's wrapped error; the service spec is
    // the durable record that next reconcile/edit will retry from.
    return nil
}
```

The comment is aspirational: the propagation error is NOT logged anywhere (neither here nor in the caller chain). The comment says "logged via the caller's wrapped error" but the return is `nil` — the caller receives `nil` and has no indication that propagation failed. The user's UI receives HTTP 200 but the env CRs weren't updated. The pod is not restarted. The next save will re-propagate, but if the user doesn't save again (e.g., they just set env vars on a stable service), the pods remain stale indefinitely.

**Fix sketch:** Either log a warning here (1 line) or return the propagation error (the service spec IS saved; callers can surface this as a partial-success 207). At minimum: `s.logger.Warn("propagation failed after SetEnv", "err", err, "project", project, "service", service)`.

---

## Review Summary

| Severity | Count | Status  |
|----------|-------|---------|
| P0       | 3     | block   |
| P1       | 7     | warn    |
| P2       | 8     | info    |

**Verdict: BLOCK — 3 P0 issues must be resolved before the next production roll.**

### P0 summary

1. **P0-1** (`buildcontroller.Start`): `running` map survives leader re-election; builds from the prior leader tenure are permanently stuck. Re-elect three times without a pod restart and every build that existed during any previous tenure is permanently deduped out.

2. **P0-2** (`kube.update`): Conflict-retry on `UpdateKusoEnvironment` re-applies the caller's stale snapshot instead of fetching-and-mutating the live object. Concurrent `propagateChangedToEnvs` calls from two different field edits can silently clobber each other, producing an env CR that reflects only one of the two saves.

3. **P0-3** (`buildcontroller.Start` + `buildreaper.Start`): Neither `Start()` method guards against being called twice (which happens on every leader re-election). After N re-elections there are N+1 informer event handlers registered, producing N+1 `ensureJob` and N+1 reap attempts per KusoBuild event, hammering the apiserver.
