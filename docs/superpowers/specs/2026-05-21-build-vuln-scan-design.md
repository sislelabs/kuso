# Build-Time Vulnerability Scanning — Design

**Date:** 2026-05-21
**Status:** Draft — for review before implementation

## Problem

kuso builds an image and deploys it with no security check on what's
inside. A base image with a known-critical CVE, or a dependency with a
published RCE, ships straight to production silently. Both Coolify
(v4.1) and Railway (Dec 2025) added build-time dependency/image
scanning and treat a critical finding as a build signal.

This adds **vulnerability scanning to the build pipeline**: after an
image is built and pushed, scan it, record the findings on the build,
surface them in the UI, and — optionally — **fail the build** on
findings above a configured severity.

## Goals

- Scan every build's output image for OS-package + language-dependency
  CVEs.
- Record a findings summary (counts by severity + the worst CVEs) on
  the `KusoBuild` so the UI can show it.
- A per-project policy: `off` (scan, report, never block) /
  `warn` (default — scan, report, annotate) / `block` (fail the build
  when findings ≥ a threshold severity).
- Zero impact when disabled — scanning is a step that can be skipped.

## Non-goals

- Runtime / admission-time scanning. Build-time only.
- Scanning addon images (postgres, redis…) — those are pinned
  upstream images kuso controls; out of scope.
- A vulnerability *management* workflow (waivers, per-CVE acceptance,
  ticket integration). v1 reports + optionally blocks; richer triage
  is a later feature. A simple allowlist (ignore specific CVE IDs)
  *is* in scope because without it one unfixable base-image CVE would
  wedge `block` mode permanently.

## Approach — Trivy as a build-Job step

The build is already a Kubernetes Job (`kusobuild` chart, `job.yaml`)
that ends by pushing an image. We add a **final scan container** to
that Job that runs **Trivy** against the just-pushed image.

Trivy is the right tool: single static binary, scans OS packages +
language dependencies (npm, pip, go modules, …) + the image config,
ships its own vuln DB, JSON output, and is the de-facto standard.
Alternatives (Grype, Docker Scout) are comparable but Trivy's
self-contained binary + JSON output fit the Job model best.

### 1. Build chart — the scan step

`kusobuild` chart `job.yaml`: when `scan.enabled`, append a container
that runs **after** the image push succeeds (a Job with ordered
containers — the scan container waits on the push container's
completion marker, same coordination the chart already uses between
build phases). It runs:

```
trivy image --format json --severity HIGH,CRITICAL \
  --ignore-unfixed --output /shared/scan.json <pushed-image-ref>
```

then a small wrapper writes a compact summary to a location the
build poller reads (see step 3). `--ignore-unfixed` keeps the signal
actionable — a CVE with no available fix is noise in `block` mode.

The Trivy vuln DB is pulled at scan time (cached on the node across
builds via the existing build-cache PVC mount, so it is not
re-downloaded every build).

### 2. CRD — scan policy + result

No CRD *schema* change is strictly required — `KusoBuild.Status` is a
free-form `map[string]any`, so the scan result lands there. We do add:

- `KusoService.spec.scan` (and a project-level default
  `KusoProject.spec.scan`): `{ policy: off|warn|block, threshold:
  high|critical, ignoreCVEs: []string }`. Service value overrides
  project value; project value overrides the instance default
  (`warn`).
- `KusoBuild.spec.scan` — the resolved policy, stamped onto the build
  CR at trigger time (so a build's behaviour is fixed at trigger,
  not subject to a mid-flight policy edit).
- `KusoBuild.status.scan` — `{ critical: N, high: N, fixable: N,
  topCVEs: [{id, severity, pkg, fixedVersion}], ranAt }`.

### 3. Build poller — consume the result + enforce the policy

The build poller (which already watches build Jobs to completion and
promotes the image) gains a step: read `scan.json`, compute the
summary, write it to `KusoBuild.status.scan`.

Policy enforcement:
- `off` / `warn` → the image is promoted regardless; `warn` emits a
  `build.vulnerabilities` notification when findings exist.
- `block` → if findings at/above `threshold` severity exist (after
  removing `ignoreCVEs`), the build is marked **failed** with phase
  reason `vulnerabilities`, the image is **not promoted**, and a
  `build.blocked_vulnerabilities` notification fires. The failure
  message lists the offending CVEs.

A scan that itself errors (Trivy crash, DB pull failure) is treated as
`warn`-equivalent regardless of policy — a broken scanner must not
block deploys; the failure is logged + surfaces as a degraded scan
status, never a silent pass *or* a false block.

### 4. UI

- Build detail / the Deployments tab build row: a scan badge —
  green check (clean), amber (findings, warn), red (blocked). Expands
  to the `topCVEs` list with package + fixed-version.
- Service → Settings: a "Vulnerability scanning" section — policy
  select (off/warn/block), threshold, and the CVE-ignore list. The
  blast-radius dialog notes that switching to `block` can fail future
  builds.
- Project → Settings: the project-level default.

### 5. CLI

`kuso build list` output gains a scan column (`✓` / `3H 1C` / `blocked`).
`kuso build scan <project> <service> <build>` prints the full findings.

## Blast radius / risks

- **`block` mode can wedge deploys.** A base image with an unfixable
  critical CVE would fail every build forever. Mitigations: default
  is `warn` (never blocks); `--ignore-unfixed` drops no-fix CVEs;
  the `ignoreCVEs` allowlist gives an explicit escape hatch. `block`
  is opt-in per service.
- **Build latency.** Trivy adds ~10–40s to a build (DB pull is
  cached; the scan itself is fast). Acceptable — it runs after the
  push, so the image exists; on `warn` the deploy could even proceed
  in parallel, but v1 keeps it simple and sequential.
- **Scanner failure must fail-open.** Explicitly: a Trivy error never
  blocks a deploy and never reports a false "clean". It degrades to
  `warn` + a visible "scan unavailable" status.
- **DB freshness.** Trivy's vuln DB is pulled per scan (cached). A
  very stale cache could miss a new CVE — acceptable; the cache TTL
  is short and the DB is small.
- **No CRD schema migration** for the result (free-form Status), but
  the new `spec.scan` blocks on `KusoService`/`KusoProject` *are* a
  CRD change → `kubectl apply` needed.

## Decisions to confirm

1. **Trivy** as the scanner — yes; self-contained, JSON, standard.
2. **Default policy `warn`** — scan + report, never block, until a
   user opts into `block`. Safe default.
3. **`--ignore-unfixed`** — yes; a CVE with no fix is not actionable
   and would only generate noise / false blocks.
4. **Scanner failure fails open** (degrades to warn) — yes; a broken
   scanner must never wedge deploys.
5. **Scan runs after push, sequential** — yes for v1; parallel
   scan-while-deploy is a later optimisation.

## Rollout

- CRD change (`KusoService.spec.scan`, `KusoProject.spec.scan`,
  `KusoBuild.spec.scan`) → `kubectl apply` the updated CRDs.
- `kusobuild` chart gains the scan container → chart change.
- Build poller reads + enforces → server change.
- Ship via `make ship`.
