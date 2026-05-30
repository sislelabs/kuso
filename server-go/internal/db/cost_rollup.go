// Cost rollup — aggregates NodeMetric samples into a daily per-node
// usage curve the /settings/usage page renders into an estimated $
// spend table. The dollar conversion is operator-configurable via
// the Kuso CR's spec.cost.{cpuPerHour, memGBPerHour} keys; the
// aggregator itself returns raw CPU·hours + GB·hours so the UI can
// re-cost on the fly without re-querying.
//
// Why per-node and not per-project: kuso's existing sample stream is
// per-node (one row per node per sampler tick). Per-project attribution
// would need either a per-pod history table (not yet built) or a
// pod-count weighting on each node row (less accurate, but doable).
// v1 ships per-node — operators get a usable monthly spend estimate
// and can attribute by node tag. Per-project rollup is a follow-up
// once the per-pod history exists.

package db

import (
	"context"
	"fmt"
	"time"
)

// CostRollupDay is one node's average usage on one calendar day,
// derived from MEAN(NodeMetric.cpuUsedMilli / cpuCapacityMilli) over
// the day's samples. NodeMetric is sampled every 5 min; a full day
// produces 288 rows / node, so the mean is meaningful even on small
// installs.
//
// CPU is reported in millicore-hours; memory in GB-hours. The UI
// multiplies by spec.cost.cpuPerHour / spec.cost.memGBPerHour for
// the dollar figure. We deliberately separate the raw units from
// the cost so a future feature ("show me in EUR") doesn't need a
// schema change.
type CostRollupDay struct {
	Node          string    `json:"node"`
	Day           time.Time `json:"day"`           // UTC midnight of the day
	CPUMilliHours int64     `json:"cpuMilliHours"` // sum(cpuUsedMilli * dt_hours)
	MemGBHours    float64   `json:"memGBHours"`    // sum(memUsedBytes/1e9 * dt_hours)
	SampleCount   int       `json:"sampleCount"`
}

// CostRollup returns per-(node, day) usage over the last `days` days.
// Reads from NodeMetric directly. Empty result is fine and means "no
// samples yet" — fresh installs see an empty page for ~30 min after
// boot until the sampler accumulates data.
//
// Math notes:
//
//   - Sampler runs every 5 min, so each row represents 5/60 = 0.0833
//     hours of usage at the recorded cpuUsedMilli.
//   - We bucket by date_trunc('day', ts) and SUM(cpuUsedMilli) × dt.
//     The result is total millicore-hours consumed that day.
//   - Memory is identical, divided by 1e9 for GB. We use 1e9 (decimal
//     gigabytes) not 2^30 because operator-facing cost dashboards
//     conventionally bill in decimal GB.
func (d *DB) CostRollup(ctx context.Context, days int) ([]CostRollupDay, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	since := time.Now().UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour)
	// dt_hours = 5/60 ≈ 0.08333. Inlined as a literal because Postgres
	// can't bind a constant into a SUM expression cleanly.
	rows, err := d.QueryContext(ctx, `
		SELECT
		  "node",
		  date_trunc('day', "ts") AS day,
		  COALESCE(SUM("cpuUsedMilli") * (5.0/60.0), 0)::BIGINT     AS cpu_milli_hours,
		  COALESCE(SUM("memUsedBytes" / 1e9) * (5.0/60.0), 0)::FLOAT AS mem_gb_hours,
		  COUNT(*) AS sample_count
		FROM "NodeMetric"
		WHERE "ts" >= $1
		GROUP BY "node", date_trunc('day', "ts")
		ORDER BY day ASC, "node" ASC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("cost rollup: %w", err)
	}
	defer rows.Close()
	out := make([]CostRollupDay, 0, days*4)
	for rows.Next() {
		var r CostRollupDay
		if err := rows.Scan(&r.Node, &r.Day, &r.CPUMilliHours, &r.MemGBHours, &r.SampleCount); err != nil {
			return nil, fmt.Errorf("scan cost row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CostTotal is the rolled-up totals across the last `days`, one row
// per node. The /settings/usage page renders this as the top-line
// "top spenders" table.
type CostTotal struct {
	Node          string  `json:"node"`
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	Days          int     `json:"days"`
}

// CostTotals collapses the daily rollup into per-node totals over the
// window. The UI uses these for the headline "$N projected this
// month" + per-node breakdown; the daily curve is for the trend
// chart.
func (d *DB) CostTotals(ctx context.Context, days int) ([]CostTotal, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	since := time.Now().UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour)
	rows, err := d.QueryContext(ctx, `
		SELECT
		  "node",
		  COALESCE(SUM("cpuUsedMilli") * (5.0/60.0), 0)::BIGINT     AS cpu_milli_hours,
		  COALESCE(SUM("memUsedBytes" / 1e9) * (5.0/60.0), 0)::FLOAT AS mem_gb_hours
		FROM "NodeMetric"
		WHERE "ts" >= $1
		GROUP BY "node"
		ORDER BY cpu_milli_hours DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("cost totals: %w", err)
	}
	defer rows.Close()
	out := make([]CostTotal, 0, 16)
	for rows.Next() {
		var r CostTotal
		if err := rows.Scan(&r.Node, &r.CPUMilliHours, &r.MemGBHours); err != nil {
			return nil, fmt.Errorf("scan cost total: %w", err)
		}
		r.Days = days
		out = append(out, r)
	}
	return out, rows.Err()
}
