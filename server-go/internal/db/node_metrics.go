// Per-node resource samples. The kuso server polls every cluster
// node every 30 min (cmd/kuso-server/main.go wires the sampler
// goroutine) and writes one row per node per tick. The settings/nodes
// drill-down renders 7 days of these as sparklines.
//
// Why SQLite instead of Prometheus: a one-box kuso install is
// expected to have a handful of nodes; 7d × 48 samples/day × 5 nodes
// = 1,680 rows. Trivial to query, no extra infra to deploy. When a
// fleet outgrows that we'll wire a real TSDB.

package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// NodeMetric is one sample of a node's resource usage at a point in
// time. Capacities are repeated per-row so historical charts render
// honestly even if a node is later resized — the % is computed
// against the capacity that was true *at sample time*, not now.
type NodeMetric struct {
	Node              string    `json:"node"`
	Ts                time.Time `json:"ts"`
	CPUUsedMilli      int64     `json:"cpuUsedMilli"`
	CPUCapacityMilli  int64     `json:"cpuCapacityMilli"`
	MemUsedBytes      int64     `json:"memUsedBytes"`
	MemCapacityBytes  int64     `json:"memCapacityBytes"`
	DiskAvailBytes    int64     `json:"diskAvailBytes"`
	DiskCapacityBytes int64     `json:"diskCapacityBytes"`
}

// InsertNodeMetric writes one sample. Caller fills every field; we
// accept zero values (e.g. when metrics-server isn't installed) so
// the chart doesn't have a hole — a flat-line at 0 is honest about
// "we couldn't read this" while still showing the timestamp.
func (d *DB) InsertNodeMetric(ctx context.Context, m NodeMetric) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "NodeMetric"
		  ("node","ts","cpuUsedMilli","cpuCapacityMilli","memUsedBytes","memCapacityBytes","diskAvailBytes","diskCapacityBytes")
		VALUES (?,?,?,?,?,?,?,?)`,
		m.Node, m.Ts.UTC(),
		m.CPUUsedMilli, m.CPUCapacityMilli,
		m.MemUsedBytes, m.MemCapacityBytes,
		m.DiskAvailBytes, m.DiskCapacityBytes,
	)
	if err != nil {
		return fmt.Errorf("insert node metric: %w", err)
	}
	return nil
}

// ListNodeMetrics returns samples for one node within [since, now]
// ordered oldest-first so the UI can render left-to-right without
// re-sorting. Empty slice is fine and means "no samples yet" — the
// sampler runs every 30 min, so a fresh install will be empty for
// up to 30 minutes after first boot.
func (d *DB) ListNodeMetrics(ctx context.Context, node string, since time.Time) ([]NodeMetric, error) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT "node","ts","cpuUsedMilli","cpuCapacityMilli","memUsedBytes","memCapacityBytes","diskAvailBytes","diskCapacityBytes"
		FROM "NodeMetric"
		WHERE "node" = ? AND "ts" >= ?
		ORDER BY "ts" ASC`,
		node, since.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("list node metrics: %w", err)
	}
	defer rows.Close()
	out := make([]NodeMetric, 0, 128)
	for rows.Next() {
		var m NodeMetric
		var ts sql.NullTime
		if err := rows.Scan(&m.Node, &ts,
			&m.CPUUsedMilli, &m.CPUCapacityMilli,
			&m.MemUsedBytes, &m.MemCapacityBytes,
			&m.DiskAvailBytes, &m.DiskCapacityBytes,
		); err != nil {
			return nil, fmt.Errorf("scan node metric: %w", err)
		}
		if ts.Valid {
			m.Ts = ts.Time
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneNodeMetricsOlderThan deletes samples older than `before`.
// Called on a slow ticker (e.g. once per sample tick) so the table
// stays around 7d × 48 samples/day × N nodes.
func (d *DB) PruneNodeMetricsOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "NodeMetric" WHERE "ts" < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune node metrics: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
