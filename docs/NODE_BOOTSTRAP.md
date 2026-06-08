# Node bootstrap (pull-mode join)

The "Add node" flow — paste one curl command on the new VM. No SSH
config from kuso, no public keys to copy, works behind NAT.

## Operator UX

1. Open `Settings → Nodes`, click **Add node**.
2. Default tab is **Bootstrap**. Optionally fill region/tier/name.
3. Click **Generate command**. You'll get a `curl … | sudo sh`
   one-liner.
4. SSH into the new VM (or use the cloud console) and paste the
   command.
5. The script detects facts (arch, distro, cloud provider, instance
   type), redeems the token, and runs the standard k3s agent
   install. The kuso UI flips the status indicator to "joined" once
   the node calls home; the new node shows up in the node list
   within ~30 seconds.

For scripted use:

```bash
kuso node add-token --region eu --label tier=premium
# … prints the curl command
kuso node pending             # see what's outstanding
kuso node revoke <jti>        # cancel a pending token
```

## What the one-liner does

The script fetched by `GET /bootstrap?token=<jti>` runs in this order:

1. `set -eu` + dependency check (curl, sh).
2. Detect facts: `hostname`, `uname -m`, `/etc/os-release`, cloud
   metadata service (AWS IMDSv2 → Hetzner Cloud → silent skip).
3. POST `/bootstrap/register-node` with the token + facts. Retries
   up to 4 times with capped backoff (5s → 15s → 20s) for transient
   network blips. Hard-fails with a friendly message on 410 (token
   gone) or 404 (token unknown).
4. Receives `{ installCommand, registryHost, registryIP, nodeName, labels }`
   from kuso. (The k3s URL + token are embedded, already shell-escaped,
   inside `installCommand` — they are never returned as separate fields,
   so a script error path can't echo the cluster secret.)
5. Tunes `fs.inotify` sysctls (persisted to `/etc/sysctl.d`).
6. **Wires the in-cluster registry** (when the control plane advertised
   one): appends `<registryIP> <registryHost-without-port>` to
   `/etc/hosts` and writes `/etc/rancher/k3s/registries.yaml` pointing
   the registry at plain `http://` with `insecure_skip_verify`. This is
   required — the k3s agent install does NOT set it up, and without it
   the node can't pull build images (see Troubleshooting). Both files
   are idempotent and survive reboot.
7. Runs `curl -sfL https://get.k3s.io | K3S_URL=… K3S_TOKEN=… INSTALL_K3S_EXEC="agent --node-label …" sh -`.
8. Logs `done.` — the new node should appear in `kuso get nodes`
   within ~30 seconds as kubelet registers.

The k3s shared secret is never logged in plain text. Operator-visible
output redacts `K3S_TOKEN=***`.

## Prerequisite: KUSO_K3S_URL

The control plane must advertise an address the **new node can route
to** as the k3s join URL. By default the server falls back to
`KUBERNETES_SERVICE_HOST` — which inside the pod is the in-cluster
ClusterIP (`10.43.0.1`), **unreachable from an external VM**. A join
against that fails with `Failed to validate connection to cluster …
context deadline exceeded`.

Set `KUSO_K3S_URL` on the kuso-server deployment to the control plane's
publicly-reachable API endpoint:

```bash
kubectl set env -n kuso deploy/kuso-server KUSO_K3S_URL=https://<control-plane-public-ip>:6443
```

`/bootstrap/register-node` returns `503 control-plane URL not configured`
only when neither `KUSO_K3S_URL` nor `KUBERNETES_SERVICE_HOST` is set;
when the fallback is used you get a *working-looking* command that
silently can't reach the API — so set `KUSO_K3S_URL` explicitly for any
node that isn't on the control plane's own private network.

## Firewall: ports a joining node needs

k3s is peer-to-peer for networking. The control plane's firewall must
allow **inbound from the new node's IP** on:

| Port      | Proto | Purpose                          |
| --------- | ----- | -------------------------------- |
| 6443      | TCP   | k3s API (the join itself)        |
| 10250     | TCP   | kubelet (logs / exec / metrics)  |
| 8472      | UDP   | flannel VXLAN (pod networking)   |

**6443 alone is not enough.** A node will *join* with only 6443 open
but its pods get zero overlay connectivity (no ClusterIP, no DNS, no
registry) because flannel VXLAN rides UDP 8472. The symptom is
`ImagePullBackOff` (can't reach the registry) on every pod scheduled
to the new node, while plain `curl` to the control plane works. If the
allowlist is per-source-IP, add the new node's IP to **all three**
rules — it's easy to update 6443/10250 (TCP) and forget the 8472 (UDP)
rule, which leaves exactly this half-broken state.

## Security model

| Property                | Value                                                  |
| ----------------------- | ------------------------------------------------------ |
| Token entropy           | 128 bits (16 random bytes, base64url)                  |
| Default TTL             | 15 minutes (capped at 1 hour, floor 1 minute)          |
| Single use              | Atomically consumed on `POST /bootstrap/register-node` |
| Replay                  | Returns `410 Gone`                                     |
| Revoke before use       | Admin via UI / CLI / API                               |
| Public endpoint         | `/bootstrap` and `/bootstrap/register-node` only       |
| Auth on public endpoint | The token IS the credential; no other gating           |

A leaked token grants "join one node" power for at most 15 minutes.
Mitigations:

- The token is never logged in cleartext past the mint response.
- The pending-tokens UI shows every minted-but-unredeemed token —
  operators can revoke if they suspect a leak.
- Admins can audit consumed tokens in the `NodeBootstrapToken` table
  (`consumedFromIp` records the source IP).

### What we deliberately do NOT do

- **CA fingerprint pinning in the bootstrap script.** The script
  trusts whatever cert kuso's public URL serves. If you need
  defense-in-depth against a kuso impersonator, gate the public URL
  with mutual TLS or run the install behind a VPN. (TODO.)
- **Per-VM cloud provisioning.** This flow runs *on* an existing VM.
  "Provision a Hetzner Cloud VM and join it" is a separate Tier 2
  feature.

## API reference

```
POST   /api/kubernetes/nodes/bootstrap-tokens         (admin)
GET    /api/kubernetes/nodes/bootstrap-tokens         (admin)
DELETE /api/kubernetes/nodes/bootstrap-tokens/{jti}   (admin)
GET    /bootstrap?token=<jti>                         (public)
POST   /bootstrap/register-node                       (public)
```

### Mint

```
POST /api/kubernetes/nodes/bootstrap-tokens
{
  "labels":     {"region": "eu", "tier": "premium"},
  "nodeName":   "worker-2",          // optional; default = VM hostname
  "ttlSeconds": 900                  // optional; default 900, max 3600
}
→ 201
{
  "jti":       "abc123…",
  "expiresAt": "2026-05-06T19:15:00Z",
  "oneLiner":  "curl -fsSL https://kuso.example.com/bootstrap?token=abc123… | sudo sh",
  "labels":    {"region": "eu", "tier": "premium"},
  "nodeName":  "worker-2"
}
```

### Register (called by the bootstrap script on the new VM)

```
POST /bootstrap/register-node
{
  "token":         "abc123…",
  "hostname":      "worker-2",
  "arch":          "amd64",
  "osId":          "ubuntu",
  "osVersion":     "24.04",
  "cloudProvider": "hetzner",
  "instanceType":  "cax21",
  "region":        "fsn1"
}
→ 200
{
  "nodeName":       "worker-2",
  "labels":         {"region": "eu", "tier": "premium", "arch": "amd64", …},
  "installCommand": "unset HISTFILE; curl -sfL https://get.k3s.io | K3S_URL=… K3S_TOKEN=… INSTALL_K3S_EXEC=… sh -",
  "registryHost":   "kuso-registry.kuso.svc.cluster.local:5000",
  "registryIP":     "10.43.206.191"
}
```

The k3s URL + token are embedded (shell-escaped) inside `installCommand`,
never returned as separate fields. `registryHost`/`registryIP` are
best-effort: omitted when the server has no kube client or the
`kuso-registry` Service is missing, in which case the bootstrap skips
registry wiring (an older join that worked before this was added).

Replays / revoked / expired tokens return `410 Gone`. Unknown tokens
return `404`.

### Label merge rules

Operator-supplied labels (mint payload) override VM-detected facts on
key conflict. Facts are added when the operator didn't set the same
key:

| Source       | Keys                                  |
| ------------ | ------------------------------------- |
| Operator     | anything                              |
| Fact: arch   | `arch`                                |
| Fact: cloud  | `cloud`                               |
| Fact: type   | `instance-type`                       |
| Fact: region | `region` (only if operator didn't set)|

All keys land in the `kuso.sislelabs.com/` namespace on the joined
node so the placement matcher in
`internal/projects.PlacementMatchesNode` works without changes.

## Comparison with the SSH path

The SSH-driven flow (`POST /api/kubernetes/nodes/join`) still works
and is still mounted. Use it when:

- The new VM cannot reach kuso's public URL (firewalled outbound),
  but kuso *can* reach the VM inbound.
- You're scripting against an environment without curl preinstalled
  on minimal images.

In every other case, prefer Bootstrap. It needs less plumbing on the
operator's side and works in more network topologies.

## Troubleshooting

**"token not found" 404**: The jti in the URL doesn't match a row.
Likely cause: typo, or the token was already pruned (consumed > 24h
ago). Mint a new one.

**"token already used, expired, or revoked" 410**: As advertised.
`kuso node pending` will show whether the token was ever minted; if
it's not in the list and the response is 410, it's been used or
expired. Mint a new one.

**Script hangs at "registering with kuso"**: The new VM cannot reach
kuso's public URL. Check `curl -fsSL https://<your-kuso-url>/healthz`
on the VM. Likely cause: cloud egress firewall or a misconfigured
`KUSO_PUBLIC_URL` (the script uses the URL it was fetched from).

**Node joins but no labels**: The `kuso.sislelabs.com/` prefix is
applied at boot via `--node-label`. If `kubectl get node <name>
--show-labels` shows the labels but the kuso UI doesn't, the
operator's pending-tokens row is the source of truth — confirm it
carried the labels you expected.

**k3s install fails with "Failed to connect to https://...:6443"** or
the agent log loops on `Failed to validate connection to cluster …
context deadline exceeded`: the new VM can't reach the k3s API.
Two distinct causes:

- **Wrong URL advertised.** If the agent is trying `https://10.43.0.1:6443`
  (a ClusterIP), `KUSO_K3S_URL` isn't set — the server fell back to the
  in-cluster address. Set it (see *Prerequisite: KUSO_K3S_URL*) and
  re-run the join.
- **6443 firewalled.** Open inbound 6443/TCP from the new node on the
  control plane (see *Firewall*). From the VM, `curl -k
  https://<control-plane>:6443/` should not time out.

**Every pod on the new node is `ImagePullBackOff` / `ErrImagePull`**:
the node can't pull from the in-cluster registry. Diagnose in order:

1. **Overlay broken (most common).** Run a pod pinned to the node and
   `curl http://<registry-ClusterIP>:5000/v2/` from inside it. Timeout
   ⇒ flannel VXLAN isn't flowing — open **UDP 8472** from the node on
   the control plane (and any other node). Confirm with `tcpdump -ni any
   'udp port 8472 and host <node-ip>'` on the control plane while the
   node sends pod traffic; zero packets ⇒ firewall is dropping VXLAN.
2. **Registry not wired.** `dial tcp: lookup kuso-registry… : Try again`
   in the pull error ⇒ the node has no `/etc/hosts` entry for the
   registry (its containerd resolves from the host netns, not cluster
   DNS). A `https://` in the error ⇒ no `registries.yaml`. Both are
   written automatically by current bootstraps; on a node that joined
   before this fix, add them by hand (copy from a working node) or
   re-run the bootstrap one-liner.

**Stop the crash-looping immediately:** `kubectl cordon <node>` then
delete the stuck pods so they reschedule onto healthy nodes. Uncordon
once the node's networking/registry is fixed.

**Script aborts with `set: Illegal option -o history`**: an old kuso
build. The bootstrap install command used `set +o history`, which dash
(`/bin/sh` on Debian/Ubuntu) rejects as a special-builtin error that
exits before `curl` runs. Fixed in current builds (the line was
removed; `unset HISTFILE` covers the token-in-history concern).
