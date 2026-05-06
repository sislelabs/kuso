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
4. Receives `{ k3sUrl, k3sToken, installCommand, … }` from kuso.
5. Runs `curl -sfL https://get.k3s.io | K3S_URL=… K3S_TOKEN=… INSTALL_K3S_EXEC="agent --node-label …" sh -`.
6. Logs `done.` — the new node should appear in `kuso get nodes`
   within ~30 seconds as kubelet registers.

The k3s shared secret is never logged in plain text. Operator-visible
output redacts `K3S_TOKEN=***`.

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
  "k3sUrl":         "https://10.0.0.1:6443",
  "k3sToken":       "K…::server:…",
  "nodeName":       "worker-2",
  "labels":         {"region": "eu", "tier": "premium", "arch": "amd64", …},
  "installCommand": "curl -sfL https://get.k3s.io | K3S_URL=… sh -"
}
```

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

**k3s install fails with "Failed to connect to https://...:6443"**:
Open port 6443 outbound on the new VM (egress firewall) or the
control plane (ingress firewall, if applicable). The control-plane
reachability probe in the SSH path catches this earlier; in the
bootstrap path the failure surfaces in the install output on the
VM's terminal.
