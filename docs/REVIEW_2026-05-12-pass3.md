# kuso review — Pass 3 (2026-05-12, post-cleanup state)

Four fresh review agents (architecture, security, correctness, UX) audited `main` @ v0.9.79 after the Phase 4 cleanup. Findings filed in:

- `REVIEW_2026-05-12-pass3-architecture.md` — 21 findings (4 P0, 9 P1, 8 P2)
- `REVIEW_2026-05-12-pass3-security.md` — 11 findings (3 P0, 4 P1, 4 P2)
- `REVIEW_2026-05-12-pass3-correctness.md` — 20 findings
- `REVIEW_2026-05-12-pass3-ux.md` — 20 findings (2 P0, 8 P1, 10 P2)

**Total: 72 findings. ~9 P0 ship-blockers.**

---

## P0 ship-blockers (must fix before v1.0)

Ranked by impact × likelihood:

| # | ID | Area | What breaks |
|---|----|------|-------------|
| 1 | **Sec P0-1** | `handlers/updater.go:73` | **Any authenticated user can trigger a cluster upgrade.** Missing `requireAdmin` on `POST /api/system/update`. Viewer → effective cluster-admin in one POST. |
| 2 | **Sec P0-2** | `installscripts/scripts/install.sh:599` + empty `updater/releasekey.pub` | **Fresh installs default to no signature verification.** `KUSO_REQUIRE_SIGNATURES=false` baked into the secret on every install + empty pubkey = MITM on the GitHub Releases API serves arbitrary update payloads with SA-level kube perms. |
| 3 | **Sec P0-3** | `deploy/buildkitd.yaml:116` | **BuildKit privileged + label-only NetworkPolicy.** Any pod that can self-apply the label submits builds to a privileged daemon → node escape. |
| 4 | **Correct F-01** | `db/notification_events.go` | **Bell-feed rows vanish under storms.** INSERT + cap-prune DELETE not transactional. Concurrent Emits interleave so G2's prune removes G1's just-inserted row. |
| 5 | **Correct F-03** | `projects/services_deltas.go` | **`RemoveDomain` / `SetEnvVar` / `UnsetEnvVar` silently lose writes** under helm-operator 409. AddDomain uses retry; the other three don't. Most likely cause of "I deleted it but it came back". |
| 6 | **UX P0** | `web/src/app/(app)/projects/[project]/view.tsx` | **cmd-K "Tail logs · X" never opens the Logs tab.** `?tab=logs` param dropped — every deep-link lands on Deployments. Same for bell-icon notifications. 3-line fix. |
| 7 | **UX P0** | `components/addon/AddonOverlay.tsx` | **Addon overlay silently discards unsaved edits.** Never adopted `OverlayDirtyContext`; switching between Configuration / Backups / Resync wipes typed values, no SaveBar. |
| 8 | **Arch A-01 / E-01** | `api/apiv1/` | **apiv1 covers Project only.** Service, addon, env-var, env-group, build, secrets DTOs are hand-rolled twice (server + CLI) — three times if you count MCP. The exact drift class apiv1 was created to kill is alive in 9+ shapes. |
| 9 | **Arch B-01** | `handlers/import_coolify.go` | **270-line `applyCommit` is domain logic in a handler.** Same anti-pattern flagged for nodes last review; shipped fresh with Coolify import. Locks in before a second importer (Heroku, Render) arrives. |
| 10 | **Arch D-01** | `builds/builds.go:733-810` + helm-operator | **The helm-operator-renders-KusoBuild seam is paper-thin.** Cancel has to mutate the CR + delete the Job + delete helm-release Secrets to stop respawn. Documented 2026-05-05 outage. Coolify import bursts 50-500 builds — exactly where this compounds. |

---

## Cross-cutting themes

### The "half-done refactor" pattern is back — at a higher tier

Last review flagged this against the cleanup work itself; this review finds the *current* generation of features shipped with the same shape:

- **apiv1** still only covers Project (was the most-bemoaned half-finish; not addressed in Phase 4).
- **Coolify import** shipped fresh with all the symptoms: domain logic in handler (B-01), 270-line orchestration, mapping duplicated between handler + CLI with subtle behaviour drift (A-04 — slugifyName clamps to 50 chars in CLI, 63 in server; runtimeForBuildPack returns "" vs "dockerfile" for unknown; parseFirstPort accepts "3000:3000" server-side only).
- **SaveBar** unification skipped addons (UX P0).
- **Comment sweep** missed three more stale `invalidateDescribe` / `propagate*` references.

### Tenancy enforcement is by convention, not by structure

152 `requireProjectAccess`/`requireAdmin`/`requirePerm` calls across 29 handler files. Sec P0-1 (the updater bypass) is *exactly* the failure mode this predicts — one missing check, one cluster-admin escalation. Without a route-level enforcement pattern + a linter, the next added route is a tenancy bypass waiting to happen.

### Two CRD lifecycles, one of them load-bearing the wrong way

Helm-operator was the right tool for KusoService / KusoEnvironment / KusoAddon (slow-changing, helm-chart-rendered, declarative). It's the wrong tool for KusoBuild (transient, fast-cycling, needs precise lifecycle control). Three commits across this year have papered over the seam (chart no-op gate, Cancel-time tag blanking, helm-release Secret delete). Coolify import commit now bursts 50-500 KusoBuilds — the case that exposes the per-CR render cost. D-01 (~3-day Go controller) is the strongest cost/value fix in the architecture pass.

### Container/host trust boundary is weaker than the threat model implies

P0-3 (BuildKit privileged + label-only gating) + P1-1 (unquoted `repo.path` in build Jobs) is a chained two-hop path from "Deployer-role user pushes a malicious commit" to "host filesystem access on the build node." Single-tenant ≠ no privilege boundary; a Deployer is supposed to be one rung below an Owner.

---

## Recommended phasing

**Phase 5a — Security closeout (P0s only, ~1 day):**
- Sec P0-1 → 5-line `requireAdmin` on the updater handler.
- Sec P0-2 → ship a real `releasekey.pub`, flip install default to `true`.
- Sec P0-3 → BuildKit mTLS (heavier; minimum: tighten the NetworkPolicy to a SA-token gate + IP allow-list).
- Sec P1-1 → `repo.path | quote` in the helm chart.
- Sec P1-2 → `peerIsTrustedProxy` check before trusting `X-Forwarded-Host` in invite generation.

**Phase 5b — Correctness P0s (~half day):**
- Correct F-01 → wrap bell-feed INSERT + prune in one transaction.
- Correct F-03 → migrate all delta-ops to `UpdateKusoServiceWithRetry`.
- Correct F-17 → conditional `https://github.com/` prefix in Coolify import (already-URL vs owner/repo).

**Phase 5c — UX P0s (~half day):**
- Wire `?tab=` param into the project view.
- Build addon `OverlayDirtyContext`.

**Phase 5d — Architecture P0s (~1 week if all):**
- apiv1 fill-out (1 day mechanical).
- `migration` service extraction from coolify handler (1 day).
- KusoBuild Go controller (3 days; highest value, biggest commit).

The security and correctness P0s are ~1.5 days end-to-end; the architecture P0s are a v0.10 release theme. Recommendation: 5a + 5b + 5c as the next milestone, then plan 5d as v0.10's headline.
