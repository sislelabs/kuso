# Per-environment branch tracking for push builds

**Date:** 2026-07-04
**Status:** approved

## Problem

A `KusoEnvironment` can declare `spec.branch` (e.g. the `staging` env sets
`branch: staging`), and the build **promotion** side already routes a build's
image to the env whose `spec.branch` matches the build's branch
(`builds.go` `promoteImage`, the `e.Spec.Branch != b.Spec.Branch` skip).

But the **dispatcher trigger** side never creates a build for a non-default
branch. On a push it builds a service only when the pushed branch equals the
service's *effective default branch* (`serviceEffectiveRepo`, falling back to
the project's `defaultRepo.defaultBranch`, i.e. `main`):

    // dispatcher.go, push handler
    svcRepo, svcBranch := serviceEffectiveRepo(&raw.Items[i], &proj)
    if !repoMatches(svcRepo, repoFullName) || branch != svcBranch {
        continue
    }

So a push to `staging` is dropped: no build, no promotion. The staging env
sits frozen on whatever image it last got (observed on project `scubatony`:
staging pinned to an old opaque `staging-*` tag while production tracked
`main` correctly).

## Fix (one-sided: dispatcher only)

Widen the per-service trigger gate. Build the service when the pushed branch
is **either**:

- the service's effective default branch (unchanged behavior), **or**
- a branch that one of the service's persistent `KusoEnvironment`s tracks
  (`env.spec.branch == pushed branch`).

The promotion side already does the right thing once the build exists — it
lists all envs for the service and patches the one whose branch matches. No
change there.

### Implementation

In the push handler, after listing the project's services (already done),
also list the project's environments once and build a map:

    service (short name) → set of non-empty env branches

Then the gate becomes:

    svcRepo, svcBranch := serviceEffectiveRepo(&raw.Items[i], &proj)
    if !repoMatches(svcRepo, repoFullName) {
        continue
    }
    if branch != svcBranch && !envBranches[short][branch] {
        continue
    }

Env branch collection reuses the existing `GVREnvironments` list + the
`LabelProject`/`LabelService` labels. One env List per project (not per
service).

`req.Branch` is already set to the pushed branch and flows into
`b.Spec.Branch`, which drives correct promotion. Nothing else in the build
request changes.

### Config-as-code stays default-branch-only (image only)

The config-as-code block (`d.Reconciler.Apply` from the repo's `kuso.yaml`)
currently runs before the build loop for any push that matched a service
repo. Once we start creating builds for non-default branches, that block
would begin applying `staging`'s `kuso.yaml` — which could rewrite
production-shared project/service settings. Per the decision, non-default
branches deploy the **image only**.

Guard the config-as-code block so it runs only when the pushed branch equals
the project's default branch (`proj.Spec.DefaultRepo.DefaultBranch`, default
`main`). A `staging` push builds + promotes its image but does not touch
config.

## Edge cases

- **Env with empty `spec.branch`** — not added to the branch set, so it never
  widens the gate. Falls back to the default-branch match (unchanged).
- **Multi-repo projects** — the repo match still gates first; an env branch
  only widens the branch check for services on the matched repo.
- **PR previews** — unaffected; they flow through the separate
  `onPullRequest` path, not this push gate.
- **Two envs, same branch** — `promoteImage` already promotes to all matching
  envs; a single build correctly rolls both.

## Testing

Extend `dispatcher_test.go`:

1. Push to a non-default branch that a persistent env tracks → a build is
   created for that service with `Branch == <that branch>`.
2. Push to a branch no env tracks and that isn't the default → no build
   (regression guard so we don't build every random branch push).
3. Push to the default branch → unchanged (build created, config-as-code
   runs).
4. Push to a non-default tracked branch → config-as-code does NOT apply.

## Out of scope

- Per-environment `kuso.yaml` / config-as-code on non-default branches.
- Auto-creating a staging env from a branch (envs must already exist).
- Any change to promotion, preview, or manual `build trigger` paths.
