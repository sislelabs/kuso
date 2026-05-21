# kuso — Fix: addon-add doesn't wire conn secret into existing services — Design

**Date:** 2026-05-21
**Status:** Approved
**Repo:** kuso (`/Users/sisle/code/work/kubero-setup/kuso`)

## The bug

`addons.Add` (`server-go/internal/addons/addons.go`) creates a
`KusoAddon` CR and then calls `RefreshEnvSecrets(ctx, project)` to wire
the new addon's `<name>-conn` secret into every service's
`spec.envFromSecrets`:

```go
created, err := createAddon(ctx, s, ns, addon)   // creates the KusoAddon CR
...
if err := s.RefreshEnvSecrets(ctx, project); err != nil { ... }
```

`RefreshEnvSecrets` enumerates the project's addons via `s.List()`,
which runs a **label-selector list query** (`ListKusoAddonsByLabels`).
Kubernetes serves list queries from an eventually-consistent watch
cache — the addon CR created microseconds earlier by `createAddon` is
frequently **not yet visible** in the cache when the list runs. The
refresh then builds the `envFromSecrets` list *without* the new addon's
conn secret and merge-patches every env CR, wholesale-replacing the
list.

Net effect: existing services never get `<addon>-conn` in their
`envFromSecrets`, so the addon's connection env vars
(`REDIS_URL` / `DATABASE_URL` / etc.) never reach their pods. Services
created *after* the addon are unaffected — `ConnSecretsForProject` runs
later, once the cache has caught up.

Observed: provisioning a `distill-cache` Redis addon left all four
distill services (`web`, `api`, `worker`, `bot`) without
`distill-cache-conn` — `REDIS_URL` absent on every pod, the dependency
graph drew no addon→service edge, and distill's rate limiting silently
ran in fail-open (no-limiting) mode.

## The fix

Make the post-create refresh include the just-created addon's conn
secret **explicitly**, independent of when the watch cache catches up.

### `RefreshEnvSecrets` — split into a public delegate + variadic core

In `server-go/internal/addons/addons.go`:

- `RefreshEnvSecrets(ctx, project) error` — keeps its current public
  signature so every existing caller (notably the delete path at
  `addons.go:477`) is unaffected. It delegates:
  ```go
  func (s *Service) RefreshEnvSecrets(ctx context.Context, project string) error {
      return s.refreshEnvSecrets(ctx, project)
  }
  ```
- `refreshEnvSecrets(ctx, project string, extraConnSecrets ...string) error`
  — the existing `RefreshEnvSecrets` body, moved here, with one change:
  after building `baseSecrets` from the listed addons' conn secrets and
  `kube.SharedSecretNames(project)`, it unions in `extraConnSecrets`,
  de-duplicated.

The de-dup: a `seen` set (or `slices.Contains` guard) so an
`extraConnSecrets` entry that the label-list *did* happen to return is
not added twice. `baseSecrets` must contain each conn-secret name at
most once.

### `addons.Add` — pass the new addon's conn secret explicitly

`createAddon` returns the created `KusoAddon` (`created`) — its `.Name`
is known with certainty, no list required. The line-264 call changes
from:
```go
if err := s.RefreshEnvSecrets(ctx, project); err != nil {
```
to:
```go
if err := s.refreshEnvSecrets(ctx, project, connSecretName(created.Name)); err != nil {
```
Even if `s.List()` inside the refresh misses the new addon, the
explicit `extraConnSecrets` argument guarantees `<addon>-conn` lands in
`baseSecrets` and therefore in every env's patched `envFromSecrets`.

### What is NOT changed

- The **delete path** (`addons.go:477`, `RefreshEnvSecrets(ctx, project)`)
  is left as-is. A stale list on delete means a just-removed addon's
  conn secret lingers in `envFromSecrets` for one extra reconcile — the
  secret itself is gone so the env var is simply unset; harmless and
  self-healing on the next refresh. No "extra" arg is needed there.
- No CRD, helm chart, or API-shape change.
- `RefreshEnvSecrets`'s public signature is preserved — external
  callers and the delete path keep compiling and behaving identically.

## Components

- `server-go/internal/addons/addons.go`
  - `RefreshEnvSecrets` — becomes a thin delegate.
  - `refreshEnvSecrets` — NEW unexported variadic core (the moved body
    + the `extraConnSecrets` union with de-dup).
  - `Add` — its `RefreshEnvSecrets` call becomes a `refreshEnvSecrets`
    call passing `connSecretName(created.Name)`.
- A short comment on `refreshEnvSecrets` documenting the
  read-after-write cache hazard, so a future maintainer does not
  reintroduce a pure list-and-rebuild.

## Testing

`server-go` has a Go test suite; `addons_test.go` already covers
`RefreshEnvSecrets`. Add a test that reproduces the race:

- Set up a fake kube client whose addon **list** does NOT return a
  particular addon (simulating watch-cache lag), but the project has
  service env CRs.
- Call `refreshEnvSecrets(ctx, project, "newaddon-conn")`.
- Assert every patched env's `envFromSecrets` **includes
  `newaddon-conn`** — proving the explicit arg closes the race even
  when the list is stale.
- Also assert no duplicate: if the list *does* return the addon,
  `newaddon-conn` appears exactly once.

Run `go test ./...` from `server-go/`.

## Rollout

This is a kuso `server-go` change. It ships as a kuso release
(`./hack/release.sh vX.Y.Z`) and a cluster upgrade
(`kuso upgrade --version vX.Y.Z`) — the same path as the v0.13.11 /
v0.13.12 `RefreshEnvSecrets` secret-drop fixes. After the upgrade, a
*new* `kuso project addon add` correctly wires the conn secret into all
existing services with no manual env-CR patching.

distill's already-provisioned `distill-cache` addon is already manually
wired (its four env CRs were patched by hand) — the fix prevents the
*next* occurrence; it does not need to retroactively re-run for distill.

## Risks & Mitigations

- **Delete-path staleness** — accepted and explained above; self-heals.
- **Variadic signature** — `refreshEnvSecrets(ctx, project)` with zero
  extras behaves identically to today's `RefreshEnvSecrets` for every
  non-Add caller; the public `RefreshEnvSecrets` delegate guarantees
  no external behavior change.
- **Third fix to this function this session** — the prior two
  (v0.13.11/12) addressed *dropping* shared/per-service secrets via the
  wholesale merge-patch; this one addresses *missing* a secret due to
  read-after-write. Different root cause. The new comment flags the
  cache hazard distinctly so the three concerns stay legible.

## Success Criteria

- `refreshEnvSecrets` includes an explicitly-passed conn secret in
  every env's `envFromSecrets` even when the addon label-list is stale.
- `addons.Add` wires the new addon's conn secret into every existing
  service's env CR deterministically.
- No conn-secret appears twice in any `envFromSecrets`.
- `RefreshEnvSecrets`'s public signature and the delete path are
  unchanged.
- `go test ./...` passes, including the new race-reproduction test.
