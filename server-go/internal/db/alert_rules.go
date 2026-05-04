// Alert rule storage. Rules are evaluated by the alerts.Engine on a
// 1-minute ticker; a fired rule emits a notify.Event of the configured
// severity and stamps lastFiredAt to throttle re-firing.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AlertRule kinds. We keep this list short on purpose — every kind
// needs corresponding eval logic in the alert engine.
const (
	AlertKindLogMatch  = "log_match"  // count log lines matching .Query >= ThresholdInt
	AlertKindNodeCPU   = "node_cpu"   // any node CPU > ThresholdFloat (%)
	AlertKindNodeMem   = "node_mem"   // any node mem > ThresholdFloat (%)
	AlertKindNodeDisk  = "node_disk"  // any node disk > ThresholdFloat (%)
)

type AlertRule struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	Kind            string     `json:"kind"`
	Project         string     `json:"project,omitempty"`
	Service         string     `json:"service,omitempty"`
	Query           string     `json:"query,omitempty"`
	ThresholdInt    *int64     `json:"thresholdInt,omitempty"`
	ThresholdFloat  *float64   `json:"thresholdFloat,omitempty"`
	WindowSeconds   int        `json:"windowSeconds"`
	Severity        string     `json:"severity"`
	ThrottleSeconds int        `json:"throttleSeconds"`
	LastFiredAt     *time.Time `json:"lastFiredAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

var ErrAlertNotFound = errors.New("alert rule not found")

func (d *DB) CreateAlertRule(ctx context.Context, r AlertRule) error {
	_, err := d.DB.ExecContext(ctx, `
		INSERT INTO "AlertRule"
		  ("id","name","enabled","kind","project","service","query","thresholdInt","thresholdFloat","windowSeconds","severity","throttleSeconds")
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Name, r.Enabled, r.Kind, r.Project, r.Service, r.Query,
		nullableInt(r.ThresholdInt), nullableFloat(r.ThresholdFloat),
		r.WindowSeconds, r.Severity, r.ThrottleSeconds,
	)
	if err != nil {
		return fmt.Errorf("insert alert rule: %w", err)
	}
	return nil
}

func (d *DB) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := d.DB.QueryContext(ctx, `
		SELECT "id","name","enabled","kind","project","service","query","thresholdInt","thresholdFloat","windowSeconds","severity","throttleSeconds","lastFiredAt","createdAt","updatedAt"
		FROM "AlertRule"
		ORDER BY "name" ASC`)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer rows.Close()
	out := []AlertRule{}
	for rows.Next() {
		var r AlertRule
		var ti sql.NullInt64
		var tf sql.NullFloat64
		var lastFired, created, updated sql.NullTime
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.Kind, &r.Project, &r.Service,
			&r.Query, &ti, &tf, &r.WindowSeconds, &r.Severity, &r.ThrottleSeconds,
			&lastFired, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan alert rule: %w", err)
		}
		if ti.Valid {
			v := ti.Int64
			r.ThresholdInt = &v
		}
		if tf.Valid {
			v := tf.Float64
			r.ThresholdFloat = &v
		}
		if lastFired.Valid {
			t := lastFired.Time
			r.LastFiredAt = &t
		}
		if created.Valid {
			r.CreatedAt = created.Time
		}
		if updated.Valid {
			r.UpdatedAt = updated.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) DeleteAlertRule(ctx context.Context, id string) error {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "AlertRule" WHERE "id" = ?`, id)
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAlertNotFound
	}
	return nil
}

// MarkAlertFired stamps lastFiredAt for throttling.
func (d *DB) MarkAlertFired(ctx context.Context, id string, at time.Time) error {
	_, err := d.DB.ExecContext(ctx, `UPDATE "AlertRule" SET "lastFiredAt" = ?, "updatedAt" = CURRENT_TIMESTAMP WHERE "id" = ?`, at.UTC(), id)
	if err != nil {
		return fmt.Errorf("mark fired: %w", err)
	}
	return nil
}

// SetAlertEnabled toggles without rewriting other fields.
func (d *DB) SetAlertEnabled(ctx context.Context, id string, on bool) error {
	res, err := d.DB.ExecContext(ctx, `UPDATE "AlertRule" SET "enabled" = ?, "updatedAt" = CURRENT_TIMESTAMP WHERE "id" = ?`, on, id)
	if err != nil {
		return fmt.Errorf("toggle alert: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAlertNotFound
	}
	return nil
}

func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
