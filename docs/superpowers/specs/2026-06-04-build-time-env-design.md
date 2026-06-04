# Build-time env injection

**Date:** 2026-06-04
**Status:** approved, implementing

## Problem

kuso passes no env vars to builds — env is runtime-only (envFrom secretKeyRef +
literal env on the deployment). Coolify baked all env in at build
(is_buildtime:true). So apps whose `npm run build` reads env fail to build:
Prisma `generate`/config needs DATABASE_URL; Next.js compiles NEXT_PUBLIC_* into
the client bundle and validates env at build. Confirmed on db-masterclass,
produktche, ilikata, bukvite30 (`PrismaConfigEnvError: Missing DATABASE_URL`).

## Approach (approved)

Inject ALL service env into the build, matching Coolify. Values bake into image
layers in the private in-cluster registry — documented exposure, acceptable for
single-tenant. Resolve server-side; the build pod stays token-less.

1. **Resolve at build-trigger (server).** When builds.Create makes the
   KusoBuild CR, gather the service's effective env: literals verbatim;
   `${{ <addon|svc>.KEY }}` refs resolved to literal values via the existing
   RewriteEnvVars + cluster reads. Unresolvable refs (addon conn secret not
   materialized yet) are OMITTED, never fatal. Put resolved KEY=VALUE on a new
   `KusoBuild.spec.buildEnv` (map[string]string).
2. **Inject via the existing mechanism.** kusobuild's job.yaml already prepends
   `ENV KEY=VALUE` after the FROM line of the nixpacks-generated Dockerfile (and
   passes `nixpacks --env`) for toolchain hints. Extend that loop to also emit
   the buildEnv pairs. Dockerfile builds: same ENV-after-FROM injection. So
   build-time process.env.* / NEXT_PUBLIC_* resolve.
3. **Build pod unchanged** — automountServiceAccountToken:false; it never reads
   secrets. Values arrive pre-resolved on the CR.

## Changes

1. `internal/kube/types.go`: `KusoBuildSpec.BuildEnv map[string]string` +
   CRD `spec.buildEnv` (additionalProperties string) + golden refresh.
2. `internal/builds/builds.go`: in Create, resolve service env → buildEnv on the
   CR. New helper `resolveBuildEnv(ctx, project, service)`.
3. `operator/helm-charts/kusobuild/templates/job.yaml`: emit `.Values.buildEnv`
   as ENV-after-FROM lines (+ nixpacks --env), reusing the toolchain block.
   Values shell-escaped.
4. Operator image rebuild (chart change) + CRD apply + ship.

## Edge cases / safety

- Values with spaces/newlines/quotes: escape when writing `ENV` lines (single
  ENV per key, value quoted). Keys validated `[A-Za-z_][A-Za-z0-9_]*`.
- Unresolvable `${{ ref }}`: omit (logged), don't fail the build.
- Reserved kuso/kubelet vars (PORT etc.) excluded — same RESERVED list logic.
- Documented: build-time values bake into image layers; rotate if registry
  exposed. No new secret grant to the build pod.

## Testing

- Go TDD: resolveBuildEnv returns literals + resolved refs, omits unresolvable,
  excludes reserved. Populates KusoBuild.spec.buildEnv.
- helm-template golden: buildEnv renders ENV lines after FROM; empty buildEnv
  renders nothing (regression).
- CRD golden refresh.
- Live: db-masterclass + produktche rebuild SUCCEED with DATABASE_URL present;
  apps deploy + serve.

## Non-goals

- Opt-in per-var build-time flag (approved: inject all).
- Build secrets via BuildKit --secret (mounts, not baked) — future hardening.
