# Frontend rewrite: Next.js + Railway-style UX + robiv0 design system

**Status:** spec, pre-implementation
**Date:** 2026-05-02
**Replaces:** `client/` (Vue 3 + Vuetify SPA)
**Visual reference:** [biznesguys/robiv0](https://github.com/biznesguys/robiv0) (Next.js 16 + Tailwind 4 + shadcn/ui `base-nova` + Base UI)

---

## Vision

The kuso dashboard becomes a Railway-feel PaaS console: a project canvas that visualises services and addons as connected nodes, a ⌘K command palette for navigation, live deploy logs streamed over WebSockets, and a deploy-first creation flow that gets a user from "I have a repo" to "it's building" in two clicks.

Visually, kuso adopts the robiv0 token system verbatim: neutral light mode, dark mode with a violet accent, Roboto/DM Sans/Geist Mono type stack, and shadcn `base-nova` component primitives sitting on top of Base UI behaviours.

The Vue/Vuetify frontend is deleted in full. The Go backend is unchanged except for additions called out in §13.

---

## Decisions log

These are the answers from the brainstorming round, recorded so future readers don't have to reconstruct them:

| # | Decision |
|---|----------|
| D1 | Next.js 16 + App Router in `web/`, static export (`output: "export"`), embedded in Go binary via `go:embed` (same shipping model as today). |
| D2 | Existing Go JWT auth stays as-is. Frontend wraps it in a `useSession()` hook + `<AuthGate>` shaped like Better Auth so robiv0 components port unmodified. |
| D3 | All eight Railway-style UX patterns are v1 must-haves: canvas, ⌘K palette, live logs panel, env switcher, "+ New" everywhere, variable references, activity feed, sleep visualisation. |
| D4 | Logs stream over WebSockets (new endpoint on the Go server), not SSE. |
| D5 | Canvas is built on `@xyflow/react` (React Flow v12). |
| D6 | Project creation is a Railway-style fast path (single screen), not the 7-step wizard described in `docs/REDESIGN.md`. |
| D7 | Single branded landing page for unauthenticated visitors at `/`; full marketing pages from robiv0 are not ported. |

---

## 1. Architecture

### 1.1 Top-level layout

```
kuso/
├── client/                     ← DELETED
├── web/                        ← NEW
│   ├── src/
│   │   ├── app/
│   │   │   ├── (marketing)/    ← landing only
│   │   │   ├── (auth)/         ← /login
│   │   │   ├── (app)/          ← authed dashboard
│   │   │   ├── globals.css
│   │   │   ├── layout.tsx
│   │   │   └── not-found.tsx
│   │   ├── components/
│   │   │   ├── ui/             ← shadcn primitives
│   │   │   ├── layout/         ← Sidebar, Header, MobileNav, DashboardShell
│   │   │   ├── canvas/         ← React Flow nodes/edges/controls
│   │   │   ├── command/        ← ⌘K palette
│   │   │   ├── logs/           ← LogStreamPanel
│   │   │   ├── activity/
│   │   │   ├── env-switcher/
│   │   │   ├── service/
│   │   │   ├── addon/
│   │   │   └── shared/
│   │   ├── features/           ← per-domain (auth, projects, services, …)
│   │   ├── lib/
│   │   ├── hooks/
│   │   └── types/
│   ├── public/
│   ├── next.config.ts          ← output: "export"
│   ├── tailwind.config.ts
│   ├── postcss.config.mjs
│   ├── components.json         ← shadcn (style: base-nova)
│   ├── package.json
│   └── tsconfig.json
└── server-go/internal/web/
    ├── dist/                   ← legacy Vue bundle (deleted at end of Phase F)
    └── dist-next/              ← Next.js export lands here at build time
```

During the rewrite, both dists coexist on disk. Because `//go:embed` directives are static, `web.go` is updated to embed both:

```go
//go:embed dist
var distLegacyFS embed.FS

//go:embed dist-next
var distNextFS embed.FS

func Dist() (fs.FS, error) {
    if os.Getenv("KUSO_FRONTEND") == "next" {
        return fs.Sub(distNextFS, "dist-next")
    }
    return fs.Sub(distLegacyFS, "dist")
}
```

`KUSO_FRONTEND` defaults to legacy (i.e. unset = legacy). Both dists are baked into the binary so a single image can flip between them at runtime — useful for staging-vs-production rollout. At end of Phase F the legacy `dist/` is deleted, `dist-next/` is renamed to `dist/`, the dual-embed is reverted to a single `//go:embed dist`, and the env flag is removed.

### 1.2 Build sequence (production)

1. `cd web && npm ci && npm run build` → emits `web/out/`
2. CI step `rsync -a --delete web/out/ server-go/internal/web/dist/`
3. `cd server-go && go build -o bin/kuso-server ./cmd/kuso-server` → single binary with frontend baked in

The existing `server-go/internal/web/web.go` (`//go:embed dist`) is unchanged. The CI job and the `Dockerfile` get a frontend build stage prepended.

### 1.3 Dev sequence

Two terminals:

- `cd web && npm run dev` → Next.js dev server on `:3000` with HMR
- `cd server-go && go run ./cmd/kuso-server` → API + WS gateway on `:8080`

`web/next.config.ts` declares dev-only rewrites that proxy `/api/*` and `/ws/*` to `:8080`. No Go binary rebuild is needed during frontend iteration. The `output: "export"` setting is honoured at build time; in dev the full Next runtime is available, but no code is allowed to depend on it (lint rule + smoke check).

### 1.4 Why this works

Static export means no SSR, no API routes, no middleware. The Go server already provides every runtime concern (auth, persistence, kube interactions, WebSockets). Next.js is reduced to a build-time tool that gives us file-system routing, RSC for the marketing page rendered to static HTML at build time, TypeScript-first DX, and the entire shadcn / Tailwind / Base UI ecosystem. The runtime is a pure SPA, same shape as today, just with a much better foundation.

---

## 2. Design system

### 2.1 Tokens

Lifted verbatim from `robiv0/src/app/globals.css` with one change.

**Light mode** — identical to robiv0 (neutral palette, `#111114` accent).

**Dark mode** — identical to robiv0 except the accent swaps:

| Token | robiv0 | kuso |
|---|---|---|
| `--accent` | `#F07042` (orange) | `#7C5CFF` (violet) |
| `--accent-hover` | `#E85D2A` | `#6845F0` |
| `--accent-subtle` | `rgba(240,112,66,0.1)` | `rgba(124,92,255,0.1)` |
| `--primary` (dark) | `#F07042` | `#7C5CFF` |
| `--ring` (dark) | `#F07042` | `#7C5CFF` |
| `--sidebar-primary` (dark) | `#F07042` | `#7C5CFF` |
| `--chart-1` / `--chart-4` (dark) | orange variants | violet variants |

Violet reads as "platform / infra" rather than "consumer SaaS" and avoids visual collision with Coolify's yellow, Vercel's mono, and Supabase's green. All other tokens (`--bg-*`, `--text-*`, `--border-*`, `--shadow-*`, `--radius`, sidebar tokens, chart tokens 2/3/5) are copied unmodified.

### 2.2 Fonts

Loaded via `next/font/google` in `app/layout.tsx`:

- `Roboto` → `--font-roboto` → body
- `DM Sans` → `--font-dm-sans` → headings, hero copy
- `Geist Mono` → `--font-geist-mono` → code, status pills, env keys, terminal

### 2.3 shadcn primitives ported verbatim

From `robiv0/src/components/ui/`: button, card, badge, avatar, dialog, dropdown-menu, popover, sheet, tabs, separator, table, input, input-group, label, textarea, switch, checkbox, select, tooltip, command, skeleton, terminal, animated-list. Some animation-heavy components (flickering-grid, marquee, word-rotate) are skipped — they belong to marketing pages we are not porting.

### 2.4 Domain components built on the primitives

New components, all built with the ported primitives + tokens:

- `<DeployStatusPill>` — `{building | deploying | active | sleeping | failed | crashed}`, mono font, six colour mappings.
- `<SleepBadge>` — dimmed dot + "asleep · 12m".
- `<EnvSwitcher>` — segmented control, prod / preview-pr-N.
- `<RuntimeIcon>` — Dockerfile / Nixpacks / Buildpacks / Static (lucide icons).
- `<AddonIcon>` — Postgres / Redis / Mongo / etc., svg set under `web/public/icons/addons/`.
- `<CommitChip>` — `feat: do thing · abc1234 · 3m ago`, mono font.
- `<KbdHint>` — styled `<kbd>` for shortcut hints (⌘K, esc, enter).

### 2.5 Theme

`next-themes` driven, dropdown in header with `light / dark / system`. Choice persists to `localStorage`. Same as robiv0.

---

## 3. Auth & session

### 3.1 `useSession()`

Mimics Better Auth's API shape so robiv0 component code (`Sidebar.tsx`, `Header.tsx`, etc.) ports without edits.

```ts
const { data, isPending, error } = useSession();
// data: { user: { id, name, email, image, role }, session: { id, expiresAt } } | null
```

Implemented as a `@tanstack/react-query` wrapper around `GET /api/auth/session`. Cached, auto-refetched on window focus, invalidated by `signOut()`. The Go endpoint returns the kuso-shaped user record; the hook reshapes it to the Better Auth shape on the client.

### 3.2 `<AuthGate>`

Top-level wrapper inside `(app)/layout.tsx`. Behaviour:

1. Read session via `useSession()`.
2. While `isPending` → render skeleton dashboard shell (same Sidebar/Header silhouette, content area is `<Skeleton>`).
3. If `data` is null or the underlying fetch hit 401 → `router.replace('/login?next=' + pathname)`.
4. Else render `children`.

### 3.3 Sign-in flows

The login page (`(auth)/login/page.tsx`) reads `GET /api/auth/methods` to decide which buttons to show.

- **Local:** form → `POST /api/auth/login` → response `{ access_token }` → store in `localStorage` AND set cookie `kuso.JWT_TOKEN` (cookie path `/`, `Secure`, `SameSite=Lax`) → invalidate session query → redirect to `?next` or `/projects`.
- **GitHub OAuth:** button does `window.location = '/api/auth/github'`. Backend handles the redirect dance, sets the cookie, drops the user back at `/`.
- **Generic OAuth2:** identical pattern via `/api/auth/oauth2`.

### 3.4 `api-client.ts`

Single fetch wrapper used by every feature hook. Responsibilities:

- Reads JWT from `localStorage` on each call.
- Sets `Authorization: Bearer <jwt>` if present.
- On 401 → clears JWT, calls `queryClient.invalidateQueries({ queryKey: ['session'] })` → AuthGate kicks the user to `/login`.
- Throws `ApiError` with status + parsed body on non-2xx.

### 3.5 Token rotation

Out of scope. The backend currently issues long-lived JWTs and the cookie / localStorage just carries them. No silent-refresh dance.

---

## 4. Layout shells

Three shells under `app/`:

### 4.1 Marketing shell (`(marketing)/layout.tsx`)

Centered content, no chrome. Used only for `/` (the landing page).

### 4.2 Auth shell (`(auth)/layout.tsx`)

Centered card on `--bg-secondary`, kuso logo on top, theme toggle in the corner. Used for `/login`.

### 4.3 App shell (`(app)/layout.tsx`)

`<AuthGate>` → `<DashboardShell>`. The shell mirrors robiv0's pattern:

```
┌─────────────────────────────────────────────────┐
│ [Sidebar 260px / 64px] │ [Header 56px]          │
│                        ├────────────────────────┤
│                        │                        │
│                        │ <main>                 │
│                        │                        │
└─────────────────────────────────────────────────┘
```

#### Sidebar (left, collapsible 260px ↔ 64px)

Three sections:

- **Projects (top)** — list of the user's projects, each rendered with a colored health dot (green = all services active; amber = any building/deploying; red = any failed/crashed; grey = all sleeping). Currently selected project highlighted. "+ New project" button at the bottom of the list.
- **Project context (middle, only when inside a project)** — Canvas, Activity, Logs, Settings. These are project-scoped tabs but rendered as sidebar items so the top bar stays free for env-switching.
- **Account (bottom)** — Profile, Tokens, Settings (Users / Roles / Groups for admins), Theme toggle.

#### Header (top, 56px)

- Left: breadcrumb auto-generated from the URL (robiv0's pattern, ported verbatim).
- Center: `<EnvSwitcher>` — only renders on project pages, flips the entire UI between `production` and any active `preview-pr-N`. Persisted in URL query (`?env=preview-pr-42`).
- Right: ⌘K command palette trigger (`<KbdHint>`), notifications bell (icon present but inert in v1), user avatar dropdown.

#### Mobile nav (`<MobileNav>`)

Sheet sliding in from the left, identical content to the desktop sidebar. Robiv0's pattern.

---

## 5. The canvas

`/projects/[project]` — the centerpiece.

### 5.1 Library

`@xyflow/react` (React Flow v12). MIT-licensed, ~50 KB gzipped, used by n8n, Typebot, etc.

### 5.2 Node types

- **`<ServiceNode>`** — rounded card ~280×140. Contents: service name, runtime icon, deploy status pill, replica count, sleep badge if sleeping, URL chip linking to the live deployment. Border-pulse animation when status is `building` or `deploying`.
- **`<AddonNode>`** — rounded card. Contents: addon icon, name, kind, version, connection status. Slightly smaller than a service node, with a distinct shadow + subtle gradient so the node types are visually distinguishable at a glance.
- **`<PreviewEnvNode>`** — only renders when env switcher is set to `preview-pr-N`. Shows PR number, branch, author avatar. Attached visually to each service node that has the matching preview env.

### 5.3 Edge types

- **Addon → service** (solid edge with animated gradient flow). Drawn from each service that includes the addon's connection secret in its `envFromSecrets`. The animation runs only when the service is non-sleeping.
- **Service → service variable reference** (dashed edge). Drawn when a service references another service's env var via the `${{ Other.PORT }}` syntax (§ 8.3).

### 5.4 Controls

- Pan: drag empty canvas.
- Zoom: ⌘+/- or wheel.
- Minimap: bottom-right (React Flow built-in, restyled to match tokens).
- Fit-to-view button.
- "+ New" floating button (top-right of canvas) → dropdown: New Service, New Database (with addon-kind submenu).

### 5.5 Layout

First load: services in a vertical column on the left, addons in a vertical column on the right. Layout computed via `dagre` once. User can drag to override; node positions persist to `localStorage` under key `kuso.canvas.layout.<projectName>`. Server-side persistence is out of scope — node positions are a UI concern and per-device drift is acceptable.

### 5.6 Selection & detail panel

Click a node → opens a right-side `<Sheet>` (slides in from right, ~480 px wide) with the service or addon detail panel:

- Service: env vars, recent deploys, logs link, settings, danger zone.
- Addon: connection secret reference, status, size/version, settings, danger zone.

Detail interactions never navigate away from the canvas — the user keeps spatial context.

### 5.7 Empty states

- Project just created with zero services → big centered "+ New service" CTA on the canvas with a subtle dot-grid background.
- Has services but zero addons → smaller addon-empty CTA on the right column.

---

## 6. Logs panel (live streaming)

Bottom drawer that slides up from the bottom of the canvas (or any service detail page) when the user clicks "Logs" on a service.

### 6.1 Shape

- ~40vh tall, resizable by dragging the top edge.
- Header bar: service name · env switcher (overrides page-level for this panel) · "Follow" toggle (auto-scroll to latest) · "Wrap" toggle · "Download" button · close X.
- Body: monospace, line-numbered, virtualized (`react-virtuoso`) for high log volume.
- stderr lines tinted red.
- Build phase markers (`[CLONE]`, `[BUILD]`, `[DEPLOY]`) get a coloured gutter prefix and bold weight.

### 6.2 Streaming protocol

New backend endpoint:

```
GET  /ws/projects/:p/services/:s/logs?env=X
GET  /ws/projects/:p/services/:s/builds/:buildId/logs
```

Both upgrade to WebSocket. Authenticated via JWT in the `Sec-WebSocket-Protocol` subprotocol header (browsers can't set `Authorization` on WS handshakes; the subprotocol trick is standard).

Server side:

- For runtime logs: open `corev1.Pods.GetLogs(opts).Stream(ctx)` per pod in the env, fan into a single goroutine that emits framed JSON over WS.
- For build logs: tail kaniko Job pod the same way.
- Frame shape: `{ type: "log", pod: "<podName>", line: "<text>", stream: "stdout"|"stderr", ts: "RFC3339" }`.
- Phase frames: `{ type: "phase", value: "BUILDING" }` emitted from server when build job conditions or Deployment rollout state advance.
- Heartbeat: server sends `{ type: "ping" }` every 30 s; client replies with pong frame (gorilla/websocket's built-in control message).

### 6.3 Client hook

`useLogStream(project, service, env)`:

- Wraps a reconnecting `WebSocket` (exponential backoff capped at 30 s).
- Buffers last N=10000 lines (configurable).
- Exposes `{ lines, phase, isConnected, reconnect, clear }`.

### 6.4 Phase visualisation

Above the log lines, a thin horizontal stepper:

```
CLONING → INSTALLING → BUILDING → PUSHING → DEPLOYING → ACTIVE
```

Steps highlight as the server emits `{type: "phase", ...}` frames. Phase detection lives server-side: kaniko build job conditions feed CLONING/INSTALLING/BUILDING/PUSHING; Deployment rollout status feeds DEPLOYING/ACTIVE.

---

## 7. Command palette (⌘K)

`cmdk` library (already in robiv0's deps). Triggered by ⌘K from anywhere. Single text input, virtualized result list, sectioned by category:

- **Navigation** — `g p` go projects, `g s` go settings, etc.
- **Projects** — every project, fuzzy match on name.
- **Services** — every service across every project, shown as `project / service`.
- **Recent deploys** — last 20, click to view logs / activity.
- **Actions** — New project, New service, Toggle theme, Sign out, Open docs.
- **Settings** — every settings page.

Implementation:

- Each feature registers its own command groups via a `useCommandRegistry()` hook (functions live in `web/src/components/command/registry.ts`, callers add groups on mount).
- Hotkey bound via `react-hotkeys-hook`.
- Recents persisted in `localStorage` under `kuso.cmdk.recents`.

---

## 8. Activity feed, env switching, variable references

### 8.1 Activity feed

`/projects/[project]/activity` — chronological event feed for the project: build started/succeeded/failed, deploy rolled out, service slept/woke, addon created, env var changed, preview env spawned/torn down, GitHub webhook received.

**Backend change:** the existing `GET /api/audit?limit=N` is extended with project filtering: `GET /api/audit?project=X&limit=N&after=<id>`. Each event has shape `{ id, timestamp, actor, kind, project, service?, env?, payload }`. Pagination is keyset (`after=<id>`) — no offset.

**Frontend:** `<ActivityFeed>` is a virtualized list grouped by day. Each row shows a kind→lucide icon, a one-line description, the actor avatar, a relative timestamp. Clicking a row opens a side panel with the full payload JSON and a deep link to the relevant deploy logs.

Deploy status pills (§ 2.4) are reused so the visual language stays consistent between canvas and activity.

### 8.2 Env switcher

Top-bar segmented control. Options:

- `production` (always present).
- `preview-pr-N` for each active preview env, queried from `GET /api/projects/:p/envs`.

Switching updates the URL query param `?env=X`, and every component that depends on "current env" (canvas service status pills, logs panel, activity filters, env var editor) re-binds. Implemented via a `useCurrentEnv()` hook backed by `useSearchParams`.

### 8.3 Variable references

In the env vars editor (service detail panel), values can be like `${{ analiz-pg.DATABASE_URL }}` — referencing an addon's connection secret.

**Autocomplete UX:**

- Type `${{ ` → popover lists addons in the project.
- Pick one → lists keys in its `<addon>-conn` secret.
- Pick a key → inserts `${{ analiz-pg.DATABASE_URL }}`.

**New backend endpoint:**

```
GET /api/projects/:p/addons/:a/secret-keys → { keys: ["DATABASE_URL", "PGHOST", ...] }
```

Returns key names only. Values are never returned.

**Resolution rule (server-side, at create/update time):**

- Pure references (entire value matches `^\$\{\{\s*<addon>\.<KEY>\s*\}\}$`) are rewritten into a `valueFrom.secretKeyRef` against the addon's connection secret.
- Composite references (e.g. `prefix-${{ ... }}-suffix`) → 400 with message "variable references must be the entire value".

This keeps the runtime resolution path zero — kube does the work via `envFrom` / `valueFrom`. No runtime templating engine is built.

---

## 9. Sleep state visualisation

Service nodes in `sleeping` state get distinct treatment on the canvas:

- Reduced opacity (~60 %).
- Slow breathing border-pulse animation.
- "Asleep · 12m" badge (`<SleepBadge>`).
- "Wake" button on hover.

**New backend endpoint:**

```
POST /api/projects/:p/services/:s/wake
```

Sets desired replica count from 0 to `service.scale.min` on the production env's KusoEnvironment. Sleep itself is already handled by the operator's HPA + scale-to-zero logic, so this endpoint just nudges the spec. Auditable via the same audit pipeline.

---

## 10. Project creation (Railway-style fast path)

Single page at `/projects/new`. Layout:

- **Top:** big input — "Project name". Auto-suggested from selected repo.
- **Middle: repo picker.**
  - GitHub App not installed → "Install kuso GitHub App" CTA → opens GitHub install flow (`GET /api/github/install-url`) → returns to this page with `?github=installed`.
  - Installed → `<Command>`-style searchable list of repos, grouped by org/installation (`GET /api/github/installations`, `GET /api/github/installations/:id/repos`).
  - Picking a repo triggers `POST /api/github/detect-runtime` for each detected service path.
- **Bottom: detected services card.** Collapsible. Rows: `[runtime icon] service-name @ path/in/repo`, each editable inline (name, path, runtime, port). User can delete or add manual rows.
- **Aside: suggested addons card.** Kuso scans the file tree for hints (`DATABASE_URL` in any `.env.example` → suggest Postgres, `REDIS_URL` → Redis, etc.). Pre-checked checkboxes that the user can uncheck.
- **Bottom-right: Deploy button (primary).** On click:
  1. `POST /api/projects` (creates `KusoProject`).
  2. `POST /api/projects/:p/services` for each detected service (auto-creates production env on the backend).
  3. `POST /api/projects/:p/addons` for each chosen addon.
  4. Redirect to `/projects/:p` → canvas; status pills show `building`; logs panel slides up automatically so the user watches the first build.

**Defaults locked at create time, editable after:**

- Previews: ON if GitHub App installed; OFF otherwise.
- Base domain: `<project>.<global-base>`.
- Custom domains, env vars, scale, sleep: defaults from each service's runtime, editable via the service detail panel.

**New backend endpoint** (small additive):

```
POST /api/github/scan-addons  → { suggestions: [{kind: "postgres", reason: "DATABASE_URL in services/api/.env.example"}, ...] }
Body: { installationId, owner, repo, branch }
```

Server-side: same GitHub App-driven file fetch as the existing `/api/github/detect-runtime`, but greps file contents for known addon hints.

---

## 11. Routing map

```
/                                    landing (marketing shell)
/login                               login (auth shell)

/projects                            list (app shell)
/projects/new                        creation flow
/projects/[project]                  canvas (default tab)
/projects/[project]/activity         activity feed
/projects/[project]/logs             full-screen logs view
/projects/[project]/settings         project settings
/projects/[project]/services/[svc]   service detail (canvas detail panel as a full page; same component, different shell)

/settings                            redirect → /settings/profile
/settings/profile                    profile + avatar
/settings/tokens                     personal access tokens
/settings/users                      admin: users
/settings/roles                      admin: roles & permissions
/settings/groups                     admin: groups
/settings/notifications              notification channels
/settings/config                     cluster config (admin)

/not-found                           404
```

Admin pages (`users`, `roles`, `groups`, `config`) are gated client-side based on `session.user.permissions` — same model as the current Vue app's `meta.requiresUserWrite`. A `<RequirePermission>` component wraps the page.

---

## 12. State, data, and side-effect management

### 12.1 Server state: TanStack Query

All API reads via `@tanstack/react-query`. Conventions:

- One hook per resource: `useProjects()`, `useProject(name)`, `useServices(project)`, `useService(project, service)`, `useEnvs(project)`, `useAddons(project)`, `useBuilds(project, service)`, `useAuditFeed(filters)`.
- Default `staleTime` 30 s for lists, 10 s for detail; refetch on window focus.
- Mutations invalidate the relevant query keys; no manual cache surgery in components.

### 12.2 UI state: Zustand (small, scoped stores)

Two explicit stores — kept tiny on purpose:

- `useCanvasStore` — selected node id, sheet open state, pan/zoom, layout overrides (synced to `localStorage`).
- `useLogsPanelStore` — open/closed, height, follow toggle, wrap toggle, current target (project/service/env or build id).

Anything else is component-local.

### 12.3 WS state: per-hook

`useLogStream` owns its own WS instance + reconnect timer. No global WS manager.

### 12.4 Feature folder convention

Borrowed from robiv0:

```
web/src/features/<domain>/
  api.ts          ← fetchers using lib/api-client
  hooks.ts        ← React Query hooks
  schemas.ts      ← Zod schemas + inferred types
  components/     ← feature-specific UI
  index.ts        ← public surface
```

Cross-feature imports go through `index.ts`; internal files are not imported across feature boundaries.

---

## 13. Backend changes required

Additive only. No existing endpoint changes shape.

| Endpoint | Purpose | Section |
|---|---|---|
| `GET /ws/projects/:p/services/:s/logs?env=X` | Runtime log stream over WS. | § 6.2 |
| `GET /ws/projects/:p/services/:s/builds/:buildId/logs` | Build log stream over WS. | § 6.2 |
| `POST /api/projects/:p/services/:s/wake` | Wake a sleeping service. | § 9 |
| `GET /api/projects/:p/addons/:a/secret-keys` | List keys in addon connection secret. | § 8.3 |
| `POST /api/github/scan-addons` | Suggest addons from repo file tree. | § 10 |
| `GET /api/audit?project=X&after=<id>` | Project filter + keyset pagination on existing audit endpoint. | § 8.1 |

`gorilla/websocket` is added to `server-go/go.mod`. (The retired NestJS server is not a reference; the Go server has no existing WS code.) JSON-framed text messages over WebSocket, JWT in `Sec-WebSocket-Protocol` subprotocol header.

In addition to the new endpoints in the table, the existing `POST /api/projects/:p/services/:s/env` gains additive variable-reference rewrite behaviour (§ 8.3). The endpoint shape is unchanged; the new logic is a server-side parser that converts pure-reference values into `valueFrom.secretKeyRef`, with composite references rejected as 400. Existing payloads without `${{ ... }}` syntax are unaffected.

---

## 14. Scope cuts (named so they don't sneak back)

- **Interactive pod shell.** The `<terminal>` component from robiv0 is ported but used read-only in v1. Full interactive shell ships v1.1 once the WS log infra has bedded in.
- **Cron jobs UI.** `KusoJob` CRD doesn't exist yet (per `docs/REDESIGN.md` § "Open questions parked for later"). Not in v1.
- **Per-user canvas node position sync across devices.** localStorage only.
- **Notifications bell.** Icon renders in the header but is inert. The existing notification settings page (`/settings/notifications`) is wired up to the existing notifications CRUD endpoints unchanged.
- **Marketing pages.** Single landing only. No pricing/testimonials/FAQ/SkillsMarquee.
- **i18n.** The current Vue app has 8 locales. The new app is English-only at v1; `next-intl` is added in a follow-up.
- **Server-side canvas layout persistence.** localStorage only.
- **`kuso migrate from-pipelines` UI.** CLI-only; no UI surface (matches `docs/REDESIGN.md` § "Migration from v0.1").

---

## 15. Tech stack summary

| Layer | Tool | Source |
|---|---|---|
| Framework | Next.js 16 (App Router, RSC, static export) | robiv0 |
| Language | TypeScript strict | robiv0 |
| Styling | Tailwind 4 + shadcn/ui (`base-nova`) + Base UI primitives | robiv0 |
| Theme | `next-themes` | robiv0 |
| Icons | `lucide-react` | robiv0 |
| Server state | `@tanstack/react-query` | new |
| UI state | `zustand` | new |
| Forms | `react-hook-form` + `zod` | new |
| Canvas | `@xyflow/react` v12 | new |
| Command palette | `cmdk` | robiv0 |
| Hotkeys | `react-hotkeys-hook` | new |
| Virtual lists | `react-virtuoso` | new |
| Markdown | `react-markdown` + `remark-gfm` + `rehype-highlight` | robiv0 |
| Toasts | `sonner` | robiv0 |
| WebSocket | native `WebSocket` + thin reconnect wrapper | new |
| Testing | `vitest` + `@testing-library/react` + Playwright | robiv0 |

---

## 16. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Canvas drag interactions are tricky to get right under the design tokens. | Build the canvas page last (after the rest of the dashboard works as a non-canvas list view). The list view is the fallback if the canvas slips. |
| WebSocket auth via subprotocol is an unusual pattern. | Add an integration test that proves an unauthenticated WS upgrade is rejected; smoke-test through the production ingress before relying on it for kuso.sislelabs.com. |
| Static export cannot do redirects → handling `?next=` correctly across login is fiddly. | The redirect lives in the `<AuthGate>` component (`router.replace`), not in middleware. Smoke test covers `/projects → /login?next=/projects → log in → /projects`. |
| Embedding the Next export inside the Go binary at build time creates a hard ordering dependency in CI. | One CI job, two stages: build frontend, then build Go. Cache `node_modules` between runs. |
| The variable-reference parser (§ 8.3) is a new server-side concern. | Implement the parser as a pure function in `internal/projects/varrefs.go` with table-driven tests covering valid / invalid / composite cases. |
| Robiv0 uses Better Auth + Prisma — copying its components naïvely will pull in dependencies we don't need. | Ports go file-by-file, hand-edited; we lift JSX + className + tokens, not auth wiring or Prisma calls. The `useSession()` shim covers the only session-shaped import in the visible UI. |

---

## 17. Phasing

Each phase ends in a commit; `main` stays runnable. The Vue `client/` directory stays in place until phase F is green so the existing dashboard keeps working during the rewrite.

| Phase | Scope | Rough effort |
|---|---|---|
| A | `web/` scaffold: Next.js 16, Tailwind 4, shadcn primitives ported, tokens + theme, auth shell, login page, AuthGate, useSession, api-client. Login flow works against existing backend. | 1 session |
| B | App shell: Sidebar (projects list, account section), Header (breadcrumb, user menu, theme toggle), MobileNav. Projects list page (`/projects`) replicating current behaviour. | 1 session |
| C | Project detail (canvas-less list view first), service detail page, env vars editor (without var refs), addon detail, settings pages. Activity feed (basic). | 1.5 sessions |
| D | WebSocket logs endpoint on Go server. Logs panel client. Phase stepper. | 1 session |
| E | Backend additions: wake endpoint, secret-keys endpoint, scan-addons endpoint, audit project filter. Var-ref parser + tests. | 1 session |
| F | Project creation fast path. End-to-end: pick repo → detect → deploy → land on canvas. Vue `client/` deleted at end of this phase. CI build pipeline updated to build Next.js + rsync into `server-go/internal/web/dist`. | 1 session |
| G | Canvas: React Flow nodes/edges/controls, sleep visualisation, env switcher, variable reference autocomplete UI on top of Phase E backend. | 2 sessions |
| H | ⌘K command palette + landing page polish + smoke test on kuso.sislelabs.com. Cut `0.3.0`. | 1 session |

**Total: ~9.5 sessions.**

The Vue app stays runnable in parallel (its routes still serve from `server-go/internal/web/dist/` until Phase F). The transition mechanism is described in § 1.1: phases A–E build into `server-go/internal/web/dist-next/`; the Go server picks which dist to embed based on `KUSO_FRONTEND=next|legacy`, defaulting to `legacy`. Phase F deletes the Vue dist, renames `dist-next/` to `dist/`, and removes the env flag.
