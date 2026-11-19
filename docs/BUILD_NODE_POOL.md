# Build node pool

Default kuso runs every build Job on whichever node the kube
scheduler picks. On a single-node install that's the control
plane, which means a build storm can starve kuso-server / traefik /
the operator off CPU and crash the dashboard. We've patched the
control-plane resilience three different ways (priority class,
tolerant probes, memory limits) — but the structural fix is to give
build Jobs a node of their own.

> **Where the compile actually runs.** For `dockerfile`, `nixpacks` and
> `static` builds the build Job is a thin `buildctl` client; the compile
> runs inside a `kuso-buildkitd` daemon (`deploy/buildkitd.yaml`), one per
> build node: every non-control-plane node unless labelled
> `kuso.sislelabs.com/build-daemon=off` (a control-plane node only with
> `=on`). Each project's builds stick to one daemon, so the compile load of
> projects homed on different nodes is isolated. A build node pool moves
> the Jobs, which matters for `buildpacks` builds (they compile in-pod) and
> for clone/cache steps. To also keep the compile load off app nodes, label
> those nodes `build-daemon=off` so only the pool's nodes run daemons (the
> daemons tolerate the `kuso.sislelabs.com/build` taint).

## How it works

The build Job that kuso-server's build controller renders
(`server-go/internal/buildcontroller/render.go`) carries:

```
tolerations:
  - key: "kuso.sislelabs.com/build"
    operator: "Exists"
    effect: "NoSchedule"
affinity:
  nodeAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        preference:
          matchExpressions:
            - key: "kuso.sislelabs.com/build"
              operator: "In"
              values: ["true"]
```

Three behaviours fall out:

* No build node exists → preference is a no-op, Jobs schedule
  wherever (today).
* A node has the `kuso.sislelabs.com/build=true` *label* but no
  taint → builds *prefer* it but other workloads can still schedule
  there too. Useful when you want builds to land on a beefy worker
  but don't mind sharing.
* A node has both the label *and* a `kuso.sislelabs.com/build:NoSchedule`
  *taint* → only build pods schedule there. This is the
  control-plane-protection setup.

## Promoting a worker to a build node

Once you've added a node to the cluster (settings → nodes → Add node),
flip the label + taint by hand:

```
kubectl label node <node-name> kuso.sislelabs.com/build=true
kubectl taint node <node-name> kuso.sislelabs.com/build=true:NoSchedule
```

(`kuso node label set <node-name> build=true` sets the same label —
the prefix is added server-side — but the CLI only manages the
`region` taint, so the build taint still needs `kubectl`.)

Now every new build Job lands on that node. Existing in-flight builds
finish where they were scheduled — kube doesn't evict them.

To remove the node from the build pool:

```
kubectl taint node <node-name> kuso.sislelabs.com/build:NoSchedule-
kubectl label node <node-name> kuso.sislelabs.com/build-
```

## Sizing

A reasonable build node for nixpacks workloads:

* 4 vCPU, 8 GB RAM. Lets two concurrent builds run without contention.
* 50 GB disk. The /nix store + dep cache PVCs eat ~5 GB per service;
  with 5–10 services that's 50 GB of cache state under steady use.
* Same OS / k3s install path as the rest of the cluster (the kuso
  Add-Node flow handles this).

A single 4 GB build node is enough for hobby use; bump to 8 GB once
you have more than 2–3 nixpacks projects in the rotation.

## Cost vs. just upgrading the control plane

The trade-off:

* **Build node pool** — one extra €13/mo box. Control plane stays
  small. Build storms can never affect the dashboard.
* **Beefier control plane** (e.g. CCX23, 4vCPU/16GB) — one box, ~€26/mo.
  Build storms still share resources with kuso-server but the headroom
  is large enough that contention is rare.

The build node pool wins long-term — it's the only way to *guarantee*
the control plane survives any build pattern. The single-beefy-box is
a faster fix when you don't mind tail-risk.
