# kuso metrics

The kuso-server pod exposes a Prometheus scrape endpoint at
`/metrics` on port 8080. The deploy YAML
(`deploy/server-go.yaml`) sets `prometheus.io/scrape="true"` on the
pod template; the in-cluster Prometheus
(`deploy/prometheus.yaml`) picks it up automatically via the
`kubernetes-pods` job — no extra ServiceMonitor needed.

## What's exposed

Three things matter for capacity decisions:

| Metric | Type | Why it matters |
|---|---|---|
| `kuso_http_requests_total{method, status_class}` | counter | request rate + error rate |
| `kuso_http_request_duration_seconds{method}` | histogram | p95 latency by HTTP method |
| `kuso_db_pool_in_use` | gauge | how many of the 25 Postgres conns are checked out |
| `kuso_db_pool_idle` | gauge | idle slots — drops to 0 under burst |
| `kuso_db_pool_open` | gauge | total open conns (in_use + idle) |
| `kuso_build_queue_depth` | gauge | KusoBuild CRs in `queued` state cluster-wide |
| `kuso_build_running` | gauge | running kaniko build pods cluster-wide |

Plus the standard `go_*` and `process_*` metrics from the Prometheus
client library (heap size, goroutines, file descriptors, GC pauses).

## The three Grafana panels you should build first

These three turn the scalability work from "guess" to "measure":

### 1. Request p95 latency by route

```
histogram_quantile(0.95,
  sum by (le, method) (
    rate(kuso_http_request_duration_seconds_bucket[5m])
  )
)
```

Watch for: handlers consistently over 500ms. The biggest offenders
in practice are `/api/projects/{p}/services` (kube list-on-every-call
on cold cache) and `/api/audit` on a busy install.

### 2. DB pool in-use over time

```
kuso_db_pool_in_use
```

Plot alongside `kuso_db_pool_open`. The pool max is 25 (see
`SetMaxOpenConns` in `internal/db/db.go`). When `in_use > 20` for sustained periods
you're seeing pool contention — every additional dashboard tab adds
to the queue and a slow query stalls the rest. Either bump
`MaxOpenConns` (env `KUSO_DB_MAX_OPEN_CONNS`, when implemented) or
move log search / heavy reads off the request path.

### 3. Build queue depth

```
kuso_build_queue_depth
kuso_build_running
```

Plot both as a stacked area. `running` should hover at or just below
the cluster cap (`build.maxConcurrent` setting); `queue_depth > 0`
for sustained periods means the cap is the bottleneck. The
operator's response: raise `build.maxConcurrent` in `/settings`, or
add cluster CPU.

## Dashboard JSON

A starter Grafana dashboard for the three panels above:

```json
{
  "title": "kuso · capacity",
  "panels": [
    {
      "type": "timeseries",
      "title": "Request p95 by method",
      "targets": [{
        "expr": "histogram_quantile(0.95, sum by (le, method) (rate(kuso_http_request_duration_seconds_bucket[5m])))",
        "legendFormat": "{{method}}"
      }],
      "unit": "s"
    },
    {
      "type": "timeseries",
      "title": "DB pool",
      "targets": [
        {"expr": "kuso_db_pool_in_use", "legendFormat": "in use"},
        {"expr": "kuso_db_pool_idle", "legendFormat": "idle"},
        {"expr": "kuso_db_pool_open", "legendFormat": "total open"}
      ]
    },
    {
      "type": "timeseries",
      "title": "Build queue + running",
      "targets": [
        {"expr": "kuso_build_queue_depth", "legendFormat": "queued"},
        {"expr": "kuso_build_running", "legendFormat": "running"}
      ]
    }
  ]
}
```

Save as `kuso-capacity.json` and import via
`Grafana → Dashboards → Import`. Or paste each `expr` into a fresh
panel.

## Alerts worth adding

| Condition | Why |
|---|---|
| `kuso_db_pool_in_use / 25 > 0.8 for 5m` | one slow query holds 4% of the pool; sustained 80% utilisation = users will see slow page loads |
| `kuso_build_queue_depth > 5 for 10m` | cluster build cap is the bottleneck; a redeploy storm queued past the operator's SLA |
| `histogram_quantile(0.95, rate(kuso_http_request_duration_seconds_bucket[5m])) > 2` | p95 over 2s on any handler — usually means kube apiserver QPS exhaustion |

## Cost

The build-queue gauges are the only metric that issues kube list
calls. They're cached for 10s, so worst-case the prometheus scrape
(15s default) issues 4 lists/min. On a cluster with 1k builds across
all phases, that's 4k items/min in list payloads — bounded and not
material.
