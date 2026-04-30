---
name: rebrand-conventions
description: Use when adding new code, renaming things, or auditing leftover kubero strings. Defines the canonical kuso naming so the agent doesn't accidentally reintroduce upstream brand names.
---

# kuso naming conventions

kuso forked kubero-dev/kubero with a wholesale rebrand. Anywhere you'd be tempted to write "kubero" — don't. Use these forms instead.

## Canonical replacements

| Upstream                           | kuso                                       | Where it appears                            |
| ---------------------------------- | ------------------------------------------ | ------------------------------------------- |
| `kubero` (lowercase identifier)    | `kuso`                                     | Code, package names, binary, helm charts    |
| `Kubero` (capitalized brand)       | `Kuso`                                     | Type names, UI strings, comments            |
| `KUBERO` (env var prefix, etc.)    | `KUSO`                                     | Env vars, constants                         |
| `KuberoApp`                        | `KusoApp`                                  | Operator CRD                                |
| `KuberoPipeline`                   | `KusoPipeline`                             | Operator CRD                                |
| `KuberoBuild`                      | `KusoBuild`                                | Operator CRD                                |
| `KuberoConfig`                     | `KusoConfig`                               | Operator CRD                                |
| `application.kubero.dev`           | `application.kuso.sislelabs.com`           | CRD group                                   |
| `kubero.dev`                       | `kuso.sislelabs.com`                       | Domain references in code/docs              |
| `KUBERO_*` env vars                | `KUSO_*`                                   | Env vars                                    |
| `kubero-cli` / `kubero-operator`   | `kuso-cli` / `kuso-operator`               | Component names                             |
| `ghcr.io/kubero-dev/kubero`        | `ghcr.io/sislelabs/kuso-server`            | Server image                                |
| `ghcr.io/kubero-dev/kubero-operator` | `ghcr.io/sislelabs/kuso-operator`        | Operator image                              |

## What stays as `kubero-dev`

Some upstream URLs are deliberately preserved because we don't host equivalents yet. **Don't replace these without checking `docs/REBRAND.md`:**

- `ghcr.io/kubero-dev/buildpacks/*`
- `ghcr.io/kubero-dev/{fetch,build,run}`
- `raw.githubusercontent.com/kubero-dev/templates/*`
- `raw.githubusercontent.com/kubero-dev/kubero/main/services/*`
- `git@github.com:kubero-dev/template-*`

## What stays as `Kubero`

Three files at the repo root preserve attribution to the upstream authors per GPL-3.0:

- `README.md` (the "forked from" paragraph)
- `LICENSE`
- `NOTICE`

Do **not** rename `Kubero` to `Kuso` in those three files.

## When you're unsure

If you find a `kubero` reference somewhere and aren't sure whether to replace it:

1. Check `docs/REBRAND.md` — is it on the preserved-upstream-asset list?
2. Is the file in `{LICENSE, NOTICE, README.md}` at the root?
3. Is it inside a third-party dependency (`node_modules/`, `vendor/`)?

If "no" to all three, it's a leftover and should be renamed.
