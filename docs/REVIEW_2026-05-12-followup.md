# kuso — Follow-up Review (2026-05-12, post-30-commits)

Re-audit after the first review's 30 commits landed. Same four lenses (architecture / security / correctness / UX). The picture: most P0 ship-blockers from the original audit are genuinely closed, but **roughly half of the prior fixes shipped only the skeleton of the recommended change** — new abstractions exist alongside the old ones, dead code accumulates, and a few "fixes" made UX worse than the pre-fix state.

The codebase is now legitimately **half-modern, half-legacy**, and the legacy half still load-bears.

Plus a few genuine new bugs introduced by the recent work — most notably a race in `SetEnvWithOpts` and an SSRF in the new Coolify importer.

---

## Cross-cutting top-10 (do these first)

Ordered by ratio of (impact × likelihood) to (cost of fix).

1. **B2 — `SetEnvWithOpts` missing per-service mutex** *(P1 correctness)* — concurrent UI bulk-save + settings PATCH lose updates and propagate stale env vars to running pods. One-line fix at `projects/services_ops.go:822`.
2. **P1-2 — Empty `releasekey.pub` makes signature verification theatre** *(P1 security)* — `KUSO_REQUIRE_SIGNATURES=true` doesn't help because the `ErrUnsignedNoKey` branch bypasses it entirely. The auto-updater accepts any unsigned release.
3. **P1-1 — Coolify import SSRF** *(P1 security)* — `POST /api/import/coolify/preview` doesn't block RFC1918; admin-only is the only gate. An admin (or anyone who escalates) can hit `http://10.96.0.1` (kube apiserver), AWS IMDS, etc. and exfiltrate via the 502 response body.
4. **U-P0-A — `/welcome` Step 3 in stepper but never renders** *(P0 UX)* — the third dot says "Deploy" and is permanently grey. Either build it or drop it.
5. **U-P0-D — Unified SaveBar is *more* inconsistent now than before** *(P0 UX)* — only `ServiceSettingsPanel` migrated. Variables tab still has its inline button; addon overlay has neither pattern. Five different save UXes across the app.
6. **U-P0-C — `/welcome` redirect loop trap** *(P0 UX)* — a user who skips lands back at `/projects` and gets bounced to `/welcome` again. No sessionStorage memo.
7. **U-P0-B — Coolify wizard ends in a "use the CLI" wall** *(P0 UX)* — preview-only with a footer banner telling users to drop to the CLI. Worse than the pre-fix "no UI" because it raises the expectation cliff.
8. **P1-1 (arch) — `propagateChangedToEnvs` shipped but 7 obsolete per-field propagators still in the file** *(P1 architecture)* — 225 lines of dead code, plus comments throughout still reference the old names. Anyone reading the file sees seven helpers, no callers, and has to reconstruct the contract.
9. **P0-1 (arch) — `coolify/` is byte-duplicated between server and CLI** *(P0 architecture)* — the classifier verdicts (the contract between preview and commit) will drift. Should be a third shared module like `api/apiv1`.
10. **P0-3 (arch) — Schema-drift hard-exit during boot kills readyz contract** *(P0 architecture)* — `os.Exit(3)` instead of `readyz` failure means CrashLoopBackoff with no UI signal during a botched update.

---

## Severity counts

| Tier | Architecture | Security | Correctness | UX  | Total |
|------|-------------:|---------:|------------:|----:|------:|
| P0   |            3 |        0 |           1 |   4 | **8**  |
| P1   |            7 |        4 |           4 |   9 | **24** |
| P2   |            5 |        4 |           3 |  10 | **22** |

54 findings total. Most are smaller than the originals; several reclassify items that landed only halfway.

---

## 1. Architecture follow-up

### P0

#### A-P0-1. `coolify/` package is byte-duplicated between server and CLI
**Where:** `server-go/internal/coolify/{api,classify,client,inventory,types}.go` vs `cli/pkg/coolify/{...}` — five files in each tree, byte-identical.

This was created to ship `/api/import/coolify/preview` without giving the CLI a server dependency, but it instantly violates the "one source of truth for wire shape" principle that `api/apiv1` was created to enforce.

**Failure mode:** the classifier verdicts (`migrate`/`flag`/`skip`) and the kuso-shape mapping are the contract between server preview and CLI commit. The moment the server learns a new Coolify field, the CLI's `classify.go` disagrees and the preview UI lies about what `kuso migrate coolify` will do.

**Direction:** Move to `coolify/` as a third no-deps Go module under `go.work` (same pattern as `api/apiv1`). Both consumers depend via `replace`. The web UI's wire types in `settings/import/page.tsx:27-50` are also hand-rolled — wire with codegen against the canonical types.

#### A-P0-2. Coolify import is preview-only; UI tells users to drop to CLI
**Where:** `web/src/app/(app)/settings/import/page.tsx:199-205`, `import_coolify.go:22-27`.

Already covered under UX (U-P0-B). Architecturally: the missing `POST /api/import/coolify/commit` endpoint blocks the wizard from being a real product. The CLI already has the logic in `cli/cmd/kusoCli/migrate.go`; once A-P0-1 lands the server can call it directly.

Plus: `import_coolify.go:94-97` has a `context.WithTimeout` immediately discarded (`_ = ctx`). The Snapshot can hang the request for as long as Coolify takes to respond. (See B4 in correctness.)

#### A-P0-3. CRD schema-drift hard-fail bypasses readyz contract
**Where:** `cmd/kuso-server/main.go:308-332`, `internal/http/probes.go:40-90`.

The new `CheckSchemas` is good engineering. But the failure mode is `os.Exit(3)` *during boot* before `readyz` is mounted. The previous review's A-P1-6 explicitly recommended "readyz fails on mismatch with a loud message" — what shipped is "CrashLoopBackoff with a log line."

**Why this matters:** a CrashLoopBackoff pod is invisible to the Service endpoint, so the LB shifts traffic to the still-running pod with the *old* image. The new server never gets to log what stale field tripped it. The install/updater self-roll can leave the cluster wedged with no UI to surface why — the user sees "kuso went away after I clicked update."

**Direction:** Start the server, mark `readyz` unready with `checks.crd: "stale: spec.foo,spec.bar"`, refuse all `/api/*` writes via middleware. The SPA still loads, the user sees a banner explaining "operator: kubectl apply -f operator/config/crd/bases/", and the LB happily fails over per the normal readiness contract.

Also: `extractExpectedFields` deliberately doesn't recurse (`schema_check.go:177`). Correct for false-positive reasons, but means sub-field removal (e.g., `spec.placement.labels` dropped) won't be caught. The v0.7.x bug was at a sub-field. Worth a follow-up: opt-in deep walk for known-deep paths.

### P1

- **A-P1-1.** `propagateChangedToEnvs` chokepoint shipped, 7 obsolete per-field propagators still in `services_ops.go:1502-1726` with zero callers. ~225 lines of dead code. Comments in `drift.go:182-183` and `services_ops.go:309, 897, 1530, 1556` still reference the old propagators by name as if they're the canonical mechanism. **Delete the seven helpers; rewrite the stale comments.**
- **A-P1-2.** `invalidateDescribe` no-op shim called at 14 sites. Documented as "stays as a no-op so call sites compile; deleted once swept" — hasn't been swept. **`sed`-out the calls in one commit, delete the function.**
- **A-P1-3.** `ProjectAPI` / `ServiceAPI` / `EnvironmentAPI` facades exist in `projects/facades.go` with **zero consumers**. Every handler holds `*projects.Service` directly. Dead architecture pretending to be migration progress. **Either wire them in (`handlers/projects.go` takes `*ProjectAPI` not `*Service`) or delete the file.**
- **A-P1-4.** `projects.CreateProjectRequest` and `apiv1.CreateProjectRequest` are parallel types on the server side. CLI now type-aliases through `apiv1`, but server handlers still decode into the local `projects.CreateProjectRequest` (different field names: `CreateProjectRepoSpec` vs `RepoRef`). JSON tags happen to align today; a single rename de-syncs them. **Server handlers should decode into `apiv1.CreateProjectRequest`.** ~1h mechanical refactor; until done, `apiv1` is decoration.
- **A-P1-5.** Schema-check (P0-3 above) changes the human procedure for releases — CRD additions need a separate ssh step *before* image rollout, not after. **Document in `hack/release.sh` and `docs/SCHEMA_MIGRATION.md`.**
- **A-P1-6.** `nodes` package is type-safe but stops at one transformation. Handler still owns metrics-server probe, pod aggregation, node-edit endpoints. **Finish the migration into `nodes.Service.List(ctx)` or rename the package to `nodeshape` to honestly signal its scope.**
- **A-P1-7.** KusoBuild → Go controller is now **more urgent**: the recent commits added two things (annotations-as-status race fights, anticipated bulk Coolify import) that pressure this further. **Promote: do this before commit-path of Coolify import ships.**

### P2

- **A-P2-1.** `KusoCron` split — defer indefinitely (no concrete bug pressure).
- **A-P2-2.** `welcome/page.tsx` step 3 unreachable (also U-P0-A).
- **A-P2-3.** `import_coolify.go:85-92` SSRF check duplicates `notify`'s but the *check* is two lines (scheme parse only) — there is no actual RFC1918 block, despite the comment claiming there is. (See P1-1 security.)
- **A-P2-4.** mTLS still critical, zero movement; re-flag.
- **A-P2-5.** Per-service mutex drop — leave it; correctness wins.

---

## 2. Security follow-up

### P0
None new. The previous P0s are genuinely closed.

### P1

#### S-P1-1. Empty `releasekey.pub` makes signature verification theatre
**Where:** `server-go/internal/updater/releasekey.pub` (0 bytes), `internal/updater/updater.go:293-304`.

`resolveReleasePubKey()` returns `""` when both the embed and env var are empty. The `ErrUnsignedNoKey` branch then logs a warning and returns `(nil, nil)` — proceeding with the update — **regardless of `KUSO_REQUIRE_SIGNATURES`**. The setting is only checked in the `else if requireSignatures()` branch, which is never reached when `ErrUnsignedNoKey` fires.

**Attack:** An attacker who compromises the GH releases endpoint (BGP/DNS hijack OR repo compromise) publishes a `release.json` pointing at a malicious image. The updater accepts it.

**Fix:** Either generate the key (`hack/release-keygen.sh`), commit it, and ship — OR change the `ErrUnsignedNoKey` branch to honour `requireSignatures()` consistently.

#### S-P1-2. Coolify import SSRF — RFC1918 not blocked
**Where:** `server-go/internal/http/handlers/import_coolify.go:89-98`.

The handler validates only `http`/`https` scheme. It then passes `req.BaseURL` directly to `coolify.New()`, which uses a stock `http.Client` with no SSRF dialer. **No IP range check, no DNS-resolution-before-dial, no re-dial hardening.**

**Attack:** An admin POSTs `{"baseUrl":"http://10.96.0.1","token":"x"}` to `/api/import/coolify/preview`. The kuso server dials the kube apiserver's ClusterIP, receives the 401/403 body, and surfaces it in `"couldn't reach Coolify: ..."`. With a valid SA token as the "Coolify token", arbitrary kube API resources read. `http://169.254.169.254/latest/meta-data/` exfiltrates cloud metadata creds.

Admin-only limits blast radius, but admins should still not pivot from kuso's SA to the kube API via SSRF.

**Fix:** Apply the same `ssrfSafeTransport()` the notify dispatcher uses, or expose a `NewWithTransport` constructor and pass `ssrfSafeTransport()` from the handler.

#### S-P1-3. `?env=build:<name>` label selector concat without name validation
**Where:** `server-go/internal/logs/logs.go:201`, also `logs.go:134`, `builds/builds.go:823, 2226`, `projects/drift.go:273, 545`.

`kube.LabelSelector` is wired most places, but six raw string concats bypass it. The `logs.go:201` path is the worst: `buildName` from `?env=build:<name>` is concatenated into `"app.kubernetes.io/instance="+buildName` with no RFC 1123 / DNS-label validation.

**Attack:** Authenticated viewer for project `myproject` sends `?env=build:legit-build,app.kubernetes.io%2Fcomponent%3Dkusobuild`. The selector evaluates as two conditions and returns every active build pod across all projects.

**Fix:** Validate `buildName` against `[a-z0-9-]+` before concat, or route through `kube.LabelSelector(...)`.

#### S-P1-4. (residual) Buildkitd mTLS still absent
NetworkPolicy landed but the TCP port remains unauthenticated gRPC. Residual risk: any pod that can carry `app.kubernetes.io/component=kusobuild` (compromised SA with pod-create rights, or label-mutation path in kuso server itself) bypasses the NetworkPolicy and has full unauthenticated access to a privileged daemon.

### P2

- **S-P2-1.** Coolify import error response (`import_coolify.go:111`) passes `err.Error()` directly, leaking up to 256 bytes of the upstream response body. Compounds the SSRF above.
- **S-P2-2.** `GET /api/github/installations` returns all repos visible to the GitHub App without user-scoping (`github.go:399-429`). In single-tenant: expected. In any multi-org scenario: viewer in project A sees repos from org B.
- **S-P2-3.** `env-detect` image pinned to mutable `v1` tag (`values.yaml:45`). A re-push silently replaces what init containers pull. **Pin to digest or a never-re-pushed tag like `v1.0.0`.**
- **S-P2-4.** `kubernetes.go:143` `MaxBytesReader(nil, ...)` suppresses automatic 413. Callers that handle decode errors as 500 misclassify oversize bodies.

### Verified fixed
- S-P1-3 (persistent rate limiter): wired correctly.
- S-P2-1 (OAuth JWT off the fragment): all three cookie paths use `HttpOnly + SameSite=Lax + Secure`.
- S-P2-2 (notify SSRF): solid; DNS-rebind protection in place.

---

## 3. Correctness follow-up

### P0

#### B1. Context cancel not deferred in `backup.go` cleanup
**Where:** `server-go/internal/http/handlers/backup.go:234`.

```go
cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
if cerr := h.deleteRestoreSecret(cleanupCtx, secretName); cerr != nil { ... }
cancel()  // ← not deferred
```

The `cleanupSecret` closure at line 299 correctly uses `defer cancel()`. This path at 234 doesn't. If `deleteRestoreSecret` panics (nil pointer in kube client), `cancel()` never runs and the timer-backing goroutine leaks until 5s deadline.

**Fix:** `defer cancel()` immediately after the `WithTimeout`.

### P1

#### B2. `SetEnvWithOpts` missing per-service mutex
**Where:** `server-go/internal/projects/services_ops.go:822`.

Performs read-modify-write (`GetService` → mutate → `UpdateKusoService` → `propagateChangedToEnvs`) without holding `s.lockService(project, service)`. Every other delta operation (`PatchService`, `AddDomain`, `SetEnvVar`, `UnsetEnvVar`) does hold it. **Race scenario:** canvas bulk-save races with settings panel PATCH. Whichever kube write wins on resourceVersion, the loser's `propagateChangedToEnvs` then patches env CRs with stale spec.

**Fix:** Add `mu := s.lockService(project, service); defer mu.Unlock()` at the top.

#### B3. `propagateChangedToEnvs` does N+1 kube GET inside per-env loop
**Where:** `server-go/internal/projects/services_ops.go:1461`.

When `Placement` is in `changed`, the function fetches the `KusoProject` CR **once per owned environment**. 20 preview envs (active PR stack) = 20 GETs per PatchService call. On a loaded cluster this pushes handler latency above the 60s UI timeout.

**Fix:** Hoist `GetKusoProject` outside the loop when `changed.Placement` is true. Project spec doesn't change during propagation.

#### B4. Coolify Snapshot ignores handler's 60s context timeout
**Where:** `server-go/internal/http/handlers/import_coolify.go:94-96`.

```go
ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
defer cancel()
_ = ctx // coolify.Client doesn't yet take a context; future refactor
```

The 60s guard cancels nothing. A 500-project Coolify instance issues `1 + N_projects + 1 + 1 + 1` sequential GETs at up to 30s each — over 15 minutes wall-clock. Handler goroutine leaks until either Coolify responds or process restarts.

**Fix:** Thread `ctx` into `coolify.New` or `getRaw`. The plumbing is one parameter.

#### B5. Coolify client unbounded `io.ReadAll`
**Where:** `server-go/internal/coolify/client.go:80`.

```go
body, _ := io.ReadAll(resp.Body)
```

No body cap. A Coolify instance (or a DNS-spoofed server) returning a 2 GB response OOMs the kuso-server pod. Reachable from `/api/import/coolify/preview` by any admin.

**Fix:** Wrap with `io.LimitReader(resp.Body, 32<<20)`. 32 MiB is generous for any realistic Coolify inventory.

### P2

- **B6.** Dead per-field propagators (4 of them) invisible to `go vet`/`staticcheck` because methods aren't flagged unused. Future contributor copies one as a template, re-introduces the O(fields × envs) pattern. **Delete.**
- **B7.** `decodeJSON` passes `nil` to `http.MaxBytesReader` (also S-P2-4).
- **B8.** `ListNotificationEventsForProjects`: no upper bound on `projects` slice length. Single-tenant install caps at tens, but a 10k-arg Postgres query approaches `max_function_args` and degrades query planning.

### Verified fixed
- C-P0-1 (Rollback decode error): fixed.
- C-P0-2 (Provisioner Scan): fixed.
- C-P0-3 (loadSettings DCL): fixed.
- C-P1-1 (logship runCtx init): fixed.
- C-P1-4 (token Secret Update→Create fallback): fixed.
- C-P1-5 (paginated cleanup): fixed.
- C-P2-1 (`%w` wrappings): fixed.
- C-P2-3 (notify.Emit ctx parent): fixed.

---

## 4. UX follow-up

### P0

#### U-P0-A. `/welcome` Step 3 in the stepper but never renders
**Where:** `web/src/app/(app)/welcome/page.tsx:99-104` declares three steps; lines 74-90 only mount Step 1 + Step 2. Step 2's `onPicked` jumps straight to `/projects/<slug>`. **Step 3 is never visited.**

The Stepper draws three dots; the third reads "Deploy" and stays grey forever. Users count three, get bounced after two, see no "first deploy" guidance on the canvas. The file's own comment at lines 33-36 says Step 3 *should* land the user on canvas with a first-deploy banner — never built.

**Fix:** Either drop Step 3 from the rail (1-line change) OR render a `?fromWelcome=1` banner on the project page (~30 lines).

#### U-P0-B. Coolify wizard ends in a "use the CLI" wall
**Where:** `web/src/app/(app)/settings/import/page.tsx:199-205`.

The wizard finishes with a banner reading *"Preview only. The commit step is a follow-up endpoint. For now, run `kuso migrate coolify --token=...`"*. This is worse than the pre-fix "no UI" because it raises the expectation cliff. A migrating user clicks "Import from Coolify" *because they don't want to learn the CLI*. They paste a URL+token, hit Preview, get a beautiful classifier table — and the last row is "now go do the work somewhere else."

**Fix (short term):** Replace the banner with the exact CLI command interpolated with their URL (token redacted to `<your-token>` with a copy button). Better: gate the form behind *"Migration preview (commit lands in v0.10)"* so the framing is honest before they paste a token.

**Fix (real):** Ship `POST /api/import/coolify/commit`.

#### U-P0-C. `/projects` → `/welcome` redirect loop trap
**Where:** `web/src/app/(app)/projects/page.tsx:37-43`.

Fires whenever `data.length === 0 && installations.length === 0 && canCreate`. No sessionStorage memo, no `?fromWelcome` guard. **Flow that breaks:** new user → `/welcome` → clicks "Skip" → Step 2 dead-end → clicks "skip to dashboard" → `/projects` → effect re-fires → `/welcome`. Loop.

**Fix:** `sessionStorage.setItem("kuso.welcome.dismissed", "1")` on skip; read in the `useEffect` guard. 5 lines.

#### U-P0-D. Unified SaveBar is *more* inconsistent now, not less
**Where:** `useOverlayDirty` at `web/src/components/service/ServiceOverlay.tsx:36-47`.

Only `ServiceSettingsPanel.tsx:252` calls it WITH `onSave`. `EnvVarsEditor.tsx:270` registers dirty-only and keeps its inline button. Addon `SettingsTab.tsx:256-274` has its own footer Save bar. `BackupsTab.tsx:420` ditto. `AddonOverlay.tsx` has no `OverlayDirtyContext` at all.

**User sees:** Settings tab shows a floating bottom-center pill. Switching to Variables, the pill *disappears* even when there are unsaved edits — replaced by an inline button on a long form. Addon overlay: no floating pill ever. **Behaviour the user sees: this app has different save UX in five places.** Worse than pre-fix.

**Fix:** Migrate `EnvVarsEditor`, addon `SettingsTab`, addon `BackupsTab` to register `onSave` with `useOverlayDirty`. ~30 lines per panel. The infrastructure already exists.

### P1

- **U-P1-A.** `EnvVarsEditor`'s `useOverlayDirty` registration is dead code (no `onSave`).
- **U-P1-B.** Activity audit page locks out non-admins despite the comment claiming a project-filter fallback. Drop the early-return at `activity/page.tsx:64-71`; the API path already supports project-scoped Viewer access.
- **U-P1-C.** Partial-down red/amber project cards are color-only encoding (a11y). Add icon variants + `title` attrs.
- **U-P1-D.** `/welcome` Step 1 "Skip" leads to dead-end Step 2 ("No GitHub installations yet"). Add second CTA: "start a project without a repo".
- **U-P1-E.** Build logs streaming has no scroll-pause indicator. When user scrolls up mid-stream, autoscroll silently stops with no "↓ jump to live" pill.
- **U-P1-F.** `restoreFormDraft()` is **never called anywhere**. Drafts snapshotted on 401 sit in sessionStorage doing nothing. Wire it into long-form pages.
- **U-P1-G.** Service overlay Logs tab uses 10s polling and labels it "live tail" — the build-side gets real WS, pod logs don't.
- **U-P1-H.** Settings tab has both the unified SaveBar and a stale `saveError` sticky pill that overlaps. Fold error into the SaveBar copy.
- **U-P1-I.** Tab strip `scrollIntoView` fires during render on every parent re-render, not in a tab-keyed `useEffect`. Jittery on narrow viewports as background queries poll.

### P2

10 smaller items (Step 3 stepper, marketing landing flicker, `/projects` empty-state copy, 403 vs 404 disambig, etc.). See the per-finding section above for details.

### Verified fixed (UX)
- U-P0-3 Activity card (admin-only scope is the right issue, but page renders).
- U-P1-2 Pinned Settings tab — works.
- U-P1-3 Addon CTA on empty project — present.
- U-P1-4 Non-admin bell — correctly degrades.
- U-P1-5 Card health colors — present (but a11y, see U-P1-C).
- U-P1-6 Previews toggle — present on `/projects/new:118-126`.
- U-P1-7 Locked card hint — present.
- U-P1-8 Settings cog — present (TopNav + canvas right-click).
- U-P1-9 Logs streaming pill — non-issue (it IS a real WS).
- U-P1-10 Service URL chip — works correctly with stopPropagation defeating the card overlay.

---

## Recurring pattern

Roughly half the prior-review fixes shipped only the *skeleton* of the recommended change. Placement and schema-check landed end-to-end. **Nodes service, facades, apiv1, propagation refactor, SaveBar, Coolify import, /welcome wizard** each shipped the new abstraction without retiring the old one.

The codebase is now half-modern, half-legacy, and the legacy half still load-bears. The cheapest wins are not new features — they're closing out the half-finished work:

1. Delete the dead per-field propagators + the `invalidateDescribe` shim (~30 mins).
2. Wire the apiv1 module into server handlers (~1 hour).
3. Migrate the three SaveBar holdouts to the unified pattern (~1 hour).
4. Either build Step 3 of `/welcome` or drop it from the stepper (~5 mins to drop).
5. Either build the Coolify commit endpoint or relabel the wizard honestly (~30 mins for the relabel).
6. Fix the genuine new bugs (B2 race, B4 context, B5 OOM, P1-1 SSRF, P1-2 empty pubkey).

All together this is ~1-2 focused days. After that, the codebase will be in a much healthier state to take on the genuinely-deferred items (KusoBuild controller, buildkitd mTLS, full KusoCron split).

---

Generated by 4 parallel review agents on 2026-05-12 against `main` @ commit `f8c71f1`. Each finding has file:line refs; drill in for context, scenarios, fix direction.
