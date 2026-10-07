# kuso sandbox

A disposable single-node kuso in Docker, built from the working tree. Use it to test
changes end to end without touching a live instance.

Host requirements: Docker, `curl`, and `jq` for the smoke test. Go is used to build the CLI
when it is installed; without it the CLI is cross-compiled in a container.

## Commands

| Command               | What it does                                                    |
| --------------------- | --------------------------------------------------------------- |
| `make sandbox-up`     | Build images, start k3s in Docker, install kuso, log the CLI in |
| `make sandbox-reload` | Rebuild server and operator, restart them                       |
| `make sandbox-smoke`  | Scripted end-to-end check                                       |
| `make sandbox-down`   | Delete the containers, volumes and `hack/sandbox/.state/`       |

`make sandbox-reload TARGET=server` (or `TARGET=operator`) reloads one of the two.
`make sandbox-up` is safe to re-run. Run it again after the node container or Docker
restarts: Docker regenerates the node's `/etc/hosts`, and image pulls from the in-cluster
registry fail until `up` writes the registry entry back.

## Using it

```sh
make sandbox-up
hack/sandbox/kuso get projects
hack/sandbox/kuso project create demo --repo https://github.com/ivo9999/kuso-demo-todo-api
hack/sandbox/kuso service add demo api --runtime dockerfile --port 8080
curl http://api.demo.kuso.localhost:18080/healthz
```

- UI and API: `http://kuso.localhost:18080`, login `admin` / `kuso-admin`.
- Services: `http://<service>.<project>.kuso.localhost:18080`. Don't pass `--domain` to
  `project create`; a project base domain must be a public one.
- `hack/sandbox/kuso` is the CLI built from this tree. It runs with
  `HOME=hack/sandbox/.state/home`, so it never reads or writes `~/.kuso`. Use it for
  everything aimed at the sandbox, and your normal `kuso` for real instances.
- kubectl: `KUBECONFIG=hack/sandbox/.state/kubeconfig kubectl get pods -A`, or
  `docker exec kuso-sandbox-node kubectl get pods -A` with no kubectl on the host.

`KUSO_SANDBOX_PORT` (default 18080) and `KUSO_SANDBOX_API_PORT` (default 16443) change the
host ports. Set them the same way for every command.

## What is inside

One privileged `rancher/k3s` container with traefik disabled, like a real install.
`bootstrap.sh` then applies, from the working tree: traefik, the CRDs, the registry,
buildkitd, the operator, the server and the activator.

Images are built for the host architecture and loaded straight into the node, so this works
on arm64 even though release images are amd64-only. The nixpacks and env-detect builder
images are loaded under the exact names the build controller hardcodes.

## How it differs from a real install

- **Plain HTTP everywhere.** No cert-manager and no Let's Encrypt.
- **kuso's own database is a plain `postgres:16`**, not the CloudNativePG cluster.
- **buildkitd runs on the control-plane node.** The manifest forbids that; the sandbox
  removes the rule because it has one node.
- **Kubelet eviction thresholds are lowered to 1%.** The node shares the Docker VM's disk.
- **Not installed:** prometheus, pkg-probe, postgres-backup, the incident agent and bot,
  the GitHub App, the updater. `manifests.sh` has the list.
- **Not testable here:** PR previews and webhooks, addon backups, the upgrade path,
  multi-node behaviour, `hack/install.sh` itself.

`bootstrap.sh` is a second copy of `install.sh`'s sequence. `make verify-sandbox` fails when
a file is added to `deploy/` without being listed in `manifests.sh`. A change to ordering or
secrets inside `install.sh` still has to be ported by hand.

## Smoke test

`make sandbox-smoke` creates a project from the public demo repo, builds one dockerfile
service and one nixpacks service, curls both through traefik, adds a Postgres addon and
writes a row through one service that it reads through the other, rebuilds, rolls back, and
deletes the project. `KEEP=1 hack/sandbox/smoke.sh` leaves the project in place.

It needs internet access to clone the repo and pull base images.

## Troubleshooting

- **Pods evicted, "low on resource: ephemeral-storage".** The Docker VM disk is full.
  Builds need several GB free. `docker system df` shows what is using it;
  `docker builder prune` frees build cache.
- **`ImagePullBackOff` on a freshly built service after a restart.** Run `make sandbox-up`.
- **Login returns 404.** Something else is listening on the sandbox port. Pick another with
  `KUSO_SANDBOX_PORT`.
