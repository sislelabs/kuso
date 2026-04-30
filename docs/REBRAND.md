# Rebrand notes

kuso is a hard fork of [kubero-dev/kubero](https://github.com/kubero-dev/kubero). The initial rebrand commit renames every `kubero/Kubero/KUBERO` occurrence to the `kuso` equivalent, with a few intentional exceptions documented here.

## Preserved upstream references

The following references still point at `kubero-dev/*` assets because we don't yet host equivalents. They MUST be mirrored under `sislelabs/*` before kuso is used in production.

| Reference                                                          | Used by                                       | Mirror plan                                                              |
| ------------------------------------------------------------------ | --------------------------------------------- | ------------------------------------------------------------------------ |
| `ghcr.io/kubero-dev/fetch`                                          | Build job init container (clone source)       | Build + push as `ghcr.io/sislelabs/kuso-fetch`                           |
| `ghcr.io/kubero-dev/build`                                          | Nixpacks build container                      | Build + push as `ghcr.io/sislelabs/kuso-build`                           |
| `ghcr.io/kubero-dev/run`                                            | Cloud Native Buildpacks runtime base          | Pin a third-party CNB base or build our own                              |
| `ghcr.io/kubero-dev/buildpacks/{fetch,php,...}`                     | Per-language buildpack images                 | Build + push under `ghcr.io/sislelabs/kuso-buildpacks/*`                 |
| `https://raw.githubusercontent.com/kubero-dev/kubero/main/services/`| Service template base path                    | Mirror service catalog into `sislelabs/kuso/main/services/`              |
| `https://raw.githubusercontent.com/kubero-dev/templates/main/...`   | Template index JSON (apps marketplace)        | Fork to `sislelabs/kuso-templates`                                       |
| `git@github.com:kubero-dev/template-nodeapp.git` (and friends)      | Sample app templates referenced by buildpacks | Fork stack starter templates into `sislelabs/kuso-template-*`            |

## CRD migration

The CRD group has changed from `application.kubero.dev` to `application.kuso.sislelabs.com`. Existing `KuberoApp` resources in a cluster are NOT compatible with kuso. There is no automatic migration path in v0.1.

If you need to migrate from a Kubero install to kuso, the manual procedure is:

1. Export each `KuberoApp` to YAML.
2. Edit: `apiVersion: application.kubero.dev/v1alpha1` → `application.kuso.sislelabs.com/v1alpha1`, `kind: KuberoApp` → `kind: KusoApp`.
3. Apply against the kuso operator.

A `kuso migrate from-kubero` CLI command is planned for v0.2.

## Brand attribution

Per GPL-3.0, kuso preserves attribution to the original Kubero authors:

- `LICENSE` (root): GPL-3.0 text, unchanged.
- `NOTICE`: lists the three upstream repos and credits original copyright.
- `README.md`: states up front that kuso is a hard fork of `kubero-dev/kubero`.

These three files are explicitly excluded from the brand-replacement passes and should remain authoritative attribution.
