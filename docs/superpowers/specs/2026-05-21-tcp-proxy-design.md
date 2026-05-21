# TCP Proxy — Design

**Date:** 2026-05-21
**Status:** Draft — for review before implementation

## Problem

kuso only exposes services over HTTP(S) — every public endpoint is an
HTTP `Ingress` fronted by Traefik on the `web`/`websecure` entrypoints.
A user who runs a non-HTTP service (a game server, a raw TCP API, an
MQTT broker, or wants to reach an addon database from outside the
cluster) has **no way to expose it**. Railway's "TCP Proxy" is the
feature people pick Railway for in exactly these cases.

This adds an opt-in **TCP proxy**: expose a service (or addon) on a
raw, externally-reachable TCP port.

## Goals

- Opt-in, per-service: a service can request a public TCP port.
- The cluster assigns a port; the user is told `host:port` to connect.
- Works for the common cases: a game server, a raw TCP service, and
  (opt-in, admin-gated) an addon database for external tooling.
- No HTTP assumptions — no TLS termination, no Host routing. Raw L4.

## Non-goals

- TLS/SNI termination for TCP (Traefik can do SNI routing, but that
  needs per-service certs — out of scope; the proxy is plain L4).
- UDP. TCP only. (UDP game servers are a later, separate ask.)
- Exposing every addon by default — addon exposure is explicit and
  admin-gated (a public Postgres port is a real attack surface).

## Approach — chosen: Traefik TCP entrypoints + a port pool

Traefik already fronts the cluster. We add a **pool of TCP
entrypoints** to the Traefik install (e.g. `tcp-30000`…`tcp-30019`,
20 ports) and a Traefik `IngressRouteTCP` per exposed service that
binds one pooled entrypoint to the service's `ClusterIP:port`.

Rejected alternatives:
- **`Service type=LoadBalancer` per service** — on bare-metal k3s
  there is no cloud LB; each would need MetalLB + a routable IP.
  Heavy, and most kuso installs are single-node.
- **`NodePort`** — works, but NodePorts are 30000–32767, unfriendly
  numbers, and bypass Traefik (no central choke point, no metrics).
- **Traefik TCP entrypoints (chosen)** — one Traefik, one place to
  reason about exposure, reuses the existing LB Service. The cost is
  the entrypoint pool must be declared at Traefik install/upgrade
  time (entrypoints are static Traefik config).

## Architecture

### 1. Install — Traefik TCP entrypoint pool

`hack/install.sh`'s Traefik `helm upgrade` gains a configurable pool:

```
KUSO_TCP_PROXY_PORTS=30000-30019   # default; 0 disables the feature
```

For each port in the range, the Traefik chart gets
`--set ports.tcp-<N>.port=<N> --set ports.tcp-<N>.expose.default=true
--set ports.tcp-<N>.protocol=TCP`. The Traefik LoadBalancer Service
then exposes the whole pool. Changing the pool is a Traefik
`helm upgrade` — documented as an install-time/maintenance action,
not something the kuso server does at runtime.

### 2. CRD — `KusoService.spec.tcpProxy`

```yaml
tcpProxy:
  enabled: false        # opt-in
  # port is server-assigned from the pool; surfaced read-only in
  # status. The user does not pick it.
```

`KusoEnvironment` carries the resolved assigned port in
`status.tcpProxyPort` so the UI can show `host:port`.

### 3. Port allocation — `server-go/internal/tcpproxy`

A small allocator service: on `tcpProxy.enabled` flipping true, it
picks a free port from the pool (the pool range comes from a cluster
config / instance secret written by install.sh), records the
assignment, and writes it to the env CR's spec so the chart can
render the `IngressRouteTCP`. Allocation is first-free; the
assignment persists in the control-plane Postgres (`TcpProxyPort`
table: port, project, service, env) so a restart doesn't reassign.
Freed on `tcpProxy.enabled=false` or service delete.

Pool exhaustion → the API returns `ErrConflict` ("no free TCP proxy
ports; the operator can widen KUSO_TCP_PROXY_PORTS").

### 4. Helm — `IngressRouteTCP` in the kusoenvironment chart

When the env CR carries an assigned `tcpProxyPort`, the chart renders:

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRouteTCP
metadata: { name: <env>-tcp }
spec:
  entryPoints: ["tcp-<assignedPort>"]
  routes:
    - match: HostSNI(`*`)        # plain L4 — no SNI routing
      services:
        - name: <env-service>
          port: <service port>
```

### 5. Addon exposure (admin-gated)

`KusoAddon.spec.tcpProxy.enabled` does the same for an addon's
Service. Because a public database port is a real attack surface,
the API gates this behind the `settings:admin` permission (a project
deployer can expose their own *app* on TCP, but only an admin can
expose a *database*), and the UI shows a prominent warning. The
addon's connection secret gains `EXTERNAL_HOST`/`EXTERNAL_PORT` keys
when exposed.

### 6. UI

- Service settings → Networking section: a "Public TCP port" toggle.
  When on and reconciled, shows the assigned `host:port` with a copy
  button. The blast-radius dialog (already shipped) flags it: enabling
  it makes the service publicly reachable on a raw port.
- Addon settings: the same toggle, admin-only, with a red warning.

### 7. CLI

`kuso domains` is HTTP-only; add `kuso tcp enable <project> <service>`
/ `kuso tcp disable` / `kuso tcp list` mirroring the domains commands.

## Blast radius / risks

- **Network exposure** — this is the headline risk. An exposed TCP
  port is reachable from the internet with no TLS and no auth layer
  kuso provides; the *service itself* must authenticate. The UI and
  docs must be explicit. Addon exposure is admin-gated for this
  reason.
- **Install coupling** — the entrypoint pool is static Traefik
  config. A cluster installed before this feature must re-run the
  Traefik `helm upgrade` (or `hack/install.sh`) to get the pool. The
  feature no-ops cleanly when the pool is empty (`tcpProxy.enabled`
  is accepted but allocation fails with a clear "TCP proxy not
  configured on this cluster" message).
- **Firewall** — the host/cloud firewall must allow the pool's port
  range. Documented as an operator prerequisite; kuso can't open
  cloud firewall rules.
- **Single-node** — works fine; the LB Service / k3s servicelb binds
  the ports on the node IP.

## Decisions to confirm

1. **Pool size default** — 20 ports (`30000-30019`). Enough for most
   single-team installs; widened via `KUSO_TCP_PROXY_PORTS`.
2. **Addon exposure admin-gated** — yes (a public DB is dangerous).
3. **Port numbers user-visible but not user-chosen** — the allocator
   picks; the user copies `host:port`. (Letting users pick invites
   collisions and is rarely what they want.)
4. **Plain L4, no SNI/TLS** — yes; TLS-for-TCP is a separate feature.

## Rollout

- CRD change (`KusoService.spec.tcpProxy`, `KusoAddon.spec.tcpProxy`,
  env `status.tcpProxyPort`) → `kubectl apply` the updated CRDs.
- `hack/install.sh` Traefik block change → existing clusters re-run
  install or `helm upgrade traefik` to get the entrypoint pool.
- New `TcpProxyPort` table → DB migration.
- Ship via `make ship`.
