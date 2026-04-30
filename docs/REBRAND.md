# Rebrand notes

kuso is a hard fork of [kubero-dev/kubero](https://github.com/kubero-dev/kubero). The rebrand commits rename every `kubero/Kubero/KUBERO` occurrence to the `kuso` equivalent, with intentional exceptions documented here.

## Mirror plan: upstream → sislelabs

All references in the kuso codebase already point at the **target** sislelabs paths below. The actual mirror artifacts are not yet created; they will be created the first time we install kuso end-to-end (see task: kind smoke test). Until then, those URLs return 404.

### Container images

| Code currently references                       | Mirrors what (upstream)                      | Action needed                                                                  |
| ------------------------------------------------ | -------------------------------------------- | ------------------------------------------------------------------------------ |
| `ghcr.io/sislelabs/kuso-fetch`                   | `ghcr.io/kubero-dev/fetch`                   | Pull → tag → push, OR build from forked source                                 |
| `ghcr.io/sislelabs/kuso-build`                   | `ghcr.io/kubero-dev/build`                   | Same                                                                           |
| `ghcr.io/sislelabs/kuso-run`                     | `ghcr.io/kubero-dev/run`                     | Same                                                                           |
| `ghcr.io/sislelabs/kuso-buildpacks/fetch`        | `ghcr.io/kubero-dev/buildpacks/fetch`        | Same                                                                           |
| `ghcr.io/sislelabs/kuso-buildpacks/build`        | `ghcr.io/kubero-dev/buildpacks/build`        | Same                                                                           |
| `ghcr.io/sislelabs/kuso-buildpacks/php`          | `ghcr.io/kubero-dev/buildpacks/php`          | Same                                                                           |
| `ghcr.io/sislelabs/kuso-runpacks/<lang>`         | `ghcr.io/kubero-dev/runpacks/<lang>`         | Per-language runtime images (node, python, ruby, …) used in sample CRs         |
| `ghcr.io/sislelabs/kuso-server`                  | `ghcr.io/kubero-dev/kubero` (server image)   | Build from `server/` after first end-to-end test                               |
| `ghcr.io/sislelabs/kuso-operator`                | `ghcr.io/kubero-dev/kubero-operator`         | Build from `operator/`                                                         |

### Git repos

| Code currently references                                              | Mirrors what (upstream)                                  | Action needed                       |
| ---------------------------------------------------------------------- | -------------------------------------------------------- | ----------------------------------- |
| `github.com/sislelabs/kuso-templates`                                  | `github.com/kubero-dev/templates`                        | Fork                                |
| `github.com/sislelabs/kuso-template-nodeapp` (and other `kuso-template-*`) | `github.com/kubero-dev/template-*`                   | Fork each (~13 repos total)         |
| `raw.githubusercontent.com/sislelabs/kuso/main/services/`              | `raw.githubusercontent.com/kubero-dev/kubero/main/services/` | Already inside this repo (`services/`); just needs to exist on `main` |

### How to actually mirror (when ready)

For container images:

```bash
# requires Docker + ghcr login: echo $GH_TOKEN | docker login ghcr.io -u ivo9999 --password-stdin
for src in fetch build run buildpacks/fetch buildpacks/build buildpacks/php; do
  dst="${src/buildpacks\//kuso-buildpacks/}"
  dst="${dst/#/sislelabs/kuso-}"
  # …pull, tag, push…
done
```

For git repos:

```bash
gh repo create sislelabs/kuso-templates --public --description "kuso application templates index" --source=...
# or: gh repo fork kubero-dev/templates --org sislelabs --clone=false --fork-name=kuso-templates
```

## CRD migration

The CRD group has changed from `application.kubero.dev` to `application.kuso.sislelabs.com`. Existing `KuberoApp` resources in a cluster are NOT compatible with kuso. There is no automatic migration path in v0.1.

Manual migration:
1. Export each `KuberoApp` to YAML.
2. Edit: `apiVersion: application.kubero.dev/v1alpha1` → `application.kuso.sislelabs.com/v1alpha1`, `kind: KuberoApp` → `kind: KusoApp`.
3. Apply against the kuso operator.

A `kuso migrate from-kubero` CLI command is planned for v0.2.

## Brand attribution

Per GPL-3.0, kuso preserves attribution to the original Kubero authors:

- `LICENSE` (root): GPL-3.0 text, unchanged.
- `NOTICE`: lists the upstream repos and credits original copyright.
- `README.md`: states up front that kuso is a hard fork of `kubero-dev/kubero`.

These three files are explicitly excluded from the brand-replacement passes and remain authoritative attribution.
