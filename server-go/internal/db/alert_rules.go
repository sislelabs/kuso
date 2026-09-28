// Alert rule storage. Rules are evaluated by the alerts.Engine on a
// 1-minute ticker; a fired rule emits a notify.Event of the configured
// severity and stamps lastFiredAt to throttle re-firing.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AlertRule kinds. We keep this list short on purpose — every kind
// needs corresponding eval logic in the alert engine.
const (
	AlertKindLogMatch = "log_match" // count log lines matching .Query >= ThresholdInt
	AlertKindNodeCPU  = "node_cpu"  // any node CPU > ThresholdFloat (%)
	AlertKindNodeMem  = "node_mem"  // any node mem > ThresholdFloat (%)
	AlertKindNodeDisk = "node_disk" // any node disk > ThresholdFloat (%)

	// Episodic kinds: fire once when a condition starts, send a
	// resolution when it clears (FiringSince tracks the open episode).
	AlertKindHTTP5xxRate    = "http_5xx_rate"    // 5xx share of requests >= ThresholdFloat (%), needs >= ThresholdInt requests
	AlertKindHTTPP95Latency = "http_p95_latency" // p95 latency >= ThresholdFloat (ms), needs >= ThresholdInt requests
	AlertKindCertExpiry     = "cert_expiry"      // TLS cert expires within ThresholdInt days, or its Certificate isn't Ready
	AlertKindDNSMismatch    = "dns_mismatch"     // env host doesn't resolve to the cluster's ingress IPs
)

// IsEpisodicAlertKind reports whether kind uses fire-once + resolve
// semantics instead of the throttled re-fire of the original kinds.
func IsEpisodicAlertKind(kind string) bool {
	switch kind {
	case AlertKindHTTP5xxRate, AlertKindHTTPP95Latency, AlertKindCertExpiry, AlertKindDNSMismatch:
		return true
	}
	return false
}

type AlertRule struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	Kind            string     `json:"kind"`
	Project         string     `json:"project,omitempty"`
	Service         string     `json:"service,omitempty"`
	Env             string     `json:"env,omitempty"`
	Query           string     `json:"query,omitempty"`
	ThresholdInt    *int64     `json:"thresholdInt,omitempty"`
	ThresholdFloat  *float64   `json:"thresholdFloat,omitempty"`
	WindowSeconds   int        `json:"windowSeconds"`
	Severity        string     `json:"severity"`
	ThrottleSeconds int        `json:"throttleSeconds"`
	LastFiredAt     *time.Time `json:"lastFiredAt,omitempty"`
	// FiringSince is set while an episodic rule's condition holds;
	// FiringTargets names what's breaching (env names, hosts) so a new
	// target joining an open episode can still page.
	FiringSince   *time.Time `json:"firingSince,omitempty"`
	FiringTargets []string   `json:"firingTargets,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

var ErrAlertNotFound = errors.New("alert rule not found")

func (d *DB) CreateAlertRule(ctx context.Context, r AlertRule) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "AlertRule"
		  ("id","name","enabled","kind","project","service","env","query","thresholdInt","thresholdFloat","windowSeconds","severity","throttleSeconds")
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		r.ID, r.Name, r.Enabled, r.Kind, r.Project, r.Service, r.Env, r.Query,
		nullableInt(r.ThresholdInt), nullableFloat(r.ThresholdFloat),
		r.WindowSeconds, r.Severity, r.ThrottleSeconds,
	)
	if err != nil {
		return fmt.Errorf("insert alert rule: %w", err)
	}
	return nil
}

func (d *DB) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT "id","name","enabled","kind",COALESCE("project",''),COALESCE("service",''),COALESCE("env",''),"query","thresholdInt","thresholdFloat","windowSeconds","severity","throttleSeconds","lastFiredAt","firingSince","firingTargets","createdAt","updatedAt"
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
		var lastFired, firingSince, created, updated sql.NullTime
		var targets string
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.Kind, &r.Project, &r.Service, &r.Env,
			&r.Query, &ti, &tf, &r.WindowSeconds, &r.Severity, &r.ThrottleSeconds,
			&lastFired, &firingSince, &targets, &created, &updated); err != nil {
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
		if firingSince.Valid {
			t := firingSince.Time
			r.FiringSince = &t
		}
		if targets != "" {
			r.FiringTargets = strings.Split(targets, ",")
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
	res, err := d.ExecContext(ctx, `DELETE FROM "AlertRule" WHERE "id" = $1`, id)
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
	_, err := d.ExecContext(ctx, `UPDATE "AlertRule" SET "lastFiredAt" = $1, "updatedAt" = CURRENT_TIMESTAMP WHERE "id" = $2`, at.UTC(), id)
	if err != nil {
		return fmt.Errorf("mark fired: %w", err)
	}
	return nil
}

// SetAlertEpisode records an episodic rule's state: since=nil closes the
// episode. lastFired=nil leaves lastFiredAt untouched (a resolution
// keeps the last fire time so the cooldown still applies to a flap).
func (d *DB) SetAlertEpisode(ctx context.Context, id string, since *time.Time, targets []string, lastFired *time.Time) error {
	var sinceArg, lastArg any
	if since != nil {
		sinceArg = since.UTC()
	}
	if lastFired != nil {
		lastArg = lastFired.UTC()
	}
	_, err := d.ExecContext(ctx, `
		UPDATE "AlertRule"
		SET "firingSince" = $1, "firingTargets" = $2,
		    "lastFiredAt" = COALESCE($3, "lastFiredAt"), "updatedAt" = CURRENT_TIMESTAMP
		WHERE "id" = $4`,
		sinceArg, strings.Join(targets, ","), lastArg, id)
	if err != nil {
		return fmt.Errorf("set alert episode: %w", err)
	}
	return nil
}

// SetAlertEnabled toggles without rewriting other fields.
func (d *DB) SetAlertEnabled(ctx context.Context, id string, on bool) error {
	res, err := d.ExecContext(ctx, `UPDATE "AlertRule" SET "enabled" = $1, "updatedAt" = CURRENT_TIMESTAMP WHERE "id" = $2`, on, id)
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
