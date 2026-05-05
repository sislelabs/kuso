// ErrorEvent is the row written by the Sentry-style error scanner.
// Every occurrence of an error line in a pod log lands here; the
// API endpoint groups by fingerprint to show "this error happened
// 47 times since 09:00, last seen 12:31" without scanning the raw
// log every request.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrorEvent is one occurrence of a captured error.
type ErrorEvent struct {
	ID          int64
	Project     string
	Service     string
	Env         string
	Pod         string
	Fingerprint string
	Message     string
	RawLine     string
	Ts          time.Time
	CreatedAt   time.Time
}

// ErrorGroup is the aggregated shape returned to the UI.
type ErrorGroup struct {
	Fingerprint string    `json:"fingerprint"`
	Message     string    `json:"message"`
	Count       int64     `json:"count"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	SampleLine  string    `json:"sampleLine"`
	SampleEnv   string    `json:"sampleEnv,omitempty"`
	SamplePod   string    `json:"samplePod,omitempty"`
}

// InsertErrorEvent appends an occurrence row.
func (d *DB) InsertErrorEvent(ctx context.Context, e ErrorEvent) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "ErrorEvent"
		    ("project","service","env","pod","fingerprint","message","rawLine","ts")
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Project, e.Service, e.Env, e.Pod, e.Fingerprint, e.Message, e.RawLine, e.Ts.UTC(),
	)
	if err != nil {
		return fmt.Errorf("InsertErrorEvent: %w", err)
	}
	return nil
}

// ListErrorGroups returns aggregated groups for (project, service)
// within the lookback window. Newest-first by lastSeen.
//
// The aggregation runs server-side to keep the wire payload small;
// a chatty 1000-error-per-minute service would otherwise return
// 60k rows for a 1-hour view.
func (d *DB) ListErrorGroups(ctx context.Context, project, service string, since time.Time, limit int) ([]ErrorGroup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
		SELECT fingerprint,
		       MIN(message) AS message,
		       COUNT(*) AS occurrences,
		       MIN(ts) AS first_seen,
		       MAX(ts) AS last_seen,
		       (SELECT "rawLine" FROM "ErrorEvent" e2
		         WHERE e2.project = e.project
		           AND e2.service = e.service
		           AND e2.fingerprint = e.fingerprint
		         ORDER BY ts DESC LIMIT 1) AS sample_line,
		       (SELECT env FROM "ErrorEvent" e3
		         WHERE e3.project = e.project
		           AND e3.service = e.service
		           AND e3.fingerprint = e.fingerprint
		         ORDER BY ts DESC LIMIT 1) AS sample_env,
		       (SELECT pod FROM "ErrorEvent" e4
		         WHERE e4.project = e.project
		           AND e4.service = e.service
		           AND e4.fingerprint = e.fingerprint
		         ORDER BY ts DESC LIMIT 1) AS sample_pod
		FROM "ErrorEvent" e
		WHERE project = ? AND service = ? AND ts >= ?
		GROUP BY project, service, fingerprint
		ORDER BY last_seen DESC
		LIMIT ?`,
		project, service, since.UTC(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("ListErrorGroups: %w", err)
	}
	defer rows.Close()
	out := []ErrorGroup{}
	for rows.Next() {
		var g ErrorGroup
		var first, last sql.NullTime
		if err := rows.Scan(&g.Fingerprint, &g.Message, &g.Count, &first, &last,
			&g.SampleLine, &g.SampleEnv, &g.SamplePod); err != nil {
			return nil, fmt.Errorf("scan ErrorGroup: %w", err)
		}
		if first.Valid {
			g.FirstSeen = first.Time
		}
		if last.Valid {
			g.LastSeen = last.Time
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ScannerWatermark returns the last LogLine.id processed by the
// error scanner. Returns 0 + nil error when the scanner hasn't run
// yet (so the first tick scans from id 0 forward, picking up the
// freshest few hundred lines).
func (d *DB) ScannerWatermark(ctx context.Context, key string) (int64, error) {
	var v int64
	err := d.QueryRowContext(ctx,
		`SELECT value FROM "ErrorScannerState" WHERE key = ?`, key,
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("ScannerWatermark: %w", err)
	}
	return v, nil
}

// SaveScannerWatermark upserts the watermark.
func (d *DB) SaveScannerWatermark(ctx context.Context, key string, value int64) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "ErrorScannerState" (key, value, "updatedAt")
		VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, "updatedAt" = EXCLUDED."updatedAt"`,
		key, value, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("SaveScannerWatermark: %w", err)
	}
	return nil
}

// PruneErrorEvents drops rows older than `before`. Called from the
// daily cleanup goroutine.
func (d *DB) PruneErrorEvents(ctx context.Context, before time.Time) (int, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "ErrorEvent" WHERE ts < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("PruneErrorEvents: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
