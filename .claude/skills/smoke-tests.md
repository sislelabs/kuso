---
name: smoke-tests
description: Use when something looks like it might be broken after a refactor, or before opening a PR that touches operator/server/cli/mcp. Tells you which smoke test catches what.
---

# kuso smoke tests

There are three layers of smoke tests, in increasing order of cost and coverage. Run the cheapest one first.

## Level 1: per-subproject unit tests

Fast (sub-second to a few seconds). No external dependencies.

```bash
cd cli && go test ./...
cd mcp && go test ./...
cd operator && for c in helm-charts/*/; do helm lint "$c"; done
cd server && yarn test     # not yet wired up locally — skip in agent sessions
```

What they catch: pure logic bugs, type errors, helm template syntax, broken imports.

## Level 2: MCP integration test (3a)

Builds the `kuso-mcp` binary, spawns it as a child process pointed at a fake kuso server, drives it via the MCP SDK over stdio.

```bash
cd mcp && go test -tags=integration ./...
```

What it catches: tool registration regressions, JSON args/results shape bugs, transport wiring issues, env var handling, `--read-only` flag plumbing. Around 7s wall time.

## Level 3: CRD dry-run on kind (3b)

Brings up a kind cluster, applies every CRD, server-side-dry-runs every sample CR.

```bash
hack/smoke/crd-dryrun.sh                  # creates and tears down kuso-smoke cluster
KEEP_CLUSTER=1 hack/smoke/crd-dryrun.sh   # leave cluster running for inspection
```

What it catches: CRD schema regressions, group/kind/plural mismatches, rebrand artifacts in operator manifests. Around 30-60s wall time.

Required tools: `kind`, `kubectl`, Docker.

## What's NOT covered yet

- **Real operator install.** We don't yet build the operator binary or apply the deployment. To do that, we'd need to publish `ghcr.io/sislelabs/kuso-operator:*` images.
- **Real server install.** Same — needs `ghcr.io/sislelabs/kuso-server:*`.
- **End-to-end deploy.** `kuso install` → deploy a hello-world app → tear down. This is the next big smoke test, but it's gated on building real images.

When you've shipped a change and want to know what's covered, the order is: run Level 1 first (cheap, broad). If it passes, run Level 2 if you touched `mcp/`. Run Level 3 if you touched `operator/`. Don't skip Level 3 on operator changes — CRD bugs are how you spend a Saturday afternoon hunting a typo.
