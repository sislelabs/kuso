---
name: crd-architecture
description: Historical — describes the v0.1 CRDs (KusoPipeline / KusoApp / per-addon kinds). v0.2 replaces these wholesale; read .claude/skills/projects-redesign.md first.
---

# kuso CRD architecture (v0.1 — DEPRECATED)

> **NOTE:** This skill describes the v0.1 model. v0.2 is a hard fork that deletes
> these CRDs and replaces them with `KusoProject` / `KusoService` /
> `KusoEnvironment` / polymorphic `KusoAddon`. See
> [`projects-redesign.md`](./projects-redesign.md) and
> [`docs/REDESIGN.md`](../../docs/REDESIGN.md) for the canonical model.
>
> This file is kept for context on what we're migrating *away* from.

kuso is Kubernetes-native. Every concept (app, pipeline, addon) is a CRD reconciled by the operator. There is no separate database for app state — the source of truth lives in `etcd` as Kubernetes objects.

## CRD group

All kuso CRDs live under `application.kuso.sislelabs.com/v1alpha1`.

| Kind            | Helm chart                              | Purpose                                                                        |
| --------------- | --------------------------------------- | ------------------------------------------------------------------------------ |
| `KusoApp`       | `operator/helm-charts/kusoapp`          | A single deployable app. The most-touched CRD.                                 |
| `KusoPipeline`  | `operator/helm-charts/kusopipeline`     | Multi-phase deployment (review/staging/production) tied to a git repo.         |
| `KusoBuild`     | `operator/helm-charts/kusobuild`        | A single build job (clone + build + push image). Created by the server.        |
| `KusoConfig`    | `operator/helm-charts/kuso`             | Cluster-wide kuso config (registries, runpacks, templates).                    |
| `KusoAddon*`    | `operator/helm-charts/kusoaddon<name>`  | Per-addon CRDs: `KusoAddonPostgres`, `KusoAddonRedis`, etc.                    |
| `KusoMail`      | `operator/helm-charts/kusomail`         | Built-in Haraka mail server                                                    |
| `KusoPrometheus`| `operator/helm-charts/kusoprometheus`   | Optional bundled Prometheus stack                                              |

## Reconciliation flow

1. User runs `kuso app deploy myapp` (or hits REST API, or applies YAML).
2. `server/` validates and writes a `KusoApp` CR via the k8s API.
3. `operator/` reconciles the CR via Helm — renders the chart in `operator/helm-charts/kusoapp/` against the CR spec, applies the rendered manifests (Deployment, Service, Ingress, HPA, etc.).
4. App pod starts. Health visible via `kubectl get kusoapp myapp` or `kuso app status myapp`.

## The KusoApp spec (cliffs notes)

Roughly:

```yaml
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoApp
spec:
  buildstrategy: dockerfile | buildpacks | nixpacks
  image: { repository, tag, pullPolicy }
  envVars: [ { name, value } ]   # see secrets gotcha below
  ingress: { enabled, className, hosts, tls }
  autoscaling: { enabled, minReplicas, maxReplicas, targetCPUUtilizationPercentage }
  sleep: enabled | disabled
  cronjobs: [ ... ]
  addons: [ ... ]
  gitrepo: { clone_url, autodeploy }
  branch: main
```

Full schema: `operator/helm-charts/kusoapp/values.yaml` is the canonical source of defaults; the CRD's OpenAPI schema is at `operator/config/crd/bases/`.

## The secrets gotcha

`KusoApp.spec.envVars` only supports plaintext `name + value`. Anything you put there ends up plaintext in `etcd`. There is no `valueFrom: secretKeyRef` support in v0.1.

This is a known gap and is the **C1** workstream in `docs/PRD.md`. Until it lands, the workaround is: create a Kubernetes Secret directly via `kubectl`, then reference it via `envFrom` in the underlying Deployment — but that bypasses the operator's reconciliation, so it'll get clobbered on re-reconcile. Don't do this in production.

## Adding a new CRD field

1. Edit `operator/helm-charts/<chart>/values.yaml` to add the default.
2. Edit the chart templates under `operator/helm-charts/<chart>/templates/` to use the new field.
3. Update the OpenAPI schema in `operator/config/crd/bases/` so kubectl validates it.
4. Wire it through `server/src/<module>/` if the REST API needs to expose it.
5. Add CLI flags in `cli/cmd/kusoCli/` and an MCP tool input in `mcp/` (when MCP exists).

## Adding a new CRD kind

Use `operator-sdk` from inside `operator/`. The PROJECT file lists existing CRDs and their plugins. A new addon CRD looks like one of the existing `kusoaddon*` charts — copy and rename.
