# UX deep-dive — top-5 fix bundle

**Status:** approved 2026-05-23
**Branch:** `ux-deepdive-fixes`
**Persona target:** regular dev who isn't into infra; preserve devops parity

## Background

Four parallel UX audits of the web UI surfaced a converging list of friction points concentrated in three flows:

1. Diagnosing a failed deploy
2. Rolling back a healthy service to a prior image
3. Discovering the `${{ ref }}` env-var syntax

Plus two smaller polishes: cleaning up the 9-tab service overlay, and a one-shot first-deploy tour.

The data plumbing already exists for most of the fixes — `DetectedEnvBanner` (EnvVarsEditor.tsx:946-1041) already classifies env-var crashes; `DiffConfirmDialog` (DiffConfirmDialog.tsx) + `blast-radius.ts` already gate dangerous mutations; tab routing in `ServiceOverlay.tsx:70-88` already accepts a tab param. The work is mostly routing existing capability to the right place at the right time.

## Out of scope

- Vibecoder-tier UX (runtime descriptions, docs-link tooltips on every jargon term). Persona is "regular dev", not "no infra experience at all."
- Bell-popover filtering (project filter, severity filter) — separate ticket.
- Non-admin notification self-service — separate ticket.
- Settings hierarchy recency / locked-card affordances — separate ticket.
- Status-color token sweep across the whole UI — opportunistic fixes only inside touched files.

## Three shippable units

Each unit is one commit, pushed independently. Release happens at the end after all three are on the branch.

---

### Unit A — Failure routing & classifier

**Goal:** Notification ping → one click → land in the right overlay tab with the failure line and a human-language summary.

#### A.1 New Go package `server-go/internal/failures/`

- `classifier.go` — `Classify(logs []string, podStatus *kube.PodStatus, build *kube.Build) Classification`
- `kinds.go` — enum:
  - `missing_env` — log regex `(?i)(missing.*env|undefined.*env|KeyError.*env)`, or `CreateContainerConfigError`
  - `oom` — exit code 137 or `OOMKilled` last-state reason
  - `crash_loop` — `CrashLoopBackOff`
  - `image_pull_failed` — `ErrImagePull` / `ImagePullBackOff`
  - `port_conflict` — log regex `Address already in use|EADDRINUSE`
  - `healthcheck_failed` — readiness probe failures over threshold from pod conditions
  - `build_command_failed` — build pod non-zero exit with a parseable failing command line
  - `generic` — fallback
- `tabhint.go` — map kind → overlay tab (`variables` for missing_env; `settings` for image_pull_failed; `logs` for the rest)
- `linehint.go` — best-effort line snippet for highlighting

Table-driven tests under `failures/testdata/`; target ≥ 80% coverage per detector.

#### A.2 Notification payload extension

`internal/notify/notify.go` — extend `Event`:

```go
type Classification struct {
    Kind     string `json:"kind"`
    Tab      string `json:"tab"`
    Summary  string `json:"summary"`
    LineHint string `json:"line_hint,omitempty"`
    LineNum  *int   `json:"line_num,omitempty"`
}
type Event struct {
    // existing fields…
    Classification *Classification `json:"classification,omitempty"`
}
```

Persisted as JSONB column on `NotificationEvent`. Migration goes in the existing migrations pipeline.

#### A.3 Call-site wiring

- `internal/builds/` — when a build pod terminates non-zero, classify build logs
- `internal/nodewatch/` (or whoever watches pod transitions) — classify on `CrashLoopBackOff` / `OOMKilled` / `ImagePullBackOff` transitions
- Both pass result into `notify.Dispatcher` so it lands on every channel + the bell feed

#### A.4 Web — bell popover row

`web/src/components/layout/TopNav.tsx` (bell popover 440-586, status colors at 376-377):

- Row links to `/projects/{project}?service={service}&tab={classification.tab}&highlight={line_num}&kind={kind}`
- Subtitle line shows `classification.summary` (e.g. *"Missing env var: DATABASE_URL"*)
- Status dot uses `--error|--warning|--building` CSS vars (not hardcoded `bg-amber-400`/`bg-red-400` — fixes audit design-drift finding opportunistically)

#### A.5 Web — `ServiceOverlay` deep-link handling

`web/src/components/service/ServiceOverlay.tsx`:

- New URL params: `tab`, `highlight`, `kind`
- `?tab=variables&kind=missing_env` → opens Variables tab + renders `<FailureBanner>` at top
- `?tab=logs&highlight=<n>` → LogStream scrolls to line `n`, highlights for 4s, then fades

#### A.6 New component `FailureBanner`

`web/src/components/service/overlay/FailureBanner.tsx`:
- Props: `{ kind, summary, lineHint?, dismissable: true }`
- Per-kind copy (see Section A above), styled with `--error` accent
- Dismiss state keyed by event id (re-opens for newer events)

#### A.7 Coexistence

`DetectedEnvBanner` (EnvVarsEditor.tsx:946-1041) is suppressed when `FailureBanner` of kind `missing_env` is showing, to avoid duplicate "missing env" UI.

#### A.8 LogStream improvements (in-scope because we're already here)

`web/src/components/logs/LogStream.tsx`:
- Severity filter toggle: `all | stderr | error`
- Word-wrap defaults to `true` on viewports < 768px
- Status colors swap to `--building` / `--error` CSS vars (lines 58-62)

---

### Unit B — Generic rollback + tab cleanup

#### B.1 Rollback

`web/src/components/service/overlay/ServiceDeploymentsPanel.tsx`:

- Every successful build row gains a `Promote` button (right-aligned, `secondary` variant)
- Button disabled if the build's image tag matches the currently-deployed tag
- Click → builds patch `{ image: <selected-build-image> }` → routes through existing `DiffConfirmDialog`
- `blast-radius.ts` — confirm `image` field already has a `warn` level entry; if not, add it with copy *"Rolls the deployment to this image. Current pod stays up until the new one is Ready."*

Gated by `Services:Write` (existing perm); no new permission added.

#### B.2 Tab cleanup

`ServiceOverlay.tsx` (tab list at 70-88):

- `Crons` tab — hide when `service.cron_jobs.length === 0`
- `Runs` tab — hide when `service.runs.length === 0`
- `Shell` stays always (it's an action, not data)
- An "Advanced ▾" disclosure replaces the hidden tabs when at least one is hidden, expanding to show them on demand (in case the user wants to add the first cron)

---

### Unit C — Ref-picker discoverability + first-deploy tour

#### C.1 Paste-detection swap

`web/src/components/service/EnvVarsEditor.tsx`:

- On value paste/change, compare against a synthesized map of `{ literal_value → ref_form }` built from project's addon `*-conn` secret values + sibling service URLs
- If match: render inline chip below the value input: *"This matches `postgres.DATABASE_URL`. Use `${{ postgres.DATABASE_URL }}` instead? [Swap]"*
- Match comparison is local (no network — the addon `*-conn` secret values are already fetched for the existing reference picker)
- Chip auto-dismisses after the user edits the value again

#### C.2 Type-ahead trigger

`EnvVarsEditor.tsx` value input:

- On `${{` keystroke, auto-open the existing `ReferencePicker` dropdown anchored to the input
- ESC closes the picker without inserting; arrow keys + Enter pick

#### C.3 First-deploy coachmark

`web/src/components/service/ServiceOverlay.tsx`:

- New component `<FirstDeployCoachmark>` overlays the open ServiceOverlay
- Triggered when:
  - Per-user `localStorage.kuso_tour_seen_first_deploy !== '1'`
  - **AND** ServiceOverlay opened on a service whose first successful deploy completed within the last 60s
- 4 numbered bubbles pointing at: Variables tab · Logs tab · `${{ }}` icon button · Promote button on Deployments
- Each bubble has 1-2 sentence copy + `Got it` / `Skip` actions
- Skip or finish sets `localStorage.kuso_tour_seen_first_deploy = '1'`

---

## Test plan

- **Unit A — Go:** table-driven classifier tests per kind; integration test wiring fixture pod → notify dispatcher; verify Event.Classification populated.
- **Unit A — Web:** component tests for `FailureBanner` per kind; URL-param routing test on `ServiceOverlay`; `LogStream` severity filter test.
- **Unit B:** Promote button enable/disable test; rollback patch builds + DiffConfirmDialog opens; tab visibility tests for empty/non-empty crons/runs arrays.
- **Unit C:** paste-detection match against synthesized literal map; `${{` type-ahead opens picker; coachmark only shows on fresh first deploy within window; localStorage gate respected on second visit.

## Release

Per user direction: no backwards compatibility carve-outs. Each unit is one commit, pushed to `ux-deepdive-fixes` independently. After all three land, cut a release (separate task). Old web/server pairings during the rolling window are not a concern.
