# One-click app marketplace

**Date:** 2026-07-02
**Status:** Draft — pending user review

## Goal

Let users deploy curated self-hostable apps (Umami, n8n, Uptime Kuma, …) in one
click: pick an app from a catalog, answer a handful of prompts, get a running
service with its addons wired, a domain, a real cert, and generated secrets —
without writing a kuso.yaml or reading the app's docs.

This is the adoption feature the scope guardrails already assume exists
("Sentry self-hostable is one click in marketplace"). Nearly all the machinery
is built: a marketplace template is a curated `spec.File` (kuso.yaml), and
`spec.Apply` already handles services, addons, crons, volumes, domains,
`runtime: image` deploys, and `{generate: hex32}` secret minting. What's
missing is the catalog, a prompt layer, and the UI.

## Non-goals

- **Remote / community catalog.** v1 ships templates inside the kuso release.
  A fetched `kuso-marketplace` repo with community PRs is a v2 possibility;
  it brings trust, signing, caching, and version-skew questions we don't need
  yet.
- **App lifecycle management.** No "update available" badges, no migration
  assistance between app versions. Template updates arrive with kuso releases;
  redeploying picks them up. (Backups already cover the data.)
- **Multi-service mega-apps.** Real Sentry self-host is ~20 containers; v1
  templates are 1 service + addons. Sentry is the stretch goal that validates
  a later multi-service template, not the v1 bar.
- **Quantity.** Coolify has ~300 templates, many broken. v1 ships ~8 that are
  each tested. Curation is the differentiator.

## Decision log

Decisions taken without live input (user AFK); each is revisitable:

1. **Catalog lives in the kuso repo, embedded in the server binary**
   (like the web bundle). Zero new infrastructure, works air-gapped, templates
   are CI-tested against the exact server version they ship with, and the
   existing self-updater distributes catalog updates — kuso already releases
   several times a week, so "adding an app needs a release" is not a real
   bottleneck at this stage. Alternatives (separate fetched repo; hybrid
   bundled+refresh) deferred to v2.
2. **Template = kuso.yaml + manifest, no new DSL.** A template is the existing
   `spec.File` schema with `${{ prompt.<key> }}` placeholders, plus a
   `manifest.yaml` (metadata + prompt definitions). Authoring a template =
   running the app's compose file through the existing compose importer, then
   curating.
3. **Prompt substitution happens on the parsed struct, not the YAML text.**
   The template is `spec.Parse`d first; prompt tokens are replaced inside the
   already-parsed string fields. A malicious or clumsy answer can never change
   the YAML structure (no injection), and unresolved/unknown tokens are
   structured errors.
4. **Render is read-only; apply reuses the existing endpoint.** Mirrors the
   compose importer exactly: `POST /api/marketplace/{app}/render` returns
   kuso.yaml + notes, the actual create goes through
   `POST /api/projects/{p}/apply` (with the same project-auto-create the
   compose path does). No second write path.
5. **Deploys target a project chosen in the dialog** — default "new project
   named after the app", but deploying into an existing project is allowed
   (apply is additive when `prune` is false, so it composes).

## Considered alternative: marketplace apps as addon kinds

Marketplace apps and addons rhyme ("pick from a catalog, get a running thing"
— mailpit already blurs the line), but the substrate is deliberately
different:

- **Contract.** An addon is "StatefulSet + `<name>-conn` Secret", built to be
  consumed by services; backups/HA/pooler/public-TCP all hang off that.
  A marketplace app is a *service* — it needs domains, TLS, sleep, env
  editing, and usually consumes addons itself (Umami → postgres). Addons
  can't depend on addons, so app-as-addon-kind would embed its datastore in
  the helm template and lose backups, HA, pooler, and the data editor for it.
- **Closed vs open set.** An addon kind is code (helm template + conn wiring
  + backup integration + operator release) — the right cost for
  infrastructure. A marketplace app is data (a kuso.yaml through the normal
  apply path) — adding the 9th app must stay cheap.
- **Post-deploy UX.** As a service, the app gets the whole service overlay
  (logs, env, domains, stop/sleep) for free; as an addon it would need all
  of that re-grown in the addon UI.

Convergence point kept from this idea: the marketplace page presents **one
unified catalog** — app templates alongside the existing addon kinds, where
addon cards simply deep-link into the existing Add Addon flow. One browsing
surface, two existing backends.

## Architecture

```
server-go/internal/marketplace/
  templates/<app>/manifest.yaml   — metadata + prompts (go:embed)
  templates/<app>/kuso.yaml       — parameterized spec.File
  templates/<app>/icon.svg
  catalog.go                      — embed, parse-once, list/get
  manifest.go                     — Manifest + Prompt types, validation
  render.go                       — answers → substituted spec.File + notes
  render_test.go, catalog_test.go — every template CI-validated

server-go/internal/http/handlers/marketplace.go
  GET  /api/marketplace                 — catalog list (metadata only)
  GET  /api/marketplace/{app}           — manifest + prompts + raw template
  GET  /api/marketplace/{app}/icon      — icon bytes
  POST /api/marketplace/{app}/render    — {project, answers} → {yaml, notes}

cli/pkg/kusoApi/marketplace.go          — resty methods
cli/cmd/kusoCli/marketplace.go          — list / info / deploy subcommands
web/src/features/marketplace/{api,hooks,index}.ts
web/src/app/(app)/marketplace/page.tsx  — catalog grid
web/src/components/marketplace/DeployDialog.tsx
```

The `marketplace` package is pure (embed + parse + substitute, no kube calls),
matching the `compose` converter split. The handler mounts on the
bearer-protected router; render is capped and rate-limited like
`/api/import/compose`.

## Manifest + prompt schema

```yaml
# templates/umami/manifest.yaml
name: umami                      # slug, = directory name
title: Umami
description: Privacy-friendly web analytics, a Google Analytics alternative.
category: analytics              # analytics | automation | monitoring | dev-tools | data | comms
website: https://umami.is
appVersion: "2.13"               # informational; image tags are pinned in kuso.yaml
prompts: []                      # umami needs none: secret is generate:, DB is an addon ref
```

A prompt definition, when an app does need one (shape, not umami):

```yaml
prompts:
  - key: webhook_url             # substituted where ${{ prompt.webhook_url }} appears
    title: External webhook URL
    kind: string                 # string | password | domain
    required: true
    help: Where the app posts outbound notifications.
```

Prompt rules:

- `key` ∈ `[a-z0-9_]+`, unique per manifest. `kind` drives the input widget
  (`password` masks, `domain` pre-fills `<app>.<baseDomain>` and validates as
  a hostname). `default` and `placeholder` are optional strings.
- Most templates need **zero or very few prompts**: secrets use the existing
  `{generate: hex32}`, datastore wiring uses the existing `${{ addon.KEY }}`
  refs, and the default domain comes from the project's baseDomain. Prompts
  are only for values kuso cannot invent (admin email, external API key).
- Two values are collected by the dialog itself, not the prompt list: the
  **target project** (default: app slug, auto-created) and, when the template
  declares a `domain`-kind prompt, the hostname.

## Render pipeline

```
answers ──▶ validate against manifest (required present, kinds well-formed)
kuso.yaml ─▶ spec.Parse (template must already be valid YAML/schema)
        └─▶ walk string fields of spec.File, replace ${{ prompt.<key> }}
            • token with no matching prompt key → error (typo-loud)
            • required prompt never referenced   → template CI failure
        └─▶ set File.Project from the request
        └─▶ re-validate (spec.Parse round-trip) → return YAML + notes
```

Notes mirror the compose importer's report: e.g. "generated 2 secrets",
"addon umami-db (postgres 16) will be created", "domain umami.kuso.example.com".
The UI then feeds the YAML through the existing dry-run plan
(`POST /api/projects/{p}/apply?dryRun=1`) and renders the standard
`DiffConfirmDialog` before the real apply.

## UI flow

1. **`/marketplace`** top-level page (nav next to Settings): card grid with
   icon, title, one-liner, category filter + search. Static data from
   `GET /api/marketplace`. The grid also lists the addon kinds as a
   "Datastores" category; those cards deep-link into the existing Add Addon
   dialog rather than the template deploy path.
2. **Deploy dialog** on card click: project picker ("new project «umami»" |
   existing), prompt form (only this app's prompts), Deploy button.
3. **Preview:** render → dry-run plan → `DiffConfirmDialog` (exists) shows
   what will be created.
4. **Apply → navigate** to the project canvas, where the normal build/deploy
   status takes over. `runtime: image` apps skip the build entirely and go
   straight to rollout.

CLI parity: `kuso marketplace list`, `kuso marketplace info <app>`,
`kuso marketplace deploy <app> [--project p] [--set key=val]... [--dry-run]`.

## v1 catalog (8 apps)

| App | Addons used | Why it's in v1 |
| --- | --- | --- |
| Uptime Kuma | none (volume) | zero-prompt showcase; deploys in seconds |
| Umami | postgres | the "replace Google Analytics" hook |
| n8n | postgres | most-wanted automation tool |
| Vaultwarden | none (volume) | huge self-host demand, trivial template |
| Gitea | postgres + volume | pairs with future non-GitHub webhook work |
| Metabase | postgres | BI on the DBs users already run in kuso |
| Plausible | postgres + clickhouse | exercises the multi-addon path |
| Listmonk | postgres | newsletter tool, simple, popular |

All single-service, `runtime: image` with pinned tags, secrets via
`generate:`, datastores via addon refs. (Ghost was cut: needs MySQL, which
isn't an addon kind. Sentry deferred: multi-service.)

## Testing

- **Catalog CI test** (table-driven over every embedded template): manifest
  parses + validates; kuso.yaml `spec.Parse`s; every `${{ prompt.* }}` token
  matches a declared prompt and every required prompt is referenced; image
  tags are pinned (reject `:latest`); addon kinds are in the supported set.
- **Render tests:** substitution correctness, injection attempt via an answer
  containing YAML syntax, missing required answer, unknown token.
- **Handler tests:** render endpoint happy path + validation errors, mirroring
  `import_compose_test.go`.
- **Live smoke** (AGENT_SMOKE_TEST.md addition): `kuso marketplace deploy
  uptime-kuma --project mkt-smoke` on the test cluster → service Ready →
  HTTP 200 on its domain → delete project.

## Risks / open questions

- **Template rot:** pinned image tags age. Mitigation: catalog CI can't catch
  a dead upstream tag at rest — add a weekly scheduled CI job that pulls every
  pinned image manifest. Cheap and catches removals early.
- **Existing-project deploys** can collide on service/addon names; apply
  already surfaces conflicts (409 with the wrapped message) — the dialog just
  needs to display them.
- **Icon licensing:** use each project's official logo per their brand
  guidelines (all eight allow referential use); keep SVGs small and local.
