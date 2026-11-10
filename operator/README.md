# kuso operator

A [helm-operator](https://sdk.operatorframework.io/docs/building-operators/helm/) (operator-sdk, `helm-operator` v1.42 base image). There is no Go code here: `watches.yaml` maps each CR kind to a chart, and the operator renders and upgrades one Helm release per CR.

| Path | What |
| --- | --- |
| `watches.yaml` | Watched kinds (KusoProject, KusoService, KusoEnvironment, KusoAddon, KusoCron, KusoRun) and their reconcile periods. |
| `helm-charts/` | One chart per kind. `kusobuild/` and `kuso/` are not in `watches.yaml`. |
| `config/crd/bases/` | CRDs for group `application.kuso.sislelabs.com`, including KusoBuild and the instance-config `Kuso` CR. |
| `Dockerfile` | Copies `watches.yaml` and `helm-charts/` into the helm-operator image. |

KusoBuild is not reconciled here. The `buildcontroller` in `server-go/internal/buildcontroller` turns builds into Jobs.

## Deploy

`hack/install.sh` applies the CRDs first, then `deploy/operator.yaml` (namespace `kuso-operator-system`, Deployment `kuso-operator-controller-manager`). Concurrency comes from the `--max-concurrent-reconciles` flag in that manifest; the per-watch keys in `watches.yaml` are ignored. Upgrades go through the kuso updater (`kuso upgrade`), not a manual apply.

## Checks

- `make chart-check` at the repo root: chart/CRD text checks.
- `hack/smoke/crd-dryrun.sh`: applies every CRD to a kind cluster and dry-runs the samples.
- CRD changes must stay additive; see "Release flow" in the root `CLAUDE.md`.
