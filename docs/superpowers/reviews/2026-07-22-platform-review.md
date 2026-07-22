# kuso — Deep Platform Review

**Date:** 2026-07-22
**Branch reviewed:** `feat/openship-inspired-improvements` (whole platform, not just the diff)
**Method:** Multi-agent static review — 12 domain reviewers (authz/tenancy, secrets/crypto, SSRF/injection, operator blast-radius, reconcile/leader races, data-integrity/backup, API input-validation, node lifecycle, web frontend, CLI contract, MCP server, outbound/webhooks) fanned out over the codebase; **every finding adversarially verified** (refute-by-default — a skeptic tried to disprove each claim by reading the code before it was allowed to survive); survivors synthesized + ranked. 65 agents, 0 errors, ~4.6M tokens.
**Result:** 50 verified findings — **0 P0 · 19 P1 · 26 P2 · 5 P3**. 49 CONFIRMED, 1 PLAUSIBLE.

> **Caveat:** static read-only review. Findings are verified by tracing code paths end-to-end, **not** proven by live exploitation. Blast-radius/recoverability claims (esp. "recoverable via repair-password / re-add") should be spot-checked before operational reliance. The one PLAUSIBLE finding (P3 repo-URL XSS) needs human confirmation.

---

# kuso Platform Review — Synthesis Report

## 1. Executive summary

This static multi-agent review produced **49 verified findings** (one duplicate merged from an original 50) across authz, data-integrity, reconcile/leader races, operator blast-radius, CLI/MCP contracts, and supply-chain. **There are no P0s** — nothing is remotely/unauthenticated exploitable into RCE, cross-tenant compromise, or platform-caused unconditional data loss on the as-shipped single-tenant system. The weight sits at **19 P1** and **25 P2**, with **5 P3**. The most serious recurring themes are (a) **authz-tier bypass inside a project** — a non-admin `editor` can reach admin-only capabilities (arbitrary command-with-secret-env via crons, plaintext env values via nearly every non-GetService endpoint); (b) **a large, systemic gap in backup/restore integrity** — restores can silently no-op, duplicate, or apply truncated/unverified dumps, and multi-DB/HA postgres data is silently not backed up or is GC'd on delete; and (c) **a family of "field-drop / missing-guard" bugs** where update/create paths and CR literals silently omit fields or accept documented-immutable changes, wedging addons or breaking envs. The findings are verified by reading, not by exploitation; the one PLAUSIBLE-only finding (repo-URL XSS) is P3.

**Severity distribution:** P0 = 0 · P1 = 19 · P2 = 25 · P3 = 5.

---

## 2. Findings by severity

### P1 (19)

**[P1][authz-tenancy] Editor can run arbitrary commands with the service's full secret env via crons**
`server-go/internal/http/handlers/crons.go:177` (Add) and `:196` (Update).
Defect: service-attached `kind=command` cron Add/Update is gated only on `ProjectRoleEditor`, but the cron inherits the service's image + `envFromSecrets` and runs a caller-supplied command on schedule.
Scenario: editor POSTs a cron whose command exfiltrates `$(env)`; on next tick the CronJob runs it with `<addon>-conn`/shared secrets (DATABASE_URL, passwords, JWT/payment keys) mounted.
Blast radius: full secret disclosure to a non-admin insider — the exact capability `runs.Create` and `TerminalWSHandler` are admin-gated for; Update lets an editor repoint an existing cron.
Fix: require `callerCanReadSecrets`/`PermShellExec` (not just editor) for any cron path that resolves a service env's `envFromSecrets`.

**[P1][ssrf-injection] Cron onFailure webhook is a server-side SSRF (no validation, default transport)**
`server-go/internal/cronwatch/cronwatch.go:329`.
Defect: editor sets `onFailure.webhookURL` with zero URL validation; cronwatch POSTs via a bare `http.Client` (not `httpx.SSRFSafeTransport`).
Scenario: editor points webhook at `169.254.169.254`/kube apiserver/internal `.svc`, forces a non-zero cron exit; kuso-server (control-plane SA, in-cluster) issues the request.
Blast radius: blind fire-and-forget SSRF to IMDS/internal services from a privileged network position (no response read-back, so no direct exfil).
Fix: call `validateWebhookURL` before storing (as the notify/backups paths do) AND set the SSRF-safe transport on cronwatch's client.

**[P1][operator-blast-radius] HA postgres addon delete GC's all data PVCs**
`operator/helm-charts/kusoaddon/templates/postgres-ha.yaml:82`.
Defect: the CNPG `Cluster` is rendered without `helm.sh/resource-policy: keep`; CNPG PVCs carry ownerReferences, so delete → helm uninstall → Cluster delete → GC removes all replica PVCs — contradicting the "delete doesn't nuke data" contract that holds only for single-node StatefulSets.
Scenario: operator deletes (or project-deletes) a production HA pg; all three PVCs gone within seconds; the orphan-trail warning (`retainedPVCsForAddon`) doesn't fire because it filters a label CNPG PVCs lack.
Blast radius: irreversible production data loss, silent (no log about lost PVCs); `<name>-app` bootstrap secret also lacks keep.
Fix: gate HA-addon deletion behind confirm/backup; retain the Cluster's PVCs; fix `retainedPVCsForAddon` to match `cnpg.io/cluster`.

**[P1][operator-blast-radius] Env-group clone copies production custom domains → traffic hijack + LE churn**
`server-go/internal/projects/env_groups.go:594`.
Defect: `CreateEnvGroup` stamps the clone with the source service's `Domains` in `AdditionalHosts`/`TLSHosts` (TLS on, letsencrypt-prod) — the sibling single-env path explicitly nils this and documents why.
Scenario: a "staging" group claims prod `shop.example.com`; traefik gets two routers, live traffic non-deterministically hits staging code/DB; cert-manager races the prod cert.
Blast radius: production traffic served by stale staging against staging DB; Let's Encrypt rate-limit pressure on repeated create/delete.
Fix: set `AdditionalHosts=nil`, `TLSHosts=computeTLSHosts(host,nil)` in the clone literal.

**[P1][operator-blast-radius] Addon size change rewrites immutable VCT and wedges the helm release**
`server-go/internal/addons/addons.go:522`.
Defect: `Update` applies `req.Size` unguarded though `ha/storageSize/database` are refused; when `storageSize` is empty (default), size derives the VCT storage request, so a tier change mutates the immutable `volumeClaimTemplates`.
Scenario: user PATCHes small→medium on a pg without explicit storageSize; every subsequent helm upgrade fails (immutable-field reject); backup/tls/pooler/publicTCP edits silently never land. API returns 200.
Blast radius: addon becomes silently unmanageable until size reverted; no data loss.
Fix: refuse size changes that alter effective storage, or pin `storageSize` to the derived value at Add.

**[P1][operator-blast-radius] Addon version change accepted despite documented immutability**
`server-go/internal/addons/addons.go:519`.
Defect: `Update` applies `req.Version` with no guard although EDIT_SAFETY marks version "treat as new addon"; charts consume it as the image tag.
Scenario: pg 16→17 PATCH restarts the pod against a 16-initialized PVC → crash-loop "database files are incompatible"; downgrades too.
Blast radius: production DB outage until version reverted; no data destroyed.
Fix: refuse version changes (ErrConflict + backup/recreate message), at least across major versions for data-dir-versioned kinds.

**[P1][reconcile-races] Webhook/incident fan-out silently dropped when the two leader leases split across pods**
`server-go/cmd/kuso-server/main.go:1127`.
Defect: `notify.Dispatcher`'s `isLeader` reads `leaderActive` (flipped only by the "singletons" lease), but nodewatch/alerts/cronwatch/pkgupdates/backuphealth emit from the independent "cluster-singletons" lease; `dispatch()` and outbox workers gate on `leaderActive`, so events emitted on the cluster-singletons leader never reach the durable outbox when that pod isn't also the singletons leader.
Scenario: 2 replicas, leases on different pods; a node goes NotReady, pod B emits `node.unreachable`, its `dispatch()` returns before `EnqueueOutbox`; pod A never saw it (in-memory channel local to B).
Blast radius: entire out-of-band alert class (node up/down, alert.fired, cron.failed, backup-health, pkg-updates) + incident creation silently lost at ~coin-flip probability in the documented HA (2+ replica) config; bell feed survives.
Fix: use one lease for both singleton groups, or enqueue outbox rows unconditionally and keep the leader gate only on the drain workers.

**[P1][data-integrity-backup] In-place postgres restore silently no-ops or duplicates data**
`server-go/internal/backup/registry.go:56`.
Defect: restore pipes the dump into `psql` without `-v ON_ERROR_STOP=1`/`--single-transaction`, and every pg backup is produced without `--clean/--if-exists`, so replaying onto a live schema swallows every error and `psql` exits 0.
Scenario: user restores yesterday's backup after a bad change; CREATE TABLE fails "exists", PK COPYs fail (bad data kept), unconstrained COPYs append (rows duplicated); Job/UI/audit report success.
Blast radius: deterministic silent corruption via the primary recovery path with a false success signal.
Fix: add `--clean --if-exists` to the dumps and `-v ON_ERROR_STOP=1 --single-transaction` to the restore psql.

**[P1][data-integrity-backup] Release-hook gate silently skipped: JobName truncates tag to 12 chars**
`server-go/internal/releaserun/releaserun.go:217`.
Defect: `JobName()` keeps only tag[:12]; synthetic-ref builds (UI Redeploy / CLI without `--ref`) tag as `<branch-slug>-<unixms36>`, so for branch slugs ≳10 chars the uniqueness nonce is truncated and every deploy yields the same release Job name; `Run()`'s fast-path then observes the prior succeeded Job and skips the new migrations.
Scenario: branch `deploy-kuso`; deploy #1 runs migration; a later Redeploy adding a NOT-NULL migration reuses the 09:00 Job → returns Succeeded instantly, promotes the un-migrated image. Inverse: one failed hook poisons every redeploy for 24h (TTL).
Blast radius: silent promotion of un-migrated images (app/schema breakage) or 24h promotion lockout; webhook/explicit-SHA builds unaffected.
Fix: hash the full tag into the Job name (or keep the nonce suffix, not the prefix).

**[P1][data-integrity-backup] Backup pipelines run without pipefail — truncated dump gets a valid manifest**
`operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml:78`.
Defect: pg/external-pg/mysql backup scripts use only `set -eu`; `pg_dump|gzip` takes gzip's exit status, so a dump failure yields a truncated/empty artifact that is then sha256'd, given a matching manifest, uploaded, and the Job exits 0.
Scenario: nightly dump fails (password drift, DB restart, lock timeout); empty `.sql.gz` + self-consistent manifest uploaded; no failed-Job alert; retention prunes the last good dumps; restore "verifies checksum OK" and applies an empty dump.
Blast radius: silent unrecoverable-backup outcome; becomes data loss when a primary loss coincides.
Fix: add `set -o pipefail` (image supports it; restore scripts already use it) or dump-then-gzip, and fail on implausibly-small dumps.

**[P1][data-integrity-backup] Pre-deploy snapshot is fire-and-forget; migration runs before snapshot completes/verified**
`server-go/cmd/kuso-server/snapshot_adapter.go:114`.
Defect: `CreateSnapshotJob` returns the S3 key immediately after `Jobs().Create()`; the poller proceeds straight into `ReleaseRunner.Run`, so the migration mutates the DB concurrently with (or before) the snapshot pod's `pg_dump`, and a snapshot Job that fails outright is never detected — the key is still stamped as the restore point.
Scenario: `snapshotBeforeDeploy=true`; release migrate starts while the snapshot pod still pulls its image; migration half-corrupts, `pg_dump` captures the broken state (or the Job failed because the S3 secret isn't mirrored into the project namespace); "restore to pre-deploy" returns broken/nonexistent state.
Blast radius: the promised safety net doesn't exist; opt-in feature; nonexistent-artifact case fails loudly at restore time.
Fix: poll the snapshot Job to Complete + verify the object exists before `Run`; mirror `kuso-backup-s3` into the project namespace like Restore does.

**[P1][data-integrity-backup] Destructive in-place restore confirm gate bypassed by short-name/FQN aliasing of `into`**
`server-go/internal/http/handlers/backups.go:365`.
Defect: the typed-confirmation check compares raw strings (`destAddon == addon`) BEFORE name resolution, but `CRName`/`GetOwned` accept both `pg` and `myproj-pg`; with `addon=myproj-pg`, `into=pg` both resolve to the same CR yet skip the confirm requirement.
Scenario: editor in the UI (FQN in URL) types short `into`; confirm skipped; Job overwrites the live prod DB in place with no acknowledgment. CLI has the same naive compare.
Blast radius: bypass of an accidental-data-loss safety gate (the caller is otherwise authorized); real prod-DB-overwrite.
Fix: require Confirm whenever `destCR.Name == srcCR.Name` after resolution (accept short or FQN).

**[P1][data-integrity-backup] Scheduled backups of multi-DB postgres addons dump only the primary logical DB**
`operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml:78`.
Defect: `pg_dump ... "$POSTGRES_DB"` with POSTGRES_DB fixed at render time, while kuso now first-classes one-DB-per-tenant postgres addons (SQL-browser `?database=`, commit 713c5f67). Every non-primary logical DB is never backed up; no `pg_dumpall`/loop anywhere.
Scenario: one addon hosting `tenant_a/tenant_b/...` with nightly backups; list looks green; PVC lost → restore recovers only the primary; every tenant DB gone with no warning ever.
Blast radius: silent unrecoverable data loss with false assurance; at least one production deployment exposed today.
Fix: enumerate non-template DBs and dump each with per-artifact manifests, or emit a loud "not backed up" health warning when >1 DB exists.

**[P1][api-input-validation] Env-var values leak unmasked through every non-GetService endpoint**
`server-go/internal/http/handlers/projects.go:478`.
Defect: the admin-only env mask is applied only in GetService/GetEnv/Spec; ListServices (viewer-gated), Describe, ListEnvironments, GetEnvironment, and all editor-gated mutators (PatchService, AddDomain, SetEnvVar, AddEnvironment, env-domain ops) return full CRs with plaintext values.
Scenario: viewer calls `GET /api/projects/P/services` and reads DATABASE_URL/API keys/webhook secrets in cleartext; editor gets them via any PATCH response.
Blast radius: intra-team privilege-tier secret leak (the codebase's own `SetEnvScopedVar` returns 204 specifically to avoid this).
Fix: apply the `callerCanReadSecrets()` mask uniformly wherever a Service/Environment CR is serialized (or 204 the mutators).

**[P1][api-input-validation] AddEnvironment env literal drops SecurityContext/Healthcheck/PublicEnv/Release/SnapshotBeforeDeploy/Image**
`server-go/internal/projects/services_ops.go:952`.
Defect: the custom-env CR literal omits fields the production literal + propagate.go stamp; propagation repairs only some (Release/Snapshot gated on `changed`, Image never for `runtime=image`).
Scenario: new staging env → (a) setpriv image crashes exit 127; (b) publicEnv serves literal sentinels; (c) release-hook env promotes without migrations indefinitely; (d) `runtime=image` env holds 0 replicas forever.
Blast radius: silent breakage on every custom env; two paths (Release, Image) never self-heal; staging/qa scope (no prod data loss). Same "AddService-literal" class as prior incidents.
Fix: copy the missing fields into the literal; add a regression test diffing the two literals' field sets.

**[P1][node-lifecycle] reconcileReboots uncordons a node mid-apply, defeating drain and the one-node gate**
`server-go/internal/pkgupdates/apply.go:172`.
Defect: the finalize condition processes any Ready node carrying the pkgupdates cordon marker regardless of phase, so the 15s ticker uncordons + clears the marker + stomps `running→settling→done` while the apply Job's apt/drain is still running.
Scenario: multi-node reboot-apply; node stays Ready during apt; within 15s it's uncordoned, evicted pods reschedule back onto the about-to-reboot node (singletons like pg-pooler go down), false "patches applied" fires, and a second node can drain concurrently.
Blast radius: defeats both availability protections on essentially every multi-node apply-with-reboot; false success signal.
Fix: skip `running`/`draining` phases in the marker clause; add settling/draining coverage to `apply_test.go`.

**[P1][web-frontend] Env editor silently deletes valueFrom-backed env vars on save**
`web/src/components/service/EnvVarsEditor.tsx:131`.
Defect: `toEnvVar` serializes `fromSecret` rows as `{name}` (no value/valueFrom); the server drops entries with neither, and the confirm-dialog diff renders both sides through the same lossy chain so the deletion is invisible.
Scenario: a var backed by a valueFrom the UI can't map (legacy fieldRef/configMapKeyRef, renamed/deleted addon, addons query not yet resolved) is silently removed when the user saves any unrelated change; pod rolls missing the var.
Blast radius: silent, invisible-in-dialog destruction of env wiring with a rolling restart from a routine save; underlying Secret data survives.
Fix: capture the original `valueFrom` on the Row and re-emit it; server already carries intact secretKeyRef/fieldRef entries through.

**[P1][mcp-server] logs tool decodes lines as []string but server returns objects — tool always fails**
`mcp/internal/tools/observe.go:36`.
Defect: `logsResult.Lines []string` vs the server's `[]{pod,line}` object array, so JSON decode fails whenever any log line exists.
Scenario: agent calls `logs(project,service)` on any non-empty service → "cannot unmarshal object into Go struct field ... of type string" → errors on every real invocation (only "works" with zero lines).
Blast radius: breaks the advertised apply→build→status→logs debug loop entirely on the MCP surface.
Fix: change `Lines` to `[]struct{Pod,Line string}` (mirror the CLI) and render pod-prefixed lines.

**[P1][supply-chain] Pinned/pre-manifest update paths deploy images with NO signature verification**
`server-go/internal/updater/updater.go:395`.
Defect: when a GH release has no `release.json` asset, both `fetchVersion` (pinned) and `fetchLatest` synthesize a manifest with hardcoded ghcr tags and return BEFORE `verifyManifestSignature`, silently skipping the ed25519 gate the code's own comment says pinned upgrades must not bypass.
Scenario: attacker who has compromised the GH releases org publishes a tag + malicious ghcr image with no `release.json`; admin POSTs `/api/system/update {version}`; the updater `kubectl set image`s the attacker image with no signature check.
Blast radius: defeats the supply-chain defense on a live path (RCE on self-updating clusters), but requires prerequisite GH+ghcr compromise + an authenticated admin trigger — defense-in-depth defeated, not unaided-attacker P0. (`fetchLatest` synth path leaves images empty → broken Job, not code exec.)
Fix: run the `verifyManifestSignature`/`ErrUnsignedNoKey` gate BEFORE the synth-manifest early returns; refuse to synthesize when a public key is configured.

---

### P2 (25)

**[P2][secrets-crypto] Admin superuser DSN can leak to a project editor via url.Parse error**
`server-go/internal/addons/instance_provisioner.go:151`. `%w`-wrapped `url.Parse` embeds the full admin DSN (with password) in an editor-reachable 400 body when a valid-to-lib/pq DSN (e.g. `%` in password) trips url.Parse. Blast radius: instance superuser credential disclosed to a non-admin editor; narrow trigger. Fix: return a static "malformed" message (mirror the guard in `instance_addons.go`).

**[P2][ssrf-injection] Backup S3 endpoint SSRF: validation skips DNS, AWS client uses default transport**
`server-go/internal/http/handlers/backups.go:162`. Admin S3 endpoint validated only by `validateWebhookURL` (no DNS resolution, IP-literal-only), then used on the default transport, so a hostname resolving to a private/metadata IP is reached; List surfaces AWS error strings. Blast radius: admin-triggered internal-reachability probing + error leakage (semi-blind, signed request). Fix: build the S3 client with `httpx.SSRFSafeTransport()` (as import_coolify does).

**[P2][operator-blast-radius] Project-delete label sweep destroys keep-annotated addon conn secrets**
`server-go/internal/projects/projects_ops.go:465`. The unconditional Secret sweep deletes every `kuso.sislelabs.com/project=<name>` Secret including helm-owned `<addon>-conn/tls`, defeating `resource-policy: keep`. On non-purge delete + same-name recreate, the chart mints a fresh password over the surviving initialized pgdata → SASL auth failure (postgres recoverable via `repair-password`; mysql/mongo need manual surgery). Fix: exclude helm-owned secrets or move the sweep behind `PurgeData`.

**[P2][operator-blast-radius] Conn Secret missing resource-policy=keep in remaining addon templates**
`operator/helm-charts/kusoaddon/templates/s3.yaml:123` (also clickhouse/nats/nats-ha/redis-ha/redpanda/mailpit). The v0.18.109 keep-annotation fix wasn't applied to all kinds; delete+re-add over a surviving PVC mints fresh credentials. Materially damaging only for s3/MinIO (credential-derived IAM/config on the PVC); nats/clickhouse/redis-ha converge self-consistently; redpanda/mailpit carry no generated creds. Fix: add `helm.sh/resource-policy: keep` to the conn Secret in s3.yaml (and the others for consistency).

**[P2][reconcile-races] buildcontroller DeleteFunc ignores DeletedFinalStateUnknown tombstones**
`server-go/internal/buildcontroller/buildcontroller.go:157`. DeleteFunc only handles `*unstructured.Unstructured`; a delete missed during a watch relist strands the CR key in `running`, so a same-name rebuild (deterministic `buildCRName`) is deduped and never reconciled. Mostly self-healing (force-fail after 30m + later real delete clears it); only explicit-SHA/webhook rebuilds affected. Fix: unwrap `cache.DeletedFinalStateUnknown` in DeleteFunc.

**[P2][reconcile-races / node-lifecycle] scaledown's HPA-ownership guard is dead code** *(merged: two reviewers reported this at the same line)*
`server-go/internal/scaledown/scaledown.go:154` (dup annotation-check in `projects/drift.go:494,543`). The "don't fight the HPA" guard tests `autoscaling.alpha.kubernetes.io/conditions` on the Deployment, an annotation only ever written onto HPA objects, so `hpaManaged` is always false. A sleep+autoscaling service is scaled to 0 anyway, the HPA goes dormant, and wake restores below `scale.Min` briefly (self-healing via the activator; drift.go emits false "replicas" drift). Fix: detect HPA ownership via the spec (`scale.Max>scale.Min`) or by `scaleTargetRef`; apply to drift.go too.

**[P2][reconcile-races] Per-service build queue TOCTOU: active-check reads lagging informer cache, locks are process-local**
`server-go/internal/builds/builds.go:690`. The queued/active decision reads through the informer cache (not read-your-writes), and the per-service mutex only serializes within one process. Two triggers ~50ms apart (or multi-replica) can both create active CRs → two parallel kaniko builds for one service (OOM/out-of-order promotion). Fix: do the active-check with a live/field-selector list inside the lock; leader-gate or server-side-enforce the queued invariant for cross-replica.

**[P2][reconcile-races] markFailed/promoteOne stamp state with non-CAS patches that overwrite a concurrent Cancel**
`server-go/internal/builds/builds.go:2332`. `markFailed` (unlike `markSucceeded`) does no pre-patch re-read, and `promoteOne` promotes from a stale snapshot; a Cancel landing mid-tick is clobbered → false `build.failed` (@here page) + mislabeled record, or a 30-min zombie CR that spuriously fails. No data loss / no wrong image promoted. Fix: re-Get and bail on `phase=cancelled`/`done`, or use a resourceVersion-preconditioned Update.

**[P2][reconcile-races] Lexicographic promotedAt comparison unsound; stale auto-promote can overwrite a rollback**
`server-go/internal/builds/builds.go:2507` (also `:2647,:2814`). `prev > bTrigger` string-compares whole-second (build trigger) vs fractional (rollback) RFC3339Nano; `'.' < 'Z'` inverts order within the same second, so an in-flight auto-promote overwrites a user rollback with the image they just rolled away from. One-wall-clock-second race window. Fix: parse both with `time.Parse(RFC3339Nano)` and compare `time.Time` (with `>=` semantics).

**[P2][data-integrity-backup] External-postgres backups write no manifest — restores permanently unverifiable**
`operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml:231`. The external path streams `pg_dump|gzip|aws s3 cp -` with no sha/manifest, so the restore always takes the "integrity NOT verified, proceeding" branch (and can never be backfilled). Detects at-rest corruption other kinds get; upload truncation already fails the job via `aws` exit. Fix: buffer to /tmp then sha256 + upload manifest (or tee through sha256sum).

**[P2][data-integrity-backup] DeletePRAddons deletes any addon whose name ends in `-pr-<N>`**
`server-go/internal/previewdb/previewdb.go:255`. The PR-close sweep matches by name suffix, ignoring the `kuso.sislelabs.com/preview-pr` ownership label, so a real addon named e.g. `events-pr-2` is deleted when PR #2 closes. Native addons lose STS/conn (recoverable outage, PVC retained); the instance-pg logical-DB drop is separately label-gated, so no DB drop. Fix: filter on the `preview-pr` label (fall back to suffix only for legacy clones carrying `LabelEnv`).

**[P2][api-input-validation] AddService production-env literal drops PublicEnv**
`server-go/internal/projects/services_ops.go:592`. `PublicEnv` is stamped on the service spec but not the auto-created production env literal; the chart substitutes sentinels only from the env CR. First deploy serves raw `__KUSO_RUNTIME_*__` until any later service edit propagates. Same field-drop class as above. Fix: add `PublicEnv: created.Spec.PublicEnv` to the production literal.

**[P2][api-input-validation] Addon version/storageSize accepted verbatim into helm templates**
`server-go/internal/addons/addons.go:239`. Add/Update stamp `Version`/`StorageSize` with no format check; they flow verbatim into `image: postgres:{{$version}}` and PVC storage. `storageSize:"10Gigs"` → 201 then a wedged reconcile (helm render/apply fails) for that one addon. YAML-injection crosses no privilege boundary (editor can already run arbitrary images). Fix: `resource.ParseQuantity` for storageSize; restrict Version to `^[A-Za-z0-9._-]{1,128}$`.

**[P2][api-input-validation] Cron schedule validator accepts out-of-range values; cron saves but never fires**
`server-go/internal/crons/crons.go:150`. The regex checks only field shape, so `0 25 * * *` passes, the CR is created (201), and the rendered CronJob is rejected at reconcile with no surfaced status. Silent failure of a user's scheduled job (e.g. nightly backup). Fix: parse with `robfig/cron/v3` (the grammar kube uses) and return ErrInvalid.

**[P2][node-lifecycle] nodewatch auto-uncordon lost across server restart, never retried on failure**
`server-go/internal/nodewatch/nodewatch.go:176`. Uncordon is keyed off the in-memory `alerted` map, not the persisted cordon annotation, so a node recovering during a restart (release roll/leader failover) stays cordoned forever; a single transient uncordon failure also never retries. Node is visible/manually reversible; a later >5min flap heals it. Fix: also queue uncordon when the cached node carries the cordon annotation; re-add on failure.

**[P2][node-lifecycle] nodewatch claims ownership of operator-applied cordons and uncordons them on recovery**
`server-go/internal/nodewatch/nodewatch.go:294`. `cordon()` stamps the ownership annotation on a node that is already unschedulable, so recovery uncordons a node kuso never cordoned — violating the "only uncordon if WE cordoned" contract. Scheduler places workloads on a deliberately-drained box mid-maintenance. Fix: don't stamp (or stamp a distinct "pre-cordoned" value) when already unschedulable.

**[P2][node-lifecycle] anotherNodeRebooting does not treat the settling phase as busy**
`server-go/internal/pkgupdates/apply.go:141`. The one-node gate matches draining/rebooting/running but not settling (and the marker fallback is cleared before settling), so a second node's drain+reboot can begin while node A's reboot-stranded singletons are still rescheduling — extending the DB-outage window settling was added to close. Fix: add `"settling"` to the busy-phase switch.

**[P2][cli-contract] build trigger --follow never terminates on release-failed builds**
`cli/cmd/kusoCli/build.go:80`. `pollBuildToTerminal` omits `release-failed` from its terminal set, so a release-hook failure polls the full 20m and exits with a misleading timeout instead of the failure cause. CI burns 20m per failed release. Fix: add a `release-failed` arm returning the build's ErrorMessage.

**[P2][cli-contract] cron edit --image-tag alone wipes the cron's image repository**
`cli/cmd/kusoCli/cron.go:271`. Editing only `--image-tag` sends `{repository:"",tag:v2}`; server `UpdateProject` replaces `Spec.Image` verbatim (empty-repo guard exists only on create) → `:v2` InvalidImageName at next fire; CLI prints success. Fix: fetch-then-patch the current repository, or reject `--image-tag` without `--image`.

**[P2][cli-contract] cron --cmd splits on whitespace, breaking quoted commands in the CLI's own examples**
`cli/cmd/kusoCli/cron.go:100` (also `:226,:275`). `strings.Fields` ignores shell quoting, so the documented `--cmd 'sh -c "echo tick"'` becomes wrong argv; creation succeeds, runs fail. Fix: use a shlex-style splitter or a trailing `-- argv…` like `kuso run`; fix the examples.

**[P2][cli-contract] KUSO_INSECURE=1 not honored by WebSocket commands (logs -f, db tunnel)**
`cli/cmd/kusoCli/logs.go:129` (and `db.go:197`). Both dial with `websocket.DefaultDialer` and never set `TLSClientConfig`, so on LE-staging certs `logs -f` and every db tunnel connection hard-fail x509 while REST works. Also mutates the shared `DefaultDialer`. Fix: clone the dialer and set `InsecureSkipVerify` under the same env check (shared helper).

**[P2][cli-contract] Top-level `kuso service set` alias missing --cap-add / --allow-privilege-escalation**
`cli/cmd/kusoCli/project.go:1326`. The alias shares the project-scoped RunE but omits the two securityContext flags, so `kuso service set … --cap-add SETUID` errors "unknown flag" while `kuso project service set …` works. Loud failure, exact working equivalent exists. Fix: register both flags on the alias (or factor into a shared helper).

**[P2][mcp-server] apply deletes addons/services (prune) with no confirm gate**
`mcp/internal/tools/apply.go:49`. The apply tool has no `confirm` param, yet `prune:true` in the YAML deletes services/addons, bypassing this surface's `confirm=true` convention. Addon delete tears down the STS but retains PVCs + keep-annotated conn secret, so the realistic blast radius is a recoverable production outage, not data loss (prune-in-YAML is the deliberate config-as-code opt-in). Fix: require `confirm=true` when the YAML sets `prune:true` or the dry-run plan contains deletions.

**[P2][mcp-server] set_env whole-list replace has no confirm gate**
`mcp/internal/tools/env.go:59`. `set_env` replaces the entire env list (omitted keys deleted) with no confirm flag, unlike the less-destructive bootstrap/add tools; an incremental-intent call from an agent wipes all other plain env vars and rolls the pods. Semantics are disclosed in the tool description; excludes secrets; a per-key delta endpoint exists server-side. Fix: add `confirm=true` or a merge-mode default with explicit replace.

**[P2][notify-webhooks-egress] SMTP notification channel bypasses the SSRF-safe transport**
`server-go/internal/notify/channels.go:282`. `smtpSendMail` dials the operator-supplied SMTP host with a raw `net.Dialer` and no reserved-IP check (both at config-validate and dispatch time), unlike every HTTP channel. Admin-only + single-tenant, so defense-in-depth: a narrow internal TCP port-probe primitive (no IMDS HTTP exfil). Fix: resolve + `httpx.IsReservedIP` before dialing (shared reserved-IP dialer).

---

### P3 (5)

**[P3][secrets-crypto] External PG admin DSN echoed in 400 body via url.Parse error** — `server-go/internal/instancepg/instancepg.go:386`. `coerceSSLMode` wraps `url.Parse`'s error (full DSN) into the admin-only 400 body. Echoed only to the same admin who submitted it; no body logging. Hygiene fix: redact userinfo / return a static message.

**[P3][reconcile-races] previewdb seedAsync poll loop ignores context cancellation** — `server-go/internal/previewdb/previewdb.go:280`. Bare `time.Sleep(5s)` with no `ctx.Done()` check, violating the BaseCtx contract; but shutdown fixed timers mean the goroutine survives only ~500ms in practice, so the claimed 5-minute spin doesn't occur. Two-line fix: `select { case <-ctx.Done(): case <-time.After(5s): }`.

**[P3][api-input-validation] No kube-name validation and no IsInvalid mapping: bad names return 500** — `server-go/internal/projects/projects_ops.go:104` (also addons/crons). Invalid names reach the apiserver whose 422 is unmapped by any `fail()` helper → 500 instead of an actionable 400. Nothing persists; only wrong status + log noise. Fix: validate names at the domain boundary (RFC1123 + length budget) and map `apierrors.IsInvalid` to 400.

**[P3][web-frontend] Repo URL rendered as href with no scheme validation (PLAUSIBLE)** — `web/src/components/service/overlay/settings/SourceSection.tsx:90`. A stored `javascript:` repo URL renders into an `<a href … target="_blank">`; the sink accepts any scheme and the server stores `repo.url` unvalidated. But `target="_blank"` anchors block `javascript:` navigation in all modern browsers, so the claimed editor→admin JS-execution/privilege-escalation is not reachable as shipped — a latent sink + dangling-link nit, not a live exploit. Fix: render the anchor only for parsed http/https, and validate scheme server-side.

**[P3][cli-contract] `kuso api` GET with -f/-F/--data silently drops the request body** — `cli/cmd/kusoCli/api.go:106`. resty discards GET payloads (AllowGetMethodPayload=false, never enabled), so `-f` fields on GET are silently never sent (and kuso GET handlers read query params anyway). Fix: reject `-d/-f/-F` on GET or convert `-f` to query params (gh-api behavior).

---

## 3. Themes & systemic recommendations

- **Intra-project authz tiers are enforced inconsistently.** The `editor` role repeatedly reaches admin-only capability: arbitrary command-with-secret-env via crons (#1), plaintext env values through nearly every serializer except GetService (#27), and a superuser-DSN echo (#2). *Structural fix:* apply the secret-read mask and the "arbitrary-code-with-service-env ⇒ admin" gate at shared choke points (one serialization helper that masks EnvVars; one authz helper every command/cron/run/shell path calls), and add tests that assert `editor` is refused across the whole surface, not per-handler.

- **"Field-drop / missing-guard" is a recurring class.** Env-CR literals drift from `propagate.go` (#28, #29, and the AddService history in memory), and `addons.Update` applies documented-immutable fields (#9 size, #11 version, #30 validation). *Structural fix:* generate/derive env-CR literals from a single source (or a shared builder) and add a regression test diffing populated field sets between the production and custom-env literals; drive addon-field mutability from the EDIT_SAFETY table so "immutable" is enforced in code, not prose.

- **Addon data-lifecycle annotations are applied ad hoc per chart.** `resource-policy: keep` is present on some kinds and missing on others (#10), the project-delete sweep ignores it (#6), and HA/CNPG has no equivalent (#7). *Structural fix:* a shared chart helper/partial that emits the keep annotation for every credential + data object, plus a lint/test asserting every addon kind's conn Secret and data volume carry it; make delete/purge logic label-ownership-aware (skip helm-managed) uniformly.

- **Outbound-target SSRF protection is not centralized.** Three channels bypass `httpx.SSRFSafeTransport` (#4 cron webhook, #5 S3, #50 SMTP), and validation that skips DNS resolution gives false confidence (#5). *Structural fix:* route all user/operator-configurable egress through one reserved-IP-checking dialer/transport (including non-HTTP SMTP), and make `validateWebhookURL` resolve hostnames (or document clearly that it doesn't and pair it with a dialing guard).

- **Backup/restore integrity is the largest systemic gap.** Restores silently corrupt (#19), pipelines lack `pipefail` (#21), pre-deploy snapshots are unordered/unverified (#22), the destructive-restore confirm is bypassable (#23), external backups lack manifests (#24), and multi-DB/HA data is silently unbacked-up or GC'd (#25, #7). *Structural fix:* treat the backup/restore scripts as one hardened module — `set -eo pipefail` + `ON_ERROR_STOP=1` + `--clean --if-exists` everywhere, mandatory manifest for every artifact, dump-completeness (all logical DBs) checks, and a restore-round-trip smoke test in CI.

- **Multi-replica / reconcile correctness assumes single-replica.** Leader-lease split drops alerts (#12), informer-cache reads and process-local locks race (#13, #15, #16), and node-lifecycle state machines have phase gaps (#33, #34, #35, #36). *Structural fix:* consolidate to one lease (or lease-aware gating), prefer live reads / CAS (resourceVersion-preconditioned Updates) over merge-patches on contended objects, and add settling/draining/tombstone coverage to the node and build tests.

- **CLI/MCP contracts drift from the server and from each other.** Terminal phase vocab (#40), flag parity (#45), argv/quoting (#42), whole-list-replace without confirm (#41, #47, #48), TLS handling (#44), and a hard-broken logs decoder (#46). *Structural fix:* share flag registration between alias commands, generate the terminal-phase set and wire shapes from one source, and apply the MCP `confirm=true` convention to every mutating/destructive tool.

---

## 4. Coverage & caveats

- This was a **static, read-only multi-agent review**. Findings are **verified by reading the code paths end to end**, not proven by running or exploiting them. Line numbers, control flow, chart templates, and cross-references were checked, but no finding was reproduced against a live cluster except where a reviewer explicitly ran a local Go/CLI repro (e.g. the `url.Parse` disclosure strings and the `service set` flag error).
- **48 findings are CONFIRMED; 1 is PLAUSIBLE** (#39 repo-URL XSS) and needs human confirmation of the browser/UA behavior before any severity change — as written it is a latent sink, not a demonstrated exploit.
- **Severity is honest, not inflated: there are no P0s.** Nothing here is an unauthenticated/remote compromise or an unconditional platform-caused data-loss on the default single-tenant deployment. The P1s are serious authz-tier bypasses, silent data/backup-integrity failures, and availability/correctness breaks with realistic triggers; several depend on the documented-but-opt-in HA (2+ replica) or specific operator sequences, which is noted per finding.
- Where reviewers **corrected an initial severity** (e.g. #49 P0→P1, #47 data-loss→recoverable-outage, #18 P1→P3), the report reflects the corrected, evidence-backed rating — but those corrections were themselves reached by reading, so the blast-radius claims (especially "recoverable via re-add / repair-password") should be spot-checked before relying on them operationally.
- Duplicate handling: the scaledown HPA dead-guard was reported twice (`scaledown.go:154`) and is **merged into one P2 entry**; both reviewers' framing (scaledown zeroing + drift.go false-positive) is captured.