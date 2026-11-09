# Vulnerability scanning

kuso scans every image it runs for known CVEs with [Trivy](https://github.com/aquasecurity/trivy):

- **Service images** — after every successful build (a promotion gate), and nightly while live.
- **Addon images** — nightly (postgres, redis, minio, pgbouncer sidecars, …).
- **Platform images** — nightly (kuso-server, operator, registry, buildkitd, prometheus, traefik, …), admin-only view.

Findings land in the control-plane Postgres. New fixable critical/high CVEs fire a notification; fixable criticals block promotion by default.

This file is the build spec AND the user doc. Sections marked _(contract)_ are binding for server, CLI and web.

## Behaviour

### Scanning

- A scan runs Trivy (`aquasec/trivy:0.76.2`, pinned by digest in code) as a Job in the `kuso` namespace. Never in a project namespace: project NetworkPolicies block registry egress there.
- Scans are keyed by **image digest**. The same digest is scanned at most once per 20h unless forced; a forced or nightly rescan re-evaluates against the latest Trivy DB, since new CVEs land on old images.
- Global concurrency: **1** scan Job at a time (queue in Postgres). Job: `activeDeadlineSeconds: 600`, requests 200m/512Mi, limits 1 CPU/2Gi, drop ALL caps, `runAsNonRoot`, seccomp RuntimeDefault, `automountServiceAccountToken: false`, TTL 1h, `backoffLimit: 0`. Preferred node affinity away from `node-role.kubernetes.io/control-plane`.
- Trivy DB is cached on a PVC `kuso-trivy-cache` (2Gi) in `kuso`, created on first use by the server.
- In-cluster registry is plain HTTP: run with `--insecure`. Images with a `pullSecret` (or, for gate scans of an image pushed to an external registry, the build's push Secret from `build.registryAuthSecret`, read from the build's namespace): credentials for **that registry host only** are passed via a per-Job Secret in `kuso` (owned by the Job, deleted with it). Never pass credentials for a host other than the image's own (GHSA-xcq4-m2r3-cmrj).
- Output: `trivy image --scanners vuln --format json --quiet` written to a file, then `gzip | base64` to stdout, read back from pod logs and parsed server-side. Only vulnerability fields we store are kept.
- Scan failure (pull error, DB download failure, timeout, parse error) is recorded as `status: failed` with the error. It is never treated as "clean".

### Promotion gate

After a build's Job succeeds and before promotion (in `builds.markSucceeded`, **before** the same-repo sibling hold):

1. Resolve the image digest; request a scan (or reuse a < 20h result for the same digest).
2. While the scan is pending/running the build stays non-terminal with a hold message `scanning image for vulnerabilities…` (re-checked every poller tick, like the existing hold).
3. Verdict, using the **effective mode** for the service:
   - `off` — no scan at all for this service (nightly included).
   - `report` — scan, never block; the deploy never waits for the scan.
   - `enforce` — block if the image has ≥ 1 **fixable critical** finding that is not ignored.
4. **Blocked**: the build becomes terminal `failed` with message `blocked: N fixable critical vulnerabilities (CVE-…, CVE-…) — see kuso vulns show …` and a classification `vuln-blocked`. The previous image keeps serving (same contract as a failed release hook). Emits `security.deploy_blocked`. To unblock: fix and rebuild, ignore the CVE(s) and rebuild/`kuso build trigger`, or set the service policy to `report`.
5. **Fail-open**: if the scan fails or is still not done 15 minutes after the gate first asked for it, promotion proceeds and `security.scan_failed` is emitted with `promoted unscanned`. A broken scanner must never freeze deploys.
   - **Circuit breaker**: when the last 3 finished scans (any reason) all failed within the last 2h, the scanner is treated as broken and every gated build fails open **immediately** instead of waiting 15 minutes. The scan is still queued, so the first success closes the breaker. `security.scan_failed` fires once per breaker-open period (per server process), not per build.
   - A scan pod that stays unschedulable (`PodScheduled=False`, reason `Unschedulable`) for more than 2 minutes, or sits Pending while the `kuso-trivy-cache` PVC is still Pending, fails at once with that reason instead of running out the 12-minute backstop.
6. **Monorepo siblings** (same-repo atomic promotion): the gate runs first, so a build waiting on its scan holds with `scanning image for vulnerabilities…` and its siblings treat it as **not green** — they wait. Only after its scan clears does a build join the sibling wait (any other hold reason). A vuln-blocked build is a failed sibling: the wave holds until it is retried green, cancelled, or the 2h hold expiry, same as any failed sibling build.
7. Effective mode = service policy if not `inherit`, else instance `vulnscan.mode`. Instance default: **`report`** (scan and report, never hold or block); switch the instance or a service to `enforce` to gate deploys. Instance `off` disables the whole feature (no gate, no nightly).

### Nightly sweep

Leader-gated loop on the cluster-singletons lease, ticking every 10 minutes (registered for liveness with that interval, heartbeat every tick). Once per day (first tick at/after `vulnscan.nightlyHourUTC`, default 3, and no completed sweep in the last 20h — tracked in the Setting kv) it:

1. Builds the inventory from **live pods** (`containerStatuses[].imageID` gives the digest; init containers included):
   - service: pods of KusoEnvironments (label `kuso.sislelabs.com/project` + `/service` + env) — production and custom envs; previews skipped.
   - addon: pods labelled `kuso.sislelabs.com/addon`.
   - platform: pods in `kuso`, `kuso-operator-system`, `traefik` (and `kube-system` traefik if present) that are neither service nor addon pods.
2. Upserts the inventory (`VulnImage`), marks images not seen for 7 days as gone.
3. Queues a scan for each digest whose last successful scan is older than 20h (skip if service effective mode is `off` / addons disabled / platform disabled).
4. After scans complete, computes **new** findings per owner (service, addon, platform) — a (owner, vulnId) pair not previously notified — and emits one `security.vulnerabilities_found` event per owner listing the new **fixable critical+high** findings (top 10 + count). Notified pairs are recorded so the same CVE never re-alerts for the same owner.
5. **First sweep is a baseline.** The first sweep a cluster ever runs (no `vulnscan.lastSweepAt` yet) sends no per-owner alerts and no per-image `scan_failed` for its nightly scans (failures are logged). When every scan it queued has finished, each current finding is recorded as notified and **one** summary event goes out: `Baseline vulnerability scan complete: N images, X with fixable criticals — see Settings → Vulnerability scanning`. The state lives in the Setting `vulnscan.baseline` (`pending` while queueing → highest covered scan id → `done`), so a restart mid-sweep keeps the baseline for every scan that sweep queued, and a sweep that died before recording itself is retried as a baseline.

Retention: keep the last 5 succeeded and the last 5 failed scans per digest (ranked separately, so failures never prune the last good result); delete scans (and findings) of digests not in the inventory for 30 days; delete inventory rows unseen for 30 days and notified (owner, CVE) records whose owner has been gone that long; expired ignores are kept for audit but no longer apply.

### Ignores

Per service (or per addon), per vulnerability ID, with a mandatory reason and optional expiry (`--expires 30d`, max 365d). Ignored findings are still shown (marked ignored) but excluded from gate and alerts. Instance-wide ignores (platform) are admin-only.

### Addon image updates

Addon images are upstream tags (`postgres:16`, `redis:7-alpine`, `ghcr.io/cloudnative-pg/postgresql:16`). Nodes pull a tag once and the chart's pull policy never re-pulls it, so without this an addon runs the build it first started on forever, missing upstream's security rebuilds.

The updater (`internal/vulnscan/addonupdate.go`) runs on every sweep tick when `autoUpdateAddons` is on (and `scanAddons` is on and mode isn't `off`). Per native addon:

1. Find the main image from the addon's own pods: the first container of its StatefulSet/Deployment/CNPG instance pods, skipping pooler and Job pods. The tag ref is that image without any digest. The running digest counts only when every such pod is Ready on one digest.
2. Resolve the tag's current digest anonymously against its registry (HEAD, bearer-token challenge; at most once per 20h per addon, once per tag per tick).
3. If it differs from the running digest, queue a scan of `repo@newDigest` and wait for it.
4. **Roll** when the new image has no more fixable critical+high findings than the running one (or the running one was never scanned), and only between `nightlyHourUTC` and +4h. At most 2 addons roll per tick (10 min). Rolling stamps `spec.imagePin = {image: "<tag ref>", digest}` on the KusoAddon. The chart's `kusoaddon.image` helper renders `tag@digest` only while `imagePin.image` equals the ref it renders, so changing `spec.version` drops a stale pin by itself. Without a pin, rendering is unchanged.
4a. **The restart itself** (`internal/vulnscan/addonroll.go`), run in the background so a tick or request never blocks on it:
   - Pre-pull: one Job per node the addon runs on (in the `kuso` namespace, pinned with `nodeName`) pulls `repo@digest`. A failed pull aborts before anything restarts.
   - Single-node Postgres: `CHECKPOINT`; pin; once the old pod is terminating, `PAUSE` the addon's PgBouncer (pooled queries wait instead of failing) and terminate client sessions until Postgres refuses new logins, then drop the updater's own session. A smart shutdown waits on every session, so without this it would sit out the grace period and be SIGKILLed into crash recovery. `RESUME` once the new pod is Ready, or after 25s (under the pooler's 30s `query_wait_timeout`).
   - Pinned single-node Postgres pods also get a `pg_ctl -m fast` preStop hook, a 60s grace period and a 2s readiness probe. These render only with a pin, so shipping them restarts no database; each addon gets them with its first image update.
   - HA Postgres: CNPG updates the replicas, then switches over (`primaryUpdateMethod: switchover`).
   - Other kinds: pre-pull only.
5. **Rollback**: if the pods aren't Ready on the new digest 30 minutes after the pin, the previous digest is pinned back (still in the node's cache) and the failed digest isn't tried again until the tag moves. Rolls in flight are watched even with auto-update off, so a manual update gets the same rollback.

The version (major/minor) never changes. A newer build that scans worse is `rejected` and left alone. Per-addon state (`current | scanning | ready | rejected | scan-failed | updating | rolling | rolled-back | error`) lives in the Setting `vulnscan.addonImageUpdates` and is returned as `imageUpdate` on the addon vulns endpoint. `POST …/addons/{addon}/vulns/update-image` (editor; `kuso vulns update-image`) starts the same rollout immediately, skipping the window and the scan comparison; 409 while one is already running.

Not covered: service images (rebuild them: `kuso build trigger`), fixed sidecar images in the charts (bumped per release), external and instance-shared addons (kuso runs no image for them).

### Notifications

New event types (listed under the `other` group in notification settings):

| Type | Severity | When |
|---|---|---|
| `security.vulnerabilities_found` | `error` if any critical else `warn` | nightly/forced scan finds new fixable critical/high for an owner |
| `security.deploy_blocked` | `error` | gate blocked a build |
| `security.scan_failed` | `warn` | a scan failed; for a gate, the build was promoted unscanned |
| `security.addon_image_updated` | `info`, `warn` on rollback | an addon was moved onto a newer build of its tag, or such a move was rolled back |

Deep link: service overlay `tab=vulnerabilities`.

## Settings _(contract)_

Instance (Setting kv, keys `vulnscan.*`), admin only:

```json
{
  "mode": "report",           // enforce | report | off
  "scanAddons": true,
  "scanPlatform": true,
  "nightlyHourUTC": 3,        // 0-23
  "notifyMinSeverity": "high", // critical | high | none
  "autoUpdateAddons": true      // roll addons onto newer builds of their tag nightly
}
```

Per service policy: `"inherit" | "enforce" | "report" | "off"` (DB table, no CRD change).

## Data model

Migration `0016_vulnscan.sql` (additive only). Tables (quoted PascalCase, camelCase columns, like the rest of the schema):

- `VulnScan` — id, digest, imageRef, status (`queued|running|succeeded|failed`), error, trivyVersion, dbUpdatedAt, createdAt, startedAt, finishedAt, counts (critical, high, medium, low, unknown), fixableCritical, fixableHigh, jobName, reason (`gate|nightly|manual`).
- `VulnFinding` — scanId (FK cascade), vulnId, pkgName, pkgType, installedVersion, fixedVersion, severity, title, primaryURL, target. Index (scanId, severity).
- `VulnImage` — digest, imageRef, ownerKind (`service|addon|platform`), project, service, env, addon, namespace, workload, firstSeenAt, lastSeenAt. Unique (digest, ownerKind, project, service, env, addon, namespace, workload).
- `VulnPolicy` — project, service, mode. PK (project, service).
- `VulnIgnore` — id, scope (`service|addon|platform`), project, service (or addon name), vulnId, reason, expiresAt NULL, createdBy, createdAt.
- `VulnNotified` — ownerKey, vulnId, notifiedAt. PK (ownerKey, vulnId).

Add every new table to the TRUNCATE list in `db/db_test.go`.

## HTTP API _(contract)_

All JSON. Times RFC3339. `severity` ∈ `CRITICAL|HIGH|MEDIUM|LOW|UNKNOWN`.

Shared shapes:

```ts
type VulnCounts = { critical: number; high: number; medium: number; low: number; unknown: number; fixableCritical: number; fixableHigh: number };

type VulnScanSummary = {
  id: number;
  digest: string;              // sha256:…
  imageRef: string;
  status: "queued" | "running" | "succeeded" | "failed";
  error?: string;
  reason: "gate" | "nightly" | "manual";
  createdAt: string;
  finishedAt?: string;
  dbUpdatedAt?: string;
  counts: VulnCounts;          // ignored findings EXCLUDED from counts
  ignoredCount: number;
};

type VulnFinding = {
  vulnId: string; pkgName: string; pkgType: string;
  installedVersion: string; fixedVersion?: string;
  severity: "CRITICAL" | "HIGH" | "MEDIUM" | "LOW" | "UNKNOWN";
  title?: string; primaryURL?: string; target?: string;
  ignored: boolean; ignoreId?: number;
};

type VulnIgnore = { id: number; scope: "service" | "addon" | "platform"; project?: string; service?: string; vulnId: string; reason: string; expiresAt?: string; createdBy: string; createdAt: string; expired: boolean };

type VulnImageReport = {
  ownerKind: "service" | "addon" | "platform";
  project?: string; service?: string; env?: string; addon?: string;
  namespace?: string; workload?: string;
  imageRef: string; digest: string;
  scan: VulnScanSummary | null;   // latest scan for this digest (any status)
};
```

Endpoints:

| Method + path | Perm | Body / query | Response |
|---|---|---|---|
| `GET /api/projects/{p}/vulns` | project viewer | — | `{ mode: "enforce"\|"report"\|"off", images: VulnImageReport[] }` — every service + addon image of the project; `mode` = instance mode |
| `GET /api/projects/{p}/services/{s}/vulns` | viewer | `?env=` (default production), `?severity=CRITICAL,HIGH` (filter findings), `?includeIgnored=true` (default true) | `{ policy: Policy, effectiveMode: "enforce"\|"report"\|"off", image: VulnImageReport \| null, findings: VulnFinding[] }` |
| `POST /api/projects/{p}/services/{s}/vulns/scan` | editor | `{ env?: string }` | `202 { scanId: number }` — forced rescan of the env's live image |
| `GET /api/projects/{p}/services/{s}/vulns/policy` | viewer | — | `{ mode: "inherit"\|"enforce"\|"report"\|"off", effectiveMode: … }` |
| `PUT /api/projects/{p}/services/{s}/vulns/policy` | editor | `{ mode }` | same as GET |
| `GET /api/projects/{p}/services/{s}/vulns/ignores` | viewer | — | `VulnIgnore[]` |
| `POST /api/projects/{p}/services/{s}/vulns/ignores` | editor | `{ vulnId, reason, expiresInDays?: number }` | `201 VulnIgnore` |
| `DELETE /api/projects/{p}/services/{s}/vulns/ignores/{id}` | editor | — | `204` |
| `GET /api/projects/{p}/addons/{a}/vulns` | viewer (addon via `GetOwned`) | same query as service | `{ image: VulnImageReport \| null, findings: VulnFinding[] }` |
| `POST /api/projects/{p}/addons/{a}/vulns/scan` | editor | — | `202 { scanId }` |
| `GET/POST /api/projects/{p}/addons/{a}/vulns/ignores`, `DELETE …/ignores/{id}` | viewer / editor | as service | as service |
| `GET /api/admin/vulns/platform` | admin | — | `{ images: VulnImageReport[] }` |
| `GET /api/admin/vulns/platform/findings` | admin | `?digest=` required | `{ image: VulnImageReport, findings: VulnFinding[] }` |
| `POST /api/admin/vulns/scan-all` | admin | — | `202 { queued: number }` — runs the nightly sweep now |
| `GET /api/admin/vulns/scans/{id}` | admin | — | `VulnScanSummary` |
| `GET /api/admin/settings/vulnscan` | admin | — | Settings object above |
| `PUT /api/admin/settings/vulnscan` | admin | Settings object (fields left out keep their current value) | Settings object |
| `POST /api/projects/{p}/addons/{a}/vulns/update-image` | editor | — | `AddonImageUpdateState`; 409 when already on the newest build |

Errors: 400 invalid input (unknown mode, bad vulnId, expiresInDays out of range), 404 unknown project/service/addon/scan, 409 nothing to scan (no live image). Message passed through as `{ "error": "…" }`.

Build summaries (`GET …/builds`) keep their shape; a blocked build is `status: "failed"` with the block message in the existing message/reason field, and the existing `promoteHold` field carries `scanning image for vulnerabilities…` while gated.

## CLI _(contract)_

New command group `kuso vulns` (alias `vuln`):

```
kuso vulns list <project>                                   # per image: owner, image, status, C/H/M/L, fixable C/H, scanned
kuso vulns show <project> <service> [--env E] [--severity CRITICAL,HIGH] [--fixable] [--hide-ignored]
kuso vulns show <project> --addon <addon> [...]
kuso vulns scan <project> <service> [--env E] [--wait]      # --wait polls until done, then prints show
kuso vulns scan <project> --addon <addon> [--wait]
kuso vulns policy <project> <service> [inherit|enforce|report|off]   # no arg = print
kuso vulns ignore add <project> <service|--addon a> <VULN-ID> --reason "..." [--expires 30d]
kuso vulns ignore list <project> <service|--addon a>
kuso vulns ignore rm <project> <service|--addon a> <id> [--yes]
kuso vulns platform [--digest D]                            # admin
kuso vulns scan-all                                         # admin
kuso vulns settings get | set [--mode M] [--scan-addons bool] [--scan-platform bool] [--nightly-hour N] [--notify-min-severity S] [--auto-update-addons bool]   # admin
kuso vulns update-image <project> <addon> [--yes]            # move an addon onto its tag's newest build now (restarts it)
```

`-o json` emits the raw API JSON. Each command uses its own output-format flag var (never a shared global).

## Web _(contract)_

- Service overlay: new tab **Vulnerabilities** (`tab=vulnerabilities`): effective mode + policy selector (editor), latest scan summary (counts, scanned at, DB date, status/error), "Rescan" button, findings table (severity badge, vuln ID linking `primaryURL`, package, installed → fixed, title) with filters (severity, fixable only, show ignored), per-row "Ignore…" dialog (reason required, expiry), ignore list with remove.
- Addon overlay: a Vulnerabilities tab with the same findings table, rescan, ignores (no policy selector), and an **Image updates** panel: auto-update on/off, the updater's status for this addon, and "Update now" (confirm dialog).
- Settings: new admin page **Vulnerability scanning** — instance settings form + platform images table (expand row → findings) + "Scan everything now".
- Project overview/canvas: a small badge on a service/addon node when its live image has fixable criticals.
- Build row: blocked builds already render as failed with the message; no extra UI required.
