// Per-project resource samples + rollup. Mirrors the NodeMetric /
// cost_rollup pair but keyed on project name instead of node — see
// internal/projectmetrics/sampler.go for the writer side.
//
// Why a separate table from NodeMetric: a single project can span
// multiple nodes, and a single node hosts pods from every project.
// Re-deriving project totals from NodeMetric + a pod-count snapshot
// is lossy (the snapshot doesn't tell you per-project CPU); writing
// a per-project sample at every tick is honest, cheap (30 day × 288
// samples × N projects = small), and lets the UI render attribution
// without inferring it.

package db

import (
	"context"
	"fmt"
	"time"
)

// ProjectMetric is one sample of a project's aggregate pod usage at a
// point in time. Sum across every pod labelled
// kuso.sislelabs.com/project=<this>. memBytes is the working-set
// reported by metrics-server; podCount is the count of pods that
// contributed (so a project with 0 pods this tick still writes a
// zero-row, keeping "project exists but is sleeping" distinguishable
// from "project never existed").
type ProjectMetric struct {
	Project  string    `json:"project"`
	Ts       time.Time `json:"ts"`
	CPUMilli int64     `json:"cpuMilli"`
	MemBytes int64     `json:"memBytes"`
	PodCount int       `json:"podCount"`
}

// InsertProjectMetric writes one sample. Zero values are fine — a
// project with no running pods this tick still emits a row so the UI
// can distinguish "sampler ran, found nothing" from "sampler hasn't
// run yet".
func (d *DB) InsertProjectMetric(ctx context.Context, m ProjectMetric) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "ProjectMetric"
		  ("project","ts","cpuMilli","memBytes","podCount")
		VALUES (?,?,?,?,?)`,
		m.Project, m.Ts.UTC(),
		m.CPUMilli, m.MemBytes, m.PodCount,
	)
	if err != nil {
		return fmt.Errorf("insert project metric: %w", err)
	}
	return nil
}

// PruneProjectMetricsOlderThan deletes samples older than `before`.
// Same retention semantics as NodeMetric — the sampler piggy-backs a
// prune call on every tick so the table stays bounded.
func (d *DB) PruneProjectMetricsOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "ProjectMetric" WHERE "ts" < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune project metrics: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ProjectCostDay is one project's CPU·hours + GB·hours on one
// calendar day. Same math as CostRollupDay: SUM(cpuMilli) × (5/60).
type ProjectCostDay struct {
	Project       string    `json:"project"`
	Day           time.Time `json:"day"`
	CPUMilliHours int64     `json:"cpuMilliHours"`
	MemGBHours    float64   `json:"memGBHours"`
	SampleCount   int       `json:"sampleCount"`
}

// ProjectCostRollup returns per-(project, day) usage. Used for trend
// charts on the rewritten /settings/usage page.
func (d *DB) ProjectCostRollup(ctx context.Context, days int) ([]ProjectCostDay, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	since := time.Now().UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour)
	rows, err := d.QueryContext(ctx, `
		SELECT
		  "project",
		  date_trunc('day', "ts") AS day,
		  COALESCE(SUM("cpuMilli") * (5.0/60.0), 0)::BIGINT     AS cpu_milli_hours,
		  COALESCE(SUM("memBytes" / 1e9) * (5.0/60.0), 0)::FLOAT AS mem_gb_hours,
		  COUNT(*) AS sample_count
		FROM "ProjectMetric"
		WHERE "ts" >= ?
		GROUP BY "project", date_trunc('day', "ts")
		ORDER BY day ASC, "project" ASC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("project cost rollup: %w", err)
	}
	defer rows.Close()
	out := make([]ProjectCostDay, 0, days*4)
	for rows.Next() {
		var r ProjectCostDay
		if err := rows.Scan(&r.Project, &r.Day, &r.CPUMilliHours, &r.MemGBHours, &r.SampleCount); err != nil {
			return nil, fmt.Errorf("scan project cost row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ProjectCostTotal is the rolled-up total across the window for one
// project. Drives the "top spenders by project" table — the most
// useful thing the new /settings/usage shows.
type ProjectCostTotal struct {
	Project       string  `json:"project"`
	CPUMilliHours int64   `json:"cpuMilliHours"`
	MemGBHours    float64 `json:"memGBHours"`
	Days          int     `json:"days"`
}

// ProjectCostTotals collapses ProjectMetric across the window into
// per-project totals, ordered most-expensive first.
func (d *DB) ProjectCostTotals(ctx context.Context, days int) ([]ProjectCostTotal, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	since := time.Now().UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour)
	rows, err := d.QueryContext(ctx, `
		SELECT
		  "project",
		  COALESCE(SUM("cpuMilli") * (5.0/60.0), 0)::BIGINT     AS cpu_milli_hours,
		  COALESCE(SUM("memBytes" / 1e9) * (5.0/60.0), 0)::FLOAT AS mem_gb_hours
		FROM "ProjectMetric"
		WHERE "ts" >= ?
		GROUP BY "project"
		ORDER BY cpu_milli_hours DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("project cost totals: %w", err)
	}
	defer rows.Close()
	out := make([]ProjectCostTotal, 0, 16)
	for rows.Next() {
		var r ProjectCostTotal
		if err := rows.Scan(&r.Project, &r.CPUMilliHours, &r.MemGBHours); err != nil {
			return nil, fmt.Errorf("scan project cost total: %w", err)
		}
		r.Days = days
		out = append(out, r)
	}
	return out, rows.Err()
}
