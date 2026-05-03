# kuso — agent rules

Project-specific rules that override agent defaults. Loaded on every turn.

## Cluster inspection — always go through `kuso`, not raw kubectl

When you need to check the state of the live cluster (services, addons, builds, env vars, logs, ingress), **drive it through the `kuso` CLI** at `dist/kuso-darwin-arm64` (or whatever matches the host). Examples:

| What you want to know            | Command                                                                  |
| -------------------------------- | ------------------------------------------------------------------------ |
| What projects exist              | `kuso get projects -o json`                                              |
| Project rollup                   | `kuso status <project>`                                                  |
| Service spec                     | `kuso get services <project> -o json`                                    |
| Build state                      | `kuso build list <project> <service>`                                    |
| Logs                             | `kuso logs <project> <service> [--env <env>] [-f]`                       |
| Env vars on a service            | `kuso env list <project> <service>`                                      |
| Connect to addon DB              | `kuso get addons <project> -o json` then read DATABASE_URL               |
| Trigger a build                  | `kuso build trigger <project> <service>`                                 |
| Open a shell in a pod            | `kuso shell <project> <service>`                                         |

**Why this matters:**
- The CLI hits the same `/api/...` surface the UI uses, so what you see is what users see — no "but on my machine" mismatches.
- It exercises the auth / tenancy / perm layers you'd otherwise miss with raw kubectl.
- Bugs in the CLI become visible (we found four during the last e2e pass that way).
- The output format is stable and scriptable; raw kubectl JSON is verbose and re-shapes between server versions.

**Fall back to `kubectl` only when**:
- The CLI doesn't expose what you need (e.g. `kubectl logs` of a non-kuso pod, helm-operator state, raw CRD yaml for debugging operator reconcile bugs, kube events).
- You're debugging the CLI itself.
- You're inspecting cluster-level state (nodes, ClusterRoles, namespaces) that has no kuso-CLI equivalent.

When you do shell out to `kubectl`, run it via `ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl ..."` — the test cluster's kubeconfig isn't on the dev machine.

## Other rules

- Confirm `dist/kuso-darwin-arm64` is up to date before driving it. After server-go changes that affect the API surface, also rebuild the CLI: `cd cli && go build -o /tmp/kuso ./cmd`.
- Don't mix CLI invocations with raw kubectl in the same diagnostic — pick one and stay there. Mixing buries the actual signal in tooling noise.
- For e2e validation, the CLI is the contract. If it lies (wrong status, missing fields, decode error), that's a real bug to fix — not something to work around with a kubectl one-liner.
