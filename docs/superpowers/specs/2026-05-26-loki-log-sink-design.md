# Loki + Promtail log-sink addon (gap 6)

## Status

**Deferred to next session.** Two new addon kinds + a DaemonSet + a Grafana dependency + datasource provisioning. The single most differentiating gap per the spec, but also the widest blast radius — needs careful staging.

## What it solves

Kuso's log story today: control-plane Postgres, 14-day retention, no streaming, indexed only by service+env. Fine for routine debugging. Wrong tool for an 11pm payment outage where you want `tail -f | grep stripe_event_id=evt_...` over multiple weeks.

A first-class Loki addon turns this into:
- `kuso logs <svc> --since 7d --grep transaction_id=tx_abc123` works.
- Grafana addon ships with the Loki datasource pre-wired.
- Every kuso install gets the same observability story Tickero already has.

## Architecture

Three components:

1. **`kind=loki` addon**: StatefulSet running Loki single-binary mode, PVC for chunks + index, retention configurable via `spec.retentionDays`. Conn secret exposes `LOKI_PUSH_URL` + `LOKI_QUERY_URL`.
2. **`kind=promtail-daemonset` addon**: cluster-singleton (rejects duplicate installs). DaemonSet with hostPath mount of `/var/log/pods`. Forwards every kuso-namespaced pod's stdout/stderr to the Loki addon. Auto-discovers via kube label `kuso.sislelabs.com/project`.
3. **`kind=grafana` addon update**: when a `kind=loki` addon exists in the same project, auto-provision a datasource pointing at it on first boot. (Grafana already exists as an addon kind? Verify.)

## What needs touching

- `operator/config/crd/bases/.../kusoaddons.yaml`: add `loki`, `promtail-daemonset` to the kind enum.
- `operator/helm-charts/kusoaddon/templates/loki.yaml`: new template.
- `operator/helm-charts/kusoaddon/templates/promtail-daemonset.yaml`: new template — Daemonset + ClusterRole + ClusterRoleBinding (needs to read pod metadata cluster-wide).
- `internal/addons` reconciler: enforce singleton on `kind=promtail-daemonset` (one per cluster, not per project).
- Server `internal/logs/`: optional Loki query backend. When a `kind=loki` addon exists in the project, `kuso logs` queries it instead of pulling from control-plane Postgres.
- Web: log viewer gains a "stream" mode that's only enabled when Loki is present.

## Why deferred

1. **DaemonSet permissions**: promtail needs cluster-wide pod read + hostPath log access. That's a real ClusterRole, not a namespaced Role. CLAUDE.md notes kuso-server already has cluster-wide perms (memory: `feature-cluster-admin.md`), but adding new privileged surface needs explicit review.
2. **Disk discipline**: Loki's PVC will balloon. Retention needs to be enforced at chunk level by the Loki compactor — needs config tuning, not just a knob.
3. **Singleton semantics**: kuso's addon model is per-project. `promtail-daemonset` is cluster-singleton. The reconciler needs a new pattern, or we install it via the `deploy/` manifests pile and skip the addon abstraction.
4. **Grafana datasource provisioning** depends on whether Grafana already exists as an addon. If not, that's a fourth new addon kind, which is too much for one batch.

## Path to ship

Smallest viable version:
1. Loki addon as a normal per-project StatefulSet (no DaemonSet yet).
2. Document that users wire promtail by hand for now via raw helm.
3. `kuso logs --backend=loki` flag that queries the project's Loki addon if present.
4. DaemonSet + Grafana addon land in the follow-up.

This deliberately punts the hardest piece (DaemonSet + ClusterRole) and gets the value (Loki backend for kuso logs) in shape.
