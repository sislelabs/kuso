# Schema migration — releasing CRD changes safely

kuso bundles three things in every release:

1. **kuso-server image** — pushed to `ghcr.io/sislelabs/kuso-server-go:<tag>`.
2. **kuso-operator image** — pushed to `ghcr.io/sislelabs/kuso-operator:<tag>`.
3. **crds.yaml asset** — concatenation of every CRD YAML under
   `operator/config/crd/bases/`, attached to the GitHub release.

The in-cluster updater (`kuso upgrade` / dashboard Update button /
auto-update) reads `release.json`, downloads `crds.yaml`, and **does**
`kubectl apply` it before flipping the deployment image. So the
release flow is normally:

```
make ship VERSION=vX.Y.Z
# … live instances poll, see the new release, the updater Job
#    applies crds.yaml, then rolls the deployment image.
```

You don't normally need to ssh in.

## When the gate fires

If something went wrong — the updater Job's `kubectl apply` failed,
or you ran a `kubectl set image` by hand without applying CRDs
first — the new kuso-server boots, detects the CRD doesn't yet
carry the field it expects to write, and **comes up in degraded
mode**: readyz returns unready, /api/* writes return 503 with a
"kuso CRDs are stale — re-apply ... then restart" body, and the
operator gets a banner in the SPA. The pod logs say "starting in
degraded mode: readyz=unready, writes refused"; the cluster keeps
serving GET / read paths so users can log in and see the message.

To recover:

```
ssh root@<cluster> "kubectl apply -f /tmp/<crd>.yaml"
ssh root@<cluster> "kubectl -n kuso rollout restart deployment/kuso-server"
```

After the restart the schema preflight passes and the gate clears.

## Override (don't)

`KUSO_ALLOW_STALE_CRDS=true` in the server's environment skips the
gate entirely. This exists for development / one-off recovery only.
A production deploy running with stale CRDs will see writes silently
pruned by the apiserver — the symptom is "I saved a setting and it
didn't stick," with no error trail in audit. **Never set this in a
real cluster.**

## What counts as a schema change

Anything that adds, removes, or changes a field path under
`spec:`, `status:`, or the top-level metadata stanzas of one of:

- `KusoProject`
- `KusoService`
- `KusoEnvironment`
- `KusoAddon`
- `KusoBuild`

Renames are the most dangerous (one field disappears, a different
one appears with no clean cutover). Additions are safest (old
servers ignore them; new server writes them; the apiserver prunes
on the way in only when the schema is older than the writer).

## The pre-flight, briefly

`kuso-server/main.go` calls `kube.CheckSchemas(ctx, nil)` at boot.
For each kind it expects, it compares the live CRD's openAPI
schema against an embedded baseline (Go-side struct field paths).
Missing fields → `serverstate.SetCRDStale(...)` → readyz unready
+ write middleware refuses /api/* mutations.

This is **boot-time only**. A schema rollback while kuso-server is
already up won't be detected until the next restart. If you yank
a CRD, restart the deployment afterward.

## Release checklist for schema changes

`hack/release.sh` bundles `dist/crds.yaml` and the updater applies
it. But you should still:

1. Diff `operator/config/crd/bases/` against the last shipped tag
   to know whether you're actually shipping a schema change.
2. If you are, mention it in the release notes / changelog — kuso's
   `release.json.crds.migrations` array is still empty by default
   (additive migrations don't need a recipe; destructive ones do).
3. After `make ship`, confirm at least one live cluster has rolled
   forward cleanly and `readyz` is green. A stuck stale-CRDs banner
   on the SPA is the canary.

The release-roll loop applies CRDs first, image second. If you're
doing a hand-roll (`kubectl set image` directly), apply CRDs first
yourself.
