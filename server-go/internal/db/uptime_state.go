package db

import (
	"context"
	"fmt"
	"time"
)

// UptimeState is one pinged environment's row. Zero times are NULL.
type UptimeState struct {
	Namespace string
	Env       string
	Project   string
	Service   string
	// Paused is why the env isn't being pinged ("" = it is).
	Paused string
	// Hold is why a failing env hasn't alerted ("" = it has, or it's up).
	Hold           string
	FailStreak     int
	OkStreak       int
	DownSince      time.Time
	OkSince        time.Time
	Alerted        bool
	LastAlertAt    time.Time
	LastCheckedAt  time.Time
	LastResult     string
	LastStatusCode int
	LastLatencyMs  int
	LastError      string
}

// UptimeKey identifies a row.
type UptimeKey struct{ Namespace, Env string }

const uptimeStateCols = `"namespace","env","project","service","paused","hold","failStreak","okStreak",
	"downSince","okSince","alerted","lastAlertAt","lastCheckedAt","lastResult","lastStatusCode","lastLatencyMs","lastError"`

// ListUptimeStates returns every row, or one project's when project != "".
func (d *DB) ListUptimeStates(ctx context.Context, project string) ([]UptimeState, error) {
	q := `SELECT ` + uptimeStateCols + ` FROM "UptimeState"`
	args := []any{}
	if project != "" {
		q += ` WHERE "project" = $1`
		args = append(args, project)
	}
	q += ` ORDER BY "project","service","env"`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list uptime states: %w", err)
	}
	defer rows.Close()
	out := []UptimeState{}
	for rows.Next() {
		var s UptimeState
		var downSince, okSince, lastAlert, lastChecked prismaTime
		if err := rows.Scan(&s.Namespace, &s.Env, &s.Project, &s.Service, &s.Paused, &s.Hold,
			&s.FailStreak, &s.OkStreak, &downSince, &okSince, &s.Alerted, &lastAlert, &lastChecked,
			&s.LastResult, &s.LastStatusCode, &s.LastLatencyMs, &s.LastError); err != nil {
			return nil, fmt.Errorf("scan uptime state: %w", err)
		}
		s.DownSince, s.OkSince, s.LastAlertAt, s.LastCheckedAt = downSince.Time, okSince.Time, lastAlert.Time, lastChecked.Time
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveUptimeStates upserts rows and deletes keys in one transaction, so
// a tick's decisions land together or not at all.
func (d *DB) SaveUptimeStates(ctx context.Context, rows []UptimeState, del []UptimeKey) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save uptime states: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, s := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO "UptimeState" (`+uptimeStateCols+`,"updatedAt")
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17, now())
			ON CONFLICT ("namespace","env") DO UPDATE SET
				"project"=EXCLUDED."project","service"=EXCLUDED."service","paused"=EXCLUDED."paused","hold"=EXCLUDED."hold",
				"failStreak"=EXCLUDED."failStreak","okStreak"=EXCLUDED."okStreak",
				"downSince"=EXCLUDED."downSince","okSince"=EXCLUDED."okSince","alerted"=EXCLUDED."alerted",
				"lastAlertAt"=EXCLUDED."lastAlertAt","lastCheckedAt"=EXCLUDED."lastCheckedAt",
				"lastResult"=EXCLUDED."lastResult","lastStatusCode"=EXCLUDED."lastStatusCode",
				"lastLatencyMs"=EXCLUDED."lastLatencyMs","lastError"=EXCLUDED."lastError","updatedAt"=now()`,
			s.Namespace, s.Env, s.Project, s.Service, s.Paused, s.Hold, s.FailStreak, s.OkStreak,
			prismaTime{s.DownSince}, prismaTime{s.OkSince}, s.Alerted, prismaTime{s.LastAlertAt}, prismaTime{s.LastCheckedAt},
			s.LastResult, s.LastStatusCode, s.LastLatencyMs, s.LastError)
		if err != nil {
			return fmt.Errorf("upsert uptime state %s/%s: %w", s.Namespace, s.Env, err)
		}
	}
	for _, k := range del {
		if _, err := tx.ExecContext(ctx, `DELETE FROM "UptimeState" WHERE "namespace"=$1 AND "env"=$2`, k.Namespace, k.Env); err != nil {
			return fmt.Errorf("delete uptime state %s/%s: %w", k.Namespace, k.Env, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit uptime states: %w", err)
	}
	return nil
}
