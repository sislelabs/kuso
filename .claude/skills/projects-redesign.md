---
name: projects-redesign
description: Use whenever working on operator/server/CLI/MCP/UI in v0.2+. The pipelines model from v0.1 has been replaced by projects/services/environments. This skill names the canonical model and points at the spec.
---

# kuso v0.2: projects, not pipelines

v0.1 shipped a Heroku-style pipelines model (`KusoPipeline` + `KusoApp` × phase). v0.2 replaces it wholesale with a Railway-style projects model. **Hard fork — no back-compat.**

## Canonical model

```
KusoProject              one product, one repo, multiple services
├── KusoService          one deployable process (web, api, worker, …)
│   └── runs in:
│       └── KusoEnvironment   one running instance per (service, env)
│                              env = "production" (always) or "preview-pr-N" (PR-driven)
└── KusoAddon            one managed dep (postgres, redis, …); shared across all services in the project
                          via auto-injected envFrom secret
```

**Branches map to environments:**
- `main` (or project.defaultRepo.defaultBranch) → `production`
- PR opened (when project.previews.enabled) → `preview-pr-<n>`, torn down on close, 7-day TTL safety net

**Addons are project-scoped, not service-scoped.** Adding a Postgres to project `analiz` injects `DATABASE_URL` (and friends) into every Deployment in the project via `envFrom`. There is no explicit attach/detach.

## What's gone

These v0.1 CRDs no longer exist: `KusoPipeline`, `KusoApp`, `KusoBuild`, every `KusoAddon*` per-kind chart (one polymorphic `KusoAddon` replaces them), `KusoMail`, `KusoPrometheus`. The pipeline/app concept is removed from the UI, server API, CLI, and MCP.

## What's new

- **GitHub App integration is mandatory.** Every project references a GitHub App installation id. OAuth sign-in, webhook-driven deploys (push → production rebuild; PR → preview env), repo browser via the Installation API.
- **Auto-detect runtime + port** at service-create time. Server fetches the file tree at `service.repo.path` via the GitHub App and runs detection rules — Dockerfile wins, otherwise nixpacks for known languages, otherwise prompt.
- **Polymorphic `KusoAddon`** with `spec.kind` field selecting one of: postgres, redis, mongodb, mysql, rabbitmq, memcached, clickhouse, elasticsearch, kafka, cockroachdb, couchdb.

## Required reading

- **`docs/REDESIGN.md`** — full spec with CRD schemas, API surface, CLI/MCP shapes, UI flow, phasing. Single source of truth. Diverge from it only by updating it first.

## Phase status

The redesign ships in tracked phases — see TaskList. Phases progress:

0. design doc (✓ if you're reading this skill)
1. operator: new CRDs + helm charts, delete old ones
2. server: project/service/env/addon controllers + new API
3. GitHub App
4. UI rewrite
5. CLI + MCP rewrite
6. preview env controller + TTL
7. end-to-end smoke on real Hetzner cluster

Don't skip ahead. Each phase has dependencies on the previous. Each phase commits + pushes; `main` stays runnable.

## When you make a v0.2 change

- All references in code/docs/skills should say "project / service / environment / addon" — never "pipeline / phase / app".
- New API paths: `/api/projects/...`, `/api/projects/:p/services/...`, `/api/projects/:p/envs/...`, `/api/projects/:p/addons/...`.
- New CLI shape: `kuso project|service|env|addon ...`.
- New MCP tools: `list_projects`, `describe_project`, `bootstrap_project`, `add_service`, `set_service_config`, `deploy_service`, `tail_logs(project, service, env?)`, `troubleshoot_service`, `manage_addon`, `manage_env`, `cluster_health`.

If you find a "pipeline" reference in v0.2+ code, it's either (a) a bug, (b) something we missed during the rewrite, or (c) a comment in code that pre-dates the rewrite — patch it.
