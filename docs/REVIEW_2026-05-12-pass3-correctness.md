# kuso Correctness & Bug Review — Pass 3 (2026-05-12)

Reviewer: automated pass via Claude Sonnet 4.6  
Scope: concurrency, context plumbing, resource leaks, k8s API misuse, DB races, Coolify commit endpoint, tests vs reality.  
Already-closed: per-service mutex on env path, N+1 KusoProject GET in propagators, decodeJSON body cap, defer cancel in backup cleanup, ctx plumbing into coolify client.

---

## Findings

### P0 — Critical / data-loss or crash

---

**F-01 · [P0] `InsertNotificationEvent` insert + prune are not in a transaction — concurrent writers can delete the just-inserted row**

File: `server-go/internal/db/notification_events.go:69–94`

The function does:
1. `INSERT INTO "NotificationEvent" …`
2. `DELETE FROM "NotificationEvent" WHERE id < (SELECT id … ORDER BY id DESC LIMIT 1 OFFSET 200)`

These are two separate `ExecContext` calls with no transaction. Under concurrent `Emit` (which calls this synchronously from multiple goroutines — once per event emitted during a build storm), two goroutines can interleave as: G1 inserts row 201 → G2 inserts row 202 → G2's prune fires and sees 202 as the cutoff → G2 deletes everything ≤ row 201, including the row G1 just wrote. The user sees a bell-feed entry vanish seconds after it appeared.

Fix: wrap both statements in a `BEGIN … COMMIT` transaction, or use a single CTE that does insert + prune atomically:
```sql
WITH ins AS (
  INSERT INTO "NotificationEvent" (…) VALUES (…) RETURNING id
)
DELETE FROM "NotificationEvent"
WHERE id < (SELECT id FROM "NotificationEvent" ORDER BY id DESC LIMIT 1 OFFSET 200)
```

---

**F-02 · [P0] `writeStatus` (updater) has a create/update race — concurrent update triggers can lose the watchdog's rollback status write**

File: `server-go/internal/updater/updater.go:848–863`

```go
_, err := s.Kube.Clientset.CoreV1().ConfigMaps(s.Namespace).Create(ctx, cm, metav1.CreateOptions{})
if apierrors.IsAlreadyExists(err) {
    _, err = s.Kube.Clientset.CoreV1().ConfigMaps(s.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
}
```

`Update` here sends the ConfigMap with an empty `resourceVersion` (the object was constructed in-memory, never fetched). Under kube's optimistic concurrency rules an Update without a resourceVersion is treated as "unconditional replace" in the legacy API. That is mostly fine — except that `watchOperatorHealth` calls `writeStatus` from a goroutine that raced the `StartUpdate` path's own `writeStatus("pending")` call. The goroutine's Update will 409 if the apiserver enforces strict RV on the second call (some versions do), and `writeStatus` returns the error which is `_ =`'d in the watchdog, silently losing the rollback status. The user's UI is stuck at "pending" while the operator is actually rolled back.

Fix: use SSA (Apply with field manager) or GET→patch inside `writeStatus` so the RV is always fresh.

---

**F-03 · [P0] `RemoveDomain` and `SetEnvVar`/`UnsetEnvVar` delta-ops use a plain `UpdateKusoService` (no retry on conflict), while `AddDomain` correctly uses `UpdateKusoServiceWithRetry`**

Files:
- `server-go/internal/projects/services_deltas.go:136` — `RemoveDomain` calls `s.persistDomains` which calls `UpdateKusoService`
- `server-go/internal/projects/services_deltas.go:203,235` — `SetEnvVar`/`UnsetEnvVar` call `s.persistEnvVars` which also calls `UpdateKusoService`

`AddDomain` uses `UpdateKusoServiceWithRetry` (read-modify-write with conflict retry). The other three delta-ops do a `fetchServiceForDelta` (a plain Get) followed by `UpdateKusoService` (a plain Update). If the helm-operator patches `.status` between the Get and the Update, the Update 409s and the caller sees an internal server error — the domain remove or env set is lost.

The in-process `svcMutexesMu` only guards same-replica races. Multi-replica or operator-status races still hit this path.

Fix: route `persistDomains` and `persistEnvVars` through `UpdateKusoServiceWithRetry`, re-running the delta logic inside the retry callback.

---

### P1 — High / user-visible misbehaviour under realistic load

---

**F-04 · [P1] `deleteCloneTokenSecret` spawns a goroutine using `context.Background()` — goroutine outlives server shutdown and races against a closing kube client**

File: `server-go/internal/builds/builds.go:2659–2668`

```go
go func() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    …
}()
```

The parent poller already holds a proper lifecycle context (passed to `Run`). The goroutine uses `context.Background()` instead, so it survives a graceful shutdown and issues a kube API call against a client whose underlying transport is being torn down — either panicking or returning a confusing error. On a cluster with 50 concurrent builds at the time of a rolling deploy, this spawns 50 leaked goroutines.

Fix: thread the Poller's lifecycle context into `deleteCloneTokenSecret` and parent the timeout off it (same pattern as `logship.Shipper.runCtx`).

---

**F-05 · [P1] `Emit` takes `d.mu` to read `baseCtx`, then immediately calls `d.db.InsertNotificationEvent` synchronously — if the DB call is slow, callers block holding nothing but their own goroutine, but the mutex acquisition pattern sets a precedent that can deadlock if `Run` ever needs the same lock during shutdown**

File: `server-go/internal/notify/notify.go:220–251`

```go
func() {
    d.mu.Lock()
    parent := d.baseCtx
    d.mu.Unlock()
    // …
    persistCtx, cancel := context.WithTimeout(parent, 2*time.Second)
    defer cancel()
    if err := d.db.InsertNotificationEvent(persistCtx, …); err != nil { … }
}()
```

The actual DB call runs outside the lock (correct), but every single `Emit` call on the hot path — including from within request handlers — blocks the calling goroutine for up to 2 seconds on a busy SQLite. `Emit` is documented as non-blocking for the *channel* but is blocking for the *persist*. A build storm with 50 concurrent webhook delivers (each calling `Emit` in their goroutines) saturates the SQLite write connection; the 2s timeouts pile up, each holding an OS thread for the full duration.

The real issue: Emit's contract in the comment says "Non-blocking: domain code never waits on a slow webhook" but the persist path introduced a synchronous wait. If the DB is contended (e.g., logship's 1-second bulk insert holds the connection), an `Emit` from a webhook handler can wait 2 full seconds before returning the HTTP response.

Fix: move the persist call into a short-lived goroutine derived from `baseCtx`, similar to how the webhook send already works. Accept that the bell feed may lag the event by a few hundred ms; it already lags by the channel round-trip for webhooks.

---

**F-06 · [P1] `SweepOrphanHelmReleases` lists every KusoBuild/Service/Env/Addon CR without pagination — on a cluster with hundreds of builds this can exhaust the apiserver's list budget in one shot**

File: `server-go/internal/builds/cleanup.go:197–209`

```go
for _, g := range gvrs {
    l, err := kc.Dynamic.Resource(g.gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
```

No `Limit` on any of these six lists. k3s's SQLite-backed apiserver has a hard limit of 10,000 objects per list response by default, but more practically a cluster that has run for months can accumulate thousands of KusoBuild CRs (one per commit × N services), and a single List of all of them can consume 50–100 MB of RAM and take 2–5 seconds, spiking apiserver CPU.

Same issue in `kube/finalizer.go:41` (`StripHelmFinalizers`) and in `cleanup.go` itself for the `build-state=done` list (line 46) and `CapBuildsPerService` (line 105).

Fix: add `metav1.ListOptions{Limit: 500}` with a `Continue` pagination loop, or (cheaper) use the informer cache (already wired) for all but the finalizer strip.

---

**F-07 · [P1] `propagateBaseDomain` in `projects_ops.go` logs via `fmt.Printf` instead of the structured logger, bypassing log routing and rate-limiting**

File: `server-go/internal/projects/projects_ops.go:227`

```go
fmt.Printf("warn: propagate baseDomain project=%s: %v\n", name, perr)
```

This writes to stdout outside the `slog` pipeline. In containerised deployments where log collectors scrape structured JSON from stdout, this line appears as unstructured text and is often dropped or misclassified. The `Service` struct in the same package has no `Logger` field — this is the root cause. During a baseDomain change that partially fails (e.g., apiserver throttle on env updates), the warning is effectively silent in production.

Fix: add a `Logger *slog.Logger` field to `projects.Service`; replace `fmt.Printf` with `s.Logger.Warn(…)`.

---

**F-08 · [P1] Coolify commit: `applyCommit` iterates items grouped by `coolifyOrder`, but env-var fetching calls `c.ListApplicationEnvs` for every app — if the Coolify instance becomes unreachable mid-commit, the function continues creating kuso resources with incomplete env vars, leaving services that will OOM on missing DATABASE_URL etc.**

File: `server-go/internal/http/handlers/import_coolify.go:354–373`

```go
envs, err := c.ListApplicationEnvs(ctx, it.App.UUID)
if err != nil {
    out.Errors = append(out.Errors, CommitDetail{…})
    continue   // ← continues, service was already created above
}
```

The service is created (line 345) before the env-var fetch is attempted. If `ListApplicationEnvs` fails (network blip, Coolify rate-limit, token expiry mid-import), the service exists in kuso with zero env vars. On the next `kuso build trigger` the pod spawns without `DATABASE_URL` etc. and crashes. There's no rollback of the just-created service CR.

This is a partial-failure idempotency gap, not a total correctness failure — a second commit will hit `ErrConflict` on the service and skip it, but also skip its env vars because the `continue` on error makes env-var apply contingent on success of a fresh fetch. The user would have to manually delete the service and re-import.

Fix: pre-fetch all env vars before writing any CRs, or record a separate "env fetch failed" status and surface it prominently so the user knows to set env vars manually before the first build.

---

**F-09 · [P1] `Coolify commit: UUIDs from the client body are used verbatim as map keys to filter the server-re-fetched inventory — a crafted UUID list could induce unbounded work but not data corruption, and there is no per-UUID format validation**

File: `server-go/internal/http/handlers/import_coolify.go:239–245`

```go
picked := make(map[string]struct{}, len(req.UUIDs))
for _, u := range req.UUIDs {
    picked[u] = struct{}{}
}
```

The 500-item cap (line 213) exists and is correct. However, UUIDs are not validated as valid UUID format — an attacker who can reach the admin endpoint (already requires `requireAdmin`) could send 500 arbitrary strings that match no Coolify UUIDs, causing a full Snapshot re-fetch against the target Coolify instance on every commit attempt. The real risk is accidental rather than malicious (a UI bug could send stale/wrong UUIDs), silently resulting in a commit that imports zero resources while returning 200 OK with empty counters — misleading the user.

Fix: validate each UUID against `uuid.Parse` before the Snapshot round-trip; surface a 400 if any entry is not UUID-shaped.

---

**F-10 · [P1] `nodewatch.Watcher.tick` stamps `NotReadySinceAnnotation` with `time.Now()` at dispatch time, not the time the node was first observed NotReady — after a leader handover the timer resets by up to `Tick` (1 min) for every newly-observed NotReady node**

File: `server-go/internal/nodewatch/nodewatch.go:231–239`

```go
case "stamp-notready-marker":
    ts := time.Now().UTC().Format(time.RFC3339)
    patch := []byte(fmt.Sprintf(
        `{"metadata":{"annotations":{%q:%q}}}`, NotReadySinceAnnotation, ts))
```

The `notReadySince` map is populated at the `!ok` branch with `now` (the start of the current tick), which is already `w.notReadySince[n.Name] = now`. The stamp action fires on the next tick but uses a *new* `time.Now()` — potentially one `Tick` (up to 1 minute) later than when the node was first observed NotReady in-memory. On a leader handover the freshly-started watcher then reads this annotation and uses the wrong origin, potentially delaying the cordon alert by up to 1 minute.

Fix: stamp the annotation with the same timestamp stored in `w.notReadySince[n.Name]` (pass it through the `pendingAction` struct), not a fresh `time.Now()`.

---

**F-11 · [P1] `logship.Shipper.reconcileNamespacePods` deletes stream entries for vanished pods under `s.mu` after registering the cancel func under the same lock — the `streamPod` defer also takes `s.mu` to `delete(s.streams, uid)`, creating a potential lock ordering inversion when pod eviction races the reconciler**

File: `server-go/internal/logship/shipper.go:207–223`

```go
streamCtx, cancel := context.WithCancel(ctx)
s.mu.Lock()
s.streams[uid] = cancel      // ← under lock
s.mu.Unlock()
go s.streamPod(streamCtx, ns, *p)
// …
s.mu.Lock()
for uid, cancel := range s.streams {   // ← under same lock
    if _, ok := seen[uid]; !ok {
        cancel()
        delete(s.streams, uid)
    }
}
s.mu.Unlock()
```

`streamPod`'s defer also takes `s.mu`:
```go
defer func() {
    s.mu.Lock()
    delete(s.streams, ns+"/"+string(pod.UID))
    s.mu.Unlock()
}()
```

If `reconcileNamespacePods` calls `cancel()` and immediately `delete(s.streams, uid)` while `streamPod` is in the middle of its defer (waiting for the lock), `streamPod`'s defer will find the key already absent — that is benign. The real risk is if the goroutine scheduler yields between `cancel()` and `delete(s.streams, uid)` inside the reconciler's lock — but since both `cancel()` and `delete` happen under the *held* lock, and `streamPod`'s defer tries to acquire the same lock, there is no ordering inversion. However, `cancel()` being called under the lock could wake a goroutine that immediately tries to re-acquire the same lock (the goroutine scheduler may or may not preempt here). In practice this is low-risk but should be cleaned up: call `cancel()` after releasing `s.mu`, with the UIDs collected first.

Fix: collect `{uid, cancel}` pairs into a local slice while holding `s.mu`, then release the lock before iterating to call each `cancel()`.

---

### P2 — Medium / incorrect behaviour in edge cases

---

**F-12 · [P2] `InsertNotificationEvent` uses `?` placeholders (SQLite-style) while `ListNotificationEventsForProjects` uses `$N` placeholders (PostgreSQL-style) — both route through `d.DB` which is the same `*db.DB`; one of these must be wrong**

File: `server-go/internal/db/notification_events.go:71` vs `185`

```go
// Line 71 — SQLite-style:
VALUES (?,?,?,?,?,?,?,?)

// Line 185 — PostgreSQL-style:
placeholders[i] = fmt.Sprintf("$%d", i+1)
```

Both go through the same `database/sql.DB`. If the underlying driver is `lib/pq` (Postgres), `?` placeholders silently fail with a parse error; if it's `mattn/go-sqlite3`, `$N` placeholders are accepted but numbered as `$1` = first in-order occurrence, which works coincidentally. The comment in `ListNotificationEventsForProjects` even says "lib-pq driver rewriter doesn't expand `IN ?` with a slice for us" — confirming Postgres is the intended target. This means `InsertNotificationEvent` is using the wrong placeholder syntax and either silently fails or uses the wrong binding, depending on which driver is wired.

Fix: standardise on `$N` placeholders throughout `notification_events.go` (or factor out a helper that generates them).

---

**F-13 · [P2] `update[T]` in `kube/crds.go` retries on conflict by fetching the latest RV and then re-issuing the ORIGINAL in-memory object's spec — if the spec itself was mutated server-side between the caller's read and the retry, the retry silently clobbers the intermediate change**

File: `server-go/internal/kube/crds.go:219–232`

```go
rerr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
    var uerr error
    updated, uerr = c.Dynamic.Resource(gvr).Namespace(namespace).Update(ctx, u, metav1.UpdateOptions{})
    if uerr == nil {
        return nil
    }
    latest, gerr := c.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, u.GetName(), metav1.GetOptions{})
    …
    u.SetResourceVersion(latest.GetResourceVersion())
    return uerr   // ← retries with the original u, not a fresh merge
})
```

`u` retains the caller's in-memory spec. On the retry it will overwrite whatever a concurrent writer (e.g., helm-operator patching `.spec.image.tag` on promotion) applied between the original call and the retry. `updateWithRetry` (used by delta-ops) re-runs the `mutate` callback against the fresh object and is correct. But the plain `update[T]` path (used by `UpdateKusoService`, `UpdateKusoProject`, `UpdateKusoEnvironment`) has this silent clobber risk.

For spec-only edits against status-only operator patches the risk is low (they edit different fields). But for `propagateBaseDomain` which writes to env CRs that the operator also writes `spec.image.tag` onto, a concurrent promotion + base-domain propagation can race this path.

Fix: align `update[T]` to re-run a merge step on retry (or always use `updateWithRetry` for CRs the operator writes to).

---

**F-14 · [P2] `builds.Poller.deleteCloneTokenSecret` goroutine leaks for every build if `p.Svc.Kube.Clientset` is non-nil but the secret was already deleted (the goroutine fires, gets `NotFound`, returns cleanly — this is actually safe). However, goroutines are spawned with no back-pressure: a 500-item Coolify commit that creates 500 builds triggers 500 token-secret deletions simultaneously**

File: `server-go/internal/builds/builds.go:2655–2668`

Each `deleteCloneTokenSecret` call spawns one goroutine. A Coolify import that succeeds with all 500 services then triggers 500 builds (if auto-build is wired), each eventually calling `deleteCloneTokenSecret` — 500 simultaneous goroutines each making a kube Secrets DELETE call. On a small cluster this can saturate the apiserver rate limiter (default 200 req/s for writes).

Fix: use a worker pool (e.g., a `chan struct{}` semaphore of size 16) inside `deleteCloneTokenSecret` to bound the concurrency, or batch the deletes via a label-based `DeleteCollection` call.

---

**F-15 · [P2] `SweepOrphanHelmReleases` and `CapBuildsPerService` operate only on `namespace` (home ns) — builds in per-project namespaces (`KusoProject.spec.namespace != ""`) are never swept**

File: `server-go/internal/builds/cleanup.go:44,101`

Both functions take a single `namespace` argument. The callers pass the home namespace. For projects that have `spec.namespace` set to a dedicated namespace, their KusoBuild CRs and helm release secrets accumulate without bound. Only `SweepFinishedBuilds` is called — and only if the cron logic calls it for each project namespace (not verified, but the sweep functions themselves don't walk the namespace set).

Fix: expose the same multi-namespace walk that `ScanNamespaces` already implements; pass all project namespaces to the sweep functions.

---

**F-16 · [P2] `logship.Shipper.append` (out-of-band flush path) snapshots `s.runCtx` without a lock — if `Run` sets `s.runCtx` concurrently on first call, there is a data race on the read**

File: `server-go/internal/logship/shipper.go:382–384`

```go
func (s *Shipper) append(l db.LogLine) {
    …
    go s.flush(s.runCtx)   // ← unsynchronised read of s.runCtx
```

`Run` assigns `s.runCtx = ctx` (line 112) without holding any lock. `append` reads `s.runCtx` without a lock. The go data-race detector will flag this as a concurrent read/write if `append` is called (from a `streamPod` goroutine that started before `Run()`) at the same moment `Run` is setting the field.

Fix: protect `runCtx` with a `sync.Mutex` or `sync.RWMutex` (or use `atomic.Pointer[context.Context]`), same as `updater.Service.runCtx` already does.

---

**F-17 · [P2] Coolify commit imports app git URLs as `https://github.com/<owner>/<repo>` by stripping `.git` — but `it.App.GitRepository` may already be an HTTPS URL (Coolify stores either bare `owner/repo` or a full URL), causing double-prefix injection**

File: `server-go/internal/http/handlers/import_coolify.go:341-343`

```go
Repo: &projects.CreateServiceRepo{
    URL: "https://github.com/" + strings.TrimSuffix(it.App.GitRepository, ".git"),
```

If `GitRepository` is already `https://github.com/org/repo`, the result is `https://github.com/https://github.com/org/repo` — an invalid URL that will cause every build to fail with a clone error. Coolify's own API docs and source show that `git_repository` can be either `owner/repo` or a full URL depending on how the app was created. The same issue exists in `defaultRepoURL` on line 300.

Fix: parse the value with `url.Parse` first; only prepend `https://github.com/` when the value has no scheme.

---

**F-18 · [P2] `notify.Dispatcher.dispatch` iterates webhook sinks sequentially and synchronously, blocking the `Run` goroutine's channel drain for the duration of all HTTP POSTs (up to 8s per sink × N sinks)**

File: `server-go/internal/notify/notify.go:311–334`

```go
for _, n := range notifs {
    …
    d.sendDiscord(ctx, url, e, mentionFor(e, n.Config))   // each can block 8s
    // or
    d.sendWebhook(ctx, url, secret, e)
```

`Run`'s `for { select { case e := <-d.ch: d.dispatch(ctx, e) } }` is a single goroutine. If an operator has configured 3 Discord channels and a webhook, a single event blocks the drain goroutine for up to 32 seconds. During that window, 256 further events can fill the buffered channel; on the 257th, events are dropped (with a log warn, and the bell feed still has them). In a build storm this is reached easily.

Fix: launch each sink's POST in its own goroutine inside `dispatch` (bounded pool to avoid unbounded fan-out), or use a per-sink worker goroutine started at Run time.

---

**F-19 · [P2] `propagateBaseDomain` in `projects_ops.go` iterates env CRs and calls `UpdateKusoEnvironment` in a loop without any conflict retry or rollback — a single 409 (from an operator status patch racing the loop) returns an error that the caller surfaces to the UI, even though some envs were already patched**

File: `server-go/internal/projects/projects_ops.go:264–268`

```go
if _, uerr := s.Kube.UpdateKusoEnvironment(ctx, ns, &env); uerr != nil {
    return fmt.Errorf("update env %s: %w", env.Name, uerr)
}
```

A partial failure here means: the project's base domain was updated (the KusoProject CR is already patched), some envs now use the new host, and the remaining envs still have the old host — split-brain until the next manual save. The error propagates to the HTTP handler which returns 500 to the user, who sees "update failed" but doesn't know which envs were actually updated.

Fix: use `updateWithRetry` for each env update; if the whole loop fails, log which envs were patched and which weren't for the drift indicator to surface.

---

**F-20 · [P2] `notify.Dispatcher.SetLeaderHook` acquires `d.mu` to write `isLeader`, and `dispatch` acquires `d.mu` to read it — this is correct, but `Emit` also acquires `d.mu` (to read `closed` and `baseCtx`) and calls `db.InsertNotificationEvent` synchronously. On a slow DB, `Emit` holds `d.mu` for 0ms (released before the DB call), but if any future refactor inlines the persist inside the lock, the system deadlocks. The current non-deadlock is only safe because of the explicit inner closure that releases the lock before calling into the DB.**

File: `server-go/internal/notify/notify.go:212–251` (documentation finding, not a current bug)

Documented as a latent risk rather than a current bug. The code is safe today precisely because of the closure pattern. A future diff that simplifies the closure away would introduce a deadlock.

Recommendation: add a comment warning that the DB call MUST remain outside the lock. Consider replacing the overloaded `d.mu` (which guards `closed`, `baseCtx`, AND `isLeader`) with separate fields/locks to make the invariant explicit.

---

## Summary Table

| ID  | P  | Package                           | Symptom                                                              | One-line fix                                    |
|-----|----|-----------------------------------|----------------------------------------------------------------------|-------------------------------------------------|
| F-01| P0 | `db/notification_events.go`        | Bell-feed rows vanish under concurrent `Emit` during build storms    | Wrap INSERT+DELETE in a transaction             |
| F-02| P0 | `updater/updater.go`               | Rollback status lost → UI stuck at "pending" after bad operator roll | Use Apply (SSA) or GET-then-patch in writeStatus|
| F-03| P0 | `projects/services_deltas.go`      | RemoveDomain / SetEnvVar / UnsetEnvVar lose edit on operator conflict | Use `UpdateKusoServiceWithRetry` in persist helpers |
| F-04| P1 | `builds/builds.go` (Poller)        | Goroutine leak + kube client use-after-shutdown on server rolling    | Parent delete goroutine off Poller lifecycle ctx|
| F-05| P1 | `notify/notify.go`                 | Webhook handlers block up to 2s per `Emit` during DB contention      | Move persist into a goroutine; accept brief feed lag |
| F-06| P1 | `builds/cleanup.go`                | Unbounded LIST on large clusters → apiserver CPU spike / OOM         | Add Limit+Continue pagination or use informer cache |
| F-07| P1 | `projects/projects_ops.go`         | baseDomain propagation errors invisible in production logs           | Add `Logger` to `projects.Service`; replace `fmt.Printf` |
| F-08| P1 | `handlers/import_coolify.go`       | Service created without env vars if Coolify API fails mid-commit     | Pre-fetch all envs before writing any CRs       |
| F-09| P1 | `handlers/import_coolify.go`       | Non-UUID strings in `uuids` cause spurious full Snapshot re-fetch    | Validate each UUID format before proceeding     |
| F-10| P1 | `nodewatch/nodewatch.go`           | NotReady timer resets by up to Tick (1 min) on stamp vs first-seen mismatch | Stamp annotation with `w.notReadySince[n.Name]` timestamp |
| F-11| P1 | `logship/shipper.go`               | `cancel()` called under lock may cause goroutine scheduling delay    | Collect UIDs, release lock, then call cancel    |
| F-12| P2 | `db/notification_events.go`        | Mixed `?` vs `$N` placeholders — one is wrong for the live driver    | Standardise on `$N` throughout the file         |
| F-13| P2 | `kube/crds.go`                     | `update[T]` retry clobbers concurrent spec edits                     | Re-run merge on retry like `updateWithRetry`    |
| F-14| P2 | `builds/builds.go` (Poller)        | 500-build Coolify commit spawns 500 simultaneous Secrets DELETE goroutines | Add a bounded worker pool for token-secret cleanup |
| F-15| P2 | `builds/cleanup.go`                | Builds in per-project namespaces never swept by CapBuildsPerService  | Walk all project namespaces in sweep callers    |
| F-16| P2 | `logship/shipper.go`               | Data race on `s.runCtx` between `Run` and `append`                  | Protect `runCtx` with a mutex/atomic            |
| F-17| P2 | `handlers/import_coolify.go`       | Double `https://github.com/` prefix when Coolify stores full URL     | Parse with `url.Parse` before prepending scheme |
| F-18| P2 | `notify/notify.go`                 | Sequential synchronous webhook POSTs block `Run` drain loop          | Fan out each sink POST in a bounded goroutine pool |
| F-19| P2 | `projects/projects_ops.go`         | Partial baseDomain propagation → split-brain host state on 409       | Retry per-env update; log which envs succeeded  |
| F-20| P2 | `notify/notify.go`                 | Overloaded `d.mu` pattern is one refactor away from deadlock         | Add comment; split `closed`/`baseCtx`/`isLeader` locks |

---

*Generated by automated code review pass — 2026-05-12*
