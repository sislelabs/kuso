# kuso — Pass 3 UX Review (2026-05-12, post-followup)

Third pass through the web app. Skipped the items closed by the most recent round of fixes (welcome step 3, Coolify commit, redirect-loop guard, EnvVarsEditor SaveBar migration, Activity perm-gate, project-card a11y, welcome step-2 dead-end, log scroll-pause, restoreFormDraft wiring, log "live tail" copy, SaveBar saveError surfacing, tab scrollIntoView).

What's left is the residue: half-finished refactors, narrow leaks, copy gaps, and one full-on broken affordance (cmd-K → "Tail logs" doesn't actually open the Logs tab). 20 findings — most are <30-minute fixes.

The product no longer has any P0-grade ship-blockers on the UX axis. It now has a long tail of papercuts that make it feel like a stack of features instead of a coherent product.

---

## Severity counts

| Tier | Count | Theme |
|------|------:|-------|
| P0   |     2 | Real-broken affordances (cmd-K tab, addon overlay drops edits) |
| P1   |     8 | Honest copy, breadcrumb gaps, duplicate error UX, double save bars |
| P2   |    10 | Smaller polish, mobile, error-leak, discoverability |

20 findings total.

---

## Cross-cutting observations

1. **Three save UXes still ship in parallel.** Service overlay panels register `onSave` with the unified `OverlayDirtyContext` (good). Addon overlay does NOT use `OverlayDirtyContext` at all — each section has its own inline `Save configuration` / `Save placement` / `Resync password` button + its own dirty state. So a user editing the addon's HA toggle, then clicking another section, may walk away from unsaved edits with no global warning. The unified shell from U-P0-D landed for services but the addon overlay never adopted it.

2. **Breadcrumb labels are missing for half the settings pages.** `TopNav.settingsBreadcrumb()` whitelists 7 sections (profile, tokens, notifications, nodes, config, users, groups). Anyone on `/settings/builds`, `/settings/backups`, `/settings/updates`, `/settings/github`, `/settings/import`, `/settings/activity`, `/settings/alerts`, `/settings/roles`, `/settings/instance-secrets`, `/settings/instance-addons` sees just `Settings ›` and no second crumb — losing the "where am I" signal in TopNav.

3. **Error leakage is patchy.** Most surfaces use `err instanceof Error ? err.message : "…"`. That's fine for ApiError (server speaks JSON: `{message:"already exists"}`), but it also lets through low-level fetch errors (`TypeError: Failed to fetch`) and direct Go-string wrappings like `addon: 409: addon kuso-shop/postgres already exists`. Some surfaces aren't wrapped at all (project view's `Failed to load project: <err.message>` leaks the URL + status code).

4. **The Coolify wizard now works end-to-end** (preview → commit landed). But the "where do I go next?" affordance after a successful import is missing — the user gets a green success card and no link to the projects list.

5. **Mobile interstitial protects /settings/* only.** Project detail + service overlay don't trigger the interstitial because of `MobileIncidentView`, but `MobileIncidentView`'s "Logs" button opens the (desktop-shaped) ServiceOverlay anyway. Net: phone users hit the interstitial-free path, then crash into the same crowded overlay they'd have seen at /settings.

---

## P0 — actually broken

### P0-1. cmd-K "Tail logs · <service>" never lands on the Logs tab
**Where:** `web/src/components/command/CommandPalette.tsx:163` builds `?service=<n>&tab=logs`. `web/src/app/(app)/projects/[project]/view.tsx:62-74` reads only `?service=` and `?addon=`. The `tab` param is silently dropped.

**Impact:** Power-user affordance the palette explicitly markets ("logs tail" → 4 keystrokes) lands them on the Deployments tab. Users either give up on the shortcut or reach for the mouse anyway — defeats the cmd-K → "do" path entirely. Bell-icon notification deep-links have the same shape (server populates `?service=X&tab=…` for build events) so this also blunts those.

**Fix sketch:** In `view.tsx`, also read `search.get("tab")` into the `selectedServiceTab` / `selectedAddonTab` state alongside the existing service/addon read. ~3 lines. Verify with: cmd-K → "Tail logs · web" should open the overlay on the Logs tab.

### P0-2. Addon overlay silently discards unsaved edits on tab switch / ESC
**Where:** `web/src/components/addon/AddonOverlay.tsx:42-65`. Setting `tab` and pressing ESC both call `onClose()` / `setTab(next)` directly — no dirty registry. `SettingsTab.tsx` has THREE local-state forms (Configuration, Placement, Resync) that aren't surfaced anywhere outside the section.

**Impact:** User edits HA toggle on the Configuration block, clicks Backups tab to peek at schedule, comes back — Configuration is reset to baseline (`useEffect` at line 133-146 re-baselines on tab remount). The "unsaved" pill in the Configuration footer disappeared the instant they switched tabs. No warning, no SaveBar, no draft restoration. Compared to the service overlay's unified SaveBar this is a step backward.

**Fix sketch:** Mirror the `OverlayDirtyContext` pattern from `ServiceOverlay.tsx` in `AddonOverlay.tsx`. Each addon Settings section calls `useOverlayDirty("settings-config", dirty, { onSave, onDiscard })` (and "settings-placement" / "settings-repair"). The overlay shell renders one SaveBar regardless of which section is dirty. ~40 lines of plumbing; saves real edits. Alternatively, if doing it properly is too big a lift, at least guard `setTab` and ESC with a `window.confirm("Discard unsaved changes?")` when any section reports dirty.

---

## P1 — uncomfortable, discoverable, fixable in <1 hour

### P1-1. Service overlay Settings shows the save error TWICE
**Where:** `ServiceSettingsPanel.tsx:252-257` registers `saveError` with `useOverlayDirty` (so SaveBar shows it inline). Lines 322-328 ALSO render `saveError` as a sticky bottom-16 pill in the panel itself. Net: when a save fails the user sees the error in two pieces of red real estate at the bottom of the screen.

This is the U-P1-H follow-up fix that landed only halfway — the unified SaveBar branch was added but the legacy inline pill was never deleted.

**Fix sketch:** Delete lines 322-328 in `ServiceSettingsPanel.tsx`. The SaveBar already surfaces saveError. ~6 lines deleted.

### P1-2. /settings/<section> breadcrumb missing for half the sections
**Where:** `web/src/components/layout/TopNav.tsx:155-175`. `settingsBreadcrumb()` only labels: profile, tokens, notifications, nodes, config, users, groups.

**Impact:** Going to `/settings/builds` shows only `Settings ›` in the breadcrumb — the user has no indication they're on the Build resources page (until they look at the H1). Disorienting on a deep-link, especially from cmd-K.

**Fix sketch:** Add the missing labels to the `labels` Record literal: `github`, `builds`, `backups`, `updates`, `import`, `activity`, `alerts`, `roles`, `instance-secrets` ("Instance secrets"), `instance-addons` ("Instance addons"). 10-line edit.

### P1-3. Coolify import success has no "what now" — strands the user on the wizard
**Where:** `web/src/app/(app)/settings/import/page.tsx:340-382` (`CommitResultPanel`). Renders a green "Imported N resources" card with project counts and the skipped/error list — and nothing else. No "View imported projects" link, no auto-redirect, no "open the first project" CTA.

**Impact:** The user just finished a multi-minute migration. The natural next click is "let me see my projects on the canvas" — but they have to navigate via the project picker themselves, and there's no breadcrumb back to the dashboard.

**Fix sketch:** Add a button row under the count line: `[View projects →]` (links to `/projects`), and if exactly one project was created, `[Open <name> →]` (deep-links to it). 12-line addition.

### P1-4. Service Logs panel still labels itself "live tail" in copy
**Where:** `web/src/components/service/overlay/ServiceLogsPanel.tsx:89`. The description reads: *"Searchable archive (FTS5). 14d retention. Polls every 10s when no query is set — for true streaming, use the Deployments tab."* That's honest now. **BUT the empty-state and the panel header still imply tail behavior:** the FTS5 archive only carries committed log rows from the logship goroutine, with a 5-10s ingestion delay. Users who tail a hot reload won't see lines for several seconds and assume the panel is broken.

**Fix sketch:** The copy already calls this out. Just add a one-line clarifier under the search bar when no query is set: *"Showing archive — 5-10s ingest delay. For the live pod log, open Deployments → expand a build."* 3-line addition.

### P1-5. Service Logs panel has no scroll-pause indicator
**Where:** `ServiceLogsPanel.tsx:194-222` (the `LogList` component). The build-side `LogStream.tsx:176-189` got a great scroll-pause indicator ("paused · jump to live ↓") — the pod-log archive list didn't. It silently flips `stickToBottom` when the user scrolls up and keeps appending lines they can't see.

**Impact:** Identical to the build-side bug that was fixed. User scrolls up to read an old error, panel keeps polling, new lines pile up at the bottom, user thinks the panel stalled and reloads — losing whatever line they had in view.

**Fix sketch:** Port the "paused · jump to live" pill from `LogStream.tsx:176-189` into `LogList`. ~15 lines. Same render condition (`!stickToBottom && lines.length > 0`), same restore handler.

### P1-6. Project view's `isError` state leaks the raw API message
**Where:** `web/src/app/(app)/projects/[project]/view.tsx:92-101`. When useProject fails (404, 403, network) the page shows `Failed to load project: <err.message>`. The message is whatever the api-client surfaced — for a missing project it's `404: kusoprojects.application.kuso.sislelabs.com "kuso-foo" not found`, for a permission denial it's `403: forbidden`.

**Impact:** A user typing a project name they don't have access to sees a Go-style kube error. A user opening a stale bookmark sees a CRD path. Neither is actionable copy. Worse, it doesn't distinguish "you don't have access" from "this project was deleted" — both are critical decisions for the user.

**Fix sketch:** Branch on the ApiError status. 404 → "This project doesn't exist or was deleted. [Back to projects]"; 403 → "You don't have access to this project. Ask the project owner to add you. [Sign in as another user]"; everything else → the current message but prefixed with "Couldn't load — try refresh." ~20 lines.

### P1-7. Addon overlay can't open via URL deep-link with a tab
**Where:** `view.tsx:67-70` reads `?addon=<n>` but not `?addon=<n>&tab=backups`. Same root cause as P0-1 but for addons. The canvas right-click menu's "SQL console" / "Backups + restore" entries deep-link correctly because they bypass the URL (they pass `tab` as a function argument), but the cmd-K palette and any notification deep-link that lands on an addon ends up on the Overview tab.

**Fix sketch:** Same as P0-1 — read `tab` from the search params and pass into `selectedAddonTab`. ~3 lines, same change.

### P1-8. /welcome Step 1 leaves admin-less users with no clear next step
**Where:** `welcome/page.tsx:194-202`. When the GitHub App isn't configured AND the user isn't an admin, the page reads: *"GitHub App connection requires an admin. Ask a team admin to install it, or skip and start without a repo."* Then a "Skip — I'll do this later" button.

**Impact:** The user clicks Skip → lands on Step 2 → Step 2's empty-state ("No GitHub installations yet") routes them to `/projects/new`. That works but it's three screens of dead-ends to get to one. Step 1 is dishonest about what "skip" leads to — the user thinks they're skipping the wizard, when actually they're being routed to the still-broken Step 2.

**Fix sketch:** When admin-less + no GitHub configured, replace the "Skip" button with a direct link to `/projects/new` (label: "Start without a repo →"). Bypasses the dead Step 2 entirely. 4-line edit.

### P1-9. Build-resources page (/settings/builds) doesn't say what happens to in-flight builds when you save
**Where:** `web/src/app/(app)/settings/builds/page.tsx`. Saving new memory/CPU limits changes the kuso-server's build-pod template — but in-flight builds keep their old limits, and the user has no idea. The page also doesn't say whether a save requires a kuso-server restart (it doesn't, but new builds use the new limits within seconds).

**Impact:** Admin doubles the memory limit because builds are OOMing → saves → next build still OOMs (it was queued before the save) → admin assumes the save didn't take. Real support-ticket bait.

**Fix sketch:** Add a one-line hint near the Save button: *"In-flight builds keep their original limits; the next build uses the new values."* 3-line addition.

### P1-10. Addon overlay storage-size field looks editable but is disabled
**Where:** `addon/overlay/SettingsTab.tsx:218-229`. The Storage size input is `disabled`, but it's a full-width Input control with the placeholder visible. Users routinely click into it, get no caret, and assume the page is broken — the `· immutable` chip in the label is too small to spot.

**Impact:** Adds a "is this app frozen?" moment in the otherwise-best place the user found in the addon overlay. The mitigation hint below the input (the amber paragraph) is correct content but it shows up after the user has already tried to type.

**Fix sketch:** Render the Storage size as a read-only `<div>` with a lock icon instead of a disabled `<Input>`. Move the amber paragraph BESIDE the value, not below. ~10 lines.

---

## P2 — smaller, but worth a sweep

### P2-1. Mobile incident view's "Logs" / "Redeploy" buttons open the desktop-shaped overlay
**Where:** `MobileIncidentView.tsx:84-90`. Both buttons call `onSelectService(shortName, "logs")` which opens `ServiceOverlay` — the same overlay the mobile interstitial says doesn't fit. The Logs panel within is wide (timestamp + pod-short + line columns) and overflows on a 360px viewport.

**Fix sketch:** Either inline a stripped-down phone-shape log view (just the line text, no pod column) on tap, or accept the mobile-shape and tell the truth. Today the affordance lies twice (interstitial says "don't" then suggests "Logs and redeploy work").

### P2-2. /projects empty-state's "Import from Coolify" CTA is admin-only but not gated
**Where:** `projects/page.tsx:112-117`. Non-admin user with zero projects sees both "Create your first project" AND "Import from Coolify". Clicking the import link drops them on `/settings/import` which 403s with "The Coolify import is admin-only. Ask a team admin to run it for you." Pointless click + dead end.

**Fix sketch:** Render the import button only when `useCan(Perms.SettingsAdmin)` is true. 4-line guard.

### P2-3. Empty-state copy on the project canvas describes a flow the user can't see
**Where:** `projects/[project]/view.tsx:128-150`. Empty project shows "+ Add service" and "Add addon" buttons — good. The description says: *"Wire your first GitHub repo or provision a managed database to get this canvas lit up."* But the user doesn't see the canvas yet — there's no skeleton showing what "lit up" looks like. Users on a Trial / Demo install bounce because the empty state doesn't communicate the product's payoff.

**Fix sketch:** Use a faint grayscale screenshot of the canvas as the EmptyState's background, behind 80% opacity. The user sees what they're about to build. 1 image + 4 CSS lines.

### P2-4. Confirmation dialogs use 3 different patterns
**Where:** `web/src/components/shared/ConfirmDialog.tsx` (the canonical pattern, used by canvas delete + EnvVarsEditor diff confirm), `web/src/components/service/overlay/settings/DangerSection.tsx:60-103` (inline `confirming` toggle state + bare `<Input>`), `web/src/app/(app)/projects/[project]/settings/view.tsx:246-282` (inline `confirmDelete` toggle + bare `<Input>`).

**Impact:** Three destructive flows behave subtly differently: ConfirmDialog has ESC-cancels + click-outside + a real modal; the inline patterns have neither (the user can navigate away mid-confirmation and the type-to-confirm text persists). Inconsistency reads as sloppy.

**Fix sketch:** Migrate service-delete + project-delete to `ConfirmDialog` with `typeToConfirm={name}`. ~30 lines deleted across two files; behavior unifies.

### P2-5. Service overlay header diagnostic chip stacks on narrow viewports
**Where:** `ServiceOverlay.tsx:285-358`. On a 1280-wide overlay (the default sm:max-w-3xl), the header packs: project label, URL pill, "no URL yet", and the diagnostic chip ("pending changes" / "pending restart"). They wrap to two lines below the H2 when present together. Looks fine. On a 600px viewport (overlay max-w-3xl drops to full-width) the chips stack 4 deep and push the close button below the fold.

**Fix sketch:** Hide the URL pill at sm- and surface it inside the body's first row instead. ~10 lines.

### P2-6. Canvas right-click menu has no keyboard navigation
**Where:** `CanvasContextMenu.tsx`. Open with Shift+F10 on a focused service node → menu appears at coords but the up/down arrow keys don't move focus between menu items. Only Tab works (browser-default tab traversal).

**Fix sketch:** Manage `aria-activedescendant` via keyboard arrow handlers; or migrate to base-ui's Menu primitive (the one CLAUDE.md says is broken in static export — would need a different fix).

### P2-7. NotificationsPage uses generic "Discord webhook" framing but supports Slack too
**Where:** `web/src/app/(app)/settings/notifications/page.tsx:14, 33-41`. The wire type literal is `"discord" | "webhook" | "slack"` (good), but the empty-state copy reads *"to add a Discord webhook"* and the description in `/settings/page.tsx:47` says *"Discord webhooks, generic webhook fan-out"* — Slack isn't mentioned. A Slack-shop admin scanning the index won't notice this works for them.

**Fix sketch:** Update copy strings in 2 places. 4-line edit.

### P2-8. /settings cards stay visible for locked admin sections but say "ask a team admin" with no contact info
**Where:** `settings/page.tsx:209-237`. Locked-card variant displays for non-admins, with copy *"ask a team admin to enable it for you"*. No way to actually see who that is. /awaiting-access page has the same gap. Single-tenant installs typically have 1-3 admins; surfacing them would close the loop.

**Fix sketch:** Add a `useAdmins()` hook that hits `/api/users?role=admin` (or read from group membership), surface their names + emails on the locked card's tooltip / awaiting-access page. ~25 lines + one API endpoint if not already there.

### P2-9. Service Variables tab's "redeploys on save" pill goes away on tab switch
**Where:** `EnvVarsEditor.tsx:682-688`. The pill is conditional on `dirty && canWrite`. When the user switches to another tab the dirty registry persists (ServiceOverlay tracks it), but THIS pill renders inside the panel that just unmounted on the AnimatePresence swap. So the user navigating "Variables → Logs → Variables" sees the pill blink off and back on.

**Fix sketch:** Hoist the "redeploys on save" badge into the SaveBar copy itself ("Save · redeploys"), drop the local pill. ~8 lines moved.

### P2-10. Custom domain hint inside service Networking section is hidden until you add a domain
**Where:** `NetworkingSection.tsx:131-181`. The whole DNS-needs-to-point-at-the-cluster-IP hint sits in the `hint` prop of the Row — and the hint only shows when `hosts.length > 0`. So a user who just clicked "Add domain" and types their hostname is left without the "now point DNS at …" copy until they save once.

**Fix sketch:** Surface the cluster's ingress IP inline (we can read it from `/api/config` or `/api/kubernetes/nodes`) the moment the user enters a domain string. ~15 lines + the IP fetch.

---

## Recurring themes

1. **Half-finished refactor residue.** P1-1 (double saveError surface), P0-2 (addon overlay never adopted OverlayDirtyContext), P1-5 (scroll-pause indicator not ported to pod logs). The pattern is "shipped the new abstraction, left the old code in place." Same observation as the previous review's recurring-pattern section. Worth a focused 1-hour pass to delete the legacy shapes.

2. **URL-param fanout is incomplete.** P0-1, P1-7 — `view.tsx` reads `service`, `addon`, `env` but not `tab`. Every deep-link consumer (cmd-K, notification feed) expects tab to round-trip. This is a 3-line fix once you know it's there.

3. **Copy is honest but not consistent.** ServiceLogsPanel correctly calls out 10s polling now (P1-4 closes the remaining "live" implication), but settings descriptions still describe features in admin-shop language ("Discord webhooks, generic webhook fan-out") rather than ALL the channels they support.

4. **Error copy leaks Go strings.** Project detail's load-error path (P1-6), the audit page's 403 inference (works), the AddonOverlay backup error surface (passes through). Every page needs a single helper that maps ApiError.status → user-facing copy.

5. **Mobile is "incident-mode only" but the affordances are desktop-shaped.** P2-1. Either commit to the incident-mode pattern (don't open the desktop overlay) or rethink. Today it's a half-measure.

---

## Suggested next-day list

If you have ~2 focused hours:

1. P0-1 (cmd-K tab plumbing) — 5 min
2. P0-2 (addon overlay SaveBar) — 40 min, biggest UX win
3. P1-1 (delete duplicate saveError pill) — 2 min
4. P1-2 (settings breadcrumb labels) — 10 min
5. P1-3 (Coolify success "view projects" link) — 10 min
6. P1-5 (port scroll-pause to pod logs) — 15 min
7. P1-7 (addon URL tab plumbing) — 5 min, same change as P0-1
8. P1-6 (project-not-found copy split) — 20 min
9. P2-4 (consolidate confirm dialogs) — 30 min

Everything else is genuinely P2 polish.

---

Generated on 2026-05-12 against `main` @ commit `1b97c53`. File:line refs in every finding; drill in to verify.
