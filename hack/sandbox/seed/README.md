# Sandbox seed

`seed.sh` fills a running sandbox with data that looks like a small team's instance, so
the dashboard can be reviewed against something other than one demo service.

```sh
make sandbox-up
hack/sandbox/seed/seed.sh
```

It only talks to kuso through the sandbox CLI (`hack/sandbox/kuso`, or `KUSO=<path>` to
point it at another copy). Every step checks what already exists first, so running it
again only fills gaps. A full first run takes about 10 minutes, mostly the three builds of
`shop/api`. It needs internet access to clone the demo repo and pull images.

## What it creates

| Where            | What                                                                                     |
| ---------------- | ---------------------------------------------------------------------------------------- |
| `shop`           | `api` (dockerfile build of the demo repo), `web` (nginx image), `worker` (reuses the `api` image), Postgres `db`, Redis `cache` |
| `shop/api`       | Env vars with addon references, a generated `SESSION_SECRET`, a `STRIPE_SECRET_KEY` secret, a nightly `prune-carts` cron, a `staging` environment on the `staging` branch with its own Postgres and Redis |
| `shop/api` history | Build of `main`, then a build of a commit that exits on boot, then a rollback to the first build |
| `shop/web`       | Custom domain `shop.example.com`, which never resolves to the sandbox and stays pending DNS |
| `shop/worker`    | Logs an INFO line every 10s, a WARN every 40s and an ERROR with a multi-line stack every 60s (Go-style `at` frames and a Python traceback, alternating) |
| `shop`           | Uptime checks on, alert rules "Worker errors" (log match on `ERROR`) and "Storefront 5xx" |
| `blog`           | HedgeDoc from the marketplace, with its own Postgres, on `blog.kuso.localhost`            |
| `internal-tools` | `request-inspector` (traefik/whoami), `docs` (nginx, sleeps after 15 idle minutes), `reports` (dockerfile service on branch `feature-csv-export`, which doesn't exist, so its only build failed at clone) |
| Instance         | Webhook channel `ops-webhook` to `https://hooks.example.com/kuso` (deliveries fail), viewer `maya` who has signed in, editor `dana` who never has, and one pending editor invite |

`demo` and anything else already on the sandbox are left alone. The seeded users' password
is `kuso-review-1` (override with `SEED_PASSWORD`).

## Files

- `seed.sh`: the steps above, in order.
- `shop.kuso.yml`, `internal-tools.kuso.yml`: the services and addons, applied with
  `kuso apply`. Edit these to change images, env vars or domains, then re-run `seed.sh`.
- `worker.sh`: the worker's log generator. It's sent as the worker's command at creation,
  so changing it later means deleting the worker service and re-running `seed.sh`.

## Notes

- Images run on arm64 and amd64. `nginx` is the unprivileged build because kuso drops
  every capability and the stock image needs `CAP_CHOWN`.
- The first run warns that `STRIPE_SECRET_KEY` isn't set yet. The script sets it on the
  next line, and later runs don't warn.
- Builds need the env-detect and nixpacks images inside the node. If the node runs low on
  disk, kubelet garbage-collects them and builds sit in `ImagePullBackOff`; `make sandbox-up`
  imports them again.
- To start over: `hack/sandbox/kuso project delete shop` (and `blog`, `internal-tools`),
  then delete the users, the invite and the notification channel, and run `seed.sh` again.
