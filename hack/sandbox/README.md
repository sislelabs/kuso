# kuso sandbox

A disposable single-node kuso in Docker, built from the working tree. Use it to test
changes end to end without touching a live instance. It runs on arm64 Macs and amd64 Linux.

Host requirements: Docker, `curl`, `jq` and `openssl`. Go is used to build the CLI when it
is installed; without it the CLI is cross-compiled in a container.

## Commands

| Command                | What it does                                                         |
| ---------------------- | -------------------------------------------------------------------- |
| `make sandbox-up`      | Build images, start k3s in Docker, install kuso, log the CLI in      |
| `make sandbox-reload`  | Rebuild server and operator, restart them                            |
| `make sandbox-smoke`   | Scripted end-to-end checks (see [Smoke test](#smoke-test))           |
| `make sandbox-doctor`  | Docker disk, and every sandbox instance's ports, memory and volumes  |
| `make sandbox-down`    | Delete the instance's containers, volumes and state                  |

`make sandbox-reload TARGET=server` reloads one image. Targets: `server`, `operator`, and the
Job images `backup`, `env-detect`, `nixpacks`.
`make sandbox-up` is safe to re-run. Run it again after the node container or Docker
restarts: Docker regenerates the node's `/etc/hosts`, and image pulls from the in-cluster
registry fail until `up` writes the registry entry back.

## Using it

```sh
make sandbox-up
hack/sandbox/kuso project create demo --repo https://github.com/ivo9999/kuso-demo-todo-api
hack/sandbox/kuso service add demo api --runtime dockerfile --port 8080
hack/sandbox/curl http://api.demo.kuso.localhost/healthz
```

- UI and API: `http://kuso.localhost`, login `admin` / `kuso-admin`.
- Services: `http://<service>.<project>.kuso.localhost`. Don't pass `--domain` to
  `project create`; a project base domain must be a public one.
- `hack/sandbox/kuso` is the CLI built from this tree. It runs with its own `HOME` under
  `hack/sandbox/.state/<instance>/`, so it never reads or writes `~/.kuso`.
- `hack/sandbox/kubectl` is kubectl for the instance (the host's kubectl if there is one,
  otherwise the node's).
- `hack/sandbox/curl <url> [curl args]` requests a service through the instance's traefik
  with the right `Host` header and port. Paste the port-less links kuso prints.

## Parallel instances

```sh
KUSO_SANDBOX_NAME=t1 make sandbox-up      # picks free ports, prints the URLs
make sandbox-smoke                        # later commands in this checkout target t1
KUSO_SANDBOX_NAME=t1 make sandbox-down
```

Each instance has its own compose project, volumes and state dir
(`hack/sandbox/.state/<name>/`). A named instance started without ports gets free ones
(HTTP from 18080, kube API from 16444), stored in its `ports.env` and reused on the next
`up`. `KUSO_SANDBOX_PORT` / `KUSO_SANDBOX_API_PORT` still override. The default instance
(`kuso-sandbox`) keeps ports 80 and 16443.

`up` records the instance in `.state/current`, and every script and helper uses that one
unless `KUSO_SANDBOX_NAME` says otherwise. On a non-80 port the UI is at
`http://kuso.localhost:<port>`; for services, use `hack/sandbox/curl`.

Cost per instance, idle after `up`: about 1.6 GB RAM (the node) plus 20 MB (the mirror),
and about 8.5 GB of Docker disk (7.8 GB node volume, 0.5 GB mirror). Builds and addons add
to both: after `SMOKE=all` the node volume was 11.6 GB. Two instances fit an 8 GB Docker VM; running builds in both at once is tight.

## What is inside

One privileged `rancher/k3s` container with traefik disabled, like a real install, plus a
small registry container (the mirror, below). `bootstrap.sh` then applies, from the working
tree: traefik, the CRDs, the registry, buildkitd, the operator, the server and its admission
policies, the activator, Prometheus, and the backup S3.

**Images.** The server and operator, and the images platform Jobs run (`kuso-backup`,
`kuso-env-detect`, `kuso-nixpacks`), are built for the host architecture. Release images are
amd64-only, and several Jobs pull with `imagePullPolicy: Always`, so loading them into the
node is not enough. They are also pushed to the instance's mirror registry, which the node's
containerd tries before `ghcr.io` (`registries.yaml`). Anything the mirror doesn't hold
comes from `ghcr.io` as usual.

**Backup S3.** A MinIO in namespace `kuso-sandbox`, served through traefik as
`http://s3.sandbox.test` (a CoreDNS stub zone resolves it in-cluster). `up` saves it as the
backup settings: bucket `kuso-backups`, plain HTTP allowed. Backup, restore, preview DB
seeding and backup encryption all run against it. `KUSO_SANDBOX_S3=0` skips it.

**Prometheus.** `deploy/prometheus.yaml`, with a smaller memory request. It feeds the metric
cards, alert rules and scale-to-zero. For sleep to fire fast, give the service the shortest
window: `hack/sandbox/kuso project service sleep <p> <s> on --after 1m`. The env goes to 0
about 75 to 110 seconds after its last request. Production defaults are unchanged.

**GitHub.** A sandbox-only GitHub App: fake app id, a throwaway RSA key generated by `up`, and
webhook secret `kuso-sandbox-webhook-secret`. That registers the webhook route. Projects
keep installation id 0, so the push and preview paths skip every GitHub API call (token
minting, commit statuses, PR comments, `kuso.yml` fetch) and builds clone public repos
anonymously. `hack/sandbox/gh-event` sends signed deliveries:

```sh
hack/sandbox/gh-event push ivo9999/kuso-demo-todo-api main
hack/sandbox/gh-event pr opened ivo9999/kuso-demo-todo-api 1     # builds refs/pull/1/head
hack/sandbox/gh-event pr closed ivo9999/kuso-demo-todo-api 1
```

The SHA defaults to what GitHub has for the branch or `refs/pull/<n>/head`. The server
answers 204 and works in the background. Look for results in
`hack/sandbox/kubectl -n kuso logs deploy/kuso-server`. A preview needs `--previews=on` and a
staging environment on the service. The server keeps closed PR numbers in memory and skips
a later `opened` for them until it restarts; send `reopened` instead.

## How it differs from a real install

- **Plain HTTP everywhere.** No cert-manager and no Let's Encrypt.
- **kuso's own database is a plain `postgres:16-alpine`**, not the CloudNativePG cluster.
- **buildkitd runs on the control-plane node.** The manifest forbids that; the sandbox
  removes the rule because it has one node.
- **Kubelet eviction thresholds are lowered to 1%.** The node shares the Docker VM's disk.
- **`KUSO_ALLOW_PRIVATE_OUTBOUND=true`** on the server, so it can reach the sandbox S3
  through a ClusterIP. Notification webhooks and uptime probes can therefore reach private
  addresses too: SSRF guards can't be tested here.
- **The GitHub App is fake.** Anything that needs a real installation token (private
  repos, commit statuses, PR comments, `kuso.yml` config-as-code on push) doesn't run.
- **Not installed:** pkg-probe, postgres-backup (control-plane DB), the incident agent and
  bot, the updater. `manifests.sh` has the list.
- **Not testable here:** the upgrade path, multi-node behaviour, `hack/install.sh` itself.

`bootstrap.sh` is a second copy of `install.sh`'s sequence. `make verify-sandbox` fails when
a file is added to `deploy/` without being listed in `manifests.sh`. A change to ordering or
secrets inside `install.sh` still has to be ported by hand.

## Smoke test

`make sandbox-smoke` runs the `core` group: a project from the public demo repo, one
dockerfile and one nixpacks service curled through traefik, a Postgres addon written
through one service and read through the other, a rebuild, a rollback, and project delete.

Other groups are opt-in, alone or together: `SMOKE=isolation,backup make sandbox-smoke`,
or `SMOKE=all`.

| Group       | Checks                                                                          | Time   |
| ----------- | ------------------------------------------------------------------------------- | ------ |
| `core`      | the deploy lifecycle above                                                      | ~6 min |
| `isolation` | same-env traffic works; cross-project and staging→production are refused         | ~2 min |
| `backup`    | postgres backup now (encrypted), restore in place and into a sibling; s3 addon snapshot and restore | ~3 min |
| `preview`   | staging env + PR opened → preview built with staging's config and its own DB, served through traefik; PR closed → env and DB reclaimed | ~6 min |
| `sleep`     | idle env scales to 0; a request wakes it (wake time printed)                     | ~3 min |
| `registry`  | anonymous pull and push refused; a build pushes; the env pins the pushed digest  | ~3 min |

Every check prints PASS or FAIL and the run ends with a summary. A failed check doesn't stop
the run unless later checks depend on it. Each group deletes what it created. `KEEP=1`
leaves it in place.

It needs internet access to clone the repo and pull base images.

## Troubleshooting

- **`make sandbox-up` refuses to start: "only N GB free on the Docker disk".** `up` prunes
  Docker build cache when the disk has less than 15 GB free or is over 80% used. It never
  touches images or volumes, and it refuses to start below 8 GB free.
  `make sandbox-doctor` shows what each instance uses; remove ones you no longer need.
  `KUSO_SANDBOX_MIN_FREE_GB`, `KUSO_SANDBOX_PRUNE_BELOW_GB` and `KUSO_SANDBOX_MAX_USED_PCT`
  move the thresholds.
- **Pods evicted, "low on resource: ephemeral-storage".** The Docker VM disk is full; see
  above.
- **`ImagePullBackOff` on a freshly built service after a restart.** Run `make sandbox-up`.
- **`make sandbox-up` fails to bind port 80, or login returns 404.** Something else is
  listening on that port. Stop it, or use a named instance, which picks free ports.
