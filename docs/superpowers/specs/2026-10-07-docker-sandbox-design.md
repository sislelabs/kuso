# Docker sandbox — design

**Date:** 2026-10-07
**Status:** implemented
**Author:** ivo (with Claude)

## Goal

One command starts a disposable kuso on a laptop, built from the working tree, with Docker
as the only host requirement. You and agents drive it with the normal `kuso` CLI, so e2e
testing no longer needs the live instance. A scripted smoke test runs against it headless,
and CI reuses that script later.

Success looks like this:

- `make sandbox-up` on an arm64 Mac with only Docker installed ends with a working login.
- A dockerfile service and a nixpacks service build from a public git repo, deploy, and
  answer `curl` from the laptop.
- A Postgres addon comes up and a `${{ addon.KEY }}` env ref resolves in a service.
- After a server-go change, `make sandbox-reload` has the new code running without a full
  rebuild of the cluster.
- `make sandbox-down` leaves no containers or volumes behind.
- Nothing the sandbox does can touch the live instance or its CLI credentials.

## What exists today

- **No dockerized kuso.** `hack/smoke/crd-dryrun.sh` starts kind and applies CRDs and sample
  CRs only. `docs/AGENT_SMOKE_TEST.md` is a manual 16-step protocol against the live
  instance. There is no scripted e2e.
- **`hack/install.sh` targets a real host.** It needs root and systemd, fetches every
  manifest from `raw.githubusercontent.com`, and rewrites image tags but never image
  repositories.
- **kuso images are amd64-only.** `hack/release.sh` builds server, operator, updater,
  nixpacks and env-detect with `--platform linux/amd64`. The server, operator, updater and
  backup Dockerfiles have no arch assumptions and build natively on arm64.
  `quay.io/operator-framework/helm-operator:v1.42` publishes arm64.
- **Two builder Dockerfiles are missing.** `release.sh` builds from `build/nixpacks/` and
  `build/env-detect/`, but `.gitignore` (`/build/*`) excludes both and neither is on disk.
  `release.sh` skips the build while the tag exists on ghcr, so this has not failed yet.
- **buildkitd cannot schedule on one node.** `deploy/buildkitd.yaml` requires
  `node-role.kubernetes.io/control-plane DoesNotExist`.
- **The registry host is a constant.** `builds.RegistryHost` is
  `kuso-registry.kuso.svc.cluster.local:5000`, plain HTTP. Kubelet reaches it through
  `/etc/rancher/k3s/registries.yaml` plus an `/etc/hosts` line for the Service ClusterIP.
- **Builder image refs are constants** in `buildcontroller.go` with no env override.
- **No hard dependency on GitHub App, DNS or Let's Encrypt.** With no installation the
  build clones anonymously and uses a synthetic `<branch>-<ts>` ref. The environment chart
  skips the TLS block for reserved TLDs, `.localhost` included.
- **The CLI keeps its config under `$HOME/.kuso/`** and has no other override.

## Alternatives considered

- **Run the real `install.sh` inside the node container.** Highest fidelity and it would
  test the installer, but it adds code paths to the script every user runs. Rejected for
  this version.
- **kind.** Already installed, but its registry config, CNI and storage differ from
  production, it has no servicelb, and it needs a host binary.
- **Chosen: a k3s node container plus a dev-only bootstrap script.** k3s matches
  production (kube-router NetworkPolicy, local-path storage, `registries.yaml`). The cost is
  a second copy of the install sequence, covered by the drift check below.

## Design

### Commands

| Command               | What it does                                                         |
| --------------------- | -------------------------------------------------------------------- |
| `make sandbox-up`     | Build images, start the node, run bootstrap, print URL and login     |
| `make sandbox-reload` | Rebuild server and/or operator image, import, restart the deployment |
| `make sandbox-smoke`  | Run the scripted e2e against a running sandbox                       |
| `make sandbox-down`   | Remove containers, volumes and `.state/`                             |

`sandbox-up` is safe to re-run: it rebuilds changed images and re-applies manifests.

### Files

All new, under `hack/sandbox/`:

- `compose.yaml` — two services:
  - `node`: pinned `rancher/k3s` image, privileged, `server --disable=traefik`,
    `registries.yaml` mounted before k3s starts, Traefik's port published on the host.
    Kubelet's eviction thresholds are lowered to 1% because the node shares the Docker
    VM's disk.
  - `tools`: `alpine/k8s` (kubectl and helm), working tree mounted read-only, shares the
    node's kubeconfig through a volume. Runs on demand, not as a daemon.
- `registries.yaml` — the same mirror entry `install.sh` writes.
- `up.sh`, `down.sh`, `reload.sh`, `lib.sh` — host-side orchestration.
- `bootstrap.sh` — runs inside `tools`.
- `manifests.sh` — the applied and skipped lists; `check-drift.sh` checks them.
- `smoke.sh` — the e2e.
- `kuso` — wrapper that runs the CLI built from the tree with the isolated `HOME`.
- `.state/` — gitignored. Holds the kubeconfig, the CLI binary and the isolated CLI home.

Also new: `build/nixpacks/Dockerfile` and `build/env-detect/Dockerfile`, with matching
`!/build/nixpacks/` and `!/build/env-detect/` lines in `.gitignore`.

### Images

`up.sh` builds for the host architecture and loads each image into the node with
`docker save | docker exec -i <node> ctr -n k8s.io images import -`. Nothing is pushed.

| Image      | Source                        | Ref inside the node                            |
| ---------- | ----------------------------- | ---------------------------------------------- |
| server     | `server-go/Dockerfile`, root  | `ghcr.io/sislelabs/kuso-server-go:sandbox`     |
| operator   | `operator/Dockerfile`         | `ghcr.io/sislelabs/kuso-operator:sandbox`      |
| nixpacks   | `build/nixpacks/Dockerfile`   | `ghcr.io/sislelabs/kuso-nixpacks:1.41.0`       |
| env-detect | `build/env-detect/Dockerfile` | `ghcr.io/sislelabs/kuso-env-detect:v1`         |

- Server and operator manifests get their tag rewritten to `:sandbox` and their pull policy
  set to `IfNotPresent` at apply time. The activator uses the server image.
- The two builder images are imported under the exact refs the build controller hardcodes,
  so no server code changes. The versions come from the constants in `buildcontroller.go`,
  not from a second copy in the script.
- The two missing Dockerfiles are reconstructed from the published images' layer history
  and the comments in `release.sh` (nixpacks binary; ripgrep and jq on alpine).

### What bootstrap applies

In `install.sh`'s order:

1. `kuso-platform` PriorityClass.
2. Traefik by helm, with the values `install.sh` uses, in namespace `traefik`.
3. CRDs from `operator/config/crd/bases/`.
4. `deploy/registry.yaml`, then the `/etc/hosts` line inside the node.
5. `deploy/buildkitd.yaml` with the control-plane anti-affinity removed.
6. Postgres for kuso's own database, and the `kuso-postgres-conn` Secret with its `dsn`.
7. `kuso-server-secrets` and `kuso-admin-credentials`, using the same well-known values
   as `KUSO_INSECURE_SECRETS=1` (admin password `kuso-admin`).
8. `deploy/operator.yaml`.
9. `deploy/server-go.yaml` with `KUSO_DOMAIN=kuso.localhost` and `KUSO_UPDATER_DISABLED`.
10. `deploy/kuso-activator.yaml`.
11. The `kuso-server` Service and a plain-HTTP Ingress for `kuso.localhost`.

Each step waits for readiness and fails with the resource's events and logs.

**Skipped, each listed by name in `bootstrap.sh`:** cert-manager and the Let's Encrypt
issuers, `prometheus.yaml`, `pkg-probe.yaml`, `postgres-backup.yaml`, the incident agent
and bot manifests, `cluster-issuer.yaml`, `review-ingress.yaml`, `hello-world.yaml`, the
GitHub App secret.

**Deliberate divergence:** kuso's own database is a single `postgres:16` Deployment, not
the CloudNativePG cluster in `deploy/postgres.yaml`. The control-plane database is not what
the sandbox tests, and skipping the CNPG operator shortens cold start.

### Networking

- `KUSO_DOMAIN=kuso.localhost`. The API is `http://kuso.localhost:<port>` and services land
  at `http://<service>.<project>.kuso.localhost:<port>`.
- The host port defaults to 18080 and is set by `KUSO_SANDBOX_PORT`. It is published on
  both `127.0.0.1` and `[::1]`: `*.localhost` resolves to `::1` first on macOS, and with an
  IPv4-only bind another process on `[::1]:<port>` answered the CLI with a 404.
- Everything is plain HTTP. There is no cert-manager, and the chart emits no TLS block for
  `.localhost` hosts.

### Isolation from the live instance

- Every CLI call the scripts make runs with `HOME=hack/sandbox/.state/home`, so
  `~/.kuso/kuso.yaml` and `~/.kuso/credentials.yaml` are never read or written.
- `up.sh` prints the `HOME=… kuso login …` line for manual use.
- `smoke.sh` refuses to start unless the API host is `kuso.localhost`.
- The kubeconfig lives only in `.state/`. Nothing writes to `~/.kube/config`.

### Drift check

`bootstrap.sh` holds two lists: manifests applied and manifests skipped. A script wired
into `make verify` fails when a file in `deploy/*.yaml` is in neither list. A new manifest
then forces a decision. It does not catch a change in ordering or secrets inside
`install.sh`; those still need a manual port.

### Smoke flow

1. Log in as `admin`.
2. Create a project from `https://github.com/ivo9999/kuso-demo-todo-api`.
3. Add one service with `--runtime dockerfile` and one with `--runtime nixpacks`.
4. Trigger both builds and wait for them to succeed.
5. `curl` both services through Traefik and check `/healthz`.
6. Add a Postgres addon, set `DATABASE_URL` to a `${{ addon.KEY }}` ref, write a row through
   one service and read it through the other.
7. Trigger a second build, then roll back to the first.
8. Delete the project and confirm its namespace objects are gone.

The script exits non-zero on the first failure and prints `kuso` output for the failing
step. The demo repo has a Dockerfile and also builds with nixpacks, so both services use
it. `service add` starts the first build itself; the script follows that build.

## Spike results

Measured on an arm64 Mac, Docker Desktop, 8 GB VM, 2026-10-07.

1. Privileged buildkitd with overlayfs runs inside the k3s container. A dockerfile build
   and a nixpacks build both finish natively on arm64.
2. The kuso CLI resolves `*.kuso.localhost` on macOS, to `::1` first (see Networking).
3. Build Jobs set no pull policy, so the imported builder images are used.
4. The idle platform uses about 0.8 GB and about 1 GB during the smoke test. Disk is the
   real constraint: with 3.5 GB free in the Docker VM the first builds filled it and
   kubelet evicted every pod. Builds need several GB free.

## Not in this version

- The GitHub Actions workflow. It follows once `sandbox-smoke` is stable locally.
- PR previews and webhooks (they need a GitHub App).
- Addon backups (they need S3, and `kuso-backup:latest` would pull amd64).
- Multi-node clusters, the upgrade path and the updater.
- Testing `install.sh` itself.
- A hermetic in-cluster git server. The smoke test needs internet to clone.
- Buildpacks and static runtimes in the smoke test. They may work; they are not verified.
