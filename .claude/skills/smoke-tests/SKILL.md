---
name: smoke-tests
description: Use before opening a PR that touches server-go/, operator/, cli/, mcp/ or web/, or when something looks broken after a refactor. Lists the test layers (unit, MCP integration, kind CRD dry-run, sandbox e2e) in cost order and what each catches.
---

# kuso smoke tests

There are four layers of smoke tests, in increasing order of cost and coverage. Run the cheapest one first.

## Level 1: per-subproject unit tests

Fast (sub-second to a few seconds). No external dependencies.

```bash
make verify                # web typecheck + every go.work module's tests + CLI/API parity + sandbox manifest drift + chart/RBAC/CRD-guard checks
make test-db               # Go tests incl. the Postgres-backed ones (-race, one DB per package), = the CI gate
make test-docker           # the *_Docker tests that run real images (KUSO_TEST_DOCKER=1); CI runs them as a separate job
# Or per-subproject:
cd server-go && go test ./...
cd cli && go test ./...
cd mcp && go test ./...
cd web && pnpm typecheck && pnpm test   # tsc + vitest unit tests
cd operator && for c in helm-charts/*/; do helm lint "$c"; done
```

What they catch: pure logic bugs, type errors, helm template syntax, broken imports, and (via `verify-parity`) new HTTP routes added without a matching CLI command.

The repo's pre-push hook (`make hooks-install` once per clone) runs the same validation on touched modules; `make verify-ci` runs the full matrix locally (includes `next build` + the kind CRD dry-run).

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

## Level 4: sandbox end-to-end

A disposable single-node kuso (k3s in Docker) built from the working tree. See `hack/sandbox/README.md`.

```bash
make sandbox-up       # build images, start k3s, install kuso, log the CLI in
make sandbox-smoke    # project → dockerfile + nixpacks builds → live HTTP → postgres addon → rebuild → rollback → delete
SMOKE=isolation,backup,preview,sleep,registry make sandbox-smoke   # opt-in scenario groups (or SMOKE=all)
make sandbox-down
```

Parallel agents: `KUSO_SANDBOX_NAME=<lane> make sandbox-up` gives each its own instance on free ports; later commands in that checkout target it. Check `make sandbox-doctor` first: ~1.6 GB RAM and ~8.5 GB disk per instance.

What it catches: real operator + server install, the full deploy lifecycle, backups/restore against a bundled S3, PR previews from signed webhooks (`hack/sandbox/gh-event`), scale-to-zero, network isolation, registry auth. Minutes, not seconds. Required tools: Docker, `curl`, `jq`, `openssl`.

When you've shipped a change and want to know what's covered, the order is: run Level 1 first (cheap, broad). If it passes, run Level 2 if you touched `mcp/`. Run Level 3 if you touched `operator/`. Run Level 4 for anything that changes deploy behaviour. Don't skip Level 3 on operator changes — CRD bugs are how you spend a Saturday afternoon hunting a typo.
