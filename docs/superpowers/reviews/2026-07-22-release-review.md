GO/NO-GO synthesis. No verification needed — the JSON is pre-verified. I'll write the report directly.

Reading the verdicts: zero blockers survived verification (all were downgraded). Two high, four medium, two low.

---

# kuso RELEASE — GO / NO-GO

## VERDICT: **GO WITH CAUTION**

Zero blockers survived verification. All eight concerns are real, but none breaks a live instance on auto-update or corrupts data. Two high-severity concerns both point at the **same root cause** (dump flags vs. restore flags) and must be fixed before anyone relies on the new backup/snapshot recovery paths — but neither degrades a running instance the moment it rolls. Ship is safe; the recovery-path promises are not yet true.

---

## Blockers (must-fix before release)

**None.** Every finding that entered as a blocker candidate was downgraded on verification. Specifically refuted blocker mechanics worth recording:

- The snapshot-poller freeze does **not** recycle the leader pod or churn leadership — liveness uses `/healthz` (unconditional 200) not the poller, and lease renewal runs in an independent goroutine (`leader.go:170`), so a blocked poller cannot lose the lease.
- Both restore-path failures are **fail-safe**: `--single-transaction` guarantees full rollback, leaving the DB byte-for-byte intact. No corruption, no partial writes.
- Auto-update flips image tags only; it runs no restore Job and applies no helm/CRD changes, so the restore and backup regressions cannot fire on the roll itself.

---

## High

**1. Restore hardening breaks the snapshot's own rollback path** — `server-go/internal/backup/registry.go:70`
The postgres restore now runs `psql -v ON_ERROR_STOP=1 --single-transaction`, which fails-and-rolls-back onto a non-empty DB unless the dump was `--clean --if-exists`. But no artifact producer emits `--clean`: the pre-deploy snapshot (`snapshot_adapter.go:92`), the native backup CronJob, and the external CronJob all run plain `pg_dump`. The advertised "bad migration → one-click snapshot restore" flow restores the pre-migration snapshot back onto the *same still-populated* DB, hits "relation already exists" on the first `CREATE TABLE`, and aborts. **What breaks:** the headline recovery path errors instead of recovering, but only when an operator invokes it during an incident — the DB is left safely in its failed-migration state. **Fix:** add `--clean --if-exists` to the pg_dump in `snapshot_adapter.go` and both branches of `backup-cronjob.yaml`, matching the download handler.

**2. Release never pushes the backup image the new CronJobs depend on** — `Makefile:157` / `hack/release.sh`
The updated `build/backup/Dockerfile` adds `mongodb-tools`, `mysql-client`, and `redis`, but that image is only built by the manual `make backup-image` target. `make ship` → `release.sh` has zero references to it, yet this release ships operator charts that render **new** mongodb and mysql backup CronJobs pulling `ghcr.io/sislelabs/kuso-backup:latest` with `imagePullPolicy: IfNotPresent`. Since `operator/` changed, the operator image *is* rebuilt and pinned, so on auto-update the operator reconciles existing addons with the new charts. **What breaks:** any existing mongodb addon with a backup schedule (no kind gate — `addons.go:595`) starts rendering a CronJob whose `mongodump` is absent from the stale `:latest` → `command not found` → silent backup failure. Same for redis and mysql. `IfNotPresent` means a later manual push won't be re-pulled on nodes with the cached image. **Fix:** add a version-pinned backup-image build+push to `release.sh` and reference the pinned tag from the chart's `.Values.backup.image` (drop `:latest`), OR make `make backup-image` a mandatory pre-ship step with a bumped `BACKUP_VERSION` tag.

> Note: findings #1 and #4 are the **same defect** described from two angles (dump flags don't match the hardened restore). One fix — adding `--clean --if-exists` to every S3-bound pg_dump — closes both.

---

## Medium

- **Snapshot blocks the serial build poller for up to 5 min** (`snapshot_adapter.go`) — an opt-in `snapshotBeforeDeploy` pg_dump >30s freezes every other in-flight build cluster-wide and flips the leader's `/readyz` to unready (LB drains it, but it is not killed and leadership does not churn). Off by default, self-heals at the 5-min `waitForJob` timeout. Fix: run the snapshot off the poller's critical path, or stamp `PollerHeartbeat()` around the blocking wait.
- **In-place restore aborts on populated DB** (`registry.go`, dup of High #1) — same dump/restore-flag mismatch; classified medium in isolation because restore-into-fresh-sibling still works and the prior behavior (silent no-op `psql`) was arguably worse. Rolled into the High #1 fix.
- **Snapshot Job lacks `ActiveDeadlineSeconds` + `TTLSecondsAfterFinished`** (`snapshot_adapter.go:115`) — hung pods leak DB connections/locks; completed Jobs accumulate unbounded. Outlier vs. every other Job path in the repo (release Job sets both at `releaserun.go:298,302`). Opt-in, so dormant on upgrade. Fix: mirror the release Job; best-effort delete on timeout.
- **`set -euo pipefail` lets retention-prune fail a good backup** (`backup-cronjob.yaml:119`) — a nonzero `aws s3 ls` in the bare prune pipeline now aborts the whole CronJob *after* the artifact + manifest already uploaded successfully → false-positive Job failure + spurious alert. Reproduced in Alpine 3.21 busybox ash. Steady-state prefix is non-empty (returns 0), so realistic trigger is transient/backend list errors, not every run. Fix: guard the prune pipeline (`... || true`) or scope pipefail to the dump.

---

## Low

- **Leader-gate fix (P0-3) has a vacuous test** (`buildcontroller/leader_gate_test.go:39`) — `TestMaybeReconcileGate` asserts nothing; passes even with the entire gate deleted (empirically confirmed). Production fix is present and correct; only the regression guard is missing. Fix: inject a sink, feed a valid `*unstructured.Unstructured`, assert fire count 0 when `LeaderActive=false`, N when true/nil.
- **notify outbox enqueue-on-any-replica fix has no test** (`notify/notify.go:411`) — real data-loss correctness fix (events from non-leader replicas were lost), but `outbox_test.go` was untouched and covers only backoff arithmetic. A future refactor re-adding a leader check to `dispatch()` would go undetected by CI. Fix: add a Dispatcher test asserting rows persist regardless of leadership and drain only runs on the leader.

---

## Release-safety summary (gating verdicts)

- **CRD additivity: PASS.** The only new spec surface is the per-service `spec.snapshotBeforeDeploy` opt-in (`types.go:335`, default false/nil). No existing field changed type or semantics; dormant on upgrade. No additivity violation found.

- **Chart-immutability: PASS with an operational caveat.** No immutable-field mutation (no repeat of the VCT-annotation class of regression) was reported. The caveat is not immutability but a **missing image dependency**: the new mongodb/mysql backup-CronJob branches reference an image the release never pushes (High #2). The charts themselves apply cleanly; their runtime dependency is unmet.

- **Data-path-fix correctness: FAIL (recovery paths), fail-safe.** The restore-hardening change (`ON_ERROR_STOP=1 --single-transaction`) is correct *in isolation* and strictly safer than the prior silent-wrong `psql`. But composed with the plain-`pg_dump` producers, it makes both the in-place restore and the pre-deploy-snapshot rollback **fail loudly on a populated target** — the exact recovery scenarios they were built for. The failure is non-destructive (guaranteed rollback, DB intact), so it does not gate the binary rolling out, but it **does** mean the release's advertised recovery guarantees are not yet functional. Fix High #1 before these features are documented as working.

**Bottom line:** Ship is safe — nothing bricks on auto-update, nothing corrupts data. But the two high-severity items are load-bearing for features this release introduces (snapshot rollback, multi-engine backups). Fix them (one dump-flag change + one release.sh image push) before those features are announced or relied upon.